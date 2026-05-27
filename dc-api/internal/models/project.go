// Package models — project.go
//
// Project is the workspace boundary beneath a Tenant. Every resource belongs
// to exactly one Project. Projects carry capacity quotas (cpu_cores,
// memory_gb, storage_gb) and object-count guardrails (max_vnets, etc.).
//
// The design mirrors the Tenant type (tenant.go): a human-readable slug (ID)
// used in URLs and a stable UUID (ProjectUUID) used as the FK on every
// per-resource table. Re-registering a recycled slug produces a fresh UUID,
// so orphan rows from the deleted project become invisible.
package models

import (
	"time"

	"github.com/google/uuid"
)

// Project matches the `projects` table.
type Project struct {
	// ID is the human-readable slug, unique within the tenant. Used in URLs.
	ID          string    `json:"id"`
	TenantID    string    `json:"tenant_id"`
	TenantUUID  uuid.UUID `json:"tenant_uuid"`
	// ProjectUUID is the immutable canonical identity for this project.
	// Per-resource tables reference this UUID, not the slug.
	ProjectUUID uuid.UUID `json:"project_uuid"`
	Name        string    `json:"name"`
	Description string    `json:"description,omitempty"`
	// Capacity quotas — enforced at resource create time.
	CPUCores  int `json:"cpu_cores"`
	MemoryGB  int `json:"memory_gb"`
	StorageGB int `json:"storage_gb"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
	CreatedBy string    `json:"created_by"`
}

// ProjectQuota holds object-count guardrails for a project.
// Separate from the capacity quotas (which live on the Project row itself).
type ProjectQuota struct {
	ProjectUUID uuid.UUID `json:"project_uuid"`
	MaxVNets    int       `json:"max_vnets"`
	MaxClusters int       `json:"max_clusters"`
	MaxVolumes  int       `json:"max_volumes"`
	MaxPublicIPs int      `json:"max_public_ips"`
	UpdatedAt   time.Time `json:"updated_at"`
}
