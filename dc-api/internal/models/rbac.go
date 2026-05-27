// Package models — rbac.go
//
// Domain types for M1.5 RBAC: role assignments, service accounts, and the
// scope/principal enumerations used throughout the authorisation layer.
//
// Design notes:
//   - PrincipalType and ScopeType are string-typed enums so new values can be
//     added (in M5) without a schema migration — they are stored as TEXT in
//     PostgreSQL rather than as a ENUM type.
//   - ScopeType today only takes the value "tenant". When M5 lands
//     ("subscription", "resource_group", "resource") the constant list grows
//     but existing DB rows are unaffected.
//   - Scope is a small value object pairing a ScopeType with its ID string.
//     The RBAC helper in Chunk 2 will walk a []Scope chain (narrow → broad) to
//     compute an effective role.
package models

import (
	"time"

	"github.com/google/uuid"
)

// ─────────────────────────── Enumerations ───────────────────────────────────

// PrincipalType distinguishes human users (identified by JWT sub) from
// DC-API-issued service accounts (identified by service_account.id).
type PrincipalType string

const (
	PrincipalTypeUser           PrincipalType = "user"
	PrincipalTypeServiceAccount PrincipalType = "service_account"
)

// ScopeType identifies the hierarchical level at which a role is granted.
// M1.5 only uses ScopeTypeTenant. The remaining values are placeholders that
// will become valid in M5 when the Organisation → Subscription → Resource Group
// → Resource hierarchy is implemented. No schema migration will be required
// when they land — they are stored as plain TEXT.
type ScopeType string

const (
	ScopeTypeTenant  ScopeType = "tenant"
	ScopeTypeProject ScopeType = "project"
	// Future M5 values (not yet used — listed here for discoverability):
	//   ScopeTypeSubscription  ScopeType = "subscription"
	//   ScopeTypeResourceGroup ScopeType = "resource_group"
	//   ScopeTypeResource      ScopeType = "resource"
)

// Role is the permission level granted within a scope.
// platform-admin is handled by the Asgardeo group short-circuit in the
// middleware (dc-admin group) — it does not appear as a role_assignments row.
type Role string

const (
	RoleOwner  Role = "owner"
	RoleMember Role = "member"
	RoleViewer Role = "viewer"
)

// ─────────────────────────── Value objects ──────────────────────────────────

// Scope pairs a scope type with its identifier string. Used by the RBAC
// helper (Chunk 2) when walking the scope chain for a request:
//
//	chain := []Scope{{ScopeTypeTenant, tenantID}}
//	effective, err := rbac.EffectiveRole(ctx, repo, principal, chain)
//
// In M5 the chain will have up to four entries (resource → RG → sub → tenant).
type Scope struct {
	Type ScopeType
	ID   string
}

// ─────────────────────────── Role Assignment ────────────────────────────────

// RoleAssignment is the canonical DC-API representation of a single role
// binding. One row in the role_assignments table.
//
// The UNIQUE constraint (principal_type, principal_id, scope_type, scope_id,
// role) means a principal can hold multiple roles at different scopes, or even
// multiple roles at the same scope (e.g., both owner and viewer), though the
// authorisation helpers collapse them to the most permissive.
type RoleAssignment struct {
	ID            uuid.UUID     `json:"id"`
	PrincipalType PrincipalType `json:"principal_type"`
	PrincipalID   string        `json:"principal_id"`
	ScopeType     ScopeType     `json:"scope_type"`
	ScopeID       string        `json:"scope_id"`
	// ScopeUUID is the immutable identity for the scope (Phase 6a).
	// For scope_type='tenant' this is tenants.tenant_uuid. Populated on write;
	// read back on every SELECT. M5 will use it for other scope_types too.
	ScopeUUID     uuid.UUID     `json:"scope_uuid"`
	Role          Role          `json:"role"`
	GrantedAt     time.Time     `json:"granted_at"`
	GrantedBy     string        `json:"granted_by"` // principal_id of the granter
	// DisplayAlias is an admin-set mnemonic shown by cloud-ui instead of
	// the opaque sub. Optional. No PII is sourced from the IdP — purely
	// what the inviter typed when they added the principal.
	DisplayAlias string `json:"display_alias,omitempty"`
}

// ─────────────────────────── Service Account ────────────────────────────────

// ServiceAccount is a DC-API-managed principal for non-human callers (CI/CD
// pipelines, automation scripts). It authenticates via a long-lived token
// rather than an OIDC JWT. The raw token is returned exactly once on creation
// and is never stored; only its bcrypt hash (token_hash) lives in the DB.
//
// Token format: dcapi_sa_<lookup_id>_<secret>
//   - lookup_id (12 chars): stored in plaintext as token_lookup_id; used to find
//     the candidate row in O(1) via an indexed column lookup.
//   - secret (32 chars): the part that is bcrypt-hashed and stored in token_hash.
//
// This two-part design avoids an O(N) bcrypt scan across all service accounts on
// every authenticated request, while keeping the security properties of bcrypt.
//
// ServiceAccount rows appear in role_assignments with
// principal_type = PrincipalTypeServiceAccount and
// principal_id   = ServiceAccount.ID.String().
type ServiceAccount struct {
	ID          uuid.UUID `json:"id"`
	TenantID    string    `json:"tenant_id"`
	TenantUUID  uuid.UUID `json:"tenant_uuid"`
	ProjectID   string    `json:"project_id"`
	ProjectUUID uuid.UUID `json:"project_uuid"`
	Name          string     `json:"name"`
	TokenLookupID string     `json:"-"`                    // first 12 chars of raw token; stored plain for indexed lookup
	TokenHash     string     `json:"-"`                    // bcrypt hash of the secret portion; never serialised to callers
	Description   string     `json:"description,omitempty"`
	CreatedAt     time.Time  `json:"created_at"`
	LastUsed      *time.Time `json:"last_used,omitempty"` // nil until the token is used for the first time
}
