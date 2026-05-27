# M2 Networking Spike — Self-managed KubeOVN on Harvester

**Purpose:** prove upstream KubeOVN v1.15 coexists with Canal + Multus on the
Harvester cluster, and that we can drive overlay-attached VMs end-to-end via
its CRDs. Decision gate before writing any DC-API networking code.

**Scope:** lk-dev only. Throwaway artifacts. Production version is a Terraform
module under `wso2-datacenter-project` per the **DC-API Bootstrap** milestone.

See `MILESTONES.md` § M2 — Networking for the full architectural rationale.

---

## What's here

```
dc-api/deploy/kubeovn/
├── README.md                # this file
├── values.yaml              # pinned upstream KubeOVN Helm values (v1.15.4, multus secondary)
├── spike-install.sh         # idempotent installer + pre-flight + verify + uninstall
└── spike-manifests/
    ├── 00-namespace-and-vpc.yaml
    ├── 01-subnets-and-nads.yaml
    ├── 02-vms.yaml                       # two CirrOS KubeVirt VMs, one per subnet
    └── 03-acl-cross-subnet-deny.yaml     # apply only for the ACL-toggle test
```

---

## Prerequisites

- `kubectl` context pointing at the Harvester cluster (lk-dev)
- `helm` 3.x, `jq` in PATH
- Harvester's bundled KubeOVN add-on **disabled** (verified by the script)

---

## Run

```bash
cd dc-api/deploy/kubeovn

# Phase 1: pre-flight (read-only, safe)
./spike-install.sh preflight

# Phase 2: install (helm install with --atomic, rolls back on failure)
./spike-install.sh install

# Phase 3: verify (waits for readiness)
./spike-install.sh verify

# Or all three at once:
./spike-install.sh
```

The pre-flight phase checks: bundled add-on is off, `configurations.kubeovn.io`
stub CRD is compatible (or guidance to delete it), cluster pod/service CIDRs
match `values.yaml`, tunnel NIC is detected and is **not** `eno2np1` (the VM
VLAN bridge uplink), and the `openvswitch` kernel module is loadable on every
node. If anything fails, it exits without touching the cluster.

---

## Verification gates (must all pass)

After `spike-install.sh verify` succeeds, apply the test manifests and run
each gate manually:

```bash
kubectl apply -f spike-manifests/00-namespace-and-vpc.yaml
kubectl apply -f spike-manifests/01-subnets-and-nads.yaml
kubectl apply -f spike-manifests/02-vms.yaml

# Wait for both VMs to reach Running
kubectl -n kubeovn-spike get vmi -w
```

| # | Gate | How to test |
|---|---|---|
| 1 | VMs receive IPs from KubeOVN's IPAM | `kubectl -n kubeovn-spike get vmi -o wide` — IPs must be in 10.99.1.0/24 and 10.99.2.0/24 |
| 2 | Cross-subnet ping works (intra-VPC logical router routes) | `virtctl console spike-vm-a1` → `ping <a2-ip>` should succeed |
| 3 | Live-migrate spike-vm-a1 to the other node — IP and connectivity persist | `virtctl migrate spike-vm-a1 -n kubeovn-spike` then re-run the ping |
| 4 | ACL enforcement is deterministic | `kubectl apply -f spike-manifests/03-acl-cross-subnet-deny.yaml` → ping a1→a2 must fail. Revert (`kubectl apply -f spike-manifests/01-subnets-and-nads.yaml`) → ping must succeed again |
| 5 | No regression on existing bridge-NAD VMs | `kubectl get vm -A` — pre-existing VMs (dc-api/dcapi-controlplane-rke2, iaas/vm-node-01,02, rancher-infra/rancher-dev) all still Running, kubelet/Canal pods all healthy |

CirrOS login: `cirros / gocubsgo`

---

## Decision

- **All five gates pass** → green-light DC-API `NetworkProvider` interface
  design and the `kubeovn` driver. Convert this script + values into a
  Terraform module under `wso2-datacenter-project` (DC-API Bootstrap milestone).
- **Any gate fails** → fall back to **Option C**: pause M2 implementation,
  do design-only work, reassess at Harvester v1.9 GA (~Aug 2026).

Record outcome in `MILESTONES.md` Decision Log either way.

---

## Rollback

```bash
./spike-install.sh uninstall   # asks for confirmation; helm uninstall + CRD cleanup
kubectl delete ns kubeovn-spike --wait=false
```

CRD finalizers may take a few minutes. If they hang, edit them to remove the
finalizer:

```bash
kubectl patch crd vpcs.kubeovn.io -p '{"metadata":{"finalizers":[]}}' --type=merge
```

---

## Path to production

Once the spike is green, this directory's content gets translated to:

- `wso2-datacenter-project/modules/.../harvester-kubeovn/` — Terraform module
  wrapping `helm_release` + the pre-flight checks (as `null_resource` or a
  small Job) + per-region tfvars (tunnel NIC, replica counts, storage class)
- DC-API `internal/providers/kubeovn/client.go` — Go driver implementing
  `NetworkProvider`, constructs the same `Vpc` / `Subnet` / NAD CRDs at
  runtime from `POST /v1/vnets` / `POST /v1/vnets/{id}/subnets` requests

The shell-and-YAML form here is the spike artifact; it intentionally does
not pretend to be production IaC.
