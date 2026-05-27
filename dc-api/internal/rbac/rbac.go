// Package rbac provides pure authorisation helpers for DC-API.
//
// This package has no knowledge of HTTP or PostgreSQL. It contains only the
// business logic for computing an effective role from a set of role assignments
// and deciding whether a principal is permitted to perform an action.
//
// ── Design: scope-polymorphic role walk ───────────────────────────────────────
//
// Role assignments are stored against a (scope_type, scope_id) pair. In M1.5
// there is only one scope type ("tenant"), so the chain always has length 1.
// From M5 onward the chain grows to (resource → resource_group → subscription →
// tenant). The helpers here handle any chain length — no code changes are
// needed when M5 lands.
//
// The walk fetches all assignments for the principal in a single DB round-trip,
// then filters in memory. At M1.5 scale a principal will have O(1) assignments,
// so this is fine. If a future milestone shows millions of assignments per
// principal we can push the scope filter down into SQL.
//
// ── Circular-import safety ────────────────────────────────────────────────────
//
// This package imports only "context", "errors", "fmt", and the project's own
// models package. It does NOT import internal/db. The Repo interface defined
// here is satisfied implicitly by *db.Repository, avoiding a circular import
// when middleware (which imports this package) also imports db.
package rbac

import (
	"context"
	"errors"
	"fmt"

	"github.com/wso2/dc-api/internal/models"
)

// ─────────────────────────── Sentinel errors ────────────────────────────────

// ErrInsufficientRole is returned by RequireRole when the principal's effective
// role falls below the required minimum. Handlers translate this to HTTP 403.
// Using a typed sentinel (rather than a generic "forbidden" string) lets callers
// use errors.Is for precise branching.
var ErrInsufficientRole = errors.New("rbac: insufficient role for action")

// ─────────────────────────── Repo interface ─────────────────────────────────

// Repo is the narrow data-access interface required by this package.
// *db.Repository satisfies it implicitly — no explicit declaration needed there.
// Tests satisfy it with a tiny in-memory mock (see rbac_test.go).
type Repo interface {
	// ListRoleAssignmentsForPrincipal returns all role assignments for the
	// given principal across all scopes. The scope filtering happens in memory
	// inside EffectiveRole — a single DB call per request.
	ListRoleAssignmentsForPrincipal(
		ctx context.Context,
		principalType models.PrincipalType,
		principalID string,
	) ([]models.RoleAssignment, error)
}

// ─────────────────────────── Role ordering ──────────────────────────────────

// RolePower returns a numeric rank for a role — higher means more permissive.
// This allows comparing two roles without a switch in every call site.
//
//	viewer  → 1
//	member  → 2
//	owner   → 3
//	unknown → 0   (treated as no permission)
func RolePower(r models.Role) int {
	switch r {
	case models.RoleViewer:
		return 1
	case models.RoleMember:
		return 2
	case models.RoleOwner:
		return 3
	default:
		return 0
	}
}

// ─────────────────────────── Core helpers ───────────────────────────────────

// EffectiveRole computes the most permissive role a principal holds across the
// given scope chain.
//
// The scopeChain is ordered narrowest → broadest (e.g., resource first, then
// resource_group, then subscription, then tenant). In M1.5 it always has one
// element: []models.Scope{{Type: models.ScopeTypeTenant, ID: tenantID}}.
//
// The function fetches all of the principal's role assignments in a single DB
// call, then filters the in-memory slice against the chain. Only assignments
// whose (scope_type, scope_id) pair appears somewhere in the chain are
// considered. The highest-power role among the matching assignments is returned.
//
// Return values:
//   - (role, true, nil)   — a matching role was found
//   - ("",  false, nil)   — no assignment matches any scope in the chain
//   - ("",  false, err)   — DB error; callers should return HTTP 500
func EffectiveRole(
	ctx context.Context,
	repo Repo,
	principalType models.PrincipalType,
	principalID string,
	scopeChain []models.Scope,
) (models.Role, bool, error) {
	assignments, err := repo.ListRoleAssignmentsForPrincipal(ctx, principalType, principalID)
	if err != nil {
		return "", false, fmt.Errorf("rbac effective role: %w", err)
	}

	// Build a set of scopes in the chain for O(1) membership checks.
	// The chain is short (≤4 elements in M5), so a map is more than adequate.
	type scopeKey struct {
		t models.ScopeType
		id string
	}
	inChain := make(map[scopeKey]struct{}, len(scopeChain))
	for _, s := range scopeChain {
		inChain[scopeKey{s.Type, s.ID}] = struct{}{}
	}

	best := models.Role("")
	bestPower := 0

	for _, ra := range assignments {
		key := scopeKey{ra.ScopeType, ra.ScopeID}
		if _, ok := inChain[key]; !ok {
			// This assignment is on a scope not in the requested chain — skip.
			continue
		}
		if p := RolePower(ra.Role); p > bestPower {
			bestPower = p
			best = ra.Role
		}
	}

	if bestPower == 0 {
		return "", false, nil
	}
	return best, true, nil
}

// RequireRole verifies that the principal holds at least the minimum role
// somewhere in the scope chain.
//
// Parameters:
//   - isAdmin: when true the check is bypassed entirely (platform admin
//     short-circuit). The middleware in Chunk 3 sets this based on whether the
//     JWT contained the configured admin group (e.g., "dc-admin").
//   - minimum: the least-permissive role that is acceptable for this action.
//     For example, DELETE handlers will pass models.RoleOwner; GET handlers
//     may pass models.RoleViewer.
//
// Return values:
//   - nil              — access granted
//   - ErrInsufficientRole — access denied; handler should respond with HTTP 403
//   - any other error  — DB failure; handler should respond with HTTP 500
func RequireRole(
	ctx context.Context,
	repo Repo,
	principalType models.PrincipalType,
	principalID string,
	isAdmin bool,
	scopeChain []models.Scope,
	minimum models.Role,
) error {
	// Platform admin short-circuit: bypass all role-assignment lookups.
	if isAdmin {
		return nil
	}

	effective, found, err := EffectiveRole(ctx, repo, principalType, principalID, scopeChain)
	if err != nil {
		return fmt.Errorf("rbac require role: %w", err)
	}
	if !found {
		return ErrInsufficientRole
	}
	if RolePower(effective) < RolePower(minimum) {
		return ErrInsufficientRole
	}
	return nil
}
