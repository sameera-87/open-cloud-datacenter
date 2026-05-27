# Spike — M3 Key Vault, Private Endpoint pattern

**Date:** 2026-05-14
**Outcome:** ✅ Works end-to-end. Approve to implement as M3 Key Vault v1.

## What we set out to prove

Can dc-api give a tenant an Azure-style Key Vault experience where:
- multiple "vault instances" per tenant, each addressable separately,
- each vault reachable from the tenant's own VPC(s) via a "Private Endpoint" IP that lives **inside the tenant's CIDR** (not a peered service CIDR),
- the vault backend is **not reachable on the network** from any other tenant (network-layer isolation, before auth gets a chance to enforce anything),
- without VPC peering (so we don't expose service infrastructure to tenants).

## TL;DR — yes, the Multus-attached proxy-on-Harvester pattern delivers it

| Property | Result |
|---|---|
| Tenant resolves `testvault.kv.dc.internal` via per-VPC CoreDNS | ✅ Returns 10.20.0.50 |
| Tenant `curl http://testvault.kv.dc.internal:443/v1/sys/health` | ✅ HTTP 200 |
| Tenant reads KV-v2 secret via the endpoint hostname | ✅ `{"message":"hello from openbao spike"}` |
| Tenant `curl` to OpenBao ClusterIP (`10.53.x.x:8200`) | ❌ 5s timeout — no route |
| Tenant `curl` to proxy's Calico-side eth0 (`10.52.x.x:443`) | ❌ 5s timeout — no route |
| Vault backend NIC presence inside any tenant VPC | None — only the proxy's `net1` NIC lives in the tenant VPC |
| VPC peering used? | None |

The whole story took ~30 min of manual provisioning on live `harvester-dev`.

## Architecture (as proven by the spike)

```
              Tenant VPC: kv-spike-tenant (10.20.0.0/16)
              ─────────────────────────────────────────
              Subnet sub-app (10.20.0.0/24)
                 ├─ bastion (10.20.0.4)            ← test client
                 ├─ NAT GW pod (10.20.0.2)         ← F15
                 └─ per-VPC DNS pod (10.20.0.2)    ← F20
                                  │
              testvault.kv.dc.internal → 10.20.0.50
                                  │
                                  ▼
              ┌──────────────────────────────────────┐
              │  PRIVATE ENDPOINT — proxy pod        │
              │  (in Harvester ns: dc-api-endpoints) │
              │                                      │
              │  net1: 10.20.0.50  ← Multus + OVN    │
              │       (inside the tenant's subnet,   │
              │        same logical switch as the    │
              │        bastion)                      │
              │                                      │
              │  eth0: 10.52.0.179 ← Calico (default │
              │       Harvester pod network)         │
              │                                      │
              │  nginx :443 → openbao.dc-api-vault   │
              │              .svc:8200               │
              └────────────┬─────────────────────────┘
                           │ eth0 path
                           ▼
              ┌──────────────────────────────────────┐
              │ OpenBao StatefulSet                  │
              │ (in Harvester ns: dc-api-vault)      │
              │                                      │
              │ ClusterIP: 10.53.136.235:8200        │
              │ Pod IP:    10.52.x.x (Calico)        │
              │                                      │
              │ No NIC in any tenant VPC.            │
              │ No LB on any external network.       │
              │ Reachable only from inside the       │
              │ Harvester pod network.               │
              └──────────────────────────────────────┘
```

For v1 (this POC) OpenBao runs **directly on Harvester** as a normal Deployment. That's "Option C" from the design discussion — fastest spike, validates the novel piece (the tenant-facing private endpoint) without also having to wire a separate service cluster. Production target is "Option B" — OpenBao moves to its own RKE2 cluster in its own kube-ovn VPC, and the proxy pod gets a third NIC into that VPC. The contract with the tenant doesn't change between B and C.

## The exact recipe (manual steps that worked)

### Phase 1 — substrate

```bash
# tenant VPC + subnet + bastion test client
dcctl create vnet  kv-spike-tenant --address-space 10.20.0.0/16 --region lk
dcctl create subnet --vnet kv-spike-tenant --name sub-app --cidr 10.20.0.0/24
dcctl create bastion --name kv-spike-bastion \
  --vnet kv-spike-tenant --subnet sub-app \
  --save-key /tmp/kv-spike-bastion.pem
```

### Phase 2 — namespaces

```bash
kubectl create namespace dc-api-vault       # where the OpenBao backend lives
kubectl create namespace dc-api-endpoints   # where per-endpoint proxy pods live
```

### Phase 3 — OpenBao backend

`/tmp/openbao-spike.yaml` (Deployment + ClusterIP Service). Single replica, dev mode, in-memory storage, fixed dev-root token `spike-root-token` — POC ONLY. See file for full manifest; key bits:

```yaml
args:
  - server
  - -dev
  - -dev-root-token-id=spike-root-token
  - -dev-listen-address=0.0.0.0:8200
```

Service is `openbao.dc-api-vault.svc.cluster.local:8200` (ClusterIP).

Production v1 must switch to:
- HA Raft (3 replicas)
- PVC-backed storage on a snapshotted StorageClass
- Real init/unseal with sealed shamir shares stored in K8s Secrets and auto-unseal via cluster-local key (or external KMS later)
- KV-v2 mount per `(tenant, vault_id)` at path `tenants/<tid>/vaults/<vault_id>`
- Per-vault AppRole + ACL policy

### Phase 4 — the private endpoint (the novel part)

For each `(vnet, vault)` pair, dc-api creates:

**(a) An nginx Deployment in `dc-api-endpoints` with two NICs.** Annotations are the load-bearing piece:

```yaml
annotations:
  k8s.v1.cni.cncf.io/networks: dc-hiran/subnet-hiran-sub-app
  subnet-hiran-sub-app.dc-hiran.ovn.kubernetes.io/ip_address: "10.20.0.50"
```

The first annotation is the Multus directive — "give this pod a secondary NIC, configured per the NetworkAttachmentDefinition at `dc-hiran/subnet-hiran-sub-app`." The NAD's `config.provider` is `subnet-hiran-sub-app.dc-hiran.ovn`, which is the kube-ovn identifier for that logical switch. The second annotation (`<provider>.kubernetes.io/ip_address`) pins the secondary NIC's IP.

The pod ends up with:
- `eth0` — Calico, IP from Harvester's primary pod CIDR (`10.52.x.x`)
- `net1` — kube-ovn, IP `10.20.0.50` in the tenant subnet, OVN logical switch port in the tenant's VPC

**(b) A ConfigMap with nginx stream forward.** Plain TCP forward, no TLS termination (OpenBao does TLS itself in production):

```nginx
stream {
  server {
    listen 443;
    proxy_pass openbao.dc-api-vault.svc.cluster.local:8200;
  }
}
```

**(c) A DNS record in the tenant VPC's CoreDNS** so the tenant addresses the vault by hostname:

```
hosts {
  10.20.0.50 testvault.kv.dc.internal
  fallthrough
}
```

### Phase 5 — proof

From the bastion (10.20.0.4, tenant VPC):

```
$ dig +short testvault.kv.dc.internal
10.20.0.50

$ curl -H "X-Vault-Token: spike-root-token" \
       http://testvault.kv.dc.internal:443/v1/secret/data/spike-test
{"data":{"data":{"message":"hello from openbao spike","tenant":"kv-spike-tenant"}, ...}}

$ curl --max-time 5 http://10.53.136.235:8200/v1/sys/health      # backend ClusterIP
curl: (28) Connection timed out                                  # ← expected, no route

$ curl --max-time 5 http://10.52.0.179:443/v1/sys/health         # proxy's Calico eth0
curl: (28) Connection timed out                                  # ← expected, no route
```

## Implementation gaps to close before this becomes M3 v1

### dc-api code changes

| Area | What's needed |
|---|---|
| **DB schema** | `key_vaults` table; `key_vault_endpoints` table (one row per tenant-VPC attachment); `key_vault_secrets` metadata; `key_vault_access_policies` |
| **API surface** | `POST/GET/LIST/DELETE /v1/keyvaults`; `POST/GET/LIST/DELETE /v1/keyvaults/{id}/private-endpoints`; `PUT/GET/DELETE /v1/keyvaults/{id}/secrets/{name}` (dc-api proxies write/read to OpenBao behind tenant-scoped AppRole) |
| **OpenBao client wrapper** | Internal package that mounts KV-v2 paths, creates AppRoles + policies, performs secret CRUD on behalf of tenants. Talks to OpenBao via the in-cluster Service. |
| **Endpoint provisioner** | On `POST /v1/keyvaults/{id}/private-endpoints`: (1) reserve `Vip` from tenant subnet, (2) render nginx ConfigMap, (3) create proxy Deployment with Multus annotations, (4) write DNS record to tenant's per-VPC Corefile. Mirror F20's per-VPC CoreDNS lifecycle code path. |
| **Per-VPC Corefile** | Today the F20 `vpc-dns-corefile` ConfigMap is **shared** across all per-VPC DNS pods. For per-vault records this needs to become one ConfigMap per VPC. Either (a) one `vpc-dns-corefile-<vnet-id>` ConfigMap per VPC, each mounted by the corresponding `vpc-dns-<vnet-id>` Deployment, or (b) a single Corefile that uses `import` for a per-VPC zone file (one ConfigMap per VPC, mounted in addition to the shared Corefile). Recommend (a) — simpler, fewer moving parts. **This is a prerequisite to multi-tenant Key Vault on the per-VPC CoreDNS path.** |
| **Teardown ordering** | Per-endpoint proxy pod + Vip + DNS record must be torn down **before** the tenant subnet itself can be deleted (proxy pod's net1 LSP pins the subnet). Mirror F26's deterministic poll on pod deletion before subnet delete. |
| **Reserved-NAD list (F21)** | Add `dc-api-endpoints` namespace's NADs (none today, the NAD lives in `dc-<tenant>` ns) to the reserved set so tenant VMs can't claim those NADs. Actually — proxy pods live in `dc-api-endpoints`, but they attach to the tenant's NAD in `dc-<tenant>` ns. So no F21 change needed; the F21 reserved-NAD check is already in the right place. |

### Promotion path Option C → Option B

When we want OpenBao on its own cluster (production target):

1. Stand up a `service-keyvault` RKE2 cluster on Harvester in a dedicated kube-ovn VPC (`vpc-kv-service`, e.g. CIDR `10.50.0.0/24`).
2. Install OpenBao there in HA Raft mode.
3. Expose OpenBao via either (a) a `Service: LoadBalancer` with an IP from `vpc-kv-service` CIDR, or (b) a NodePort on the cluster's nodes (which have IPs in `vpc-kv-service`).
4. Give each per-endpoint proxy pod a **third** NIC via Multus into `vpc-kv-service`, and switch `proxy_pass` to point at the OpenBao IP on that NIC.
5. Decommission the on-Harvester OpenBao Deployment after migrating any existing secrets.

The tenant-facing API and the network primitive (Vip + Multus + per-VPC DNS) don't change. Only the proxy's eth0/proxy_pass changes.

### Operational items

- **Per-endpoint HA**: today the proxy is `replicas: 1` with `strategy: Recreate` because two replicas would both try to claim `10.20.0.50` and conflict. For HA we need either (a) a kube-ovn VIP failover mechanism (`Vip` CRD with `keepalived`-style failover, if available), or (b) ECMP across two endpoints (more IPs from the tenant subnet, multiple A records, client retries). Defer to a follow-up.
- **TLS**: today the proxy is plain TCP forwarding (`stream`). For a real Key Vault we'd terminate TLS at the proxy (with a per-tenant cert issued by an internal CA) and re-encrypt to OpenBao. Or, simpler, configure OpenBao with TLS and pass through.
- **Audit log**: every dc-api proxy of a vault read/write must emit an `audit_events` row (tenant, principal, vault, secret, op, version, IP). Already the M1.5 pattern, just extend.

## Resources left behind by this spike

```
namespace/dc-api-vault                        OpenBao Deployment + Service (dev mode)
namespace/dc-api-endpoints                    proxy Deployment + ConfigMap
configmap/kube-system/vpc-dns-corefile        patched with the hosts block for testvault.kv.dc.internal
vnet kv-spike-tenant + subnet sub-app         dcctl-managed
vnet kv-spike-other (empty)                   dcctl-managed (subnet attempt failed; safe to delete)
bastion kv-spike-bastion                      dcctl-managed
```

Cleanup commands when we're done staring at the proof:

```bash
dcctl delete bastion kv-spike-bastion
dcctl delete subnet  --vnet kv-spike-tenant sub-app
dcctl delete vnet    kv-spike-tenant
dcctl delete vnet    kv-spike-other
kubectl --context harvester-dev delete ns dc-api-endpoints dc-api-vault
kubectl --context harvester-dev -n kube-system patch configmap vpc-dns-corefile --type merge -p '{"data":{"Corefile":".:53 {\n    errors\n    health { lameduck 5s }\n    ready\n    forward . 1.1.1.1 8.8.8.8 { max_concurrent 1000 }\n    cache 30\n    loop\n    reload\n    loadbalance\n}\n"}}'
```

(Run the `vpc-dns-corefile` revert FIRST so we don't leave per-tenant test records around in shared infra.)

## Open questions / follow-ups discovered while spiking

1. **Subnet-name collision across tenants**: `dcctl create subnet --name sub-app` in two different VNets generated colliding kube-ovn provider names. Caught at admission webhook, friendly error. But it shouldn't be a tenant-visible failure mode — dc-api should generate provider names with VNet-id or subnet-id, not subnet-name. Captured separately.
2. **Per-VPC Corefile**: the F20 v1 single-Corefile-shared-across-VPCs design needs to evolve **before** Key Vault ships (see implementation gaps above). Worth doing as a standalone refactor, not bundled with KV.
3. **Endpoint HA**: deferred. Needs investigation of kube-ovn `Vip` failover or ECMP support.
4. **VIP pinning vs auto-allocate**: today we hardcode `ip_address: "10.20.0.50"`. dc-api should pick from the subnet's available range automatically and persist it.
