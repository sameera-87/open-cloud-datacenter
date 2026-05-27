# KVI Controller — Design

**Status:** Implementation target. Begin code against this doc.
**Companion:** `docs/managed-services-integration.md` (the contract this conforms to).
**Audience:** dc-api maintainers + future contributors building or extending KVI.

---

## 1. What KVI is (and isn't)

**KVI** = KeyVault Infrastructure operator. A Kubernetes operator built with
Kubebuilder v4 that runs OpenBao instances on the platform's Harvester cluster
and provisions mount paths + AppRoles inside them.

### In scope

1. **KeyVaultBackend CRD** — represents a per-tenant OpenBao HA Raft cluster.
   The reconciler:
   - Renders and applies a StatefulSet, headless Service, ConfigMap, and PVCs
     (the same shape Helm produces from `openbao/openbao` v0.28.2 — but applied
     natively, not via in-cluster Helm).
   - On first reconcile after pods are Running, calls `bao operator init`,
     captures unseal keys + root token, stores them in a Secret in the Backend's
     namespace.
   - Unseals each pod and joins followers to the Raft cluster.
   - Sets `status.phase: Ready` when 3-pod Raft cluster is formed and leader
     elected.

2. **KeyVault CRD** — represents a single user-facing Key Vault. The reconciler:
   - Waits for parent KeyVaultBackend to be Ready.
   - Uses the Backend's root token to enable a KV-v2 mount at
     `tenants/<tenant_uuid>/<kv_uuid>/`.
   - Creates a policy scoped to that mount only.
   - Creates an AppRole bound to that policy.
   - Generates a fresh secret_id for the AppRole.
   - Writes role_id + secret_id + mount_path + backend_endpoint to a Secret
     in the project namespace.
   - Sets `status.phase: Ready` and populates `status.endpoint.secretRef`.

### Out of scope (handled by other code)

| Concern | Owner |
|---|---|
| Proxy pod creation, NAD selection, customer-VPC IP allocation | dc-api PrivateEndpoint handler (already shipped) |
| User-facing CRUD (`POST /v1/.../keyvaults`) | dc-api KeyVault handler (existing chunks 1+2) |
| Authentication of dc-api callers (OIDC, AppRole for SAs) | dc-api auth middleware |
| Tenant namespace creation (`dc-tenant-<slug>`) | dc-api admin-tenant-create handler (gap to fix) |
| Cross-VPC access path | PrivateEndpoint creates the proxy; KVI just exposes the in-cluster Service address in `status.endpoint` |

---

## 2. Repo location

`sovereign-cloud/crds/keyvault/` for now (lifts cleanly to
`open-cloud-datacenter/crds/keyvault/` once stable, alongside other
managed-service operator CRDs the platform ships).

Layout (post-`kubebuilder init`):

```
crds/keyvault/
├── PROJECT                       kubebuilder project metadata
├── Dockerfile                    controller image build
├── Makefile                      kubebuilder targets (manifests, generate, run, test)
├── go.mod / go.sum
├── cmd/main.go                   manager entrypoint
├── api/v1alpha1/
│   ├── groupversion_info.go      GVK registration
│   ├── keyvaultbackend_types.go  KeyVaultBackend spec + status
│   ├── keyvault_types.go         KeyVault spec + status
│   └── zz_generated.deepcopy.go  generated
├── internal/controller/
│   ├── keyvaultbackend_controller.go
│   ├── keyvault_controller.go
│   ├── openbao/                  OpenBao API client wrapper
│   │   ├── client.go             bao operator init/unseal/raft helpers via HTTP
│   │   ├── mount.go              KV-v2 mount management
│   │   └── approle.go            AppRole management
│   └── render/                   Helm-equivalent rendering of OpenBao manifests
│       ├── statefulset.go
│       ├── service.go
│       └── configmap.go
├── config/                       kustomize manifests for CRDs + RBAC + manager deployment
└── test/                         envtest + integration scaffolding
```

The `openbao/` wrapper uses OpenBao's HTTP API directly (not the `bao` CLI in a
subprocess) — cleaner error handling, no shell escaping. The `render/` package
emits unstructured.Unstructured objects so the reconciler can apply them via
controller-runtime client.

---

## 3. CRD: KeyVaultBackend

### 3.1 Spec

```go
type KeyVaultBackendSpec struct {
    // Capacity for the backend cluster. The controller sizes the StatefulSet
    // resource requests/limits from these.
    CPU       resource.Quantity `json:"cpu"`        // total across pods
    MemoryGB  int               `json:"memoryGB"`   // total across pods
    StorageGB int               `json:"storageGB"`  // per-pod PVC size

    // EngineConfig holds OpenBao-specific tuning.
    EngineConfig BackendEngineConfig `json:"engineConfig"`
}

type BackendEngineConfig struct {
    // HAReplicas — number of pods in the Raft cluster. Must be odd. Default 3.
    HAReplicas int `json:"haReplicas"`

    // StorageClass — Longhorn class for the data PVCs. Default "longhorn".
    StorageClass string `json:"storageClass,omitempty"`

    // AuditLogPath — file path inside each pod where the file audit device
    // writes. Default "/openbao/audit/audit.log". The reconciler also mounts
    // an audit PVC at /openbao/audit.
    AuditLogPath string `json:"auditLogPath,omitempty"`
}
```

### 3.2 Status

Conforms to the contract's status shape (see `managed-services-integration.md` §5).

```go
type KeyVaultBackendStatus struct {
    Phase      string             `json:"phase"`       // Pending|Provisioning|Ready|Failed|Terminating
    Conditions []metav1.Condition `json:"conditions,omitempty"`

    // Endpoint is the in-cluster address of the active leader.
    // Populated when Raft leader is elected.
    Endpoint *EndpointRef `json:"endpoint,omitempty"`

    // KeyMaterialRef points at the Secret holding unseal keys + root token.
    // Set once after init. Subsequent reconciles never write this again.
    KeyMaterialRef *corev1.LocalObjectReference `json:"keyMaterialRef,omitempty"`

    // Resources tracks every owned object for idempotent reconciliation.
    Resources []ResourceRef `json:"resources,omitempty"`

    Message string `json:"message,omitempty"`
}

type EndpointRef struct {
    Address   string                       `json:"address"`              // openbao-active.<ns>.svc.cluster.local
    Port      int                          `json:"port"`                 // 8200
    SecretRef *corev1.LocalObjectReference `json:"secretRef,omitempty"`  // empty on Backend (not used)
}
```

### 3.3 Reconciler state machine

```
Pending → Provisioning → Ready → (Terminating on delete)
            ↓ (any failure)
          Failed
```

Reconcile steps (each idempotent; check `status.resources` before creating):

1. **Render manifests** — StatefulSet, headless Service (`openbao-internal`),
   active Service (`openbao-active`), standby Service (`openbao-standby`),
   ConfigMap (HCL config with raft + listener + telemetry).
2. **Apply child resources** — via `client.Patch(ctx, obj, client.Apply, ...)`
   with `FieldOwner: "kvi-keyvaultbackend"`.
3. **Wait for pods Running** — query StatefulSet status; requeue if not all
   replicas Running.
4. **Init Raft cluster** (one-shot, gated on `status.keyMaterialRef == nil`):
   - Call `POST /v1/sys/init` on `openbao-0`'s pod IP via the in-cluster Service
     with `{secret_shares: 5, secret_threshold: 3}`.
   - Capture root_token + unseal_keys_b64.
   - Create Secret `keyvaultbackend-<tenant>-keys` with `root_token` and
     `unseal_keys` fields (newline-separated).
   - Set `status.keyMaterialRef` to that Secret.
5. **Unseal leader** — call `PUT /v1/sys/unseal` three times against `openbao-0`
   with the first 3 unseal keys.
6. **Join + unseal followers** — for each pod 1..(replicas-1):
   - Call `POST /v1/sys/storage/raft/join` against the pod with
     `leader_api_addr: http://openbao-0.openbao-internal:8200`.
   - Unseal that pod with the 3 keys.
7. **Verify leader election** — poll `GET /v1/sys/leader` until non-empty
   `leader_address`. Set `status.endpoint.address` to `openbao-active.<ns>.svc.cluster.local`,
   `status.endpoint.port` to 8200.
8. **Enable audit device** (one-shot, gated on a `audit-enabled` annotation
   on the Backend) — call `PUT /v1/sys/audit/file` with the audit log path.
9. **Set `status.phase: Ready`** and the `Ready` condition.

If any step fails: set `status.phase: Failed`, set the Ready condition to
False with a reason, requeue with exponential backoff.

### 3.4 Finalizer

`keyvault.opencloud.wso2.com/backend-cleanup`. On delete:

1. Set `status.phase: Terminating`.
2. Refuse if any KeyVault CR references this Backend (`spec.backendRef.name`
   matches). Caller must delete dependent KeyVaults first. Surface count in
   `status.message`.
3. Delete owned StatefulSet, Services, ConfigMap.
4. Wait for pods to actually disappear (poll, not sleep — F26 pattern).
5. Delete PVCs (Longhorn deletes underlying volumes).
6. Delete key material Secret last.
7. Remove finalizer.

---

## 4. CRD: KeyVault

### 4.1 Spec

```go
type KeyVaultSpec struct {
    // BackendRef points at the KeyVaultBackend CR in dc-tenant-<slug>.
    // dc-api sets this; users never specify it.
    BackendRef BackendReference `json:"backendRef"`

    // SoftDeleteDays is honoured by OpenBao's KV-v2 metadata
    // delete_version_after configuration. Default 30, min 7, max 90.
    SoftDeleteDays int `json:"softDeleteDays,omitempty"`
}

type BackendReference struct {
    Name      string `json:"name"`      // KeyVaultBackend CR name
    Namespace string `json:"namespace"` // dc-tenant-<slug>
}
```

No CPU/memory/storage — Key Vaults don't expose sizing to users (they're
mount paths inside an already-sized Backend).

### 4.2 Status

```go
type KeyVaultStatus struct {
    Phase      string             `json:"phase"`
    Conditions []metav1.Condition `json:"conditions,omitempty"`

    // Endpoint.address is the Backend's in-cluster address. dc-api's
    // PrivateEndpoint handler uses this to configure proxy upstreams.
    // Endpoint.secretRef points at the AppRole credential Secret.
    Endpoint *EndpointRef `json:"endpoint,omitempty"`

    // MountPath inside the Backend. Set once on first successful reconcile.
    MountPath string `json:"mountPath,omitempty"`

    Resources []ResourceRef `json:"resources,omitempty"`
    Message   string        `json:"message,omitempty"`
}
```

### 4.3 Reconciler state machine

Same `Pending → Provisioning → Ready → Terminating | Failed` shape.

Steps:

1. **Resolve Backend** — fetch the KeyVaultBackend named in `spec.backendRef`.
   If not found or not Ready: requeue with backoff; set `Phase: Pending`.
2. **Build mount path** — `tenants/<tenant-uuid-from-labels>/<resource-uuid-from-labels>`.
3. **Read root token** — from `backend.status.keyMaterialRef`.
4. **Enable KV-v2 mount** (idempotent — check first):
   - `GET /v1/sys/mounts/<mountPath>` — if exists and `type==kv-v2`, skip.
   - Else `POST /v1/sys/mounts/<mountPath>` with `{type: "kv-v2", options: {version: "2"}}`.
5. **Configure soft-delete** — `POST /v1/<mountPath>/config` with
   `delete_version_after: <softDeleteDays>d`.
6. **Write policy** (idempotent):
   - Policy name: `kv-<resource-uuid>`.
   - Body: `path "<mountPath>/data/*" { capabilities = [...] }` etc.
   - `PUT /v1/sys/policies/acl/kv-<resource-uuid>`.
7. **Enable AppRole auth method** (one-shot per Backend — check first):
   - `GET /v1/sys/auth/approle` — if exists, skip.
   - Else `POST /v1/sys/auth/approle` with `{type: "approle"}`.
8. **Create AppRole role** (idempotent):
   - `POST /v1/auth/approle/role/kv-<resource-uuid>` with policy + TTLs.
9. **Generate secret_id** — `POST /v1/auth/approle/role/kv-<resource-uuid>/secret-id`.
10. **Read role_id** — `GET /v1/auth/approle/role/kv-<resource-uuid>/role-id`.
11. **Write credentials Secret** in the KeyVault's namespace (project ns):
    - Name: `keyvault-<8-char-uuid>-creds`
    - Owner reference: the KeyVault CR (so deletion is cascaded)
    - Labels: propagate all seven `dc-api.wso2.com/*` from the CR
    - Data: `role_id`, `secret_id`, `mount_path`, `backend_address`, `backend_port`
12. **Set status** — `Phase: Ready`, `Endpoint.address: backend.status.endpoint.address`,
    `Endpoint.secretRef.name: keyvault-<...>-creds`, `MountPath: <mountPath>`.

### 4.4 Finalizer

`keyvault.opencloud.wso2.com/keyvault-cleanup`. On delete:

1. `Phase: Terminating`.
2. **Disable AppRole role** — `DELETE /v1/auth/approle/role/kv-<resource-uuid>`.
3. **Delete policy** — `DELETE /v1/sys/policies/acl/kv-<resource-uuid>`.
4. **Disable mount** — `DELETE /v1/sys/mounts/<mountPath>`. NB: this is
   destructive — all secrets in the mount are lost. Honour the soft-delete
   window by NOT calling this until `deletionTimestamp + softDeleteDays`
   has passed. Practically: the reconciler requeues for `softDeleteDays` and
   only then calls the disable; until then `Phase: Terminating` and the
   credentials Secret is deleted (auth path closed) but the data persists.
5. Delete credentials Secret (cascaded via owner reference, no explicit call).
6. Remove finalizer.

---

## 5. RBAC

### 5.1 ClusterRole for the controller

- Full CRUD on `keyvault.opencloud.wso2.com` group.
- CRUD on Secrets (in tenant + project namespaces), PVCs, Services,
  ConfigMaps, StatefulSets (apps/v1) — restricted via Role bindings to the
  namespaces the controller manages (tenant + project tiers).
- Read on Namespaces (to discover them).
- No permissions on other CRD groups.

### 5.2 What the controller does NOT need

- Cross-tenant Secret read (each Backend's keys are read only by the controller
  reconciling that Backend; same SA, but the operations are scoped to the
  Backend's namespace via the request).
- Pod exec (init/unseal go via HTTP, not pod exec).
- Cluster-scoped admin.

---

## 6. Dependencies between CRs

```
KeyVaultBackend (dc-tenant-<slug>)
   ↑
   │ spec.backendRef
   │
KeyVault (dc-<tenant>-<project>)
```

KeyVault reconciler requeues with backoff when its Backend is not Ready.
No circular dependency — Backend has no awareness of dependent KeyVaults.

dc-api creates the Backend on the first KeyVault create for a tenant (the
framework's "lazy Backend create" — see `managed-services-framework.md` §5).
Subsequent KeyVault creates reuse the existing Backend.

---

## 7. Integration with dc-api

After the controller is built, dc-api's KeyVault handler will be refactored
(separate PR, separate session) to:

1. On `POST /v1/tenants/{tid}/projects/{pid}/keyvaults`:
   - Ensure `dc-tenant-<slug>` namespace exists (eager-create gap).
   - Ensure KeyVaultBackend CR exists in `dc-tenant-<slug>`; create if not.
   - Wait for Backend Ready (or return 202 immediately and let reconciler
     monitor — the framework handles this).
   - Create KeyVault CR in project namespace.
   - Return 202.

2. On `GET /v1/.../keyvaults/{id}/credentials` (new endpoint):
   - Read the credentials Secret pointed to by the KeyVault's
     `status.endpoint.secretRef`.
   - Return `{role_id, secret_id, mount_path, backend_address, backend_port}`.
   - Mark consumed; subsequent calls 410.

3. On `DELETE /v1/.../keyvaults/{id}`:
   - Issue `DELETE` on the KeyVault CR. Controller finalizer runs.
   - Framework polls for CR removal; dc-api row removed when CR gone.

---

## 8. Testing approach

### Local (envtest)

- `make test` runs controller logic against a fake API server.
- Cover: idempotency (re-reconcile = no-op), Backend-not-Ready requeue,
  finalizer flow, error → Failed transition.

### Integration (against harvester-dev)

- Reuse the spike's tenant: `spike-kvi` / `spike-proj` / `dc-tenant-spike-kvi`.
- After scaffolding, run controller locally pointed at harvester-dev kubeconfig
  (`make run KUBECONFIG=~/.kube/harvester-dev`).
- Manually create a `KeyVaultBackend` CR, verify it provisions OpenBao identical
  to the spike's manual install.
- Manually create a `KeyVault` CR, verify mount + AppRole + credentials Secret.
- Use the existing spike proxy + bastion to verify end-to-end secret access via
  the AppRole credentials returned in the Secret.

### Spec-diff check (per CLAUDE.md)

Before merging, compare:
- Generated StatefulSet/Services/ConfigMap from `render/` against the live
  Helm-installed objects from the spike (`kubectl get ... -o yaml | yq ...`).
- Any non-empty diff needs a one-line justification.

---

## 9. Open questions

1. **HCL vs JSON for OpenBao config.** OpenBao accepts both. HCL is more
   readable; JSON is easier to generate without templating. Recommendation:
   JSON config rendered from a Go struct. Confirms before reconciler is written.

2. **Audit device — required or optional?** The contract says operators should
   audit; OpenBao supports `file` audit device writing to a PVC. Cost: 1 extra
   PVC mount per pod. Default: enabled. Off-switch via the
   `keyvault.opencloud.wso2.com/audit=disabled` annotation on the Backend CR.

3. **Backend resize.** Spec is currently immutable on cpu/memoryGB/storageGB
   per the contract. When that lifts (v1beta1), the reconciler needs to handle:
   StatefulSet replicas++, PVC resize (Longhorn supports online resize), Raft
   reconfiguration. Defer to v1beta1.

4. **Auto-unseal vs shamir for production.** Spike used shamir-keys-in-Secret.
   For production, integrate with cloud KMS or OpenBao Transit. Mark v0.1 as
   shamir-only; v0.2 adds transit auto-unseal.

5. **What about controller HA?** Default kubebuilder template runs one
   controller pod. Adding leader election is one line. Confirm: enable for
   first release.

---

## 10. Implementation order (for follow-up sessions)

1. Kubebuilder scaffold (this session — task #60).
2. `KeyVaultBackend` CRD types + manifest generation. Verify CRD installs
   cleanly on harvester-dev.
3. `KeyVaultBackend` reconciler — render + apply pipeline (no init/unseal yet).
   Verify pods reach Running.
4. `KeyVaultBackend` reconciler — init + unseal + Raft join. Verify Ready.
5. `KeyVault` CRD types + manifest generation.
6. `KeyVault` reconciler — mount + AppRole + Secret. Verify end-to-end against
   spike's bastion.
7. Finalizers on both CRDs. Verify clean teardown.
8. Audit device + soft-delete honour.
9. dc-api KeyVault handler refactor — use the framework + new CRDs.
10. Wipe spike resources, end-to-end test from `dcctl keyvault create`.
