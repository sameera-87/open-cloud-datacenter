#!/usr/bin/env bash
# bootstrap.sh — one-shot local-dev setup for the Sovereign Cloud control plane.
#
# What it does (idempotent — safe to run repeatedly):
#   1. Checks prerequisites (Go, Docker, base64, openssl) and prints install hints.
#   2. Copies .env.example → .env if .env is missing.
#   3. Starts PostgreSQL via docker compose.
#   4. Builds dc-api and dcctl.
#
# What it does NOT do:
#   - Install missing prerequisites for you (too many distros, too easy to get wrong).
#   - Configure OIDC, Harvester or Rancher credentials in .env (manual; see docs/local-dev.md).
#   - Start dc-api (you do that AFTER filling in .env, with `make run`).
#
# Supported platforms: macOS (Apple Silicon + Intel), Linux (Debian/Ubuntu/Fedora/Arch).
#
# Usage:
#   ./scripts/bootstrap.sh        # full bootstrap
#   ./scripts/bootstrap.sh check  # just verify prerequisites, no side effects

set -euo pipefail

REPO_ROOT=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)
cd "$REPO_ROOT"

MODE=${1:-bootstrap}

# ── pretty output ────────────────────────────────────────────────────────────
if [[ -t 1 ]]; then
  C_RED=$'\033[31m'; C_GREEN=$'\033[32m'; C_YELLOW=$'\033[33m'
  C_BLUE=$'\033[34m'; C_BOLD=$'\033[1m'; C_RESET=$'\033[0m'
else
  C_RED=""; C_GREEN=""; C_YELLOW=""; C_BLUE=""; C_BOLD=""; C_RESET=""
fi
ok()    { echo "${C_GREEN}✓${C_RESET} $*"; }
warn()  { echo "${C_YELLOW}!${C_RESET} $*"; }
fail()  { echo "${C_RED}✗${C_RESET} $*"; }
step()  { echo; echo "${C_BOLD}${C_BLUE}── $* ──${C_RESET}"; }

# ── OS detection ─────────────────────────────────────────────────────────────
OS=$(uname -s)
case "$OS" in
  Darwin) PLATFORM=mac ;;
  Linux)  PLATFORM=linux ;;
  *)      fail "unsupported OS: $OS (this script supports macOS and Linux)"; exit 1 ;;
esac

# Best-effort Linux distro detection — used only to suggest the right
# install command. We never auto-install anything.
LINUX_PKG_HINT=""
if [[ $PLATFORM == linux ]] && [[ -r /etc/os-release ]]; then
  # shellcheck disable=SC1091
  . /etc/os-release
  case "${ID_LIKE:-$ID}" in
    *debian*|*ubuntu*)   LINUX_PKG_HINT="sudo apt update && sudo apt install -y" ;;
    *fedora*|*rhel*)     LINUX_PKG_HINT="sudo dnf install -y" ;;
    *arch*|*manjaro*)    LINUX_PKG_HINT="sudo pacman -S --noconfirm" ;;
    *)                   LINUX_PKG_HINT="<your-package-manager> install" ;;
  esac
fi

install_hint() {
  # $1 = brew formula, $2 = apt/dnf/pacman package name, $3 = optional URL
  local brew_name=$1 linux_pkg=$2 url=${3:-}
  if [[ $PLATFORM == mac ]]; then
    echo "    install with: brew install $brew_name"
  else
    echo "    install with: $LINUX_PKG_HINT $linux_pkg"
  fi
  [[ -n $url ]] && echo "    or see:       $url"
}

# ── prerequisite checks ──────────────────────────────────────────────────────
MISSING=0

check_go() {
  if ! command -v go >/dev/null 2>&1; then
    fail "go not found"
    install_hint go golang-go "https://go.dev/dl/"
    MISSING=1
    return
  fi
  local v
  v=$(go version | awk '{print $3}' | sed 's/^go//')
  # Minimum 1.22 for dcctl, dc-api needs 1.26. We accept 1.22+ and warn loudly
  # if below 1.26 so the dc-api build error makes sense if it fails.
  if ! printf '%s\n%s\n' "1.22" "$v" | sort -V -C; then
    fail "go $v is too old — need >= 1.22 (dc-api needs >= 1.26)"
    install_hint go golang-go "https://go.dev/dl/"
    MISSING=1
    return
  fi
  if ! printf '%s\n%s\n' "1.26" "$v" | sort -V -C; then
    warn "go $v will build dcctl but not dc-api (needs >= 1.26) — upgrade if you plan to build dc-api locally"
  else
    ok "go $v"
  fi
}

check_docker() {
  if ! command -v docker >/dev/null 2>&1; then
    fail "docker not found"
    if [[ $PLATFORM == mac ]]; then
      echo "    install: https://www.docker.com/products/docker-desktop/  (or: brew install --cask docker)"
    else
      install_hint docker docker.io "https://docs.docker.com/engine/install/"
    fi
    MISSING=1
    return
  fi
  if ! docker info >/dev/null 2>&1; then
    fail "docker is installed but the daemon is not running — start Docker Desktop / systemctl start docker"
    MISSING=1
    return
  fi
  if docker compose version >/dev/null 2>&1; then
    ok "docker (with compose v2)"
  elif command -v docker-compose >/dev/null 2>&1; then
    warn "using legacy docker-compose v1 — docker compose v2 is recommended"
  else
    fail "docker compose plugin not found"
    if [[ $PLATFORM == mac ]]; then
      echo "    Docker Desktop ships compose v2 — reinstall or update Docker Desktop"
    else
      install_hint docker-compose docker-compose-plugin "https://docs.docker.com/compose/install/linux/"
    fi
    MISSING=1
  fi
}

check_tool() {
  local name=$1 brew_pkg=${2:-$1} linux_pkg=${3:-$1}
  if command -v "$name" >/dev/null 2>&1; then
    ok "$name"
  else
    fail "$name not found"
    install_hint "$brew_pkg" "$linux_pkg"
    MISSING=1
  fi
}

check_optional() {
  local name=$1 reason=$2 brew_pkg=${3:-$1} linux_pkg=${4:-$1}
  if command -v "$name" >/dev/null 2>&1; then
    ok "$name (optional)"
  else
    warn "$name not found — $reason"
    install_hint "$brew_pkg" "$linux_pkg"
  fi
}

step "Checking prerequisites ($PLATFORM)"
check_go
check_docker
check_tool base64
check_tool openssl
check_optional psql        "useful for inspecting the dev database" postgresql postgresql-client
check_optional jq          "useful for parsing API responses in scripts"           jq jq
check_optional kubectl     "needed if you want to dump a Harvester kubeconfig"     kubernetes-cli kubectl

if (( MISSING > 0 )); then
  echo
  fail "$MISSING prerequisite(s) missing — install them and re-run this script"
  exit 1
fi

if [[ $MODE == check ]]; then
  echo
  ok "all prerequisites satisfied"
  exit 0
fi

# ── .env ─────────────────────────────────────────────────────────────────────
step "Setting up .env"
if [[ -f .env ]]; then
  ok ".env already exists — leaving it alone"
else
  cp .env.example .env
  ok "created .env from .env.example"
  warn "edit .env and fill in your DCAPI_OIDC_*, DCAPI_HARVESTER_KUBECONFIG, DCAPI_RANCHER_*, and DCAPI_VPC_EXTERNAL_* values before running dc-api"
  warn "see docs/local-dev.md for the full walk-through"
fi

# ── PostgreSQL ───────────────────────────────────────────────────────────────
step "Starting PostgreSQL"
if docker compose version >/dev/null 2>&1; then
  COMPOSE="docker compose"
else
  COMPOSE="docker-compose"
fi
$COMPOSE up -d postgres
echo -n "waiting for postgres "
for i in 1 2 3 4 5 6 7 8 9 10 11 12 13 14 15; do
  if docker exec dc-postgres pg_isready -U dc_api >/dev/null 2>&1; then
    echo
    ok "postgres ready at localhost:5432 (db=dc_api, user=dc_api, password=dc_dev_password)"
    break
  fi
  echo -n "."
  sleep 1
done
if ! docker exec dc-postgres pg_isready -U dc_api >/dev/null 2>&1; then
  fail "postgres did not become ready in 15s — check 'docker compose logs postgres'"
  exit 1
fi

# ── Build ────────────────────────────────────────────────────────────────────
step "Building dc-api"
( cd dc-api && go mod download && go build -o dc-api ./cmd/dc-api/ )
ok "built dc-api/dc-api"

step "Building dcctl"
DCCTL_OUT=${DCCTL_OUT:-$HOME/bin/dcctl}
mkdir -p "$(dirname "$DCCTL_OUT")"
( cd dcctl && go mod download && go build -o "$DCCTL_OUT" . )
ok "built $DCCTL_OUT"
if ! echo "${PATH:-}" | tr ':' '\n' | grep -qx "$(dirname "$DCCTL_OUT")"; then
  warn "$(dirname "$DCCTL_OUT") is not on your \$PATH — add it to your shell profile to use 'dcctl' directly"
fi

# ── Next steps ───────────────────────────────────────────────────────────────
echo
echo "${C_BOLD}${C_GREEN}Bootstrap complete.${C_RESET}"
cat <<EOF

Next steps:

  1. Fill in .env (see docs/local-dev.md §3 for what each variable means):
       - DCAPI_OIDC_ISSUER / DCAPI_OIDC_AUDIENCE  (your IdP — Asgardeo, Keycloak, …)
       - DCAPI_HARVESTER_KUBECONFIG               (base64 of your Harvester kubeconfig)
       - DCAPI_RANCHER_URL / DCAPI_RANCHER_TOKEN  (Rancher endpoint + API token)
       - DCAPI_RANCHER_HARVESTER_CREDENTIAL       (cattle-global-data:cc-xxxxx from Rancher UI)
       - DCAPI_VPC_EXTERNAL_*                     (the external network KubeOVN SNATs through)

  2. Run dc-api:
       make run

  3. In another terminal, log in with dcctl:
       dcctl login
       dcctl tenant list
       dcctl tenant set <your-tenant>
       dcctl project create dev --cpu 8 --memory 16 --storage 100
       dcctl project set dev

  4. Open the API docs in a browser:
       http://localhost:8080/docs
EOF
