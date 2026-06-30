# Project Creation — How It Works

`POST /v1/tenants/{tenant_id}/projects` is the entry point. This document
explains every layer it touches, what gets created where, and the guarantees
and trade-offs involved.

---

## 1. System Context

```mermaid
graph LR
    Client["dcctl / cloud-ui"]
    DCAPI["dc-api"]
    PG["PostgreSQL"]
    HK8S["Harvester\nKubernetes API"]
    Rancher["Rancher API"]

    Client -->|"POST /v1/tenants/{tid}/projects\nBearer JWT"| DCAPI
    DCAPI -->|"INSERT projects\nINSERT project_quotas\n(transaction)"| PG
    DCAPI -->|"Create Namespace\nCreate ResourceQuota\n(async, best-effort)"| HK8S
    DCAPI -.->|"NOT called\nfor project creation"| Rancher

    style Rancher fill:#f0f0f0,stroke:#bbb,color:#666,stroke-dasharray: 5 5
```

Rancher is **not involved** in project creation. dc-api talks directly to the
Harvester cluster's Kubernetes API for namespace and quota provisioning.

---

## 2. End-to-End Request Flow

```mermaid
sequenceDiagram
    autonumber
    participant Client as dcctl / cloud-ui
    participant MW as Auth Middleware<br/>(OIDC + RBAC)
    participant H as ProjectHandler
    participant DB as PostgreSQL
    participant K8S as Harvester k8s API

    Client->>MW: POST /v1/tenants/{tid}/projects<br/>Authorization: Bearer <JWT>

    MW->>MW: Validate JWT signature & expiry
    MW->>DB: SELECT tenant_uuid WHERE id = {tid}
    DB-->>MW: tenant_uuid
    MW->>DB: SELECT role_assignments WHERE principal = caller
    DB-->>MW: assignments
    MW->>MW: Check ActionProjectWrite on tenant scope
    alt RBAC denied
        MW-->>Client: 403 Forbidden
    end

    MW->>H: route to handler (tenant, tenantUUID, userID in ctx)

    H->>H: Decode + validate request body<br/>Apply defaults (cpu=20, mem=64, storage=500, ...)

    H->>DB: CreateProject(project, quota) — begin transaction

    Note over DB: Transaction detail — see §3

    alt project slug already taken
        DB-->>H: ErrProjectAlreadyExists
        H-->>Client: 409 Conflict
    else quota would exceed tenant cap
        DB-->>H: ErrProjectQuotaExceedsTenantCap + TenantCapUsage
        H-->>Client: 422 quota_exceeded (cap, allocated, available)
    else success
        DB-->>H: project row + quota row
    end

    H->>H: spawn goroutine (best-effort namespace provisioning)
    H-->>Client: 201 Created — project JSON

    Note over H,K8S: Goroutine runs independently after response is sent

    H-)K8S: EnsureProjectNamespace(tenantID, projectID, projectUUID, cpu, mem, storage, maxVolumes)

    Note over K8S: Namespace detail — see §4

    alt namespace provisioning fails
        K8S-->>H: error (logged as WARN)
        Note over H: Project row is already committed.<br/>Namespace will be re-ensured on first VNet/VM create.
    else success
        K8S-->>H: Namespace + ResourceQuota created
    end
```

---

## 3. Database Transaction Detail

The `CreateProject` call runs inside a single PostgreSQL transaction with
`SELECT FOR UPDATE` on the tenant row to serialize concurrent creates.

```mermaid
flowchart TD
    A([Begin transaction]) --> B

    B["SELECT cpu_cores_cap, memory_gb_cap, storage_gb_cap\nFROM tenants\nWHERE tenant_uuid = ?\nFOR UPDATE"]
    B --> C{tenant found?}
    C -- No --> ERR1([Error: tenant not found])
    C -- Yes --> D

    D["SUM cpu_cores, memory_gb, storage_gb\nFROM projects\nWHERE tenant_uuid = ?"]
    D --> E

    E{"new alloc + existing\n≤ tenant cap?"}
    E -- "Any dimension\nexceeds cap" --> ERR2(["Rollback\n→ 422 quota_exceeded\n(cap, allocated, available headroom)"])
    E -- All within cap --> F

    F["INSERT INTO projects\n(id, tenant_id, tenant_uuid, name, description,\ncpu_cores, memory_gb, storage_gb, created_by)\nRETURNING project_uuid, ..."]
    F --> G{duplicate slug?}
    G -- "PG unique violation\n(23505)" --> ERR3(["Rollback\n→ 409 Conflict"])
    G -- Inserted --> H

    H["INSERT INTO project_quotas\n(project_uuid, max_vnets, max_clusters,\nmax_volumes, max_public_ips)\nRETURNING ..."]
    H --> I([COMMIT])
    I --> OK(["Return project + quota\n→ 201 Created"])

    style A fill:#dbeafe,stroke:#3b82f6,color:#1e3a8a
    style I fill:#dbeafe,stroke:#3b82f6,color:#1e3a8a
    style OK fill:#dcfce7,stroke:#16a34a,color:#14532d
    style ERR1 fill:#fee2e2,stroke:#ef4444,color:#7f1d1d
    style ERR2 fill:#fee2e2,stroke:#ef4444,color:#7f1d1d
    style ERR3 fill:#fee2e2,stroke:#ef4444,color:#7f1d1d
```

**Why `SELECT FOR UPDATE`?**  Two concurrent `POST /projects` calls for the same
tenant can both pass the `SUM ≤ cap` check independently and jointly breach the
cap. The row-level lock on the tenant row serializes them — the second transaction
blocks until the first commits, then re-reads the updated sum.

---

## 4. Kubernetes Namespace Provisioning

This step runs in a background goroutine after the HTTP response is already sent.
It has a **2-minute timeout** and is **best-effort** — failure is logged but does
not roll back the project row.

```mermaid
flowchart TD
    Start(["goroutine starts\n(ctx timeout = 2 min)"])
    Start --> NS_NAME["Compute namespace name\ndc-{tenantID}-{projectID}\ne.g. dc-choreo-sre-prod-infra"]

    NS_NAME --> CHECK_NS{Namespace exists?}

    CHECK_NS -- "Exists + Active" --> RQ
    CHECK_NS -- "Exists + Terminating" --> WAIT["Poll every 2s\nuntil gone or timeout"]
    WAIT --> CREATE_NS
    CHECK_NS -- "Not found" --> CREATE_NS

    CREATE_NS["kubectl create namespace dc-{tid}-{pid}\nLabels:\n  dc-api/managed: true\n  dc-api/tenant: {tenantID}\n  dc-api/project: {projectID}\n  dc-api.wso2.com/project-uuid: {uuid}\n  dc-api.wso2.com/resource-kind: project"]
    CREATE_NS --> RQ

    RQ["kubectl apply ResourceQuota 'dc-project-quota'\nin namespace dc-{tid}-{pid}"]

    RQ --> RQ_SPEC["Hard limits:\n  requests.cpu: {cpuCores}\n  requests.memory: {memGB}Gi\n  requests.storage: {storageGB}Gi\n  persistentvolumeclaims: {maxVolumes}"]

    RQ_SPEC --> SUCCESS(["Done — Namespace + ResourceQuota live"])

    style Start fill:#dbeafe,stroke:#3b82f6,color:#1e3a8a
    style WAIT fill:#fef9c3,stroke:#ca8a04,color:#713f12
    style SUCCESS fill:#dcfce7,stroke:#16a34a,color:#14532d
```

**Idempotent:** `EnsureProjectNamespace` is a create-or-patch — it can safely be
called multiple times (e.g. on the first VNet or VM create if the initial goroutine
failed). The ResourceQuota is patched to the current project quotas on every call,
so a subsequent `PATCH /projects/{pid}` quota change also calls this to sync the
k8s ResourceQuota.

---

## 5. Objects Created — Summary

```mermaid
erDiagram
    POSTGRESQL {
        uuid   project_uuid PK
        string id           "slug, unique per tenant"
        uuid   tenant_uuid  FK
        int    cpu_cores
        int    memory_gb
        int    storage_gb
        string created_by
    }

    PROJECT_QUOTAS {
        uuid project_uuid PK_FK
        int  max_vnets
        int  max_clusters
        int  max_volumes
        int  max_public_ips
    }

    K8S_NAMESPACE {
        string name        "dc-{tenantID}-{projectID}"
        label  managed     "dc-api/managed: true"
        label  tenant      "dc-api/tenant"
        label  project     "dc-api/project"
        label  uuid        "dc-api.wso2.com/project-uuid"
    }

    K8S_RESOURCEQUOTA {
        string name            "dc-project-quota"
        string namespace       "dc-{tenantID}-{projectID}"
        string requests_cpu    "e.g. 20"
        string requests_memory "e.g. 64Gi"
        string requests_storage "e.g. 500Gi"
        int    pvcs            "max_volumes"
    }

    POSTGRESQL ||--|| PROJECT_QUOTAS : "cascade"
    K8S_NAMESPACE ||--|| K8S_RESOURCEQUOTA : "owns"
```

---

## 6. Quota Enforcement — Two Layers

Project creation enforces quotas at two levels. Both must pass.

```mermaid
graph TB
    subgraph "Layer 1 — dc-api (application)"
        L1A["Tenant cap check\nSUM(all project allocs) + new project ≤ tenants.{cpu,mem,storage}_cap\nEnforced inside a SELECT FOR UPDATE transaction"]
        L1B["Object-count guardrails\nmax_vnets / max_clusters / max_volumes / max_public_ips\nChecked per resource-type handler at create time"]
    end

    subgraph "Layer 2 — Kubernetes (infrastructure)"
        L2["ResourceQuota on namespace\nrequests.cpu / requests.memory / requests.storage / PVCs\nKubernetes admission webhook blocks any Pod/PVC\nthat would exceed the hard limits — even if dc-api is bypassed"]
    end

    L1A --> L2
    L1B -. "object counts only;\nnot mirrored to k8s" .-> L2

    style L2 fill:#dbeafe,stroke:#3b82f6,color:#1e3a8a
```

The Kubernetes `ResourceQuota` is defense-in-depth: even if workloads are
submitted directly to the Harvester API (bypassing dc-api), the namespace quota
blocks any overcommit.

---

## 7. Error States and Recovery

| Scenario | dc-api response | State after | Recovery |
|---|---|---|---|
| Project slug already used in this tenant | `409 Conflict` | No rows written | Client chooses a different slug |
| New project quota + existing allocation exceeds tenant cap | `422 quota_exceeded` with headroom detail | No rows written | Admin raises tenant cap or reduce quota request |
| DB insert succeeds but Kubernetes API unreachable | `201 Created` | Project row committed; namespace absent | Auto-retried on first VNet/VM create via `EnsureProjectNamespace` |
| DB insert succeeds; namespace in `Terminating` state | `201 Created` | Goroutine polls up to 2 min | Creates namespace once old one finishes terminating; times out gracefully |
| Concurrent `POST /projects` race on same tenant | One wins, one gets serialized by `SELECT FOR UPDATE` | Both checked against live cap | No double-booking possible |
