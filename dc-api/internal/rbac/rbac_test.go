package rbac_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/wso2/dc-api/internal/models"
	"github.com/wso2/dc-api/internal/rbac"
)

// ─────────────────────────── Mock repo ──────────────────────────────────────

// mockRepo is a trivial in-memory implementation of rbac.Repo used in tests.
// It holds a fixed slice of role assignments and an optional error to return.
type mockRepo struct {
	assignments []models.RoleAssignment
	err         error
}

func (m *mockRepo) ListRoleAssignmentsForPrincipal(
	_ context.Context,
	_ models.PrincipalType,
	_ string,
) ([]models.RoleAssignment, error) {
	if m.err != nil {
		return nil, m.err
	}
	return m.assignments, nil
}

// makeRA constructs a RoleAssignment for test use. IDs and timestamps are
// generated to be non-zero but their exact values are irrelevant to these tests.
func makeRA(scopeType models.ScopeType, scopeID string, role models.Role) models.RoleAssignment {
	return models.RoleAssignment{
		ID:            uuid.New(),
		PrincipalType: models.PrincipalTypeUser,
		PrincipalID:   "user-alice",
		ScopeType:     scopeType,
		ScopeID:       scopeID,
		Role:          role,
		GrantedAt:     time.Now(),
		GrantedBy:     "user-admin",
	}
}

// tenantChain is the M1.5 scope chain for tenant "acme".
func tenantChain(tenantID string) []models.Scope {
	return []models.Scope{{Type: models.ScopeTypeTenant, ID: tenantID}}
}

// ─────────────────────────── RolePower tests ────────────────────────────────

func TestRolePower_Ordering(t *testing.T) {
	if rbac.RolePower(models.RoleViewer) >= rbac.RolePower(models.RoleMember) {
		t.Error("viewer should have lower power than member")
	}
	if rbac.RolePower(models.RoleMember) >= rbac.RolePower(models.RoleOwner) {
		t.Error("member should have lower power than owner")
	}
	if rbac.RolePower(models.RoleViewer) >= rbac.RolePower(models.RoleOwner) {
		t.Error("viewer should have lower power than owner")
	}
}

func TestRolePower_UnknownRoleIsZero(t *testing.T) {
	if rbac.RolePower(models.Role("nonexistent")) != 0 {
		t.Error("unknown role should have power 0")
	}
}

func TestRolePower_AllRolesPositive(t *testing.T) {
	for _, r := range []models.Role{models.RoleViewer, models.RoleMember, models.RoleOwner} {
		if rbac.RolePower(r) <= 0 {
			t.Errorf("role %q should have positive power, got %d", r, rbac.RolePower(r))
		}
	}
}

// ─────────────────────────── EffectiveRole tests ────────────────────────────

func TestEffectiveRole_NoAssignments_ReturnsFalse(t *testing.T) {
	repo := &mockRepo{}
	role, found, err := rbac.EffectiveRole(context.Background(), repo,
		models.PrincipalTypeUser, "user-alice", tenantChain("acme"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if found {
		t.Error("expected found=false when there are no assignments")
	}
	if role != "" {
		t.Errorf("expected empty role, got %q", role)
	}
}

func TestEffectiveRole_MatchingScope_ReturnsRole(t *testing.T) {
	repo := &mockRepo{
		assignments: []models.RoleAssignment{
			makeRA(models.ScopeTypeTenant, "acme", models.RoleMember),
		},
	}
	role, found, err := rbac.EffectiveRole(context.Background(), repo,
		models.PrincipalTypeUser, "user-alice", tenantChain("acme"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !found {
		t.Fatal("expected found=true")
	}
	if role != models.RoleMember {
		t.Errorf("expected member, got %q", role)
	}
}

func TestEffectiveRole_MostPermissiveAcrossChain(t *testing.T) {
	// Simulate a future M5 chain: resource + tenant scopes both have assignments;
	// owner at tenant should win over viewer at resource.
	chain := []models.Scope{
		{Type: models.ScopeTypeTenant, ID: "acme"},
		// We reuse ScopeTypeTenant with a different ID to simulate a second
		// scope level without needing M5 constants to be defined yet.
		{Type: models.ScopeTypeTenant, ID: "acme-rg-001"},
	}
	repo := &mockRepo{
		assignments: []models.RoleAssignment{
			makeRA(models.ScopeTypeTenant, "acme", models.RoleOwner),
			makeRA(models.ScopeTypeTenant, "acme-rg-001", models.RoleViewer),
		},
	}
	role, found, err := rbac.EffectiveRole(context.Background(), repo,
		models.PrincipalTypeUser, "user-alice", chain)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !found {
		t.Fatal("expected found=true")
	}
	if role != models.RoleOwner {
		t.Errorf("expected owner (most permissive), got %q", role)
	}
}

func TestEffectiveRole_FiltersOutOfChainScopes(t *testing.T) {
	// The principal has an assignment on tenant "other-tenant" which is NOT in
	// the chain for "acme". It must be ignored, so found should be false.
	repo := &mockRepo{
		assignments: []models.RoleAssignment{
			makeRA(models.ScopeTypeTenant, "other-tenant", models.RoleOwner),
		},
	}
	role, found, err := rbac.EffectiveRole(context.Background(), repo,
		models.PrincipalTypeUser, "user-alice", tenantChain("acme"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if found {
		t.Errorf("expected found=false for out-of-chain scope, got role=%q", role)
	}
}

func TestEffectiveRole_MixedInAndOutOfChainScopes_ReturnsInChainRole(t *testing.T) {
	// Assignments on two tenants; only "acme" is in the chain.
	// The result should be the acme assignment's role (viewer), not the
	// out-of-chain owner assignment.
	repo := &mockRepo{
		assignments: []models.RoleAssignment{
			makeRA(models.ScopeTypeTenant, "other-tenant", models.RoleOwner),
			makeRA(models.ScopeTypeTenant, "acme", models.RoleViewer),
		},
	}
	role, found, err := rbac.EffectiveRole(context.Background(), repo,
		models.PrincipalTypeUser, "user-alice", tenantChain("acme"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !found {
		t.Fatal("expected found=true")
	}
	if role != models.RoleViewer {
		t.Errorf("expected viewer (only in-chain assignment), got %q", role)
	}
}

func TestEffectiveRole_DBErrorPropagates(t *testing.T) {
	sentinel := errors.New("connection refused")
	repo := &mockRepo{err: sentinel}

	_, _, err := rbac.EffectiveRole(context.Background(), repo,
		models.PrincipalTypeUser, "user-alice", tenantChain("acme"))
	if err == nil {
		t.Fatal("expected error from DB, got nil")
	}
	if !errors.Is(err, sentinel) {
		t.Errorf("expected sentinel wrapped in error, got: %v", err)
	}
}

// ─────────────────────────── RequireRole tests ──────────────────────────────

func TestRequireRole_SufficientRole_ReturnsNil(t *testing.T) {
	// member trying to perform a member-level action
	repo := &mockRepo{
		assignments: []models.RoleAssignment{
			makeRA(models.ScopeTypeTenant, "acme", models.RoleMember),
		},
	}
	err := rbac.RequireRole(context.Background(), repo,
		models.PrincipalTypeUser, "user-alice",
		false,
		tenantChain("acme"),
		models.RoleMember,
	)
	if err != nil {
		t.Errorf("expected nil, got %v", err)
	}
}

func TestRequireRole_HigherRoleThanMinimum_ReturnsNil(t *testing.T) {
	// owner satisfies a viewer minimum
	repo := &mockRepo{
		assignments: []models.RoleAssignment{
			makeRA(models.ScopeTypeTenant, "acme", models.RoleOwner),
		},
	}
	err := rbac.RequireRole(context.Background(), repo,
		models.PrincipalTypeUser, "user-alice",
		false,
		tenantChain("acme"),
		models.RoleViewer,
	)
	if err != nil {
		t.Errorf("expected nil, got %v", err)
	}
}

func TestRequireRole_InsufficientRole_ReturnsErrInsufficientRole(t *testing.T) {
	// member trying to perform an owner-level action (e.g., DELETE)
	repo := &mockRepo{
		assignments: []models.RoleAssignment{
			makeRA(models.ScopeTypeTenant, "acme", models.RoleMember),
		},
	}
	err := rbac.RequireRole(context.Background(), repo,
		models.PrincipalTypeUser, "user-alice",
		false,
		tenantChain("acme"),
		models.RoleOwner,
	)
	if !errors.Is(err, rbac.ErrInsufficientRole) {
		t.Errorf("expected ErrInsufficientRole, got %v", err)
	}
}

func TestRequireRole_NoAssignment_ReturnsErrInsufficientRole(t *testing.T) {
	repo := &mockRepo{}
	err := rbac.RequireRole(context.Background(), repo,
		models.PrincipalTypeUser, "user-alice",
		false,
		tenantChain("acme"),
		models.RoleViewer,
	)
	if !errors.Is(err, rbac.ErrInsufficientRole) {
		t.Errorf("expected ErrInsufficientRole, got %v", err)
	}
}

func TestRequireRole_IsAdmin_BypassesCheck(t *testing.T) {
	// isAdmin=true with an empty repo — must return nil regardless
	repo := &mockRepo{}
	err := rbac.RequireRole(context.Background(), repo,
		models.PrincipalTypeUser, "user-admin",
		true, // platform admin
		tenantChain("acme"),
		models.RoleOwner, // strictest possible minimum
	)
	if err != nil {
		t.Errorf("expected nil for platform admin, got %v", err)
	}
}

func TestRequireRole_IsAdmin_DoesNotHitDB(t *testing.T) {
	// Even if the DB would error, isAdmin short-circuits before the call.
	sentinel := errors.New("db exploded")
	repo := &mockRepo{err: sentinel}
	err := rbac.RequireRole(context.Background(), repo,
		models.PrincipalTypeUser, "user-admin",
		true,
		tenantChain("acme"),
		models.RoleOwner,
	)
	if err != nil {
		t.Errorf("expected nil (admin short-circuit before DB), got %v", err)
	}
}

func TestRequireRole_DBErrorPropagates(t *testing.T) {
	sentinel := errors.New("db timeout")
	repo := &mockRepo{err: sentinel}
	err := rbac.RequireRole(context.Background(), repo,
		models.PrincipalTypeUser, "user-alice",
		false,
		tenantChain("acme"),
		models.RoleViewer,
	)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if errors.Is(err, rbac.ErrInsufficientRole) {
		t.Error("DB error should NOT be surfaced as ErrInsufficientRole")
	}
	if !errors.Is(err, sentinel) {
		t.Errorf("expected sentinel wrapped in error, got: %v", err)
	}
}
