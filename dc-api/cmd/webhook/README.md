# dc-api-webhook

Mutating admission webhook that fixes the F14 "clusters-on-VPC" MAC alignment
gap for Rancher-provisioned KubeVirt VMs on KubeOVN subnets.

## What it does

When Rancher provisions an RKE2 cluster node VM via HarvesterConfig, the
resulting VirtualMachine CRD carries a pinned MAC on each interface but does
not carry the OVN-specific pod-template annotations that KubeOVN's CNI reads
to allocate the same MAC for the OVN logical switch port. The mismatch breaks
L2 return-path delivery in KubeVirt bridge mode.

This webhook intercepts CREATE and UPDATE on kubevirt.io/v1 VirtualMachines,
resolves each multus network reference to its NAD, and for any NAD whose
spec.config JSON has `"type": "kube-ovn"` injects the three-annotation set:

    <nad>.<ns>.ovn.kubernetes.io/mac_address  (authoritative — KubeOVN reads this)
    <nad>.<ns>.kubernetes.io/mac_address      (legacy belt-and-suspenders)
    <nad>.<ns>.kubernetes.io/logical_switch   (routes IPAM to the correct subnet)

plus `v1.multus-cni.io/default-network` when absent. It is idempotent and
no-ops when all annotations already match.

failurePolicy is Ignore — if the webhook is down, VMs on non-OVN NADs are
unaffected. VMs on OVN NADs fall back to the pre-webhook failure mode.

## Running locally

    # Build
    cd dc-api
    go build -o /tmp/dc-api-webhook ./cmd/webhook/

    # Self-signed cert (for local testing only)
    openssl req -x509 -newkey ec -pkeyopt ec_paramgen_curve:P-256 \
      -keyout /tmp/wh.key -out /tmp/wh.crt -days 365 -nodes -subj '/CN=localhost'

    export DCWEBHOOK_CERT_FILE=/tmp/wh.crt
    export DCWEBHOOK_KEY_FILE=/tmp/wh.key
    export DCWEBHOOK_KUBECONFIG=$(base64 < ~/.kube/harvester-dev.yaml)
    /tmp/dc-api-webhook

## Environment variables

| Variable              | Required | Default | Description                                      |
|-----------------------|----------|---------|--------------------------------------------------|
| DCWEBHOOK_LISTEN_ADDR | no       | :9443   | HTTPS listen address                             |
| DCWEBHOOK_CERT_FILE   | yes      | —       | Path to TLS certificate PEM                      |
| DCWEBHOOK_KEY_FILE    | yes      | —       | Path to TLS private key PEM                      |
| DCWEBHOOK_KUBECONFIG  | yes      | —       | Base64-encoded kubeconfig (same as DCAPI_HARVESTER_KUBECONFIG) |
| DCWEBHOOK_LOG_LEVEL   | no       | info    | zerolog level: debug\|info\|warn\|error          |
