# Spike — M3 Key Vault chunk 3: per-tenant OpenBao HA

**Date:** 2026-05-21
**Outcome:** GO. Approve per-tenant OpenBao HA + 2-NIC proxy as the model for M3 chunk 3 + the M4-era managed-services framework.

This spike supersedes the chunk 1+2 model (single shared OpenBao + private endpoints, see [`spike-m3-keyvault.md`](spike-m3-keyvault.md)) for everything beyond the initial CRUD stub.

---

## What we set out to prove

The chunk 1+2 design used a **single shared OpenBao instance** as the backend for every tenant's key vaults. That works for a first-iteration demo but concentrates blast radius (one snapshot restore = every tenant's vault rolls back) and forces audit-stream mingling. We need to verify the operational shape we actually want before sinking a 10-day controller build into it.

Specifically:

1. Can we run a **dedicated HA OpenBao per tenant** on the same Harvester cluster without it being silly resource-wise?
2. Does the **3-pod Raft cluster** form correctly with Longhorn-backed PVCs on Harvester?
3. Does a **2-NIC proxy pod** (Calico for the in-cluster Service path + Multus for the customer VPC) carry secret traffic end-to-end from a tenant VM in the customer VPC?
4. Do the M2.5 hierarchy + standard label set + ResourceQuota guardrails coexist with this workload, or do they fight it?

---

## TL;DR — all four prove out

| Property | Result |
|---|---|
| Helm install of OpenBao HA (3-replica Raft) on Longhorn | All three pods Ready in ~3 min |
| `bao operator init` + 2-of-3 shamir unseal of leader | Leader elected (term=3) |
| Pods 1 and 2 join leader via `bao operator raft join` + unseal with same keys | Raft list-peers shows 1 leader + 2 voting followers |
| KV-v2 mount at `tenants/<tenant_uuid>/<kv_uuid>/` + AppRole with mount-scoped policy | Login returns 1h token; token reads/writes secrets at that mount only |
| nginx TCP-stream proxy in project namespace with `k8s.v1.cni.cncf.io/networks` annotation for the project's NAD | Pod gets Calico `10.52.x.x` eth0 + Multus `10.150.0.3` net1 |
| Bastion VM in the customer VPC (`10.150.0.6`) curls `http://10.150.0.3:8200/v1/...` | AppRole login + secret read both succeed via the proxy |

Time spent: ~2 hours including the M2.5 dogfooding, the bastion-quota debug detour, and writing this doc.

---

## Architecture (as proven by the spike)

```
              Tenant VPC: spike-vnet (10.150.0.0/16)
              ───────────────────────────────────────
              Subnet spike-subnet (10.150.0.0/24)
                 ├─ bastion VM           (10.150.0.6)   ← test client
                 ├─ openbao-proxy:net1   (10.150.0.3)   ← Multus NIC into VPC
                 ├─ NAT GW pod           (F15)
                 └─ per-VPC DNS pod      (F20)
                                  │ TCP 8200
                                  ▼
              ┌──────────────────────────────────────┐
              │ openbao-proxy pod                    │
              │   Namespace dc-spike-kvi-spike-proj  │  (project ns)
              │   eth0  10.52.x.x   (Calico)         │
              │   net1  10.150.0.3  (Multus → NAD)   │
              │   nginx stream → openbao-active:8200 │
              └──────────────────────────────────────┘
                                  │ TCP 8200
                                  ▼
              ┌──────────────────────────────────────┐
              │ OpenBao 3-pod Raft StatefulSet       │
              │   Namespace dc-tenant-spike-kvi      │  (per-tenant ns)
              │   openbao-{0,1,2} (HA, leader + 2)   │
              │   PVCs on Longhorn                   │
              │   Service openbao-active             │
              └──────────────────────────────────────┘

              Inside that OpenBao instance:
                tenants/<tenant_uuid>/<vault_uuid_1>/   ← Vault A (kv-v2)
                tenants/<tenant_uuid>/<vault_uuid_2>/   ← Vault B (kv-v2)
                auth/approle/role/kv-<vault_uuid_*>     ← per-vault AppRole
```

Key isolation properties verified:

- OpenBao itself is **not reachable** from the customer VPC except via the proxy IP. The bastion can't dial the in-cluster Service IP (no route) and can't dial the proxy's Calico eth0 (no route either).
- Two AppRoles in the same OpenBao instance can only touch their own mount path — the policy is path-scoped (`path "tenants/<t>/<kv>/data/*"`). Cross-vault reads return 403.
- All cross-tenant isolation is **physical**: separate StatefulSet, separate Raft cluster, separate PVCs, separate audit stream.

---

## The actual commands that worked

For full reproducibility — keep these handy when starting the controller build.

### Spike setup (M2.5 dogfooding)

```bash
dcctl admin tenant create spike-kvi --cpu-cores-cap 16 --memory-gb-cap 32 --storage-gb-cap 300
dcctl admin tenant add-member spike-kvi --user <self> --role owner
dcctl tenant set spike-kvi
dcctl project create spike-proj --cpu-cores 4 --memory-gb 8 --storage-gb 200
dcctl project set spike-proj
dcctl vnet create spike-vnet --cidr 10.150.0.0/16 --region lk
dcctl subnet create spike-subnet --vnet spike-vnet --cidr 10.150.0.0/24

# The per-tenant namespace is NOT eagerly created by the admin handler yet —
# see "Bugs noted" below. Manual create for the spike:
kubectl --context harvester-dev create ns dc-tenant-spike-kvi
kubectl --context harvester-dev label ns dc-tenant-spike-kvi \
    dc-api.wso2.com/tenant=spike-kvi \
    dc-api.wso2.com/scope=tenant-services
```

### OpenBao install + 3-node Raft formation

```bash
helm --kube-context harvester-dev upgrade --install openbao openbao/openbao \
  --namespace dc-tenant-spike-kvi \
  --version 0.28.2 \
  --set server.ha.enabled=true \
  --set server.ha.raft.enabled=true \
  --set server.dataStorage.storageClass=longhorn \
  --set server.dataStorage.size=2Gi \
  --set server.ha.replicas=3 \
  --set server.affinity="" \
  --set "server.extraLabels.dc-api\.wso2\.com/tenant=spike-kvi" \
  --set "server.extraLabels.dc-api\.wso2\.com/scope=tenant-services" \
  --set "injector.enabled=false"

# Init leader and capture unseal keys
kubectl -n dc-tenant-spike-kvi exec openbao-0 -- \
  bao operator init -key-shares=3 -key-threshold=2 -format=json > /tmp/openbao-init.json

# Unseal leader (StatefulSet uses OrderedReady; pods 1+2 spawn after leader is Ready)
for k in $(jq -r '.unseal_keys_b64[0:2][]' /tmp/openbao-init.json); do
  kubectl -n dc-tenant-spike-kvi exec openbao-0 -- bao operator unseal "$k"
done

# Join + unseal followers — repeat for openbao-1 and openbao-2
kubectl -n dc-tenant-spike-kvi exec openbao-<i> -- \
  bao operator raft join http://openbao-0.openbao-internal:8200
for k in $(jq -r '.unseal_keys_b64[0:2][]' /tmp/openbao-init.json); do
  kubectl -n dc-tenant-spike-kvi exec openbao-<i> -- bao operator unseal "$k"
done

# Verify
BAO_TOKEN=$(jq -r .root_token /tmp/openbao-init.json) \
  kubectl -n dc-tenant-spike-kvi exec openbao-0 -- bao operator raft list-peers
```

### Per-vault mount + AppRole (what the KVI controller will do per Key Vault)

```bash
ROOT=$(jq -r .root_token /tmp/openbao-init.json)
MOUNT="tenants/<tenant_uuid>/<kv_uuid>"

bao secrets enable -path="$MOUNT" kv-v2
bao auth enable approle      # one-time per OpenBao
bao policy write kv-<kv_uuid> - <<EOF
path "$MOUNT/data/*"     { capabilities = ["create","read","update","delete"] }
path "$MOUNT/metadata/*" { capabilities = ["list","read","delete"] }
EOF
bao write auth/approle/role/kv-<kv_uuid> \
    token_policies=kv-<kv_uuid> token_ttl=1h token_max_ttl=4h
ROLE_ID=$(bao read -field=role_id auth/approle/role/kv-<kv_uuid>/role-id)
SECRET_ID=$(bao write -f -field=secret_id auth/approle/role/kv-<kv_uuid>/secret-id)

# dc-api returns (ROLE_ID, SECRET_ID) to the caller token-shown-once.
```

### 2-NIC proxy + bastion end-to-end test

Proxy manifest at `/tmp/openbao-proxy.yaml` (captured in this session's working tree). Critical bits:

```yaml
metadata:
  namespace: dc-spike-kvi-spike-proj    # project namespace
  annotations:
    k8s.v1.cni.cncf.io/networks: '[{"name":"subnet-spike-kvi-spike-proj-spike-vnet-spike-subnet","interface":"net1"}]'
spec:
  containers:
    - name: nginx
      image: nginx:1.27-alpine
      resources:                         # required — project ResourceQuota refuses pods without them
        requests: { cpu: 50m, memory: 32Mi }
        limits:   { cpu: 200m, memory: 64Mi }
```

nginx config: `stream` block, `server openbao-active.dc-tenant-spike-kvi.svc.cluster.local:8200`, `listen 8200`.

From the bastion (after `dcctl bastion create --vnet spike-vnet --subnet spike-subnet`):

```bash
ssh -i /tmp/spike-bastion.pem ubuntu@<bastion_mgmt_ip>

PROXY=10.150.0.3:8200
LOGIN=$(curl -sS -X POST http://$PROXY/v1/auth/approle/login \
    -d "{\"role_id\":\"$ROLE_ID\",\"secret_id\":\"$SECRET_ID\"}")
TOKEN=$(echo "$LOGIN" | python3 -c 'import sys,json;print(json.load(sys.stdin)["auth"]["client_token"])')

curl -sS -H "X-Vault-Token: $TOKEN" http://$PROXY/v1/$MOUNT/data/demo
# → {"data":{"data":{"password":"…","username":"…"}, …}}
```

---

## Bugs uncovered (file alongside the spike close-out)

### 1. Project storage quota math ignores CDI's prime-PVC 2x requirement

VM creation in a project namespace goes through Harvester's CDI (Containerized Data Importer) image-clone flow:

1. Real PVC of `<disk size>` requested
2. CDI creates a **prime PVC** of the same size to clone the image into
3. After import, prime is deleted and its volume is bound to the real PVC

During step 2 the project's `requests.storage` quota sees `2 × disk size`. A 40Gi rootdisk in a project with `storage_gb=50` is rejected with:

```
exceeded quota: dc-project-quota, requested: requests.storage=40Gi,
used: requests.storage=40Gi, limited: requests.storage=50Gi
```

…and the bastion sits PENDING forever (DataVolume → PVC → no virt-launcher pod).

**Fix options**, in order of how cleanly they preserve the user's mental model:

- (A) `internal/db/projects.go::CreateProject` validation: silently size project storage cap to `≥ 2 × max(disk size in catalog)` and surface the doubled value in PATCH calculations. User asks for "100Gi", they get a 200Gi backstop. Cleanest UX, hides the impedance mismatch.
- (B) Document the rule — "request 2× your max VM disk size as storage quota" — and accept that users will hit this on their first VM create. Worst UX.
- (C) Push CDI to skip the prime PVC when the source image is already pre-imported as a Longhorn image template. Investigate as a follow-up — could eliminate the doubling altogether.

This is captured as **lessons learned in CLAUDE.md** and as a wso2-datacenter-project issue under defense-in-depth (Phase 1 — cheap structural wins).

### 2. Per-tenant namespace not eagerly created

`POST /v1/admin/tenants` writes the `tenants` row but does NOT create `dc-tenant-<slug>`. M2.5 added the project handler to call `ProjectNamespaceProvisioner.EnsureProjectNamespace`; we want the same eagerness for tenant namespaces (so the KVI handler doesn't have to be defensive on every Helm release).

Fix: extract `EnsureTenantNamespace(tenantSlug, tenantUUID)` and call from the admin-tenant-create handler. Labels: `dc-api.wso2.com/tenant=<slug>`, `dc-api.wso2.com/scope=tenant-services`.

### 3. dcctl `admin tenant create` / `admin tenant cap set` printed pointer addresses

Fixed locally during the M2.5 work (added `intPtr()` helper in `dcctl/cmd/admin/tenant_create.go` and `tenant_cap.go`). Not yet committed — will land in the framework branch.

### 4. F20 per-VPC CoreDNS Corefile parse error + namespace placement

(See handoff doc bugs #4 and #5 — both surfaced during the spike, both worked around manually.)

---

## What this unlocks

| Workstream | State after spike |
|---|---|
| M3 chunk 3 — per-tenant OpenBao + per-vault mount/AppRole | Architecture proven; ready to implement |
| Managed-services framework (`internal/managedservice/`) | Shape can be locked from this pattern + the contract doc |
| KVI controller (Kubebuilder operator) | First implementation of the framework's CRD contract |
| Future managed services (databases, caches, registry, …) | Will conform to the same contract; no per-team coordination needed |

---

## Cleanup

The spike resources are left running on `harvester-dev` (cheap; we'll use them for the controller build). To tear down:

```bash
helm --kube-context harvester-dev -n dc-tenant-spike-kvi uninstall openbao
kubectl --context harvester-dev -n dc-tenant-spike-kvi delete pvc -l app.kubernetes.io/name=openbao
kubectl --context harvester-dev delete ns dc-tenant-spike-kvi
kubectl --context harvester-dev -n dc-spike-kvi-spike-proj delete pod openbao-proxy
kubectl --context harvester-dev -n dc-spike-kvi-spike-proj delete configmap openbao-proxy-config
dcctl bastion delete <id> -y
dcctl subnet delete spike-subnet
dcctl vnet delete spike-vnet --force
dcctl project delete spike-proj
dcctl admin tenant delete spike-kvi   # if the admin delete endpoint exists at this point
```
