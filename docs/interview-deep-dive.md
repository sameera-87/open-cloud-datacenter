# Terraform Provider for DC-API — Interview Deep Dive

This document is a from-scratch walkthrough of this repository's design: what it is, why it's
built the way it is, and the tradeoffs behind every non-obvious decision. It's written to prep
for a technical interview, so it favors "why" over "what" — the code itself already documents
the "what" (see `Architecture.md` for the user-facing reference and `docs/dc-api-reference.md`
for the upstream API spec).

---

## 1. What this project is

A **Terraform provider** — a plugin that lets Terraform manage infrastructure on WSO2's internal
platform, **DC-API** (a REST API fronting Harvester/KubeVirt VMs, Rancher-provisioned RKE2
Kubernetes clusters, and kube-OVN-backed virtual networking). Terraform providers are just
binaries that speak a gRPC protocol to the Terraform CLI; this one is built with HashiCorp's
`terraform-plugin-sdk/v2` (the mature, widely-adopted SDK — as opposed to the newer
`terraform-plugin-framework`, discussed in §7).

The provider exposes **20 resources** (things Terraform creates/updates/destroys) and
**12 data sources** (read-only lookups of things it doesn't own), covering the full stack a
tenant needs: tenants → projects → networking (VNet/subnet/route tables/NSGs/peering/private DNS)
→ compute (VMs, bastions, RKE2 clusters + node pools) → secrets (key vaults) → IAM (service
accounts, tenant members).

---

## 2. High-level architecture

```
Terraform CLI
     │ gRPC (plugin protocol, handled entirely by the SDK)
     ▼
main.go            → plugin.Serve(provider.New)
     │
     ▼
internal/provider/provider.go
     │  - declares provider-level config schema: endpoint, token
     │  - ResourcesMap: "dcapi_x" → resources.ResourceX()
     │  - DataSourcesMap: "dcapi_x" → datasources.DataSourceX()
     │  - ConfigureContextFunc: builds one *client.DCAPIClient, shared as `meta`
     ▼
internal/resources/*.go   (CRUD glue: schema.Resource + Create/Read/Update/DeleteContext)
internal/datasources/*.go (Read-only glue: schema.Resource + ReadContext)
     │  every lifecycle function receives `meta interface{}`, type-asserted to *client.DCAPIClient
     ▼
internal/client/*.go      (one file per resource family: request/response structs + HTTP calls)
     │  every method funnels through client.go's doRequest()
     ▼
DC-API (HTTPS, bearer token auth)
```

This is a strict **3-layer separation**, repeated identically for every resource:

| Layer | Responsibility | Knows about Terraform? | Knows about HTTP? |
|---|---|---|---|
| `internal/resources/*.go` | Schema definition + state lifecycle (CRUD, ForceNew, polling, validation) | Yes | No |
| `internal/client/*.go` | Request/response DTOs, URL construction, one method per API call | No | Yes |
| `internal/client/client.go` | Auth headers, JSON marshalling, error translation | No | Yes (the only file that literally calls `http.Client.Do`) |

**Why this separation matters (the interview answer):** it keeps Terraform-specific concerns
(`schema.ResourceData`, diagnostics, `ForceNew`) completely out of the HTTP layer, so the client
package could be reused by a CLI tool or another consumer without pulling in the SDK. It also
makes each resource file readable top-to-bottom as "schema → create → read → update → delete →
polling helpers → expand/flatten helpers" — a consistent shape across 20 files, which is what
lets a new resource be added by copying an existing file and mechanically swapping names.

---

## 3. Request lifecycle, end to end (using `dcapi_vnet` as the running example)

1. **Provider configuration.** `terraform init`/`plan` calls `configureProvider()`
   ([provider.go](../internal/provider/provider.go)), which reads `endpoint`/`token` (falling back
   to `DCAPI_ENDPOINT`/`DCAPI_TOKEN` via `schema.EnvDefaultFunc`), validates both are non-empty
   (returning `diag.Diagnostics` errors otherwise — no client is built on partial config), and
   constructs one `*client.DCAPIClient`. That pointer becomes `meta` for every resource/data
   source invocation for the rest of the run — **one client, one `http.Client`, shared across all
   resources**, not one per resource instance.

2. **Create.** `resourceVNetCreate` reads schema fields via `d.Get(...)`.(type), builds a
   `client.VNetCreateRequest`, and calls `c.CreateVNet(...)`. The client method
   (`internal/client/vnet.go`) does `fmt.Sprintf` path construction, delegates to `doRequest`,
   and unmarshals the `{"resource": {...}}` envelope DC-API wraps around every object.

3. **State ID assignment.** `d.SetId(fmt.Sprintf("%s/%s/%s", tenantID, projectID, vnet.ID))` —
   see §4, this is the single most important convention in the codebase.

4. **Async wait.** DC-API returns **HTTP 202 Accepted** with `status: "PENDING"` for anything
   that provisions real infrastructure (VNets, subnets, VMs, clusters, node pools). The resource
   then calls a `waitForXActive` helper built on the SDK's `retry.StateChangeConf` — a
   polling state machine with `Pending`/`Target` status strings, a `Refresh` closure that re-GETs
   the resource, and a `Timeout`/`MinTimeout` (uniformly 15s poll interval across the codebase).
   `Create` doesn't return until the resource is actually usable — Terraform's model has no
   notion of "still provisioning," so the provider absorbs that wait itself.

5. **Read-back.** `Create` always finishes by calling `resourceVNetRead(ctx, d, meta)` rather than
   duplicating the field-setting logic — one source of truth for "what does this resource's state
   look like."

6. **Drift detection.** Every `Read` treats a `nil, nil` return from the client's `GetX` (which
   itself detects HTTP 404 and returns `nil, nil` rather than an error) as "deleted outside
   Terraform" and calls `d.SetId("")` — the SDK's signal to drop it from state and re-plan a
   create.

7. **Delete.** Same pattern in reverse: issue `DELETE`, then poll (`waitForXDeleted`) until the
   `GetX` call returns `nil, nil`, confirming the async teardown actually finished before
   Terraform considers the resource gone — this matters for dependency ordering (see §5).

---

## 4. Composite state IDs — the core state-modeling decision

Terraform gives every resource exactly one opaque string, `d.Id()`, to remember "which real-world
object is this." DC-API's URLs are hierarchical (`/v1/tenants/{t}/projects/{p}/vnets/{v}`), so a
bare UUID isn't enough to reconstruct a GET/DELETE URL later — you'd also need to know the parent
tenant and project. The fix used everywhere in this codebase: **encode the full path into the ID
as a `/`-joined composite string**, then `strings.SplitN(d.Id(), "/", N)` it back apart in every
`Read`/`Update`/`Delete`.

| Resource | ID shape | Example |
|---|---|---|
| `dcapi_tenant` | `slug` | `wso2` |
| `dcapi_project` | `tenant/project` | `wso2/infra` |
| `dcapi_vnet` | `tenant/project/vnet_uuid` | `wso2/infra/bb0e...` |
| `dcapi_subnet` | `tenant/project/vnet_uuid/subnet_uuid` | `wso2/infra/bb0e.../cc0e...` |
| `dcapi_key_vault_secret` | `tenant/project/vault_uuid/key` | `wso2/infra/ee0e.../db-password` |
| `dcapi_node_pool` | `tenant/project/cluster_uuid/pool_name` | `wso2/infra/ff0e.../workers` |
| `dcapi_tenant_member` | `tenant/principal_id` | `wso2/auth0\|abc123` |

**Why not just store the UUID and a separate `tenant_id`/`project_id` field?** Those fields *do*
also exist in the schema (as `Required, ForceNew` inputs) — the ID isn't replacing them, it's
making `Read`/`Delete` self-sufficient. Terraform calls `Read` during `terraform refresh` and
`terraform plan` using **only** the ID from state; it doesn't guarantee the rest of the config is
available in the same form. Packing the full path into the ID means those functions never depend
on anything but `d.Id()` to rebuild the URL, which is the idiomatic SDKv2 pattern for
hierarchical/nested APIs that have no global-namespace lookup.

A consequence worth calling out in an interview: **every parent identifier becomes implicitly
immutable**, because it's baked into the ID. This is why `tenant_id`/`project_id` are `ForceNew`
on every child resource — even the ones where nothing else is immutable (e.g. `dcapi_node_pool`
has `node_count`/`taints`/`labels` as updatable fields, but `tenant_id`/`project_id`/`cluster_id`
are still ForceNew) — changing the parent is a different resource, not a reconfiguration of the
same one.

Two resources needed a variant of this because DC-API has **no GET-by-ID endpoint** for them:
- `dcapi_tenant_member`: only a `ListTenantMembers` endpoint exists. `Read` lists all members of
  the tenant and scans for the one whose `principal_id` matches. Not found ⇒ drift.
- `dcapi_nsg_attachment`: attachments aren't independently addressable; they're embedded in the
  parent NSG's GET response. `Read` fetches the NSG and scans its `Attachments` list for the
  stored `attachment_id`.

Both are the same trick: when the API doesn't expose direct addressability, `Read` degrades to
"fetch the smallest enclosing object you *can* GET, and filter client-side." This is a common
real-world gap between REST API design and Terraform's assumption that everything is
independently readable by ID.

---

## 5. Async operations and the ForceNew/immutability pattern

Two decisions recur across nearly every resource file and are worth being able to explain the
reasoning behind, not just recite:

### 5a. Polling via `retry.StateChangeConf`

Every resource that provisions real infrastructure (VNet, subnet, VM, cluster, node pool, bastion,
private endpoint, etc.) follows the same shape:
```go
conf := &retry.StateChangeConf{
    Pending:    []string{"PENDING"},        // or {"provisioning", "scaling"} for node pools
    Target:     []string{"ACTIVE"},         // or {"ready"} — DC-API mixes casing conventions per resource family
    Timeout:    timeout,                     // from d.Timeout(schema.TimeoutCreate) — configurable per-resource
    MinTimeout: 15 * time.Second,
    Refresh: func() (interface{}, string, error) {
        obj, err := c.GetX(ctx, ...)
        if err != nil { return nil, "", err }
        if obj == nil { return nil, "", fmt.Errorf("... disappeared while waiting") }
        if obj.Status == "FAILED" { return nil, "FAILED", fmt.Errorf("... failed: %s", obj.Message) }
        return obj, obj.Status, nil
    },
}
_, err := conf.WaitForStateContext(ctx)
```
This is the SDK's built-in state-machine poller — it's preferred over a hand-rolled `for`/`sleep`
loop because it integrates with the request context (so a `terraform apply` cancel/Ctrl-C
actually stops polling), respects the resource's configured `Timeout` block, and gives consistent
error messages on timeout. `Timeouts` are tuned per resource by real provisioning cost: VNets/
subnets get 5 minutes, VMs 15 minutes (image pull + boot), clusters 30 minutes
(multi-node RKE2 bring-up), reflecting what was observed against the real API.

The same `StateChangeConf` shape is reused for **delete confirmation** — `waitForXDeleted` polls
until `GetX` returns `nil` (404), because DELETE is also 202/async and Terraform must not consider
the parent resource's dependencies satisfied until the child is actually gone (see 5b).

### 5b. `ForceNew` as the default, `Update` as the exception

Look at any resource file's schema and the default is `ForceNew: true`. Only a handful of fields
across the whole provider are genuinely updatable in place:
- `dcapi_project`: `cpu_cores`/`memory_gb`/`storage_gb` (quota fields, via `PATCH`)
- `dcapi_node_pool`: `node_count` (scaling), `taints`/`labels` (full-replace on every `Update`)
- `dcapi_key_vault_secret`: `value`/`metadata` (the whole resource is upsert-shaped — `Create` and
  `Update` literally call the same `PUT` method)

Everything else — VNet address space, subnet CIDR, VM size/image/network, cluster K8s version —
is `ForceNew`. This isn't a shortcut; it directly mirrors what DC-API itself supports (there is
usually no `PATCH` endpoint for those fields at all — changing a VM's size means DC-API would
have to migrate a running VM, which it doesn't support). When a resource has **no
`UpdateContext`** at all (VNet, subnet, VM, tenant member, service account), that's a deliberate
signal, not an oversight: "everything about this object is fixed at creation; the only lifecycle
transitions DC-API allows are create and destroy."

The `dcapi_project` delete semantics are a good example of ordering constraints Terraform must
respect: deleting a project while it still has VNets/VMs/clusters returns **HTTP 409**, which
`resourceProjectDelete` surfaces as a plain wrapped error (with client.go's error path already
extracting `{"error": "..."}` bodies). Terraform's own dependency graph (built from `vnet_id =
dcapi_vnet.x.vnet_uuid` style references) normally destroys children before parents automatically
— the 409 is mainly a safety net for out-of-band API calls or partial applies, and the VNet
resource goes further and detects the specific 409 (`strings.Contains(err.Error(), "HTTP 409")`)
to append a more actionable error message pointing at the exact fix.

---

## 6. Notable per-resource decisions worth being able to discuss

- **Shown-once secrets (`dcapi_virtual_machine.private_key`/`console_password`,
  `dcapi_service_account.token`).** DC-API returns these fields only in the `Create` response body;
  subsequent `GET`s omit them entirely. If `Read` naively set them from the API response, it would
  overwrite real secrets with empty strings and Terraform would plan a spurious "update" every
  run. The fix: `Read` calls `d.Get("private_key").(string)` (i.e., reads back what's *already in
  state*) and writes that same value with `d.Set(...)` — a no-op for the value, but it tells
  Terraform's diff engine "this attribute is unchanged," which suppresses the false drift. This is
  a well-known SDKv2 idiom for shown-once/side-effect values, and the comments in `vm.go` spell out
  exactly why.

- **Mutually-exclusive networking modes (`dcapi_virtual_machine`, `dcapi_cluster`).** Both support
  a legacy flat `network_name` (bridge networking) or VPC mode (`vnet_id` + `subnet_id`), but never
  both and never neither. This is validated by hand in `Create` (no `ConflictsWith`/`RequiredWith`
  schema-level validators are used here — worth noting as something SDKv2's schema package could
  have expressed declaratively instead of imperatively; see §7) with three explicit branches:
  both-set, neither-set, and vnet-without-subnet/subnet-without-vnet.

- **Quota-exceeded errors get structured parsing.** `client.go`'s `doRequest` special-cases HTTP
  400 by attempting to unmarshal a `quotaErrorResponse` (`{"error":"quota_exceeded", "tenant_cap":
  {...}, "allocated": {...}, "available": {...}, "requested": {...}}`) and — only if that succeeds
  and the error code matches — builds a detailed message showing cap/allocated/available/requested
  side by side. Any other 400 (or a 400 that doesn't parse as a quota error) falls through to the
  generic `{"error": "..."}` handler, and *that* falls through to "dump the raw body" if even that
  doesn't parse. Three-tier degrading error handling, all funneled through one function so no
  resource file has to re-implement HTTP-status-code sniffing.

- **`dcapi_key_vault_secret` is explicitly upsert-shaped.** There's no dedicated `POST` for
  create vs. `PATCH` for update on the API side — DC-API's `secrets/{key}` endpoint is a single
  `PUT` that always upserts. So `resourceKeyVaultSecretCreate` and `...Update` both call
  `client.WriteKeyVaultSecret`, and the "version" field is purely a computed audit trail the API
  increments server-side on every write — the provider doesn't do anything special to bump it.

- **Nested, structured blocks (`dcapi_cluster.system_pool`/`worker_pools`, `dcapi_node_pool.taints`
  /`labels`).** SDKv2 models nested objects as `TypeList` + `Elem: &schema.Resource{...}` (there's
  no native "struct" schema type), which is why every resource with nested config has a matching
  pair of `expandX`/`flattenX` helper functions: `expand` walks `[]interface{}` → typed Go structs
  for the outbound request, `flatten` walks the API response back into `[]interface{}`/
  `map[string]interface{}` for `d.Set`. This expand/flatten pairing is standard SDKv2 vocabulary —
  worth naming explicitly if asked "how do you model nested structures."

- **`appendSet` helper (`internal/resources/helpers.go`).** `d.Set()` returns an `error` that is
  almost always nil in practice (it only fails on schema-type mismatches, which would be a
  provider bug, not a runtime condition) but SDKv2 requires you to handle it. Instead of an
  `if err != nil { return diag.FromErr(err) }` after every single `d.Set` call (each resource sets
  8–15 fields), `appendSet(diags, d, key, val)` folds the `.Set` result into a running
  `diag.Diagnostics` slice, checked once at the end with `diags.HasError()`. Small helper, but it's
  the one piece of shared logic outside the client package, and it's why every resource file reads
  as a flat list of `diags = appendSet(...)` lines rather than a wall of repeated error checks.

---

## 7. Design tradeoffs to discuss if asked "what would you change?"

- **SDKv2 vs. `terraform-plugin-framework`.** This provider is built on the older, mutable-schema
  SDK (`map[string]interface{}` typed data, runtime type assertions everywhere —
  `d.Get("x").(string)`). HashiCorp's newer Plugin Framework uses Go generics and typed structs
  with `tfsdk` tags, catching schema/type mismatches at compile time instead of via panics from bad
  type assertions at runtime, and has first-class support for things like nested attribute
  validation and null/unknown distinction. SDKv2 was almost certainly the right choice for a
  fast-moving internal provider (bigger ecosystem, way more example code, most existing providers
  are still SDKv2), but framework's compile-time safety is the natural "if I started over" answer.

- **No `ConflictsWith`/`ExactlyOneOf` schema validators.** The mutually-exclusive networking modes
  (§6) are validated imperatively inside `Create` rather than declaratively via SDKv2's
  `Schema.ConflictsWith` / `Schema.ExactlyOneOf` fields. The declarative form would surface the
  error at `terraform plan` / `terraform validate` time (before any API call), rather than at
  `apply` time — strictly better UX. This looks like debt rather than a deliberate choice, and
  would be one of the first things to fix.

- **No automated tests.** There is no `_test.go` anywhere in the repo — `test/main.tf` is a manual
  smoke-test config, not an automated acceptance test. The standard tool for this class of project
  is `terraform-plugin-testing`'s `resource.Test`/`resource.TestStep`, which spins up a real
  `terraform apply`/`plan`/`destroy` cycle against a live (or recorded) backend and asserts on
  state. Given DC-API is an internal, stateful platform (VMs/clusters cost real compute), the
  likely reason is the cost/complexity of standing up a test tenant — but it's a real gap, and
  naming it unprompted is a good signal in an interview.

- **Client is a thin, uniform wrapper — no generated SDK.** Every `internal/client/*.go` file is
  hand-written boilerplate (build path, marshal, `doRequest`, unmarshal) rather than generated from
  an OpenAPI spec, even though `docs/Open-api-spec.md` exists in this repo. Hand-written gives full
  control over error handling nuance (e.g. the quota-error special case) but means 20 files with
  near-identical shape — a reasonable target for either codegen or a small generic helper
  (`doRequestJSON[T](ctx, method, path, body) (T, error)`) to cut boilerplate.

- **`http.DefaultClient` with no explicit timeout.** `NewClient` uses `http.DefaultClient`, which
  has no request timeout configured. In practice the SDK-level `schema.ResourceTimeout` blocks
  bound the *overall* Create/Update/Delete duration via context cancellation propagated through
  `ctx`, but a single hung TCP connection on a `GetX` call outside of a polling loop (e.g. plain
  `Read` during `terraform plan`) has no client-side timeout of its own — worth a defensive
  `http.Client{Timeout: ...}` if hardening this further.

---

## 8. If asked to extend this provider

The `Architecture.md` "Adding a New Resource" checklist is accurate and worth repeating verbatim,
because it *is* the mental model:

1. Add request/response structs + one method per API verb to a new `internal/client/<x>.go`.
2. Implement `internal/resources/<x>.go`: schema map (mark immutable fields `ForceNew`, mark
   API-owned fields `Computed`, mark secrets `Sensitive`), `Create`/`Read`/`Delete` (+ `Update` only
   if DC-API actually has a PATCH), state ID convention matching the resource's position in the
   tenant/project/... hierarchy, and `waitForXActive`/`waitForXDeleted` if creation/deletion is
   async (202-returning).
3. Register both the resource and, if a standalone lookup makes sense, a data source in
   `internal/provider/provider.go`'s `ResourcesMap`/`DataSourcesMap`.
4. Add a runnable example under `examples/<resource>/main.tf`.

The one judgment call not spelled out there: **decide the state-ID shape first** — it determines
which parent IDs must be `ForceNew`, whether `Read` can call a direct `GetByID` or has to fall back
to `List` + filter, and it's the thing every other design decision in a new resource file hangs off
of.
