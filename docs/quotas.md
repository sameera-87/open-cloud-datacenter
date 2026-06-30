# Tenant & Project Quotas

dc-api enforces resource limits at two levels: a **tenant capacity cap** (platform-admin ceiling) and **per-project quotas** (distributed from that cap by the tenant owner). The sum of all project quotas within a tenant can never exceed the tenant cap.

---

## Tenant capacity caps

Set by a platform admin via `PATCH /v1/admin/tenants/{id}`. Stored on the `tenants` table.

| Field | Default | Description |
|---|---|---|
| `cpu_cores_cap` | 80 cores | Maximum CPU cores across all projects in the tenant |
| `memory_gb_cap` | 256 GB | Maximum RAM across all projects |
| `storage_gb_cap` | 2000 GB | Maximum storage across all projects |

---

## Per-project quotas

Set by the tenant owner when creating or patching a project. Stored on the `projects` table (capacity) and `project_quotas` table (object counts).

### Capacity (hardware resources)

| Field | Default | Description |
|---|---|---|
| `cpu_cores` | 20 cores | CPU cores allocated to this project |
| `memory_gb` | 64 GB | RAM allocated to this project |
| `storage_gb` | 500 GB | Storage allocated to this project |

### Object counts

| Field | Default | Description |
|---|---|---|
| `max_vnets` | 10 | Virtual networks |
| `max_clusters` | 2 | Kubernetes clusters |
| `max_volumes` | 50 | Persistent volumes |
| `max_public_ips` | 3 | Public IP addresses |

---

## Enforcement rules

- When a project is created or its quotas are patched, dc-api checks:
  `sum(cpu_cores | memory_gb | storage_gb across all projects) ≤ tenant cap`
  inside a transaction with `SELECT FOR UPDATE` on the tenant row — no race conditions.
- A platform admin cannot shrink a tenant cap below its already-allocated project sum.
- Project capacity quotas are also mirrored as a Kubernetes `ResourceQuota` on the backing namespace (defense-in-depth).

---

## Sources

| Thing | Location |
|---|---|
| Schema DDL | [`dc-api/internal/db/schema.sql`](../dc-api/internal/db/schema.sql) lines 698–725, 781–788 |
| Tenant model | [`dc-api/internal/models/tenant.go`](../dc-api/internal/models/tenant.go) |
| Enforcement logic | [`dc-api/internal/db/projects.go`](../dc-api/internal/db/projects.go), [`tenants.go`](../dc-api/internal/db/tenants.go) |
| Admin API handler | [`dc-api/internal/api/handlers/admin_tenants.go`](../dc-api/internal/api/handlers/admin_tenants.go) |
