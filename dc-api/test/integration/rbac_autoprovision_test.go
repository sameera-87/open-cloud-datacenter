//go:build integration

package integration

// rbac_autoprovision_test.go — M1.5 Chunk 3 integration tests.
//
// These tests exercise the autoprovision membership policy in the auth middleware:
//
//   TestRBAC_AutoProvisionTrue_FirstLoginCreatesMemberRow
//     Verifies that the first authenticated request for a brand-new user auto-inserts
//     a 'member' role_assignment row when DCAPI_RBAC_AUTOPROVISION=true (the default).
//     A second request must succeed without creating a duplicate row.
//
//   TestRBAC_AutoProvisionTrue_AllTenantGroupsProvisioned
//     Verifies that when a JWT carries MULTIPLE dc-tenant-* groups, the middleware
//     autoprovisions a 'member' row for EVERY group in a single request — not just
//     the first one. This is the post-migration behavior: Auth middleware no longer
//     picks one tenant; TenantContext middleware resolves the active tenant from the
//     URL, and every group in the JWT gets a row.
//
//   TestRBAC_AutoProvisionFalse_FirstLoginReturns403
//     Verifies that when DCAPI_RBAC_AUTOPROVISION=false, a user with no existing
//     role_assignment row receives 403. Once a row is inserted manually (simulating
//     owner-invitation), the same request succeeds.
//
// Both tests create their own httptest.Server via newSubEnv so they can control
// the AuthConfig independently. They share the same PostgreSQL container (and thus
// the same db.Repository) as the rest of the suite — the DB is not contaminated
// because each test uses a unique user sub + tenantID.
//
// These tests do NOT create any VMs or clusters; they make the cheapest possible
// authenticated request (GET /v1/tenants/{tenant_id}/vnets) just to exercise
// the middleware path.

import (
	"context"
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/wso2/dc-api/internal/api/middleware"
	"github.com/wso2/dc-api/internal/models"
)

// TestRBAC_AutoProvisionTrue_FirstLoginCreatesMemberRow verifies:
//  1. First request from a brand-new user_sub (no role_assignments row) succeeds
//     when AutoProvisionMembers=true.
//  2. Exactly one role_assignments row is created with the expected fields.
//  3. A second request still succeeds and the row count stays at 1 (UNIQUE
//     constraint prevents duplicates; the middleware handles the conflict
//     gracefully with a log warning).
func TestRBAC_AutoProvisionTrue_FirstLoginCreatesMemberRow(t *testing.T) {
	t.Parallel()

	// Unique tenant and user so this test doesn't share state with other tests.
	tenantID := "test-tenant-rbac-auto"
	userSub := "sub-autoprovision-true-" + randomName("u")

	// Spin up a server with autoprovision=true.
	subEnv := newSubEnv(t, middleware.AuthConfig{
		TenantGroupPrefix:    "dc-tenant-",
		AdminGroup:           "dc-admin",
		AutoProvisionMembers: true,
	})

	token, err := subEnv.JWT.MintTokenWithGroups(userSub, userSub+"@test.dc", []string{"dc-tenant-" + tenantID})
	require.NoError(t, err, "mint JWT")
	ctx := context.Background()

	// Ensure the tenant + default project exist in the DB so the auth and
	// TenantContext middleware can resolve the slug. Autoprovision creates the
	// role_assignment row; the tenant row must exist beforehand (Phase 6a).
	if _, err := subEnv.DB.UpsertTenant(ctx, tenantID, tenantID, "dc-tenant-"+tenantID, "test-auto"); err != nil {
		require.NoError(t, err, "UpsertTenant")
	}
	setupTenantProject(t, subEnv, tenantID)

	// Bind client to the tenant + project. The probe endpoint is cap-usage,
	// which is tenant-scoped (no project context required) but still exercises
	// the full auth + TenantContext middleware chain that autoprovision sits in.
	client := NewAPIClientForTenant(subEnv.BaseURL, token, tenantID)

	// ── First request: should succeed (autoprovision inserts member row) ─────

	_, _, firstStatus, firstErr := client.GetTenantCapUsage(ctx)
	require.NoError(t, firstErr)
	require.Equal(t, http.StatusOK, firstStatus,
		"first request must succeed with autoprovision=true")

	// ── Assert the DB row ─────────────────────────────────────────────────────

	assignments, err := subEnv.DB.ListRoleAssignmentsForPrincipal(ctx, models.PrincipalTypeUser, userSub)
	require.NoError(t, err)

	// Filter to the tenant scope we care about (the user might have rows from
	// other tests if the sub were shared, but since userSub is unique, this is
	// purely defensive).
	var tenantRows []models.RoleAssignment
	for _, ra := range assignments {
		if ra.ScopeType == models.ScopeTypeTenant && ra.ScopeID == tenantID {
			tenantRows = append(tenantRows, ra)
		}
	}

	require.Len(t, tenantRows, 1, "exactly one role_assignment row must be created on first login")
	ra := tenantRows[0]
	require.Equal(t, models.PrincipalTypeUser, ra.PrincipalType)
	require.Equal(t, userSub, ra.PrincipalID)
	require.Equal(t, models.ScopeTypeTenant, ra.ScopeType)
	require.Equal(t, tenantID, ra.ScopeID)
	require.Equal(t, models.RoleMember, ra.Role)
	require.Equal(t, "autoprovision-from-asgardeo-group", ra.GrantedBy)

	// ── Second request: must succeed without duplicating the row ─────────────

	_, _, secondStatus, secondErr := client.GetTenantCapUsage(ctx)
	require.NoError(t, secondErr)
	require.Equal(t, http.StatusOK, secondStatus,
		"second request must still succeed")

	// Row count must remain 1 (unique constraint + middleware conflict-handling).
	assignments2, err2 := subEnv.DB.ListRoleAssignmentsForPrincipal(ctx, models.PrincipalTypeUser, userSub)
	require.NoError(t, err2)
	var tenantRows2 []models.RoleAssignment
	for _, ra := range assignments2 {
		if ra.ScopeType == models.ScopeTypeTenant && ra.ScopeID == tenantID {
			tenantRows2 = append(tenantRows2, ra)
		}
	}
	require.Len(t, tenantRows2, 1, "second request must NOT create a duplicate role_assignment row")
}

// TestRBAC_AutoProvisionTrue_AllTenantGroupsProvisioned verifies the new
// post-migration autoprovision behavior: when a JWT carries MULTIPLE
// dc-tenant-* groups, the auth middleware must insert a 'member'
// role_assignment row for EVERY group on the first request — not just the
// first group encountered.
//
// Before the /v1/tenants/{tenant_id}/... migration, Auth middleware resolved
// a single active tenant from the JWT on every request. Now it collects all
// groups, autoprovisions all of them, and leaves active-tenant selection to
// the TenantContext middleware (URL-driven). This test exercises that the
// multi-group provisioning path actually fires for all groups.
func TestRBAC_AutoProvisionTrue_AllTenantGroupsProvisioned(t *testing.T) {
	t.Parallel()

	tenantA := "test-tenant-rbac-multi-a"
	tenantB := "test-tenant-rbac-multi-b"
	userSub := "sub-autoprovision-multi-" + randomName("u")

	subEnv := newSubEnv(t, middleware.AuthConfig{
		TenantGroupPrefix:    "dc-tenant-",
		AdminGroup:           "dc-admin",
		AutoProvisionMembers: true,
	})

	// Mint a token with BOTH tenant groups in the claims.
	token, err := subEnv.JWT.MintTokenWithGroups(userSub, userSub+"@test.dc", []string{
		"dc-tenant-" + tenantA,
		"dc-tenant-" + tenantB,
	})
	require.NoError(t, err, "mint multi-tenant JWT")

	ctx := context.Background()

	// Pre-register both tenants in the registry (Phase 6a requirement).
	// Autoprovision creates the role_assignment rows; the tenants rows must
	// exist beforehand for TenantContext to resolve the slugs.
	if _, err := subEnv.DB.UpsertTenant(ctx, tenantA, tenantA, "dc-tenant-"+tenantA, "test-auto-multi"); err != nil {
		require.NoError(t, err, "UpsertTenant A")
	}
	if _, err := subEnv.DB.UpsertTenant(ctx, tenantB, tenantB, "dc-tenant-"+tenantB, "test-auto-multi"); err != nil {
		require.NoError(t, err, "UpsertTenant B")
	}
	setupTenantProject(t, subEnv, tenantA)
	setupTenantProject(t, subEnv, tenantB)

	// Make a single request scoped to tenant A. The auth middleware must
	// autoprovision rows for BOTH tenantA and tenantB during this single call.
	// Use cap-usage (tenant-scoped) as the probe so we don't need a project bound.
	clientA := NewAPIClientForTenant(subEnv.BaseURL, token, tenantA)
	_, _, firstStatus, firstErr := clientA.GetTenantCapUsage(ctx)
	require.NoError(t, firstErr)
	require.Equal(t, http.StatusOK, firstStatus,
		"first request (tenant A) must succeed with autoprovision=true")

	// Assert autoprovision created rows for BOTH tenants.
	assignments, err := subEnv.DB.ListRoleAssignmentsForPrincipal(ctx, models.PrincipalTypeUser, userSub)
	require.NoError(t, err)

	byTenant := make(map[string]models.RoleAssignment)
	for _, ra := range assignments {
		if ra.ScopeType == models.ScopeTypeTenant {
			byTenant[ra.ScopeID] = ra
		}
	}

	// OLD assertion (pre-migration): only one row for the "active" tenant.
	// NEW assertion (post-migration): one row per group in the JWT.
	require.Contains(t, byTenant, tenantA,
		"autoprovision must create a member row for tenant A")
	require.Contains(t, byTenant, tenantB,
		"autoprovision must create a member row for tenant B (all JWT groups, not just active tenant)")
	require.Equal(t, models.RoleMember, byTenant[tenantA].Role)
	require.Equal(t, models.RoleMember, byTenant[tenantB].Role)
	require.Equal(t, "autoprovision-from-asgardeo-group", byTenant[tenantA].GrantedBy)
	require.Equal(t, "autoprovision-from-asgardeo-group", byTenant[tenantB].GrantedBy)

	// Also verify the same token can be used to access tenant B directly
	// without a new login — the row exists so TenantContext will allow it.
	clientB := NewAPIClientForTenant(subEnv.BaseURL, token, tenantB)
	_, _, secondStatus, secondErr := clientB.GetTenantCapUsage(ctx)
	require.NoError(t, secondErr)
	require.Equal(t, http.StatusOK, secondStatus,
		"request to tenant B must succeed after multi-tenant autoprovision")
}

// TestRBAC_AutoProvisionFalse_FirstLoginReturns403 verifies:
//  1. First request from a brand-new user (no role_assignments row) returns 403
//     when AutoProvisionMembers=false.
//  2. "no membership in tenant" appears in the response body (TenantContext
//     middleware denies access when the JWT contains the dc-tenant-* group
//     for this tenant but no role_assignments row exists yet).
//  3. After directly inserting a role_assignment row (simulating owner invite),
//     the same request returns 200.
func TestRBAC_AutoProvisionFalse_FirstLoginReturns403(t *testing.T) {
	t.Parallel()

	tenantID := "test-tenant-rbac-strict"
	userSub := "sub-autoprovision-false-" + randomName("u")

	// Spin up a server with autoprovision=false (strict mode).
	subEnv := newSubEnv(t, middleware.AuthConfig{
		TenantGroupPrefix:    "dc-tenant-",
		AdminGroup:           "dc-admin",
		AutoProvisionMembers: false,
	})

	token, err := subEnv.JWT.MintTokenWithGroups(userSub, userSub+"@test.dc", []string{"dc-tenant-" + tenantID})
	require.NoError(t, err, "mint JWT")
	ctx := context.Background()

	// Phase 6a: TenantContext returns 404 when the slug isn't in `tenants`.
	// The intent here is to exercise the "in IdP group, no role row" 403 path —
	// pre-register the tenant so the lookup succeeds and the 403 actually fires.
	if _, err := subEnv.DB.UpsertTenant(ctx, tenantID, tenantID, "dc-tenant-"+tenantID, "test-autoprov-false"); err != nil {
		require.NoError(t, err, "UpsertTenant")
	}
	// Bind client to the tenant. We use cap-usage (tenant-scoped) as the probe;
	// no project context is required, so tenantID-only client is fine.
	client := NewAPIClientForTenant(subEnv.BaseURL, token, tenantID)

	// ── First request: must return 403 (no membership row, autoprovision=false) ─
	// With the new URL structure the auth middleware still lets the JWT through
	// (user has the correct dc-tenant-* group); the 403 now comes from
	// TenantContext middleware which finds no role_assignments row for this tenant.

	rawBody, firstStatus, firstErr := client.do(ctx, "GET", "/v1/tenants/"+tenantID+"/cap-usage", nil)
	require.NoError(t, firstErr)
	require.Equal(t, http.StatusForbidden, firstStatus,
		"first request must return 403 with autoprovision=false")
	require.Contains(t, string(rawBody), "no membership in tenant",
		"403 body must explain that the user has no membership row for this tenant")

	// Confirm no row was inserted.
	assignments, err := subEnv.DB.ListRoleAssignmentsForPrincipal(ctx, models.PrincipalTypeUser, userSub)
	require.NoError(t, err)
	require.Empty(t, assignments, "no role_assignment row must be created in strict mode")

	// ── Simulate owner-invitation: insert a role_assignment row directly ──────

	_, err = subEnv.DB.CreateRoleAssignment(ctx, models.RoleAssignment{
		PrincipalType: models.PrincipalTypeUser,
		PrincipalID:   userSub,
		ScopeType:     models.ScopeTypeTenant,
		ScopeID:       tenantID,
		Role:          models.RoleMember,
		GrantedBy:     "test-owner",
	})
	require.NoError(t, err, "direct role_assignment insert must succeed")

	// ── Second request: must now succeed ─────────────────────────────────────

	_, _, secondStatus, secondErr := client.GetTenantCapUsage(ctx)
	require.NoError(t, secondErr)
	require.Equal(t, http.StatusOK, secondStatus,
		"request must succeed after role_assignment row is inserted")
}
