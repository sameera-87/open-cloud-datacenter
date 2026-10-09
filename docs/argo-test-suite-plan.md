# Argo Workflow Test Suite Plan — DC-API & DC-API Terraform Provider

## Status

Draft for review. This plan covers the **production-safe, automated replacement** for
the manual `terraform apply`/`terraform destroy` cycles currently run by hand against
the `.tf` files under [examples/](../examples/). It is deliberately scoped to Argo
Workflows orchestration and does not replace
[docs/testing-plan.md](testing-plan.md) (Go unit/integration tests) — it is the
automation of that plan's **Layer 3 (end-to-end)**, redesigned around two constraints
you called out:

1. Two separate test suites: one for **DC-API itself** (black-box, HTTP-only), one for
   the **Terraform provider** (exercises the actual provider binary + Terraform CLI).
2. **Resource efficiency**: this runs against production-adjacent infrastructure, so
   nothing large or long-lived may run in one shot. Provisioning must happen one
   resource at a time, each in its own WorkflowTemplate, with expensive one-time setup
   (downloading the `terraform` binary, downloading/caching the provider plugin) done
   exactly once and reused, not repeated per resource.

---

## 1. Two independent suites

### Suite A — DC-API suite (no Terraform at all)

Pure black-box HTTP testing directly against DC-API (`curl`/`httpie`/a small Go or
Python client), scoped by `tenant_id`/`project_id` path segments, same shapes as
[docs/dc-api-reference.md](dc-api-reference.md). No `terraform` binary, no provider
plugin, no `.tf` files involved.

**Purpose:** establish ground truth for what DC-API actually does — status codes,
async polling behavior, quota errors, cleanup-on-delete — independent of whether the
Terraform provider translates it correctly. If a provider-suite test fails, this suite
tells you whether the bug is in DC-API or in the provider's translation layer.

### Suite B — DC-API Terraform Provider suite

Runs the real `terraform` CLI against the real provider binary
(`registry.terraform.io/wso2/dcapi`), using single-resource `.tf` configs derived from
[examples/](../examples/), against the same real DC-API backend. Exercises
`init → plan → apply → plan (no-diff check) → destroy`.

**Purpose:** proves the schema/CRUD code in [internal/resources](../internal/resources)
and [internal/client](../internal/client) correctly drives DC-API end-to-end — this is
what layer-1/2 Go unit tests (see [testing-plan.md](testing-plan.md)) cannot prove,
because they never invoke a real `terraform` binary or a real backend.

Both suites share one library of **validation scripts** (see §5) so the same
assertions run whether the resource was created by `terraform apply` or by a raw
`POST`. This also keeps the two suites honest against each other — a validation script
that only ever runs against one suite tends to silently drift from reality.

---

## 2. Resource-efficiency design

This is the part that departs from a naive "one big `terraform apply -auto-approve`
across all examples" approach.

### 2.1 One WorkflowTemplate per resource type

Every DC-API resource type gets its own **WorkflowTemplate**, invoked as a task, doing
exactly: build minimal config for that one resource → apply → validate via API →
destroy → validate deletion. Nothing else runs inside that template. This bounds the
blast radius and the pod footprint of any single step to "one resource."

### 2.2 Download the Terraform binary and provider plugin exactly once

- A **bootstrap WorkflowTemplate** runs first (and only once per workflow run):
  - Downloads the pinned `terraform` CLI binary.
  - Runs `terraform init` against a directory containing only the
    `required_providers` block (same shape as
    [examples/provider/main.tf](../examples/provider/main.tf)), which pulls the
    `wso2/dcapi` plugin into `.terraform/providers/...`.
  - Packages the `terraform` binary + the populated `.terraform/providers` tree +
    `.terraform.lock.hcl` into a single artifact.
- Every later resource-template step declares that artifact as an **input artifact**,
  unpacks it into its own working directory, and sets
  `TF_PLUGIN_CACHE_DIR` to point at the unpacked provider cache.
- Consequence: `terraform init` still runs once per resource's working directory
  (Terraform requires a `.terraform` dir per directory), but with the plugin cache
  warm it does **zero network calls and zero re-downloads** — it's a local
  cache-populate, not a fetch. The actual "download the binary / download the plugin"
  cost is paid exactly once per workflow run, which is what you asked for.
- Use the Argo default artifact repository (S3/MinIO) for this, not a
  ReadWriteMany PVC — avoids needing a shared-filesystem StorageClass in the
  production cluster, which is usually the harder ask operationally.

### 2.3 Small, sequential, short-lived steps

- Steps run one at a time (a DAG with linear or near-linear dependencies, not a wide
  fan-out), each with tight CPU/memory `resources.requests/limits` — a
  `terraform apply` for one resource plus a couple of `curl` calls needs very little.
- Each resource's own step destroys what it created before the workflow moves to
  dependent steps that don't need it anymore — nothing sits provisioned longer than
  the minimum needed to validate it.

### 2.4 Shared-fixture question (needs your input before finalizing)

Almost every resource in [examples/](../examples/) nests under
`tenant → project → vnet → subnet` (see the dependency table in §4). Two ways to
handle that in the DAG, worth deciding explicitly rather than defaulting silently:

- **(a) Isolated per-resource** — every resource task creates its *own* full parent
  chain (tenant, project, vnet, subnet) and tears the whole chain down again
  afterward. Maximizes isolation (a failure in one task can never affect another) but
  multiplies DC-API calls: creating/destroying a tenant+project+vnet+subnet ~19 times
  instead of once.
- **(b) Shared fixture** — one small DAG segment provisions tenant→project→vnet→subnet
  a single time; every leaf-resource task (nsg, route_table, vm, bastion, cluster,
  key_vault, dns, …) consumes those outputs; a final teardown segment destroys the
  fixture last, after every leaf task has destroyed its own resource. Far fewer
  redundant API calls and far less total runtime, at the cost of leaf-task failures
  sharing a fixture (mitigated by §6's always-cleanup rule).

Given the "resource efficient, can't run big workloads in prod" framing, **(b) is the
recommended default** — I'll assume it below, but flag it explicitly since it's a
real trade-off decision, not just an implementation detail.

---

## 3. Repository layout (proposed)

```
test/
  argo/
    templates/
      bootstrap-workflowtemplate.yaml        # terraform binary + provider plugin cache
      fixture-workflowtemplate.yaml           # tenant -> project -> vnet -> subnet (shared)
      dcapi/                                  # Suite A: direct API, no terraform
        tenant-workflowtemplate.yaml
        project-workflowtemplate.yaml
        vnet-workflowtemplate.yaml
        subnet-workflowtemplate.yaml
        nsg-workflowtemplate.yaml
        ... one per resource type ...
      provider/                               # Suite B: real terraform + provider binary
        tenant-workflowtemplate.yaml
        project-workflowtemplate.yaml
        vnet-workflowtemplate.yaml
        subnet-workflowtemplate.yaml
        nsg-workflowtemplate.yaml
        ... one per resource type ...
    workflows/
      dcapi-suite.yaml           # Workflow (DAG) wiring Suite A templates + fixture
      provider-suite.yaml        # Workflow (DAG) wiring Suite B templates + fixture
    scripts/
      validate_tenant.sh
      validate_project.sh
      validate_vnet.sh
      ...                        # one validation script per resource, shared by both suites
      cleanup_sweep.sh           # orphan-resource sweep (see §6)
    configs/
      <resource>.tf.tmpl         # single-resource HCL templates for Suite B, seeded from examples/
```

`test/main.tf` (already present, currently a manual scratch file) becomes the seed for
the composite/fixture config rather than something run by hand.

---

## 4. Resource dependency inventory

Derived from the actual example configs — this is the ordering the DAG must respect,
and what each resource's WorkflowTemplate needs as input parameters.

| Resource | Depends on | Notes |
|---|---|---|
| `dcapi_tenant` | — | Root. Fixture step 1. |
| `dcapi_tenant_member` | tenant | Independent leaf off tenant only. |
| `dcapi_project` | tenant | Fixture step 2. |
| `dcapi_service_account` | tenant, project | Leaf. |
| `dcapi_vnet` | project | Fixture step 3. |
| `dcapi_subnet` | vnet | Fixture step 4. |
| `dcapi_network_security_group` (nsg) | project | Leaf; independent of vnet/subnet. |
| `dcapi_nsg_attachment` | nsg + (subnet or other target) | Needs both nsg and subnet fixtures. |
| `dcapi_route_table` | vnet | Leaf. |
| `dcapi_route_table_association` | route_table + subnet | Needs both. |
| `dcapi_vnet_peering` | two vnets | Needs a *second* vnet beyond the shared fixture (peering requires two non-overlapping VNets) — provision a throwaway second VNet inside this task, not part of the shared fixture. |
| `dcapi_virtual_machine` | subnet | Leaf. |
| `dcapi_bastion` | subnet | Leaf. |
| `dcapi_cluster` | subnet | Async create (poll to `ACTIVE`). |
| `dcapi_node_pool` | cluster | Depends on cluster being `ACTIVE` first. |
| `dcapi_key_vault` | project | Leaf. |
| `dcapi_key_vault_secret` | key_vault | Leaf under key_vault. |
| `dcapi_private_dns_zone` | vnet | Async create (poll to `ACTIVE`). |
| `dcapi_dns_record` | private_dns_zone | Sync; needs zone. |
| `dcapi_private_endpoint` | key_vault + subnet | Needs both. |
| Data sources (`dcapi_region`, `dcapi_image`, + read variants of the above) | the resource they look up | Suite A/B both run these as a read-only step right after the matching resource is created, before that resource is destroyed. |

Async resources (`dcapi_cluster`, `dcapi_node_pool`, `dcapi_bastion`, `dcapi_virtual_machine`,
`dcapi_private_dns_zone`) need explicit poll-for-status steps in both suites — Suite A polls
via raw `GET`, Suite B relies on the provider's own polling inside `terraform apply` but
should still assert the final `status` field via an independent `GET` afterward.

---

## 5. Validation approach (shared between suites)

Per resource, per suite run:

1. **Post-create validation** — after `apply` (Suite B) or `POST` (Suite A), an
   independent `GET` against DC-API confirms the resource exists with expected field
   values (name, cidr, status = `ACTIVE`, etc.) — never trust "terraform said success"
   or "got a 201" alone.
2. **Plan-stability check (Suite B only)** — a second `terraform plan` immediately
   after `apply` must show no diff. This is the most common real Terraform provider
   bug class and has no equivalent in Suite A.
3. **Pre-destroy snapshot** — capture the resource's current state via `GET` before
   deleting, for diagnostics if destroy fails.
4. **Post-destroy validation** — after `destroy`/`DELETE`, an independent `GET` must
   return 404/"not found." This is the check most likely to be skipped in manual
   testing and the one most likely to leak real cost if silently broken.
5. **Error-path spot checks** (subset of resources, not all) — a deliberate
   quota-exceeded or conflict request confirms DC-API (Suite A) and the provider's
   error surfacing (Suite B) both produce a readable, typed error rather than a raw
   500/panic.

---

## 6. Cleanup guarantees (safety-critical, given this runs against real infra)

- Every resource WorkflowTemplate destroys what it created **even when validation
  fails** — implement via Argo's `onExit` handler at the template level (or a DAG task
  with `depends: "task.Succeeded || task.Failed"`) so a failed assertion never leaves
  an orphaned resource.
- The shared fixture (tenant/project/vnet/subnet) is destroyed **last**, only after
  every leaf task in that run has finished (success or failure), never before.
- All test-created objects use a name/ID prefix carrying the Argo
  `{{workflow.uid}}` (e.g. `citest-<uid>-tenant`), so:
  - Concurrent/retried runs never collide on DC-API's uniqueness constraints.
  - A scheduled **orphan-sweep workflow** (`scripts/cleanup_sweep.sh`) can safely find
    and force-delete any `citest-*` resource older than N hours — the backstop for a
    workflow pod that got killed mid-run.

---

## 7. Suggested rollout order

1. Bootstrap WorkflowTemplate (binary + plugin cache as an artifact) — prove the
   "download once, reuse everywhere" mechanism works before building anything on it.
2. Suite A (DC-API direct) for the fixture chain only: tenant → project → vnet →
   subnet. Cheapest, no provider involved, validates the approach end-to-end.
3. Suite B mirroring the same 4 resources, reusing Suite A's validation scripts.
4. Extend both suites to the remaining ~15 resource types from §4, in dependency
   order, async resources (cluster/node_pool/bastion/vm/private_dns_zone) last since
   they need the poll-for-status handling.
5. Wire in `onExit` cleanup + the orphan-sweep cron workflow.
6. Decide and wire the trigger model (manual submit vs. scheduled cron vs. CI-gated) —
   not decided in this plan since no CI system exists in this repo yet.

---

## 8. Open questions for you to confirm before implementation starts

- Confirm the **shared-fixture vs. fully-isolated** trade-off in §2.4 — this changes
  the DAG shape and the total DC-API call volume significantly.
- Where does the Argo cluster/namespace live relative to DC-API — same cluster, or
  does the workflow need network access configured to reach a remote DC-API endpoint?
- Confirm secrets delivery: `DCAPI_ENDPOINT`/`DCAPI_TOKEN` as a Kubernetes `Secret`
  mounted into every WorkflowTemplate's pod — any existing secret-management
  convention in this cluster to follow instead (Vault, sealed-secrets, etc.)?
- Trigger model: on-demand `argo submit`, a schedule (`CronWorkflow`), or gated behind
  a future CI pipeline for this repo?
- Should the two suites run in the same namespace as real tenants/projects, or is
  there a dedicated sandbox tenant/project prefix already reserved for testing?
