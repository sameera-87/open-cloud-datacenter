#!/usr/bin/env bash
# dev-up.sh — bring up the cloud-ui dev loop in one of two modes.
#
#   ./scripts/dev-up.sh local      # local dc-api + cloud-ui → talks to harvester-dev
#   ./scripts/dev-up.sh remote     # cloud-ui only → proxies to deployed dc-api ingress
#   ./scripts/dev-up.sh down       # tear everything down (kill bg processes)
#
# What each mode does:
#
#   local
#     1. kubectl port-forward dc-postgres :15432 (background)
#     2. build + run dc-api on :8080 with prod config sourced from the cluster
#        Secret/ConfigMap, with these overrides:
#          - DB URL  → localhost:15432
#          - BFF redirect / cookie domain / post-login → localhost
#          - LOG_LEVEL → debug
#     3. start vite dev server with VITE_API_PROXY_TARGET=http://localhost:8080
#     4. open http://localhost:5173
#
#     Reads/writes the SAME dc-api postgres as prod (the prod dc-api Deployment
#     pod is still running too — it shares the DB). Use 'scripts/dev-up.sh local
#     --scale-down-prod' to scale the in-cluster dc-api to 0 first if you need
#     exclusive reconciler access.
#
#   remote
#     Just runs vite dev with the proxy pointed at the deployed ingress
#     (https://dcapi.lk-dev.internal.wso2.com). No local dc-api. NOTE: BFF
#     callbacks redirect to the deployed cloud-ui (cloud.lk-dev.internal.wso2.com)
#     after login, NOT to localhost — so login from localhost in this mode
#     lands you in the prod UI. Useful for UI-only smoke tests where the auth
#     flow is already done, less useful for full login walk-throughs.
#
#   down
#     kill background dc-api + port-forward + vite if running.
#
# State files (gitignored): /tmp/sovereign-cloud-dev/*

set -euo pipefail

REPO_ROOT=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)
STATE_DIR=/tmp/sovereign-cloud-dev
mkdir -p "$STATE_DIR"

PGPF_PID_FILE=$STATE_DIR/pgpf.pid
DCAPI_PID_FILE=$STATE_DIR/dc-api.pid
VITE_PID_FILE=$STATE_DIR/vite.pid
PGPF_LOG=$STATE_DIR/pgpf.log
DCAPI_LOG=$STATE_DIR/dc-api.log
VITE_LOG=$STATE_DIR/vite.log

CTX=dcapi-controlplane-rke2
NS=dc-system

stop_pid() {
  local f=$1; local name=$2
  if [[ -f $f ]]; then
    local pid=$(cat "$f")
    if kill -0 "$pid" 2>/dev/null; then
      echo "stopping $name (pid=$pid)"
      kill "$pid" 2>/dev/null || true
    fi
    rm -f "$f"
  fi
}

cmd_down() {
  stop_pid "$VITE_PID_FILE"   vite
  stop_pid "$DCAPI_PID_FILE"  dc-api
  # Kill the watchdog first so it doesn't relaunch the pf we're about to stop.
  stop_pid "$STATE_DIR/pgpf-watchdog.pid" pgpf-watchdog
  stop_pid "$PGPF_PID_FILE"   port-forward
  # The watchdog spawned children; sweep them.
  pkill -f 'port-forward.*15432' 2>/dev/null || true
  echo "done. logs in $STATE_DIR"
}

cmd_local() {
  # 1. postgres port-forward with auto-restart watchdog.
  # `kubectl port-forward` drops on long HTTP/2 streams (k8s issue #74551).
  # The watchdog re-launches it whenever the listener disappears so the dc-api
  # pgx pool can reconnect instead of erroring out for the rest of the session.
  WATCHDOG_PID_FILE=$STATE_DIR/pgpf-watchdog.pid
  stop_pid "$WATCHDOG_PID_FILE" pgpf-watchdog
  stop_pid "$PGPF_PID_FILE" port-forward
  echo "[1/3] starting postgres port-forward → :15432 (with auto-restart)"
  (
    while true; do
      kubectl --context "$CTX" -n "$NS" port-forward svc/dc-postgres 15432:5432 \
        >> "$PGPF_LOG" 2>&1
      echo "[$(date +%H:%M:%S)] pf exited; restarting in 2s" >> "$PGPF_LOG"
      sleep 2
    done
  ) &
  echo $! > "$WATCHDOG_PID_FILE"
  for i in 1 2 3 4 5 6 7 8; do
    sleep 1
    nc -z localhost 15432 2>/dev/null && break
  done
  nc -z localhost 15432 2>/dev/null || { echo "  port-forward failed; see $PGPF_LOG"; exit 1; }

  # 2. build + run dc-api
  echo "[2/3] building dc-api → /tmp/dc-api-local"
  ( cd "$REPO_ROOT/dc-api" && go build -o /tmp/dc-api-local ./cmd/dc-api/ )

  # Source the live cluster Secret+ConfigMap as DCAPI_* env vars. Then override
  # the BFF + DB + listen + log to point at localhost.
  source "$REPO_ROOT/docs/dev/dc-api-local-env.sh"
  export DCAPI_LISTEN_ADDR=":8080"
  # BFF localhost overrides — requires that the cloud_ui_bff Asgardeo app
  # has http://localhost:8080/v1/auth/callback registered as an allowed
  # redirect URI. If login redirects to "Invalid redirect URI", add it via
  # the Asgardeo console (Apps → cloud_ui_bff → Protocol → Allowed origins).
  export DCAPI_BFF_REDIRECT_URL="http://localhost:8080/v1/auth/callback"
  export DCAPI_BFF_POST_LOGIN_REDIRECT="http://localhost:5173/"
  export DCAPI_BFF_POST_LOGOUT_REDIRECT="http://localhost:5173/login"
  export DCAPI_BFF_COOKIE_DOMAIN="localhost"
  export DCAPI_BFF_COOKIE_SECURE="false"

  # kill any stale local dc-api on :8080
  stop_pid "$DCAPI_PID_FILE" dc-api
  echo "  starting dc-api on :8080 (log → $DCAPI_LOG)"
  nohup /tmp/dc-api-local > "$DCAPI_LOG" 2>&1 &
  echo $! > "$DCAPI_PID_FILE"

  # health-check — kubeovn bootstrap can take 10-15s on first start
  echo -n "  waiting for dc-api health"
  for i in $(seq 1 30); do
    sleep 1
    if curl -sf http://localhost:8080/healthz > /dev/null 2>&1; then
      echo " — ready"
      break
    fi
    echo -n "."
  done
  curl -sf http://localhost:8080/healthz > /dev/null 2>&1 || {
    echo ""; echo "  dc-api never came up; see $DCAPI_LOG"; tail -30 "$DCAPI_LOG"; exit 1; }

  # 3. vite dev server
  stop_pid "$VITE_PID_FILE" vite
  echo "[3/3] starting cloud-ui (vite) → http://localhost:5173"
  ( cd "$REPO_ROOT/cloud-ui" && \
    VITE_API_PROXY_TARGET=http://localhost:8080 \
    nohup pnpm dev > "$VITE_LOG" 2>&1 & echo $! > "$VITE_PID_FILE" )

  for i in 1 2 3 4 5 6 7 8 9 10; do
    sleep 1
    if curl -sf http://localhost:5173/ > /dev/null 2>&1; then
      echo "  vite ready"
      break
    fi
  done

  cat <<EOF

=== ready ===
  cloud-ui   http://localhost:5173
  dc-api     http://localhost:8080  (BFF → /v1/auth/login)
  postgres   localhost:15432 → dc_api (prod DB via port-forward)

  logs       $STATE_DIR/{pgpf,dc-api,vite}.log
  stop       $REPO_ROOT/scripts/dev-up.sh down

EOF
}

cmd_remote() {
  echo "[remote] starting cloud-ui only → proxies to https://dcapi.lk-dev.internal.wso2.com"
  echo "         NOTE: BFF callback after login redirects to the deployed cloud-ui,"
  echo "         not localhost. For full local login flow use 'local' mode."
  stop_pid "$VITE_PID_FILE" vite
  ( cd "$REPO_ROOT/cloud-ui" && \
    VITE_API_PROXY_TARGET=https://dcapi.lk-dev.internal.wso2.com \
    nohup pnpm dev > "$VITE_LOG" 2>&1 & echo $! > "$VITE_PID_FILE" )
  for i in 1 2 3 4 5 6 7 8 9 10; do
    sleep 1
    if curl -sf http://localhost:5173/ > /dev/null 2>&1; then
      echo "  vite ready → http://localhost:5173"
      break
    fi
  done
}

case "${1:-}" in
  local)  cmd_local  ;;
  remote) cmd_remote ;;
  down)   cmd_down   ;;
  *)
    echo "usage: $0 {local|remote|down}"
    exit 1
    ;;
esac
