# Managed Services Framework — Internal Design Doc

**Status:** Design target. The shape below is what `internal/managedservice/`
will look like; today, per-service handlers live as bespoke files (e.g.
`internal/api/handlers/keyvault.go` + `internal/providers/kvi/`). Treat KVI
as the proven reference pattern when writing a new service handler — when
the generic framework lands, that handler collapses into a `Register()` call.
**Audience:** dc-api maintainers.
**Companion:** `docs/managed-services-integration.md` (operator contract — external).

---

## 0. Until the framework exists: copy the KVI pattern

A new managed service is a 6-file change with no surprises if you mirror the
KVI shape exactly. The pattern is proven — three operators (key vaults plus
two pre-framework services) all conform.

| File | What you create | Reference to copy |
|---|---|---|
| `internal/models/<service>.go` | Domain struct (`Database`, `Cache`, …) — id, tenant/project keys, sizing, status, message, credentials_consumed_at, timestamps | `internal/models/keyvault.go` |
| `internal/db/schema.sql` | New `<service>s` table appended at the bottom, idempotent — uses `resource_status` enum, `UNIQUE (tenant_uuid, project_uuid, name)`, updated_at trigger | bottom of `schema.sql` (`key_vaults` block) |
| `internal/db/<service>.go` | Repo: Create / Get / List / Delete / UpdateStatus / MarkCredentialsConsumed | `internal/db/keyvault.go` |
| `internal/providers/<service>/client.go` (+ `translator.go`) | Dynamic-client driver for the operator's CRDs. `EnsureX`, `GetXStatus`, `DeleteX`, `GetXCredentials`. Translator collapses operator-specific status/secret-ref layout into the framework's view. | `internal/providers/kvi/client.go` |
| `internal/api/handlers/<service>.go` | HTTP handlers Create / Get / List / Delete / GetCredentials. Async on Create: returns 201/202 with status PENDING; operator-side provisioning is best-effort; failures leave PENDING with a diagnostic message. | `internal/api/handlers/keyvault.go` |
| `internal/api/router.go` | One `dbHandler := handlers.New...(...)` line and one `r.Route("/<service>s", ...)` block under the project-scoped route group. | search for `r.Route("/keyvaults"` |

What the framework will eventually do (next sections) is collapse the handler
+ router wiring + the boilerplate inside each provider method into a single
`managedservice.Register(...)` call. Each new managed-service then ships:
schema chunk + repo + provider adapter + a Service registration. Until then,
follow the cargo-cult above.

The 80% of the per-service handler that is identical across services and
will move into the framework:

- TenantFromContext + TenantUUIDFromContext + role check
- JSON decode + name validate + per-field bounds check
- Project lookup (`lookupProjectUUID`)
- VPC resolution (vnet_id + subnet_id → NAD identity in project namespace)
- Quota pre-check (capCost callback)
- INSERT row with `status = PENDING`, return 201
- On Get: fetch row; if PENDING, overlay live CR status (phase + message + endpoint)
- On Delete: pre-check `deletion_protection`, mark DELETING, fire operator DELETE
- Shown-once credentials: SELECT row, if status != ACTIVE → 409, if credentials_consumed_at IS NOT NULL → 410, else fetch from Secret + MarkConsumed + return

The 20% that is service-specific:

- BuildSpec — translate canonical dc-api fields into the operator's idiomatic spec shape
- MapStatus — translate operator-specific status shape into the canonical view
- BuildCredential — extract service-specific keys from the Secret
- ClassCatalog (optional) — when the operator uses size-class strings, the lookup that picks the smallest satisfying class for a (cpu, memory) pair

---

## 1. Package layout

```
dc-api/internal/managedservice/
├── service.go      Service struct, Placement enum, CreateRequest, ServiceStatus,
│                   Credential, Cost — the framework's public data types.
├── register.go     Register() — wires CRUD routes into a chi.Router and returns
│                   a Controller instance.
├── controller.go   Controller — starts/stops the dynamic informer, maps CR status
│                   back to dc-api records, drives delete finalizer polling.
├── errors.go       Typed sentinel errors (NotFound, AlreadyExists, Forbidden,
│                   CapExceeded) so handlers map cleanly to HTTP status codes.
└── labels.go       ApplyLabels() helper — stamps all seven dc-api.wso2.com/*
                    labels onto an unstructured CR object before apply.
```

No file in this package imports any provider driver or any handler outside this
package. It depends on: `db.Repository`, `k8s.io/client-go/dynamic`, `chi`, `zerolog`.

---

## 2. Service struct

```go
package managedservice

import (
    "context"

    "github.com/go-chi/chi/v5"
    "github.com/google/uuid"
    "github.com/rs/zerolog"
    corev1 "k8s.io/api/core/v1"
    "k8s.io/apimachinery/pkg/runtime/schema"
    "k8s.io/client-go/dynamic"

    "github.com/wso2/dc-api/internal/db"
)

// Placement controls which namespace tier dc-api places a CR in.
//
// Resources are ALWAYS PlacementProject — they are the user-facing layer and
// must live in dc-<tenant>-<project> so the URL matches the namespace.
// Placement is therefore only meaningful on a Service's Backend definition,
// which can be tenant-tier or platform-tier when shared.
type Placement int

const (
    // PlacementProject — one CR per resource, in dc-<tenant>-<project>.
    // The only valid Placement for the Resource CR.
    PlacementProject Placement = iota
    // PlacementTenant — one CR per tenant, in dc-tenant-<tenant>.
    // Use for Backends shared across a tenant's projects.
    PlacementTenant
    // PlacementPlatform — one CR for the whole platform, in dc-system.
    // Use for Backends shared across all tenants.
    PlacementPlatform
)

// Service describes one managed-service type to the framework.
// Integrators construct one of these and pass it to Register().
// All function fields are called synchronously in the HTTP request path
// unless noted otherwise; they MUST be safe to call concurrently.
type Service struct {
    // Name is the human-readable service type label used in log lines and
    // dc-api API resource-kind labels. Examples: "database", "cache", "keyvault".
    Name string

    // Resource describes the user-facing CR (always project-tier).
    Resource ResourceSpec

    // Backend is optional. Non-nil when the service uses a shared Backend
    // (e.g. one OpenBao StatefulSet hosting many Key Vault mounts). When
    // non-nil, the framework ensures the Backend CR exists and is Ready
    // before creating any Resource CR that references it.
    Backend *BackendSpec

    // BuildSpec converts a dc-api CreateRequest into the Resource CR's spec map.
    // The returned map is merged directly into the CR's `spec` field.
    // Return an error to short-circuit the request with a 400 or 422.
    BuildSpec func(ctx context.Context, req CreateRequest) (map[string]any, error)

    // MapStatus converts the raw Resource CR status (from the dynamic client)
    // into the framework's ServiceStatus. Called on every GET and on every
    // informer event in the controller.
    MapStatus func(crStatus map[string]any) ServiceStatus

    // BuildCredential is called by the GET .../{id}/credentials endpoint
    // (first call only — consumed once, see open question 1). Extracts
    // credentials from the operator-managed Secret.
    // sec is the Secret pointed to by status.endpoint.secretRef.
    // crStatus is the full Resource status map at the time of the call.
    BuildCredential func(ctx context.Context, sec *corev1.Secret, crStatus map[string]any) (Credential, error)

    // CapCost returns the quota units this create request would consume.
    // The framework calls this before creating the CR and refuses the request
    // with a 403 if it would breach the tenant's capacity cap. For services
    // that don't expose sizing to users, return zero — the Backend's
    // CapCost (if any) covers the actual resource use.
    CapCost func(req CreateRequest) Cost

    // PostReadyHook is optional. When non-nil, the controller calls it once
    // after the Resource CR first reaches Ready. Use for service-specific
    // post-ready operations that require the running Backend (e.g. creating
    // a KV mount and AppRole inside an already-running secret store).
    // Returning an error sets the dc-api record to FAILED.
    PostReadyHook func(ctx context.Context, req PostReadyRequest) error
}

// ResourceSpec describes the user-facing CRD.
type ResourceSpec struct {
    GVK      schema.GroupVersionKind  // CRD identity
    Resource schema.GroupVersionResource  // plural for dynamic.Client.Resource()
    // Placement is always PlacementProject for Resources; the field exists
    // for symmetry with BackendSpec and is validated at Register() time.
    Placement Placement

    // CredentialSecretRefPath is the JSON path inside the operator's
    // status from which the framework reads the credential Secret name.
    // Convention: ["status", "endpoint", "secretRef", "name"]. Operators
    // that follow an established cloud-API convention (e.g.
    // ["status", "masterUserSecret", "name"]) declare the path here so
    // the framework looks in the right place. The path is required.
    CredentialSecretRefPath []string

    // CredentialSecretKeys names the data keys the framework reads from
    // the credential Secret when building the Credential response. Each
    // entry maps a dc-api-canonical credential field (Username, Password,
    // Token, CACert) to the actual Secret key the operator writes
    // (e.g. "admin_user", "admin_password", "ca_cert"). Operators that
    // use the canonical names ({"username","password","ca_cert"}) can
    // leave this nil — the framework defaults to those keys.
    //
    // Required when the operator writes its own non-canonical keys (e.g.
    // RDS-style "admin_user"/"admin_password", or token-only services
    // using just {"token": "..."}). Wrong keys here surface as a 503 on
    // GET .../credentials because the framework returns empty fields and
    // refuses to mark the credential consumed.
    CredentialSecretKeys map[string]string // canonical → operator-specific
}

// BackendSpec describes the shared-backend CRD (when present).
type BackendSpec struct {
    GVK      schema.GroupVersionKind
    Resource schema.GroupVersionResource
    // Placement is PlacementTenant or PlacementPlatform — not Project.
    Placement Placement

    // BuildSpec produces the Backend CR's spec. Called when the framework
    // is about to create a Backend (typically on the first Resource create
    // for a tenant). For PlacementTenant, ctx carries the tenant UUID; for
    // PlacementPlatform, the call is once at startup.
    BuildSpec func(ctx context.Context, scope BackendScope) (map[string]any, error)

    // MapStatus mirrors ResourceSpec's MapStatus but for the Backend CR.
    // Used to determine when a Backend is Ready so dependent Resources can
    // proceed.
    MapStatus func(crStatus map[string]any) ServiceStatus

    // CapCost returns the quota units the Backend consumes when it is
    // provisioned. Charged against the tenant cap on first Backend create
    // (PlacementTenant) or absorbed by the platform (PlacementPlatform).
    CapCost func(scope BackendScope) Cost
}

// BackendScope identifies which Backend instance we're talking about.
// For PlacementTenant: TenantID/TenantUUID set, ProjectID/ProjectUUID empty.
// For PlacementPlatform: all empty.
type BackendScope struct {
    TenantID    string
    TenantUUID  uuid.UUID
    ProjectID   string       // unused today; reserved for project-tier Backends if ever needed
    ProjectUUID uuid.UUID
}
```

---

## 3. Data types

```go
// CreateRequest carries everything the framework knows at CR-create time.
// BuildSpec and CapCost receive this.
type CreateRequest struct {
    // Caller identity
    TenantID   string
    TenantUUID uuid.UUID
    ProjectID  string
    ProjectUUID uuid.UUID
    PrincipalID string // IdP sub or SA ID — for audit annotations

    // Resource identity (dc-api owns these)
    ResourceUUID uuid.UUID
    ResourceName string

    // Raw request body fields after JSON decode.
    // BuildSpec is responsible for validating and transforming these.
    Params map[string]any

    // NetworkRef is set by the framework when the resolved subnet NAD exists.
    // Nil when the service's spec.networkRef is not required (PlacementTenant,
    // PlacementPlatform, or services that don't need a customer-VPC NIC).
    NetworkRef *NetworkRef
}

// NetworkRef describes the Multus NAD in the project namespace.
type NetworkRef struct {
    Namespace string
    Name      string
}

// ServiceStatus is the framework-normalised view of a CR's status.
// Returned by Service.MapStatus.
type ServiceStatus struct {
    // Phase mirrors the contract's phase enum as a dc-api status string.
    // Mapping: Pending/Provisioning → "PENDING"; Ready → "ACTIVE";
    // Failed → "FAILED"; Terminating → "DELETING".
    Phase string // "PENDING" | "ACTIVE" | "FAILED" | "DELETING"

    // Message is the operator's human-readable status.message, passed through
    // verbatim to the dc-api GET response.
    Message string

    // EndpointAddress and EndpointPort come from status.endpoint when present.
    EndpointAddress string
    EndpointPort    int

    // CredentialSecretRef is the namespace/name of the credential Secret.
    // Empty when status.endpoint.secretRef is not yet set.
    CredentialSecretRef string // "<namespace>/<name>"
}

// Credential is the service-specific credential bundle returned once on create.
// BuildCredential populates whichever fields apply to the service type.
// The framework JSON-serialises this into the create response body.
type Credential struct {
    // Common fields — set whichever apply.
    Username string `json:"username,omitempty"`
    Password string `json:"password,omitempty"`
    Token    string `json:"token,omitempty"`   // for token-based auth
    CACert   string `json:"ca_cert,omitempty"` // PEM, when TLS is used
    // Arbitrary extra fields for service-specific metadata.
    // Keys must be snake_case strings; values must be JSON-serialisable.
    Extra map[string]any `json:"extra,omitempty"`
}

// Cost represents the quota units a create request would consume.
// The framework sums this against the tenant's remaining cap before proceeding.
type Cost struct {
    CPUCores int
    MemoryGB int
    StorageGB int
}

// PostReadyRequest is passed to Service.PostReadyHook.
type PostReadyRequest struct {
    TenantID    string
    TenantUUID  uuid.UUID
    ProjectID   string
    ProjectUUID uuid.UUID
    ResourceUUID uuid.UUID
    // CRStatus is the full status map at the moment of Ready transition.
    CRStatus map[string]any
    // DynClient is available for any additional Kubernetes operations needed.
    DynClient dynamic.Interface
}

// Deps bundles the shared dependencies Register() needs.
type Deps struct {
    DynClient dynamic.Interface
    Repo      *db.Repository
    Log       zerolog.Logger
}
```

---

## 4. Register function

```go
// Register installs CRUD HTTP routes and starts the background controller
// for a managed service type.
//
// basePath is a chi route pattern, e.g.
//   "/v1/tenants/{tenant_id}/projects/{project_id}/databases"
//
// The framework auto-registers:
//   POST   basePath/                → Create (202 Accepted)
//   GET    basePath/                → List
//   GET    basePath/{id}            → Get (includes current status)
//   DELETE basePath/{id}            → Delete (202 Accepted)
//
// Register MUST be called after the chi.Router's auth middleware is in scope.
// Returns an error if the Service definition is missing required fields.
func Register(router chi.Router, basePath string, svc Service, deps Deps) error
```

The returned error is non-nil only at startup — missing `Name`, nil `BuildSpec`, etc.
After that the framework operates autonomously. Call site in `router.go` panics on
non-nil error so misconfiguration is caught at startup, not at request time.

---

## 5. Routes installed

All routes follow the existing dc-api conventions exactly.

### POST (Create)

- Returns `202 Accepted`.
- Body: the db record shape (id, status: "PENDING", created_at, updated_at).
  No credential — credentials are delivered separately via the dedicated
  endpoint below (resolved in open question 1).
- Pre-checks in order:
  1. Resolve Backend if `svc.Backend != nil`. If no Backend CR exists in the
     target namespace, build one via `Backend.BuildSpec`, apply, and wait for
     it to reach Ready (with a deadline — short-circuit to 503 if it takes
     longer than the configured Backend-provision timeout).
  2. Tenant cap via `CapCost` (Resource + Backend's `CapCost` on first Backend create).
  3. Project quota (ResourceQuota admission will also catch it, but we pre-check
     to give a better error message).
  4. Name uniqueness within the project (409 on duplicate).
- Then create the Resource CR via `BuildSpec` and return immediately.

### GET .../{id}/credentials (one-shot)

- Returns `200 OK` with the JSON-serialised `Credential` struct on first call
  after the Resource reaches `status.phase: Ready`.
- Returns `409 Conflict` if the Resource is not yet Ready (caller should poll
  `GET .../{id}` first).
- Returns `410 Gone` on second and subsequent calls. dc-api stamps a
  `credentials_consumed_at` column on the db record; the endpoint refuses
  to call `BuildCredential` once this column is set.
- Requires the same role as `GET .../{id}` (member).

### GET (single)

- Returns `200 OK`.
- Body: db record shape plus `"endpoint_address"` and `"endpoint_port"` from
  current `ServiceStatus`. No credential on GET — shown-once guarantee.
- The framework calls `MapStatus` on the live CR (not cached) on every GET.

### LIST

- Returns `200 OK`, array body.
- Filtered to the caller's tenant + project. Pagination: standard `page`/`per_page`
  query params (same as other dc-api list endpoints — not implemented in the
  framework's first version; add as a follow-up).

### DELETE

- Returns `202 Accepted`.
- Body: the resource record with status updated to "DELETING".
- Framework sets the db record to DELETING, issues DELETE on the CR, and lets the
  controller poll for finalizer completion.

---

## 5.1 VPC NAD resolution — shared helper

Every managed-service handler that takes `vnet_id` + `subnet_id` from the
user and needs to feed a Multus NAD identity to the operator runs identical
resolution code: load the VNet row, load the Subnet row, check ownership +
ACTIVE status, derive `(project-namespace, subnet.BackendUID)`. The
framework provides this as a single helper so per-service handlers do not
re-implement it:

```go
// ResolveNAD reads the user-supplied (vnetID, subnetID) and returns the
// Multus NAD identity the operator should attach to. Errors:
//   - ErrVNetNotFound        404 — "vnet_id not visible to tenant"
//   - ErrSubnetNotFound      404 — "subnet_id not visible to tenant"
//   - ErrSubnetWrongVNet     400 — "subnet does not belong to vnet"
//   - ErrSubnetNotActive     409 — "subnet is not ACTIVE"
//
// The handler maps these to HTTP responses and never sees the raw IDs.
// Callers should forward the returned NetworkRef as req.NetworkRef on
// the CreateRequest so the operator-specific BuildSpec emits whichever
// shape the CRD expects (struct or "<ns>/<name>" string — see contract §4.2).
func ResolveNAD(
    ctx context.Context,
    repo *db.Repository,
    tenantUUID uuid.UUID,
    vnetID, subnetID uuid.UUID,
    tenantSlug, projectSlug string,
) (*NetworkRef, error)
```

The helper hides the fact that NADs are produced by the KubeOVN provider at
subnet creation; the operator sees only a `(namespace, name)` pair. NADs
that back legacy bridge VLANs vs KubeOVN VPC subnets are indistinguishable
to the operator — Multus is the common interface. Operators that worked
against a flat bridge in their dev environment integrate into a VPC-backed
prod environment with no code change.

For services whose Resource lives at the VPC layer (databases, caches,
VM-backed analytics), `vnet_id` + `subnet_id` are REQUIRED on the create
request and `BuildSpec` must consume `req.NetworkRef`. For services with
no per-Resource VPC binding (key vaults, container registries — they
expose via a per-Backend Service that PrivateEndpoints proxy into VPCs),
`req.NetworkRef` is nil and `BuildSpec` ignores it.

---

## 6. Reconciler model

The `Controller` started by `Register` runs one goroutine per service type.

```
Controller
 ├── dynamic informer watching GVK across all namespaces
 │   (filtered to objects with dc-api.wso2.com/* labels)
 └── on each informer event:
       1. Read CR's status
       2. Call svc.MapStatus(crStatus) → ServiceStatus
       3. Look up dc-api DB record by resource-uuid label
       4. If phase changed: update db record status
       5. If just-reached Ready AND PostReadyHook != nil:
            call svc.PostReadyHook(...) in a goroutine
            on hook error: set db record to FAILED, set message
       6. If phase == Terminating AND CR no longer exists:
            delete db record
```

The informer uses `cache.NewFilteredListWatchFromClient` restricted to objects
carrying `dc-api.wso2.com/resource-kind=<svc.Name>`. This avoids watching
unrelated objects in shared namespaces.

Polling fallback: if the informer misses an event (controller restart, watch
timeout), the existing reconciler goroutine in `internal/reconciler/reconciler.go`
sweeps all PENDING/DELETING db records every 60 seconds and calls `MapStatus`
against a fresh GET of the CR. The managed-service controller supplements this;
it does not replace it.

---

## 7. Tenant-cap pre-check

```
POST arrives
  → framework calls svc.CapCost(req) → Cost{CPUCores:2, MemoryGB:4, StorageGB:20}
  → framework calls repo.GetTenantCapAndAllocation(ctx, tenantUUID)
  → if (allocated.CPU + cost.CPUCores > cap.CPU) → 403 {"error":"cpu quota exceeded: ..."}
  → same for memory, storage
  → proceed to CR creation
```

The check is intentionally optimistic (no locking). The ResourceQuota on the
project namespace is the hard backstop. The pre-check exists to give a readable
error message before hitting Kubernetes admission; it is not a security boundary.

---

## 8. Delete and finalizer flow

```
DELETE /v1/tenants/{tid}/projects/{pid}/databases/{id}
  → framework marks db record status = "DELETING"
  → framework issues DELETE on the CR (k8s sets deletionTimestamp)
  → operator's finalizer begins; sets status.phase = Terminating
  → controller informer detects Terminating, keeps db record as DELETING
  → operator completes teardown, removes finalizer; CR disappears from API
  → controller informer detects CR gone → deletes db record
  → subsequent GET returns 404
```

The framework does NOT poll on a fixed timer waiting for deletion. It relies on
the informer event when the CR disappears. The 60-second reconciler sweep is the
fallback if that event is missed.

Force-delete (caller sends DELETE twice): the second DELETE is idempotent —
if the db record is already DELETING and the CR still exists with a
deletionTimestamp, return 202 again.

---

## 9. Error handling

```go
// errors.go

// ErrNotFound is returned when the managed-service CR or db record does not exist.
var ErrNotFound = errors.New("managed service instance not found")

// ErrAlreadyExists is returned when a create would conflict with an existing
// record (same tenant + project + name).
var ErrAlreadyExists = errors.New("managed service instance already exists")

// ErrForbidden is returned when the caller's role does not permit the operation.
var ErrForbidden = errors.New("insufficient role for operation")

// ErrCapExceeded is returned when the create would breach the tenant cap.
// The error message includes which dimension was exceeded and by how much.
type ErrCapExceeded struct {
    Dimension string // "cpu", "memory_gb", "storage_gb"
    Cap       int
    Allocated int
    Requested int
}
func (e *ErrCapExceeded) Error() string

// ErrProviderFailure wraps an underlying Kubernetes API error.
type ErrProviderFailure struct {
    Op  string
    Err error
}
func (e *ErrProviderFailure) Error() string
func (e *ErrProviderFailure) Unwrap() error
```

HTTP mapping in the framework's handler shim:
- `ErrNotFound` → 404
- `ErrAlreadyExists` → 409
- `ErrForbidden` → 403
- `*ErrCapExceeded` → 403
- `*ErrProviderFailure` → 500
- Any other error → 500

All errors use the flat envelope: `{"error": "human-readable message"}`.

---

## 10. What the framework does NOT do

The following are explicitly NOT generic — they live in per-service handler glue
registered as `PostReadyHook` or in dedicated route extensions:

- **Mount/path creation inside a running service** — e.g. creating a KV-v2 mount
  and AppRole inside an already-running secret store after it reaches Ready.
  This is OpenBao-specific; the framework has no concept of "sub-resources within
  a running service."

- **Sub-resource CRUD** — any routes beyond POST/GET/LIST/DELETE on the instance
  itself (e.g. `POST .../databases/{id}/snapshots`) are NOT auto-generated by
  the framework. Register them separately in router.go using the normal handler
  pattern.

- **Credential rotation** — the framework delivers credentials exactly once on
  create. Rotation is a per-service concern. The framework provides no rotation
  endpoint or rotation lifecycle.

- **Cross-service dependencies** — if service A depends on service B being Ready
  first, that ordering is the caller's responsibility. The framework creates each
  CR independently.

- **Multi-instance per request** — one POST = one CR. If a service type naturally
  deploys multiple CRs per user request (unusual), that logic belongs in BuildSpec
  or PostReadyHook, not in the framework.

- **Upgrade/migrate** — spec field updates after creation are not handled. The
  framework's CRUD surface has no PATCH endpoint. Add it as a per-service extension
  if needed.

---

## 11. Open questions

### Resolved before implementation (2026-05-21)

1. **Credential delivery timing — RESOLVED.** Dedicated `GET .../{id}/credentials`
   endpoint, consumed once. dc-api stamps a `credentials_consumed_at` column on the
   db record after the first successful read; subsequent calls return 410 Gone.
   Caller flow: POST → poll `GET .../{id}` until ACTIVE → GET `.../credentials`
   exactly once.

2. **Backend/Resource singleton semantics — RESOLVED.** The framework treats
   Backends and Resources as separate concepts. Resources are always per-project,
   per-request (one user POST = one Resource CR in the project namespace).
   Backends are per-tenant or per-platform; the framework lazy-creates one when
   the first dependent Resource is created and reuses it for all subsequent
   Resources in that scope. Users never see the Backend; they see independent
   Resource records at `/v1/tenants/{tid}/projects/{pid}/<resource>`.

3. **Spec mutability — RESOLVED.** Per the contract: any spec field that maps
   to a dc-api quota dimension MUST be immutable in v0.1. Resize is a v1beta1
   concern. Other fields follow the operator's own mutability rules.

### Still open (revisit during implementation)

4. **PostReadyHook failure handling.** If the hook fails (e.g. OpenBao API call
   fails after the Backend is Ready), the framework sets the dc-api record to
   FAILED. But the CR itself is still Ready. Should the framework also patch the
   CR to surface the hook failure? Or leave CR status untouched and only reflect
   in dc-api? Current design leaves CR untouched (less coupling to operator
   internals).

5. **Dynamic informer scope.** Watching all namespaces for each service type
   is correct but generates a watch per registered service. With 4–5 service types
   and 50+ tenants this is manageable. If we hit watch limits, switch to a single
   shared informer filtered by label selector across all managed-service CRDs.
   Revisit when the tenant count exceeds 20.

6. **List pagination.** First version omits pagination on LIST. Define `page` and
   `per_page` query parameters consistent with other dc-api LIST endpoints before
   any managed-service list is expected to return > 50 records.

7. **`dc-api/internal/managedservice` vs new top-level package.** Current plan puts
   this in `internal/managedservice`. If the framework grows to own its own DB
   table (for managed-service-specific metadata beyond the generic `resources` table),
   it may need its own migration slice. Confirm whether the generic `resources` table
   is sufficient before designing the DB layer.

8. **Backend deletion / orphan cleanup.** When the last Resource bound to a
   shared Backend is deleted, should the framework delete the Backend too?
   For Key Vaults: probably no (tearing down OpenBao loses the unseal keys; tenant
   may want to recreate vaults later). For Registry: probably no (single platform
   instance, never deleted). Current default: Backends are never automatically
   deleted; cleanup is a manual platform-admin operation.
