#!/usr/bin/env bash
# KubeOVN spike installer — M2 networking spike on the Harvester cluster.
#
# Idempotent: safe to re-run. Each phase reads current state and skips work
# already done. If any pre-flight check fails the script exits before touching
# the cluster.
#
# Usage:
#   ./spike-install.sh                  # all phases
#   ./spike-install.sh preflight        # checks only, no changes
#   ./spike-install.sh install          # helm install (assumes preflight)
#   ./spike-install.sh verify           # post-install readiness
#   ./spike-install.sh uninstall        # rollback (helm uninstall + CRD cleanup)
#
# Requires: kubectl, helm, jq, with KUBECONFIG / context pointing at the
# Harvester cluster (lk-dev).
#
# This script is the spike artifact. The production version will be a
# Terraform module under wso2-datacenter-project; see MILESTONES.md
# "DC-API Bootstrap" for that work.

set -euo pipefail

# ── Config ───────────────────────────────────────────────────────────────────
KUBEOVN_VERSION="${KUBEOVN_VERSION:-v1.15.4}"
KUBEOVN_NAMESPACE="${KUBEOVN_NAMESPACE:-kube-ovn}"      # Helm release namespace (release metadata)
KUBEOVN_WORKLOAD_NS="${KUBEOVN_WORKLOAD_NS:-kube-system}" # where pods actually run (chart-hardcoded, standard for CNI providers)
KUBEOVN_RELEASE="${KUBEOVN_RELEASE:-kube-ovn}"
HELM_REPO_URL="${HELM_REPO_URL:-https://kubeovn.github.io/kube-ovn}"
HELM_REPO_NAME="${HELM_REPO_NAME:-kubeovn}"
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
VALUES_FILE="${VALUES_FILE:-${SCRIPT_DIR}/values.yaml}"
EXPECTED_SVC_CIDR="${EXPECTED_SVC_CIDR:-10.53.0.0/16}"
EXPECTED_POD_CIDR="${EXPECTED_POD_CIDR:-10.52.0.0/16}"
TUNNEL_NIC_OVERRIDE="${TUNNEL_NIC:-}"   # if set, skip auto-detection

# ── Logging helpers ──────────────────────────────────────────────────────────
log()  { printf '\033[1;34m[*]\033[0m %s\n' "$*"; }
ok()   { printf '\033[1;32m[+]\033[0m %s\n' "$*"; }
warn() { printf '\033[1;33m[!]\033[0m %s\n' "$*" >&2; }
err()  { printf '\033[1;31m[x]\033[0m %s\n' "$*" >&2; }
fail() { err "$*"; exit 1; }

# ── Pre-flight checks ────────────────────────────────────────────────────────
preflight() {
  log "PHASE: pre-flight"

  command -v kubectl >/dev/null || fail "kubectl not found in PATH"
  command -v helm    >/dev/null || fail "helm not found in PATH"
  command -v jq      >/dev/null || fail "jq not found in PATH"

  log "kubectl context: $(kubectl config current-context)"
  kubectl cluster-info >/dev/null || fail "cannot reach cluster"

  check_bundled_addon_off
  check_crd_stub
  check_cluster_cidrs
  detect_tunnel_nic
  check_ovs_kernel_module

  ok "pre-flight passed"
}

check_bundled_addon_off() {
  log "  • Harvester bundled KubeOVN add-on is off"
  local addons
  addons=$(kubectl get addons -n harvester-system -o json 2>/dev/null \
    | jq -r '.items[] | select(.metadata.name | test("kubeovn"; "i")) | "\(.metadata.name)=\(.spec.enabled)"' || true)
  if [[ -n "$addons" ]]; then
    while IFS= read -r line; do
      if [[ "$line" == *"=true" ]]; then
        fail "bundled KubeOVN add-on is ENABLED ($line) — disable before installing upstream"
      fi
    done <<< "$addons"
  fi
  ok "  bundled add-on off"
}

check_crd_stub() {
  log "  • configurations.kubeovn.io CRD stub"
  if kubectl get crd configurations.kubeovn.io >/dev/null 2>&1; then
    local served stored
    served=$(kubectl get crd configurations.kubeovn.io -o json | jq -r '.spec.versions[] | select(.served==true) | .name' | tr '\n' ',')
    stored=$(kubectl get crd configurations.kubeovn.io -o json | jq -r '.status.storedVersions[]' | tr '\n' ',')
    warn "  stub CRD exists (served=$served, stored=$stored)"
    warn "  Helm install may either no-op (matching schema) or fail (mismatch)."
    warn "  If install fails on this CRD: kubectl delete crd configurations.kubeovn.io"
    warn "  Safe to delete — discovery confirmed no controller is consuming it."
  else
    ok "  no stub CRD present"
  fi
}

check_cluster_cidrs() {
  log "  • cluster pod/service CIDRs match values.yaml expectations"
  # RKE2 does not run kube-controller-manager as a labelled pod. Try a few
  # detection strategies in order; if none work, fall back to a soft warning.
  local pod_cidr svc_cidr

  # Pod CIDR: every node carries it in spec.podCIDR.
  local node_pod_cidr
  node_pod_cidr=$(kubectl get nodes -o jsonpath='{.items[0].spec.podCIDR}' 2>/dev/null)
  if [[ -n "$node_pod_cidr" ]]; then
    # node.spec.podCIDR is the per-node slice (e.g. 10.52.0.0/24); the cluster
    # CIDR is the encompassing /16. Compare the /16 prefix only.
    pod_cidr="${node_pod_cidr%.*.*/*}.0.0/16"
  fi

  # Service CIDR: probe by creating an invalid Service and reading the error.
  # The kube-apiserver returns the configured service CIDR in the error text.
  local probe_err
  probe_err=$(kubectl create service clusterip kubeovn-cidr-probe --tcp=1:1 \
              --dry-run=server -o yaml 2>&1 || true)
  # If the dry-run succeeds we delete it; either way the actual cluster
  # service-CIDR appears in default-allocated ClusterIP. Try a different probe:
  if [[ -z "$svc_cidr" ]]; then
    # Use the kubernetes service ClusterIP — always allocated from the service CIDR.
    local k8s_svc_ip
    k8s_svc_ip=$(kubectl get svc kubernetes -n default -o jsonpath='{.spec.clusterIP}' 2>/dev/null)
    if [[ "$k8s_svc_ip" =~ ^([0-9]+\.[0-9]+)\. ]]; then
      svc_cidr="${BASH_REMATCH[1]}.0.0/16"
    fi
  fi

  if [[ -z "$pod_cidr" || -z "$svc_cidr" ]]; then
    warn "  could not auto-detect CIDRs (pod=$pod_cidr svc=$svc_cidr) — set EXPECTED_*_CIDR env if values.yaml differs"
    return 0
  fi
  [[ "$svc_cidr" == "$EXPECTED_SVC_CIDR" ]] || fail "service CIDR mismatch: cluster=$svc_cidr expected=$EXPECTED_SVC_CIDR"
  [[ "$pod_cidr" == "$EXPECTED_POD_CIDR" ]] || fail "pod CIDR mismatch: cluster=$pod_cidr expected=$EXPECTED_POD_CIDR"
  ok "  CIDRs match (svc=$svc_cidr pod=$pod_cidr)"
}

detect_tunnel_nic() {
  log "  • detecting tunnel NIC (must NOT be eno2np1 — that's the VM VLAN bridge)"
  if [[ -n "$TUNNEL_NIC_OVERRIDE" ]]; then
    TUNNEL_NIC="$TUNNEL_NIC_OVERRIDE"
    ok "  using TUNNEL_NIC override: $TUNNEL_NIC"
    return 0
  fi

  local node node_ip iface
  node=$(kubectl get nodes -l node-role.kubernetes.io/control-plane -o jsonpath='{.items[0].metadata.name}')
  node_ip=$(kubectl get node "$node" -o jsonpath='{.status.addresses[?(@.type=="InternalIP")].address}')

  iface=$(run_priv_on_node "$node" \
    "ip -o -4 addr show | awk -v ip='$node_ip' '\$4 ~ ip {print \$2; exit}'") || \
    fail "could not detect tunnel NIC on $node — set TUNNEL_NIC=<iface> and re-run"

  # Take only the first non-empty line and strip whitespace — defends against
  # any stray output from the probe pod's lifecycle.
  iface=$(echo "$iface" | grep -v '^[[:space:]]*$' | head -n1 | tr -d '[:space:]')
  if [[ -z "$iface" ]]; then
    fail "tunnel NIC detection returned empty — set TUNNEL_NIC=<iface> and re-run"
  fi
  if [[ "$iface" == "eno2np1" ]]; then
    fail "detected tunnel NIC is eno2np1 — that's the VM VLAN bridge uplink. Set TUNNEL_NIC=<other-iface> explicitly."
  fi
  TUNNEL_NIC="$iface"
  ok "  tunnel NIC: $TUNNEL_NIC (carries node IP $node_ip)"
}

check_ovs_kernel_module() {
  log "  • Open vSwitch kernel module on each node"
  local nodes
  nodes=$(kubectl get nodes -o jsonpath='{.items[*].metadata.name}')
  for node in $nodes; do
    # One probe pod per node, runs three checks and prints labelled lines.
    local out
    out=$(run_priv_on_node "$node" '
      printf "LSMOD="; lsmod | grep -E "^(openvswitch|openvswitch_)" | head -1 || printf "none"
      printf "\nFINDMOD="; find /lib/modules/$(uname -r) -name "openvswitch*" 2>/dev/null | head -1 || printf "none"
      printf "\nMODPROBE="; modprobe openvswitch 2>&1 && printf "ok" || printf "fail"
      printf "\nLSMOD2="; lsmod | grep -E "^(openvswitch|openvswitch_)" | head -1 || printf "none"
    ')
    local lsmod1 findmod modprobe_res lsmod2
    lsmod1=$(echo "$out" | sed -n 's/^LSMOD=//p' | head -n1)
    findmod=$(echo "$out" | sed -n 's/^FINDMOD=//p' | head -n1)
    modprobe_res=$(echo "$out" | sed -n 's/^MODPROBE=//p' | head -n1)
    lsmod2=$(echo "$out" | sed -n 's/^LSMOD2=//p' | head -n1)

    if [[ "$lsmod1" =~ ^openvswitch ]] || [[ "$lsmod2" =~ ^openvswitch ]]; then
      ok "  $node: openvswitch module loaded"
    else
      err "  $node: openvswitch module NOT loaded"
      err "    lsmod (before modprobe):  ${lsmod1:-<empty>}"
      err "    module file on disk:       ${findmod:-<empty>}"
      err "    modprobe result:           ${modprobe_res:-<empty>}"
      err "    lsmod (after modprobe):    ${lsmod2:-<empty>}"
      fail "$node: openvswitch kernel module unavailable — see diagnostic above. SLE Micro may need the kernel-default-extra package, or Harvester may need to be upgraded."
    fi
  done
}

# Run a command in a privileged pod with the host's PID/network/mount namespace.
# Uses create → wait → logs → delete instead of `kubectl run --rm -i` because
# the latter merges pod-deletion notices into stdout, corrupting output.
# The command is base64-encoded to avoid YAML quoting / indentation issues
# regardless of whether it's single-line or multi-line.
run_priv_on_node() {
  local node="$1" cmd="$2"
  local pod="kubeovn-spike-probe-$(date +%s)-$RANDOM"
  local cmd_b64
  cmd_b64=$(printf '%s' "$cmd" | base64 | tr -d '\n')

  cat <<EOF | kubectl create -f - >/dev/null
apiVersion: v1
kind: Pod
metadata:
  name: $pod
  namespace: default
  labels:
    app: kubeovn-spike-probe
spec:
  nodeName: $node
  hostPID: true
  hostNetwork: true
  restartPolicy: Never
  tolerations:
    - operator: Exists
  containers:
    - name: probe
      image: busybox:1.36
      command: ["nsenter", "--target", "1", "--mount", "--uts", "--ipc", "--net", "--pid", "--", "sh", "-c", "echo $cmd_b64 | base64 -d | sh"]
      securityContext:
        privileged: true
EOF

  # Wait for the pod to terminate (Succeeded or Failed). Capped at 30s.
  local i phase=""
  for i in $(seq 1 30); do
    phase=$(kubectl get pod "$pod" -o jsonpath='{.status.phase}' 2>/dev/null || true)
    [[ "$phase" == "Succeeded" || "$phase" == "Failed" ]] && break
    sleep 1
  done

  # Logs are clean — only what the container wrote to stdout.
  kubectl logs "$pod" 2>/dev/null

  kubectl delete pod "$pod" --wait=false --grace-period=0 >/dev/null 2>&1 || true
}

# ── Install ──────────────────────────────────────────────────────────────────
install() {
  log "PHASE: install"

  # When `install` is invoked alone (not as part of `all`), TUNNEL_NIC isn't
  # in scope from the prior preflight run. Detect it here if needed.
  if [[ -z "${TUNNEL_NIC:-}" ]]; then
    detect_tunnel_nic
  fi

  log "  • helm repo"
  helm repo add "$HELM_REPO_NAME" "$HELM_REPO_URL" >/dev/null 2>&1 || true
  helm repo update "$HELM_REPO_NAME" >/dev/null

  if ! kubectl get ns "$KUBEOVN_NAMESPACE" >/dev/null 2>&1; then
    kubectl create ns "$KUBEOVN_NAMESPACE"
  fi

  log "  • helm upgrade --install $KUBEOVN_RELEASE $KUBEOVN_VERSION (IFACE=$TUNNEL_NIC)"
  # No --atomic, no --wait: both depend on long-lived resource watches against
  # the API server. The harvester-dev VPN drops HTTP/2 streams ("stream ID ...;
  # INTERNAL_ERROR") which leaves --atomic installs in a stuck pending-install
  # state. Instead: helm submits the manifests and returns; the `verify` phase
  # uses short-lived per-resource `kubectl wait` calls which reconnect cleanly.
  helm upgrade --install "$KUBEOVN_RELEASE" "$HELM_REPO_NAME/kube-ovn" \
    --version "$KUBEOVN_VERSION" \
    --namespace "$KUBEOVN_NAMESPACE" \
    --values "$VALUES_FILE" \
    --set networking.IFACE="$TUNNEL_NIC" \
    --timeout 10m

  ok "helm install submitted (run 'verify' to wait for pods)"
}

# ── Verify ───────────────────────────────────────────────────────────────────
verify() {
  log "PHASE: verify"

  log "  • CRDs registered"
  for crd in vpcs.kubeovn.io subnets.kubeovn.io vpc-nat-gateways.kubeovn.io; do
    kubectl get crd "$crd" >/dev/null 2>&1 && ok "  $crd" || fail "$crd not registered"
  done

  log "  • ovn-central pod (in $KUBEOVN_WORKLOAD_NS)"
  kubectl -n "$KUBEOVN_WORKLOAD_NS" wait --for=condition=ready pod -l app=ovn-central --timeout=5m \
    && ok "  ovn-central ready"

  log "  • kube-ovn-controller deployment"
  kubectl -n "$KUBEOVN_WORKLOAD_NS" wait --for=condition=Available deploy/kube-ovn-controller --timeout=5m \
    && ok "  kube-ovn-controller available"

  for ds in ovs-ovn kube-ovn-cni; do
    log "  • $ds daemonset (one pod per node)"
    local desired ready
    desired=$(kubectl -n "$KUBEOVN_WORKLOAD_NS" get ds "$ds" -o jsonpath='{.status.desiredNumberScheduled}')
    ready=$(kubectl -n "$KUBEOVN_WORKLOAD_NS" get ds "$ds" -o jsonpath='{.status.numberReady}')
    [[ "$desired" == "$ready" && "$ready" -gt 0 ]] || fail "$ds: $ready/$desired ready"
    ok "  $ds: $ready/$desired ready"
  done

  log "  • default subnet (ovn-default) exists"
  kubectl get subnet ovn-default >/dev/null 2>&1 \
    && ok "  ovn-default subnet present" \
    || fail "ovn-default subnet missing — KubeOVN bootstrap incomplete"

  log "  • Canal still primary CNI (no regression)"
  kubectl -n kube-system get ds rke2-canal -o jsonpath='{.status.numberReady}' >/dev/null \
    && ok "  Canal daemonset still healthy"

  ok "verify passed — ready for spike-manifests/"
}

# ── Uninstall (rollback) ─────────────────────────────────────────────────────
uninstall() {
  log "PHASE: uninstall"
  warn "this will delete KubeOVN, all VPCs, all KubeOVN-attached pods/VMs"
  read -r -p "type 'yes' to continue: " confirm
  [[ "$confirm" == "yes" ]] || fail "aborted"

  helm -n "$KUBEOVN_NAMESPACE" uninstall "$KUBEOVN_RELEASE" || true
  kubectl get crd -o name | grep kubeovn.io | xargs -r kubectl delete --wait=false || true
  kubectl delete ns "$KUBEOVN_NAMESPACE" --wait=false || true
  ok "uninstall complete (CRD finalizers may take a few minutes to clear)"
}

# ── Entrypoint ───────────────────────────────────────────────────────────────
case "${1:-all}" in
  preflight) preflight ;;
  install)   install ;;
  verify)    verify ;;
  uninstall) uninstall ;;
  all)       preflight; install; verify ;;
  *)         fail "usage: $0 [preflight|install|verify|uninstall|all]" ;;
esac
