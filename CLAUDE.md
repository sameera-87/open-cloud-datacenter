# CLAUDE.md — Sovereign Cloud Control Plane

## THE BIG PICTURE (read this first, every time)

We are building a **Sovereign Cloud Control Plane** for the WSO2 LK Datacenter.
The goal: replace raw Terraform module delivery with a **Cloud Provider experience** —
developers use `dcctl` like they use `aws` or `az`, with no Terraform knowledge required.

```
dcctl  →  Asgardeo (OIDC auth)  →  DC-API  →  PostgreSQL (state)
                                          →  Harvester (VMs via KubeVirt CRDs)
                                          →  Rancher   (RKE2 clusters via REST v3)
```

**When we go down a rabbit hole** (debugging, testing, fixing a specific thing),
always return to this question: *Does this unblock the current milestone?*
See [MILESTONES.md](MILESTONES.md) for current status and next actions.

---

## Repositories

| Repo | Purpose |
|---|---|
| `HiranAdikari/sovereign-cloud` (this repo) | DC-API server + dcctl CLI (monorepo) |
| `wso2/open-cloud-datacenter` | Shared TF modules (being superseded by DC-API) |
| `wso2-enterprise/wso2-datacenter-project` | Live LK environment TF (private) |

**Before making any changes that touch `open-cloud-datacenter` or
`wso2-datacenter-project`, read the `CLAUDE.md` in each of those repos first.**
They establish the two-repo split (source modules vs consumer layers), branching
policy, cross-repo versioning, and the `terraform fmt`/`validate` commands.

---

## Project Structure

```
sovereign-cloud/
├── dc-api/                         Go REST API server
│   ├── cmd/dc-api/main.go          Entry point — wires everything, graceful shutdown
│   ├── go.mod                      module: github.com/wso2/dc-api, go 1.26
│   ├── Dockerfile                  Multi-stage; ships distroless/static:nonroot
│   ├── deploy/                     Kubernetes manifests — historical only; see "Deployment is Terraform-driven" below
│   │   ├── deployment.yaml         (legacy reference; live deployment managed by TF)
│   │   ├── ingress.yaml            (legacy reference)
│   │   ├── ingress-lb.yaml         (legacy reference)
│   │   ├── postgres.yaml           (legacy reference)
│   │   ├── configmap.yaml          (legacy reference)
│   │   ├── runner-rbac.yaml        (legacy reference)
│   │   ├── arc-runner-values.yaml  (legacy reference)
│   │   └── create-secrets.sh       DEPRECATED — use Terraform (see below)
│   └── internal/
│       ├── config/config.go        Env-var config (DCAPI_* prefix, 12-factor)
│       ├── models/resource.go      Domain types: Resource, VMSpec, ClusterSpec, Quota
│       ├── db/
│       │   ├── schema.sql          PostgreSQL DDL (resources, audit_events, quotas)
│       │   ├── db.go               ResourceRepository (Repository Pattern)
│       │   └── migrate.go          Applies schema on startup if tables don't exist
│       ├── providers/
│       │   ├── interface.go        ComputeProvider + ClusterProvider interfaces (Strategy Pattern)
│       │   ├── factory.go          NewComputeProvider / NewClusterProvider (Factory Pattern)
│       │   ├── harvester/client.go Harvester driver — Kubernetes dynamic client, KubeVirt CRDs
│       │   └── rancher/client.go   Rancher driver — REST v3 API
│       ├── api/
│       │   ├── middleware/auth.go  Asgardeo JWT validation → tenantID in context (Middleware Chain)
│       │   ├── handlers/
│       │   │   ├── vm.go           POST/GET/LIST/DELETE /v1/virtual-machines + images + networks
│       │   │   └── cluster.go      POST/GET/LIST/DELETE/kubeconfig /v1/clusters
│       │   └── router.go           Chi router composition root (Dependency Injection)
│       └── reconciler/
│           └── reconciler.go       Background goroutine: PENDING/DELETING → real provider state
│
├── dcctl/                          Cobra CLI
│   ├── main.go
│   ├── go.mod                      module: github.com/wso2/dcctl, go 1.22
│   ├── cmd/
│   │   ├── root.go                 Cobra root + Viper config layering
│   │   ├── login.go                dcctl login — OIDC Authorization Code + PKCE flow
│   │   ├── logout.go               dcctl logout — clears local credentials
│   │   ├── kubeconfig.go           dcctl kubeconfig <cluster-id>
│   │   ├── create/                 create vm | cluster | image
│   │   ├── get/                    get vm | cluster
│   │   ├── list/                   list vms | clusters | images
│   │   └── delete/                 delete vm | cluster
│   └── internal/
│       ├── auth/oidc.go            PKCE flow implementation (code verifier/challenge, callback server)
│       ├── config/config.go        ~/.dcctl/config.yaml + ~/.dcctl/credentials.json
│       └── client/client.go        DC-API HTTP client (injects Bearer token)
│
├── .github/workflows/deploy.yaml   CI/CD: build, push to ghcr.io, rollout via in-cluster ARC runner
├── docs/
│   ├── dc-api-architecture.md      ⭐ Technical architecture & component overview (start here)
│   ├── dc-api-internal-proposal.md Internal proposal — the "why"
│   ├── ops-bootstrap.md            Step-by-step runbook for standing up DC-API in a new region
│   ├── asgardeo-setup.md           Identity-provider setup guide (free Asgardeo account walk-through)
│   └── *.docx                      Pandoc-rendered copies for sharing in Google Docs
├── CLAUDE.md                       This file
└── MILESTONES.md                   Milestone plan — UPDATE THIS as work progresses
```

---

## Design Patterns (understand before editing)

| Pattern | File | Rule |
|---|---|---|
| **Strategy** | `providers/interface.go` | Handlers only see the interface. Never import `harvester` or `rancher` packages from handlers. |
| **Factory** | `providers/factory.go` | Adding a new provider = new case here + new `internal/providers/<name>/client.go`. Nothing else changes. |
| **Repository** | `db/db.go` | All SQL lives here. Handlers call `repo.Create()`, never `pool.Query()` directly. |
| **Dependency Injection** | `router.go` → `handlers/` | Handlers receive `repo` and `provider` via constructor. Never instantiate them inside a handler. |
| **Middleware Chain** | `middleware/auth.go` | Auth runs before every `/v1/*` handler. Handlers read tenant/user from `context`, never from the token. |

---

## API contract — `dc-api/openapi.yaml` is the source of truth

Every public endpoint of dc-api is described in `dc-api/openapi.yaml`
(OpenAPI 3.0.3). The spec is **the contract** between dc-api and every
client we own or plan to own. Rules:

1. **Spec and handler ship together.** A PR that changes a handler's
   path, method, request/response shape, status code, or auth
   requirement MUST update `openapi.yaml` in the same commit. Reviewers
   reject the PR if it doesn't.
2. **The spec leads, the code follows.** When designing a new endpoint,
   write the spec entry first (or at least concurrently) — don't wait
   until the handler is "done." This is what api-designer is for.
3. **Validate before merge.** Run `npx @redocly/cli lint
   dc-api/openapi.yaml` after edits. The spec must still pass.
4. **Don't add schemas you can't use.** If a `components/schemas/X` is
   never referenced by any operation, redocly flags it as unused and
   it's noise. Either wire the endpoint that returns it or remove the
   schema until you need it.

### Consumers of the spec (so you know what breaks)

| Consumer | How it consumes the spec | What breaks if you change the spec without updating it |
|---|---|---|
| **cloud-ui** | `pnpm gen:api` → `src/api/generated/types.ts` (auto-runs on `predev` / `prebuild`); `openapi-fetch` makes the runtime calls | TypeScript build fails — exactly the early-warning we want |
| **dcctl** | `oapi-codegen` Go client at `dcctl/internal/client/generated/`. Regen with `go generate ./internal/client/generated/...`. Hand-written wrapper at `internal/client/{client,vnet}.go` provides convenience helpers. | TypeScript-equivalent build failure — Go compile-error against the regenerated typed methods. |
| **terraform-provider-dcapi** (planned) | Will consume the same Go codegen as dcctl | n/a yet |
| **Contract tests** | Schemathesis hits an in-process dc-api with nopped-out backends. Runs on every push to main + on PR via `.github/workflows/contract.yaml`. Tag-scoped via `tagRegex` in `dc-api/test/contract/contract_test.go` to the operations that don't need a live Harvester/Rancher cluster (today: `health\|keyvaults\|members\|projects\|service-accounts\|tenants`). | CI failure on the contract job. |

### Regen workflow (cloud-ui)

```bash
cd cloud-ui
pnpm gen:api          # writes src/api/generated/types.ts (gitignored)
pnpm exec tsc --noEmit   # any type drift surfaces here
```

`predev` and `prebuild` invoke `gen:api` automatically — you never have
to remember.

### Adding a new endpoint, end to end

1. Design the request/response shape with `api-designer`. Add it to
   `openapi.yaml`. Lint it.
2. Implement the Go handler. Run `go build ./...`.
3. Wire it into `router.go`.
4. Write the integration test under `dc-api/test/integration/`.
5. Bump cloud-ui types: from `cloud-ui/`, run `pnpm gen:api && pnpm
   exec tsc --noEmit && pnpm lint`. If the UI uses the new endpoint,
   write the calling code now while the spec is fresh in your head.
6. If the dcctl CLI also exposes it, hand-update
   `dcctl/internal/client/` (until codegen replaces the hand-written
   layer).
7. Single PR, all of it.

---

## Core Architectural Principles (never violate these)

1. **DC-API's hierarchy is independent of Rancher and Harvester.**
   Users see: `Tenant → Project → Resource` (GCP-like — reshaped from the
   earlier "Subscription/Resource Group" plan; see M2.5 below). Rancher
   "projects", Harvester "namespaces", KubeVirt — these are internal
   plumbing. They must never appear in the public API or CLI output.

2. **Rancher and Harvester credentials never leave DC-API.**
   DC-API holds one master Harvester kubeconfig and one Rancher admin token.
   Teams get DC-API roles (owner/member/viewer at tenant scope; project scope
   coming when the role-assignments scope chain is exercised end-to-end).
   No team ever gets direct Rancher or Harvester access via DC-API.

3. **Tenant + project isolation is multi-layer.**
   Every per-tenant DB row carries both `tenant_uuid` (Phase 6a) and
   `project_uuid` (M2.5) — these are the immutable filters in every repo
   query. The slug (`tenant_id` text + `project_id` text) is the
   human-readable handle; UUIDs are what actually gate access. Within a
   project, capacity (cpu_cores/memory_gb/storage_gb) and object counts
   (max_vnets/max_clusters/max_volumes) are enforced at the dc-api layer
   AND mirrored as a Kubernetes `ResourceQuota` on the project namespace
   (defense-in-depth). RBAC is per-role via `role_assignments` whose
   `scope_type` is polymorphic (today: `tenant`; project-scoped role
   assignments use the same row shape — `scope_id=<project-slug>`,
   `scope_uuid=<project_uuid>`). See `docs/rbac.md`.

4. **Tenant capacity ceilings live on the tenant row.**
   `tenants.{cpu_cores_cap, memory_gb_cap, storage_gb_cap}` is the platform-
   admin-set ceiling per tenant; tenant owners distribute that budget across
   projects via per-project quotas. Project create/PATCH validates
   `sum(project quotas) ≤ tenant cap` inside a transaction with
   `SELECT FOR UPDATE` on the tenants row (prevents two concurrent project
   creates from sliding past the check). Admin PATCH to shrink the cap is
   refused if it would drop below already-allocated project sums. See
   `internal/db/projects.go::CreateProject` and `internal/db/tenants.go::UpdateTenantCap`.

5. **Swapping a backend must not change the API.**
   Replace Harvester with OpenStack? New provider struct, zero API changes.
   This is why the Strategy Pattern exists. Don't couple handler code to providers.

---

## Key Decisions (don't re-debate these without good reason)

1. **Async provisioning**: `POST /v1/virtual-machines` returns `202 Accepted` immediately. Caller polls `GET /{id}` for status. Reason: VM creation takes 2-5 minutes; synchronous would time out.
2. **DC-API generates SSH keys**: Caller never provides a public key. We generate ECDSA P-256, inject via cloud-init, return private key ONCE in the response. Never stored server-side.
3. **PKCE, no client secret in dcctl**: CLI binary is public. Embedding a secret is insecure.
4. **Rancher REST API directly** (not rancher2 TF provider): TF provider has RKE2 cluster creation bugs. REST API is what the UI uses.
5. **Harvester via Kubernetes dynamic client** (not HTTP): Harvester VMs are KubeVirt CRDs. `kubectl apply` is the right interface.
6. **One PostgreSQL row per resource**: State is owned by DC-API, not by Harvester/Rancher. Drift detection is possible because we have a canonical registry.
7. **Go module path `github.com/wso2/dc-api`**: Even though the repo is under `HiranAdikari`, this is the logical module name. Change with `go mod edit -module` if needed when open-sourcing.

---

## Environment Variables (DC-API)

All prefixed with `DCAPI_`:

| Variable | Required | Example |
|---|---|---|
| `DCAPI_DB_URL` | yes | `postgres://dc_api:secret@localhost:5432/dc_api?sslmode=disable` |
| `DCAPI_OIDC_ISSUER` | yes | `https://api.asgardeo.io/t/wso2` |
| `DCAPI_OIDC_AUDIENCE` | yes | comma-separated client IDs to accept, e.g. `dcctl-cid,cloud-ui-cid` |
| `DCAPI_HARVESTER_KUBECONFIG` | yes | base64-encoded kubeconfig string |
| `DCAPI_HARVESTER_NAMESPACE` | no | `default` |
| `DCAPI_RANCHER_URL` | yes | `https://rancher.internal.wso2.com` |
| `DCAPI_RANCHER_TOKEN` | yes | `token-xxxxx:yyyyyyy` |
| `DCAPI_RANCHER_INSECURE` | no | `true` (dev only — self-signed cert) |
| `DCAPI_VM_PROVIDER` | no | `harvester` |
| `DCAPI_CLUSTER_PROVIDER` | no | `rancher` |
| `DCAPI_TENANT_GROUP_PREFIX` | no | `dc-tenant-` (Asgardeo group prefix for tenant mapping) |
| `DCAPI_ADMIN_GROUP` | no | `dc-admin` (Asgardeo group for platform admins) |
| `DCAPI_RBAC_AUTOPROVISION` | no | `true` (M1.5; auto-grant member role on first login with valid tenant group) |
| `DCAPI_LOG_LEVEL` | no | `info` |
| `DCAPI_LISTEN_ADDR` | no | `:8080` |
| `DCAPI_VPC_EXTERNAL_BRIDGE` | yes (if KubeOVN) | `br-ex` (uplink bridge on Harvester nodes for the external provider network) |
| `DCAPI_VPC_EXTERNAL_CIDR` | yes (if KubeOVN) | `203.0.113.0/24` (full CIDR of the external network, including gateway) |
| `DCAPI_VPC_EXTERNAL_GATEWAY` | yes (if KubeOVN) | `203.0.113.1` (gateway IP for the external network; must be in CIDR) |
| `DCAPI_VPC_EXTERNAL_RESERVED_IPS` | no | `192.168.10.15,192.168.10.17,192.168.10.37,192.168.10.38` (comma-separated IPs to exclude from KubeOVN's IPAM — host nodes, ingress LB, anything already pinned on the external network) |
| `DCAPI_VPC_EXTERNAL_VLAN_ID` | no | `0` (VLAN tag for the external network; 0 = untagged) |

---

## Build & Run

```bash
# DC-API
cd dc-api
go mod tidy
go build ./cmd/dc-api/
./dc-api   # needs DCAPI_* env vars

# dcctl
cd dcctl
go mod tidy
go build -o ~/bin/dcctl .

# First use
dcctl login
dcctl vm create --name web-01 --size medium \
  --image default/image-rflb5 --network default/vm-net-100 \
  --save-key ~/.ssh/web-01.pem
```

---

## Deployment is Terraform-driven (source of truth)

The live `dc-api` deployment, its `dc-api-secrets` Secret, postgres,
namespace, and the ARC runner are **managed by Terraform** in the
[`wso2-datacenter-project`](https://github.com/wso2-enterprise/wso2-datacenter-project)
repo. The `dc-api/deploy/{namespace,postgres,configmap,runner-rbac,arc-runner-values}.yaml`
files and `create-secrets.sh` in this repo are historical skeletons — they
are NOT applied to any cluster and editing them changes nothing live.

The exceptions, applied by CI on every push to `main` that touches
`dc-api/**` or `cloud-ui/**`:

| File | Why CI applies it |
|---|---|
| `dc-api/deploy/deployment.yaml` | The dc-api Deployment shape (replicas, probes, container spec). CI then `kubectl set image` with the new SHA tag to roll out. |
| `dc-api/deploy/ingress.yaml` | The dc-api Ingress hostname. Changes here ship via CI; keep in sync with the TF ingress hostname so the two don't fight on re-apply. |
| `cloud-ui/deploy/deployment.yaml` | The cloud-ui Deployment shape. Same dual-ownership pattern as dc-api — TF creates it on cluster bring-up; CI re-applies and `kubectl set image`s on every push. |
| `cloud-ui/deploy/service.yaml` | cloud-ui ClusterIP Service. |
| `cloud-ui/deploy/ingress.yaml` | cloud-ui Ingress (cloud.lk-dev.internal.wso2.com). Has the `tls.secretName: dc-api-tls` block so it reuses the dc-api self-signed cert via SAN — keep this in sync with the TF ingress resource. |

`dc-api/deploy/configmap.yaml` is **explicitly skipped** by CI because the
file is a stale skeleton missing `DCAPI_BFF_*` and `DCAPI_VPC_EXTERNAL_*`
keys — applying it would crashloop dc-api on next restart. ConfigMap
changes must go through the TF module.

**Drift rule for the dual-applied files above**: the TF module is the
source of truth for Deployment/Service/Ingress *shape*; CI workflows
apply matching manifests on every push. The lifecycle ignores `image`
on each Deployment so `kubectl set image` doesn't fight TF. Any OTHER
change to a CI-applied file (env vars, probes, security context, TLS
config) must be mirrored on the TF side AND the manifest in the repo,
or the next CI apply will silently revert TF's change.

| Need to change | Where |
|---|---|
| Any `DCAPI_*` environment variable (audience, issuer, Rancher token, …) | `wso2-datacenter-project/environments/lk-dev/02-dc-controlplane-services/dc-api.tf` (call site) and the underlying module at `modules/open-cloud-datacenter/modules/management/dc-controlplane-services/` |
| Asgardeo applications + their client IDs | `wso2-datacenter-project/environments/lk-dev/03-asgardeo-auth/main.tf` (resources) and `outputs.tf` (so other layers can read them) |
| The dc-api image tag rolled out to the cluster | `02-dc-controlplane-services/variables.tf` → `dc_api_image`, then `terraform apply` |
| The dc-api Deployment shape (replicas, probes, env, …) | `modules/open-cloud-datacenter/modules/management/dc-controlplane-services/main.tf` — and bump the OCD module tag in the consumer per the wso2-datacenter-project CLAUDE.md SemVer policy |
| Ingress hostname, TLS, etc. | Same module |
| Postgres user / DB | Same module |

### Workflow when changing a Secret value (e.g. adding an OIDC audience)

```bash
cd /path/to/wso2-datacenter-project/environments/lk-dev/02-dc-controlplane-services
terraform fmt
terraform validate
terraform plan
terraform apply
# kubernetes_secret triggers a rollout automatically because the Deployment
# references the secret via envFrom; no manual kubectl restart needed.
```

**Do not** `kubectl edit secret dc-api-secrets` or run `dc-api/deploy/create-secrets.sh`
directly — TF will overwrite both on the next apply.

The two-repo split is documented in `wso2-datacenter-project/CLAUDE.md`:
the **module** (in `open-cloud-datacenter`) is the source; the **layer** (in
`wso2-datacenter-project`) is the consumer/instance. Module changes are
versioned via SemVer tags; the consumer pins a specific tag.

### `tf.sh` + `dependencies.yaml` — how the consumer pulls OCD

`wso2-datacenter-project/tf.sh` is the canonical wrapper for running
terraform in any layer. Before it runs `terraform plan/apply`, it reads
`environments/<region>/dependencies.yaml`, which looks like:

```yaml
versions:
  - src: https://github.com/wso2/open-cloud-datacenter.git
    tag: v0.8.0
```

For each entry it ensures `environments/<region>/modules/<repo>/` is
cloned and **pinned to `tag`**, via:

```
git reset --hard --quiet
git checkout "$tag" --force --quiet
```

Two things follow from this that are easy to forget:

1. **The local OCD checkout is ephemeral.** Any edits you make under
   `environments/<region>/modules/open-cloud-datacenter/` are wiped on
   the next `tf.sh` run. Always edit OCD in the *source* repo
   (`open-cloud-datacenter`), commit, push to a branch tf.sh can fetch,
   then point `dependencies.yaml` at that ref.

2. **`tag:` accepts any git ref, not just SemVer tags.** A branch name
   (e.g. `feature/dc-controlplane-3node-ha`) is fine for local testing
   before the OCD PR merges + a new tag is cut. Standard flow:

   - Working: bump `dependencies.yaml` to `tag: <branch-name>` on the
     consumer's feature branch, push OCD branch so tf.sh can fetch it.
   - Merging: once OCD PR merges and a new SemVer tag is cut, bump
     `dependencies.yaml` to `tag: vX.Y.Z` and update the consumer PR.

`tf.sh` also auto-installs missing tools (terraform, aws, bw, helm) and
auto-fetches `*.secret.tfvars` from AWS Secrets Manager — never write a
`*.secret.tfvars` file by hand. Add new secrets to the
`*.secret.tfvars.template` and to AWS SM; tf.sh fills the rest.

---

## Before pushing changes

CI runs unit tests on every push to `main`, but **integration tests are not yet
gated in CI** (they need the live Harvester+KubeOVN cluster). Before pushing
any change that touches the kubeovn driver, the network handlers, the DB layer,
or the auth middleware, run the integration suite locally:

```bash
# VPN to harvester-dev required (the suite hits the real cluster).
cd dc-api
KUBECONFIG=$HOME/.kube/config KUBE_CONTEXT=harvester-dev \
  go test -tags integration -timeout 20m ./test/integration/...
```

Expected: 28 PASS / 0 FAIL / 1 SKIP. See `dc-api/test/integration/README.md`
for what each test covers.

For changes that don't touch those areas (CLI, docs, deploy manifests),
running `go test ./...` (unit tests only) is enough.

### Spec-diff check for CR-build code

Whenever dc-api generates a Kubernetes resource that mirrors a proven
live object — Rancher `provisioning.cattle.io/Cluster`,
`HarvesterConfig`, the `harvesterconfig-<name>` Secret, KubeOVN `Vpc` /
`Subnet`, `NetworkAttachmentDefinition`, `VpcNatGateway`, the per-VPC
CoreDNS `Deployment` / `ConfigMap`, … — the PR MUST include a
structural diff against a known-working reference. Unit tests pass
on the shape we *think* is right; the live cluster doesn't lie.

The recipe:

```bash
# 1. Capture what the dc-api builder produces (e.g. via a unit test that
#    Marshals the unstructured.Unstructured to YAML, or a one-off
#    print-and-exit in the handler).
go test -tags 'integration cr_capture' -run TestCaptureCR ./test/integration/... > /tmp/generated.yaml

# 2. Capture the working reference from the cluster (one that's known
#    healthy — e.g. dcapi-controlplane-rke2 for clusters, an active
#    tenant VPC for kubeovn objects).
kubectl --context=harvester-dev get <kind> <name> -n <ns> -o yaml \
    | yq 'del(.metadata.resourceVersion, .metadata.uid,
              .metadata.generation, .metadata.creationTimestamp,
              .metadata.managedFields, .status)' > /tmp/reference.yaml

# 3. Diff. Empty is ideal; every non-empty hunk needs a one-line
#    justification in the PR description ("needed for our multi-tenant
#    model", "tuning we deliberately don't carry forward").
diff -u /tmp/reference.yaml /tmp/generated.yaml
```

This is a process rule, not a CI gate. It costs about five minutes
per PR and would have caught all four of the F32 chunk-2 bugs
(wrong Steve URL kind, networkInfo shape, missing cloud-init
bootcmd, missing machineSelectorConfig) in a single review pass.
Unit-test-clean ≠ structurally correct against the live API.

---

## What Exists vs What's TODO

### M1 — Done and running in LK dev
- [x] Domain models, PostgreSQL schema + repository, migration runner on startup
- [x] Provider interfaces + factory; Harvester driver (full CRUD via dynamic client) and Rancher driver (full CRUD via REST v3)
- [x] Auth middleware: Asgardeo JWT validate, group → tenantID, per-tenant quota check
- [x] VM and cluster handlers: create (async, 202), get, list, delete, kubeconfig
- [x] Reconciler goroutine: polls every 60s, syncs PENDING/DELETING resources to real state
- [x] `dcctl` CLI: login (PKCE), logout, create/get/list/delete vm/cluster, kubeconfig, create image
- [x] Dockerfile, Kubernetes Deployment + Service + Ingress manifests
- [x] CI/CD via GitHub Actions self-hosted runner (ARC), in-cluster, builds and rolls out on push to `main`
- [x] DC-API live at `http://dcapi.lk.internal.wso2.com` (192.168.10.37) on the `dcapi-controlplane-rke2` cluster

### M1.5 — Done
- [x] Domain models (`models/rbac.go`): PrincipalType, ScopeType, Role, Scope, RoleAssignment, ServiceAccount
- [x] PostgreSQL schema: `role_assignments` and `service_accounts` tables (scope-polymorphic for M5 compat)
- [x] RBAC helpers (`rbac/rbac.go`): RolePower, EffectiveRole, RequireRole with scope-chain walk
- [x] Auth middleware enhancements (`middleware/auth.go`): principal type/ID injection, autoprovision policy, platform-admin short-circuit
- [x] Service account auth (`middleware/serviceaccount.go`): dcapi_sa_* token validation with bcrypt
- [x] Member CRUD handlers (`handlers/members.go`): add/list/remove members (owner-only for add/remove, member for list)
- [x] Service account CRUD handlers (`handlers/service_accounts.go`): create/list/delete with token-shown-once guarantee
- [x] `dcctl tenant` subcommand group: add-member, remove-member, list-members, create-service-account, delete-service-account
- [x] 53 integration tests passing (role enforcement matrix, SA lifecycle, autoprovision toggle)
- [x] Documentation: `docs/rbac.md` (operator guide)

### M2 — Done
- [x] **Networking** — VPC/Subnet/NSG/Peering/RouteTable/PrivateDnsZone on self-managed KubeOVN. Per-VPC NAT gateway (F15) and per-VPC CoreDNS (F20). Bastion v1 (F10).

### Phase 6a — Done (May 2026)
- [x] Every per-tenant DB row carries an immutable `tenant_uuid`. Re-registering a deleted tenant slug no longer inherits orphan resources. Backfill in `internal/db/migrate.go::backfillTenantUUIDs`. Load-bearing test: `phase_6a_slug_recycle_test.go`. See `docs/defense-in-depth.md` §6a.

### M2.5 — Tenant → Project → Resource hierarchy (Done, May 2026)
- [x] **Hierarchy.** `Tenant → Project → Resource`. Every per-tenant table grows `project_id TEXT` + `project_uuid UUID` columns (immutable identity; same Phase 6a pattern). Resources can only exist inside projects. The original M5 "Subscription/Resource Group" plan was reshaped — no Resource Group; use labels instead.
- [x] **URL shape.** `/v1/tenants/{tenant_id}/projects/{project_id}/<resource>` for every per-resource endpoint. Tenant-level survivors: `/members`, `/images`, `/networks`, `/cap-usage`, `/projects` (CRUD), and the `/v1/admin/tenants{,/...}` admin endpoints.
- [x] **Capacity caps (hybrid quota model).** `tenants.{cpu_cores_cap, memory_gb_cap, storage_gb_cap}` set by platform admin; tenant owners distribute via per-project `cpu_cores/memory_gb/storage_gb`. Project create/PATCH enforces `sum ≤ cap` inside a tx with row-locked tenants row. Admin PATCH refuses shrink below allocated. PATCH project quota also refuses shrink below in-use resources (`sumProjectResourceUsageTx`). Two-layer enforcement: dc-api app-level + k8s `ResourceQuota` on the project namespace.
- [x] **Slug caps + naming.** Tenant slug ≤32 chars; project slug ≤20 chars; resource name ≤32 chars (regex enforced in handlers). K8s namespace = `dc-<tenant>-<project>` (max 56 chars). Cluster-scoped backend objects (KubeOVN VPC, Subnet, etc.) = `<kind>-<tenant>-<8-char-uuid>` (≤49 chars). Namespace-scoped = `<kind>-<8-char-uuid>`. Standardised labels on every backend object: `dc-api.wso2.com/{tenant,project,tenant-uuid,project-uuid,resource-uuid,resource-kind,resource-name}` — see `internal/providers/common/labels.go`.
- [x] **cloud-ui + dcctl + OpenAPI + integration tests** all migrated. `dcctl admin tenant {create,cap show,cap set}` + `dcctl project {create,list,get,update,delete}` + `dcctl project set/current`. UI: ProjectSwitcher, ProjectPickerPage, RegisterProjectDialog with live cap-availability display.

### Next up
- [ ] **M2 Storage** — volumes (Longhorn), snapshots, MetalLB / Harvester LB IP allocation; firewall DHCP design pinned for Longhorn storage NIC
- [ ] **M3 Managed Services** — Key Vault chunks 1+2 shipped; chunk 3 (per-tenant OpenBao HA) spike DONE 2026-05-21 — see `docs/spike-m3-keyvault-openbao-ha.md`. Integration contract for ALL managed services drafted at `docs/managed-services-integration.md` (external) + `docs/managed-services-framework.md` (dc-api Go-package design). Implementation pending: framework package + KeyVault refactor. DB/cache/registry/DNS/certs not started; will follow the same contract.
- [ ] **M4 Web UI** — Phase 1 (M1 surface) largely shipped; Phase 2 (project switcher) shipped under M2.5
- [ ] **M4 Terraform Provider** — not started; blocked on M3 stability
- [ ] **Defense-in-depth hardening** — see `docs/defense-in-depth.md` (Phases 1–5 + 6b cascade-delete). Tracked in wso2-datacenter-project #200.
- [ ] **DC-API Bootstrap** — codify `ops-bootstrap.md` as a Terraform module before EU/US regions go live

---

## Context: Why We're Building This

We were delivering infrastructure via Terraform modules in `open-cloud-datacenter`.
The problem: tenants had to understand Harvester, Rancher, RKE2, TF state — too much
leaky abstraction. Quotas were unenforceable. Audit trail didn't exist.

DC-API is the answer: one REST API that hides all backend complexity, enforces quotas,
owns state, and provides a real developer experience. `dcctl` is the UX layer on top.

## Agent Usage — Mandatory

Specialized agents exist for this project. Check the available agent list and invoke them
based on context. Do not wait to be told. Agents save context, run focused work in
isolation, and protect the main conversation from noise.

**When to invoke without being asked:**

| Situation | Agent |
|---|---|
| Any API endpoint or CLI command changed | `docs-writer` — update reference docs |
| Making any claim about Rancher/Harvester capabilities | `rancher-harvester-specialist` — verify first |
| Kubernetes manifests, Helm, RBAC, ingress, or storage on Harvester-hosted clusters | `rancher-harvester-specialist` — covers the full Harvester+k8s stack |
| Implementing a new handler, provider method, or DB query | `test-engineer` — write tests |
| Designing a new API resource, endpoint shape, or naming | `api-designer` — review before implementing |
| Implementing Go business logic or DB layer changes | `backend-developer` — delegate the implementation |
| Building or changing CLI commands or output formatting | `cli-developer` — delegate the implementation |
| Any UI design or web-app work (mockups, React/TS, Fluent UI) | `frontend-developer` — owns the web UI end-to-end |
| Any Terraform / IaC work (layer code, modules, helm_release modelling, SM-driven secrets, two-phase applies, cross-region bootstrap) | `terraform-specialist` — owns the IaC craft |
| Broad codebase exploration spanning multiple files/packages | `Explore` — faster than manual grep loops |

These are not optional steps. A task is not complete until the relevant agents have run.
If skipped, state why explicitly.

### When you create a NEW agent

Whenever you (Claude) create a new agent under `.claude/agents/`, you MUST also
register it in the table above with a clear trigger phrase and a one-line scope.
This is what makes auto-invocation actually work in future sessions — without
the table entry, the next session won't know when to spawn the agent. Don't wait
to be told to do this; it's part of the agent-creation task itself. The user
should never have to ask twice for the same registration.

## Lessons learned (don't make these again)

These are project-specific traps we've already paid for. Re-reading these on
every new session is cheap; re-discovering them is not.

### `mgmt-br` is no longer safe for new bridge-mode workloads (post-F15)

**Outage on 2026-05-11 evening: ~10 min of Harvester apiserver / Rancher UI /
dc-api ingress all unreachable from external clients when a tenant VM was
attached to the `dc-api/dc-api-mgmt` bridge NAD via dcctl.**

What changed: F15 created a KubeOVN `ProviderNetwork` claiming `mgmt-br` as
its external network. The bridge is now mediated by OVS flows (the
`localnet.ovn-vpc-external-network` logical switch port patches OVN's
logical network into the physical bridge). It's no longer a plain Linux
bridge.

Adding a NEW `type:bridge` NAD VM to the bridge triggers an OVS flow
reconverge AND new kube-ovn-cni reactions. During that window, ARP for
kube-vip-served VIPs on the LAN (Harvester `.6`, secondary `.35`, dc-api
ingress `.37`) gets dropped from upstream caches. Caches don't relearn
until the offending VM leaves the bridge.

Rules going forward:
- **Tenants always use VPCs** (`dcctl vm create --vnet … --subnet …`).
  F15's whole point is that tenant traffic stays on KubeOVN overlays.
- **Don't add new VMs to `dc-api/dc-api-mgmt`** (or any other bridge NAD
  that shares `mgmt-br`). The only VM that legitimately lives there is the
  `dcapi-controlplane-rke2` controlplane VM itself — and it's been there
  since before F15, so it's a stable port-citizen.
- **F21 (FOLLOWUPS.md)** plans to enforce this at the dc-api API layer —
  refuse VM-create on any bridge NAD whose underlying bridge has a
  KubeOVN ProviderNetwork attachment.
- **F22 (FOLLOWUPS.md)** is the cleaner long-term: dedicated VLAN for VPC
  external IPAM, separating it from the management broadcast domain
  entirely. After F22, `mgmt-br` could safely host new bridge VMs again
  (though tenants still shouldn't).

If a future session needs a tenant VM on a non-OVN network for testing
purposes, use a different VLAN-tagged bridge NAD (e.g.
`iaas/vm-network-001` on VLAN 700) — those are separate L2 domains and
unaffected by `mgmt-br`'s OVS mediation.

### Schema migrations — schema.sql is the single source

`internal/db/schema.sql` is fully idempotent — every statement is safe to
run on every dc-api boot. `internal/db/migrate.go` just `Exec`s the whole
file on startup; no sentinel, no alterations slice.

Pattern when adding new state:
- **New table**: `CREATE TABLE IF NOT EXISTS …` in schema.sql
- **New column on an existing table**: append `ALTER TABLE … ADD COLUMN IF
  NOT EXISTS …` near the bottom (do NOT rewrite the original CREATE TABLE
  body — fresh-DB installs need the column AND upgrade-path DBs need the
  ALTER)
- **New enum value**: add to the CREATE TYPE body AND mirror as
  `ALTER TYPE … ADD VALUE IF NOT EXISTS` below

The header comment of schema.sql lists all the idempotency idioms used.
The earlier "alterations slice mirror" lesson is obsolete — that
mechanism was removed when schema.sql was made fully idempotent.

### Never use `sed -i` to edit files on macOS — use the Edit tool

We have lost files **twice** to BSD `sed -i ''` silently truncating
the target — most recently 2026-05-16 when chaining three
`sed -i '' s/foo/bar/g` calls in a single Bash invocation; one file
(`cloud-ui/src/pages/NSGsListPage.tsx`, ~301 lines) ended up at
0 bytes with no error and no visible Bash output. Recovered from
git. The prior incident nuked `kubeovn_provisioner.go` the same way.

**Rule**: for any in-place modification of a tracked file, use
Claude Code's `Edit` tool (or `Write` for full rewrites). Do not
fall back to `sed -i`, `awk -i`, `perl -pi`, or any in-place stream
editor, even for trivial one-line swaps. The failure mode is silent
and destructive — by the time anyone notices, the file is already
gone.

Multi-file replacements: a small loop of `Edit` calls is the right
pattern, even if it's more tokens. Each Edit is atomic.

Read-only `sed`/`awk` (piping output to stdout, generating env
files, etc.) remains fine. The ban is specifically on `-i`-style
in-place edits.

### Git commit hygiene with parallel sub-agent work

Never use `git commit -am` (`-a` = auto-stage all tracked modifications)
when there's a sub-agent that may have written to the working tree. We
have shipped commits that swept up untested agent edits because of this.
Use `git add <specific files>` and review the staged set before committing.
If sub-agents are running in the background, treat the working tree as
"contaminated" until you've explicitly inventoried it.

### Sub-agent test claims must be verified

When a sub-agent reports "all clean" / "53/0/1 PASS" / "tests not run due
to permissions", treat it as **untested code** until you read the actual
test output yourself. We have shipped two bugs this way (composite-auth
chain with no failing-test detection, plus VM-on-VPC code that the agent
literally told us it couldn't run tests on).

Pattern: between every meaningful chunk, run the integration suite
yourself against the live cluster, count PASS/FAIL/SKIP, verify it
matches the expected total (previous + new tests), and only then mark
the chunk complete. A failing test from "I'm sure it's fine" costs more
than a 90-second suite run.

### KubeVirt bridge-mode VMs run a private DHCP server inside virt-launcher

**Caught on 2026-05-12 during the F20 spike.** Spent ~45 min
debugging why VMs on a fresh VPC subnet were getting DNS server
`10.53.0.10` (the K8s cluster DNS) even though OVN's DHCP_Options
table correctly advertised the per-VPC DNS pod IP. Root cause:
**KubeVirt's bridge-mode VMs have a built-in DHCP server in the
virt-launcher pod at link-local 169.254.75.10** that races OVN's
DHCP responder and copies the pod's `/etc/resolv.conf` directly
into the VM as DHCP option 6. Default `dnsPolicy: ClusterFirst`
gives the virt-launcher pod the cluster's DNS + namespace search
domains, so every VM ends up with `nameserver 10.53.0.10` and
`search <ns>.svc.cluster.local svc.cluster.local cluster.local`
regardless of what the VPC's DHCP says.

The tcpdump that nailed it (from inside a freshly-DHCP'd VM):
```
169.254.0.254.67 > <vm>.68 (Offer): Domain-Name-Server: 10.77.1.2  ← OVN
169.254.75.10.67 > <vm>.68 (ACK):   Domain-Name-Server: 10.53.0.10 ← KubeVirt
```

**Rule going forward:** every VM dc-api creates on a VPC subnet
**must** set both:
```yaml
spec.template.spec.dnsPolicy: None
spec.template.spec.dnsConfig.nameservers: [<vpc-dns-pod-ip>]
```
This is in `internal/providers/harvester/client.go::CreateVM` per
F20. The dnsConfig makes the virt-launcher pod's resolv.conf
agree with OVN — both DHCP responders end up advertising the same
DNS IP, so the race becomes harmless.

This applies to ANY VM on a non-cluster network where the cluster
DNS isn't reachable. The symptom is "VM has DNS but can't resolve
anything" — the cluster DNS IP isn't routable from the tenant VPC.

### Per-VPC infra pods pin tenant-subnet LSPs — subnet teardown order matters

**Caught on 2026-05-12 from F20 integration test failures.** F15
NAT gateway pods AND F20 CoreDNS pods both have NICs attached to
the tenant subnet's logical switch (via Multus + kubeovn-cni).
Their LSPs (logical switch ports) pin the subnet — kubeovn's
subnet delete won't complete until those LSPs are gone, which
won't happen until the pods are fully terminated.

Combined with the M2 contract "VNet delete requires no active
subnets," this creates a teardown ordering trap:
1. Tenant deletes subnet → dc-api calls `provider.DeleteSubnet`
   → kubeovn refuses (LSPs pinned)
2. Subnet stays in DELETING forever
3. Tenant can't delete VNet (subnet still active)
4. Resources stuck.

**The fix (already shipped):** in
`internal/api/handlers/subnet.go` DELETE handler, when this is
the LAST active subnet of the VPC, also tear down the per-VPC
NAT gateway and DNS Deployment first, then wait for the pods to
actually drain (deterministic poll, NOT a fixed sleep — see F26),
then call `provider.DeleteSubnet`.

The pattern generalizes: **any per-VPC pod with a NIC into a
tenant subnet must be cleaned up before that subnet can be
deleted.** When adding new such pods, mirror this teardown
behavior or you'll re-introduce the race.

### Project namespace must be created via the provisioner, not just the DB row

**Caught 2026-05-21 during M2.5 stage-6 integration runs.**
`internal/db/projects.go::CreateProject` writes the projects row but does
NOT create the K8s namespace `dc-<tenant>-<project>`. The project handler
calls `ProjectNamespaceProvisioner.EnsureProjectNamespace(...)` after the
DB insert to actually create the namespace + ResourceQuota on the cluster.
Test fixtures that called the repo directly (`env.DB.CreateProject(...)`)
skipped this step, causing every subsequent VNet/Subnet/VM provisioner
call to fail with `namespaces "dc-<tenant>-<project>" not found`.

Rule: any code path that creates a project MUST also call
`EnsureProjectNamespace` (or go through `POST /v1/tenants/{tid}/projects`
which does it for you). Test fixtures that bypass the HTTP layer now do
this explicitly — see `test/integration/fixtures.go::ensureDefaultProject`.

### Tenant cap aggregate queries need every selected column in GROUP BY

PostgreSQL SQLSTATE 42803. `GetTenantCapAndAllocation` initially listed
the tenant cap columns in the SELECT but only `tenant_uuid` in GROUP BY;
Postgres refused because cap columns aren't aggregated and aren't
functionally derivable from `tenant_uuid` alone (the unique index on
`tenant_uuid` doesn't make Postgres infer functional dependency the way
the primary key does in some grammars). Fix: add cap columns explicitly to
GROUP BY. Lesson generalises: when aggregating across a JOIN, every
non-aggregated column in SELECT must appear in GROUP BY.

### Project storage quota must account for CDI's 2x-disk-size prime PVC

**Caught 2026-05-21 during the M3 chunk-3 spike** when a bastion VM with a
40Gi rootdisk stayed PENDING forever in a project with `storage_gb=50`.

Harvester's VM-image-clone path goes through CDI (Containerized Data
Importer): for each new disk, CDI creates a **prime PVC** of the same size
to clone the image into; once the image is imported the prime is deleted
and its underlying Longhorn volume becomes the real disk. During the
import window the namespace's `requests.storage` ResourceQuota sees
`2 × disk size`. The 40Gi prime + the 40Gi real PVC = 80Gi requested
against a 50Gi cap → admission refuses prime PVC creation with
`ErrCreatingPVCPrime`. The DataVolume stays Pending, no virt-launcher
pod is ever created, and dc-api stays in PENDING because the VM CR
never reports Ready. Symptom looks like a dc-api hang but the block
is at the K8s admission layer.

**Rules going forward** (none implemented yet — flagged):

- `db/projects.go::CreateProject` and `UpdateProject` should validate
  storage quota against a backstop derived from the largest catalog VM
  image: `quota ≥ 2 × max(image disk size in catalog)`. Hide the
  doubling from the user — they ask for "100Gi", we silently enforce
  ≥200Gi.
- Reconciler should detect `ErrCreatingPVCPrime` events on owned PVCs
  and flip the parent VM's status message to a user-actionable string
  ("project storage quota too low for image import; expand quota or
  reduce disk size") instead of leaving the row in unexplained PENDING.
- Long-term: investigate whether pre-imported Longhorn image templates
  let CDI skip the prime PVC entirely. Would eliminate the doubling
  altogether.

Tracked under wso2-datacenter-project defense-in-depth #200 (Phase 1
— cheap structural wins).

### ARC runner queue gets orphaned after listener restart

Symptom: `gh run list` shows N runs queued for an hour+; the ARC
listener pod log repeats `"assigned job"=0 decision=0 ... lastMessageID=<n>`
forever even after restart; ephemeral runner pods never appear in
`arc-runners` namespace. Workflow runs eventually time out as
"failure" with no log past the GitHub-side setup steps.

Cause: when the ARC listener pod is restarted (rke2 crashloop, node
reboot, pod eviction, …) GitHub's broker still has the OLD session
referenced as the owner of the queued runs. The new listener starts
a fresh session and asks the broker for messages — broker returns
zero, because the queued jobs are still pinned to the dead session.
They never get assigned to anyone and stay stuck "queued" indefinitely.

Fix (already coded into `.github/workflows/deploy.yaml` as a comment
referencing this runbook):

  1. `gh run list --status queued --limit 20` — list the orphans.
  2. `gh run cancel <id>` for each. Status flips to "cancelled".
  3. `gh workflow run <yaml> --ref main` for each affected workflow.
     The fresh `workflow_dispatch` runs are assigned to the live
     listener session immediately; ephemeral runner pods come up
     within ~30 s.

The push-triggered queued runs never recover on their own — cancel +
redispatch is the only path. The latest commit's behaviour does land
because workflow_dispatch checks out HEAD of `--ref main`.

## Explaining Code Changes

The user has an SRE/sysadmin background and no Go experience. When making a non-trivial
code change, include a brief plain-English explanation in your response — 2-5 sentences
covering: what the change does, why it was needed, and any operational side-effect the
user would notice. Skip this for one-liners, typo fixes, or purely mechanical changes
(e.g. renaming a variable). The explanation goes in the text response, not as a code comment.
