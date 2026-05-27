// Package models — tenant.go
//
// Tenant is the canonical "what tenants exist" record from the `tenants`
// table. Separate from TenantSummary (the per-principal view returned by
// GET /v1/tenants for regular users) — Tenant has no principal context and
// carries no `roles` field.
//
// Populated two ways:
//  1. Auth middleware autoprovision — UPSERT on first sighting of any
//     `dc-tenant-<id>` group claim in a JWT.
//  2. Explicit POST /v1/admin/tenants — admin pre-registers empty tenants.
package models

import (
	"time"

	"github.com/google/uuid"
)

// Tenant matches the `tenants` table.
//
// TenantUUID is the immutable identity (Phase 6a). ID is the human-readable
// slug used in URLs and as the foreign key on per-tenant tables (legacy
// path); every per-tenant table also carries tenant_uuid for the
// post-backfill path. Re-registering a recycled slug produces a fresh
// TenantUUID, so orphan rows from the deleted tenant become invisible.
//
// Capacity caps (M2.5 hybrid quota model) — platform admin sets the ceiling
// per tenant; tenant owner distributes that budget across projects. Defaults
// land via the schema's column defaults (80 cpu / 256 GB RAM / 2 TB).
type Tenant struct {
	ID            string    `json:"id"`
	TenantUUID    uuid.UUID `json:"tenant_uuid"`
	Name          string    `json:"name"`
	AsgardeoGroup string    `json:"asgardeo_group,omitempty"`
	Description   string    `json:"description,omitempty"`
	CPUCoresCap   int       `json:"cpu_cores_cap"`
	MemoryGBCap   int       `json:"memory_gb_cap"`
	StorageGBCap  int       `json:"storage_gb_cap"`
	CreatedAt     time.Time `json:"created_at"`
	CreatedBy     string    `json:"created_by"`
}

// TenantCapUsage is the per-tenant capacity allocation summary used by the
// admin tenants page + the cap-shrink-guard check. Returned by
// db.GetTenantCapAndAllocation as a single round-trip.
type TenantCapUsage struct {
	Cap       TenantCap `json:"cap"`
	Allocated TenantCap `json:"allocated"`
	Available TenantCap `json:"available"`
}

// TenantCap is the (cpu, memory, storage) triplet used for caps,
// allocations, and the "remaining" view. Same shape, three roles.
type TenantCap struct {
	CPUCores  int `json:"cpu_cores"`
	MemoryGB  int `json:"memory_gb"`
	StorageGB int `json:"storage_gb"`
}
