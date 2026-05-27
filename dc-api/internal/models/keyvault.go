// Package models — keyvault.go
//
// M3 chunk 1: Key Vault domain types. v1 captures only the logical record
// (tenant ownership + name + soft-delete window). Subsequent chunks add:
//   - Private Endpoints (per-VPC NIC + Vip + CoreDNS hostname)
//   - Access policies (RBAC scoped to vault)
//   - The OpenBao KV-v2 mount that actually stores secrets
//   - Secret metadata and audit
package models

import (
	"time"

	"github.com/google/uuid"
)

// KeyVaultSpec is the caller-supplied intent for creating a Key Vault.
// The handler validates these before passing to the Repository.
type KeyVaultSpec struct {
	Name           string // Unique per tenant; [a-z0-9-], starts with a letter, 3..63 chars
	SoftDeleteDays int    // Recovery window for soft-deleted secrets; 0 means use default (30)
}

// KeyVault is the persisted representation of a Key Vault resource.
// Mirrors the key_vaults table.
type KeyVault struct {
	ID          uuid.UUID `json:"id"`
	TenantID    string    `json:"tenant_id"`
	TenantUUID  uuid.UUID `json:"tenant_uuid"`
	ProjectID   string    `json:"project_id"`
	ProjectUUID uuid.UUID `json:"project_uuid"`
	Name           string         `json:"name"`
	SoftDeleteDays int            `json:"soft_delete_days"`
	Status         ResourceStatus `json:"status"`
	Message        string         `json:"message,omitempty"`
	// CredentialsConsumedAt is nil until the GET .../credentials endpoint
	// has been called once; subsequent calls then return 410 Gone. This is
	// the shown-once flag for AppRole creds, mirroring service-account
	// token semantics (see docs/managed-services-integration.md §8).
	CredentialsConsumedAt *time.Time `json:"credentials_consumed_at,omitempty"`
	CreatedAt             time.Time  `json:"created_at"`
	UpdatedAt             time.Time  `json:"updated_at"`
}
