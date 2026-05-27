//go:build integration

package integration

// projects_recycle_test.go — verifies that re-registering a project slug
// after deletion produces a fresh project_uuid, and that orphan rows left
// behind by the deleted project are invisible to the new project.
//
// This mirrors TestPhase6a_SlugRecycle (tenant-level slug isolation) but
// tests the project-level UUID guard that was introduced in M2.5.
//
// Scenario (8 steps):
//  1. Register tenant + project A (slug="proj-recycle"). Insert VNet row
//     directly into DB tagged with project A's project_uuid.
//  2. Verify project A owner can list and GET the VNet via the API.
//  3. Orphan the VNet: UPDATE vnets SET project_uuid = NULL so the project
//     row can be deleted without FK violation (simulating what would happen if
//     a hard platform-admin purge bypassed the FK). Then delete the project row.
//  4. Re-register the same slug → fresh project_uuid (project B).
//  5. List VNets under project B → empty (orphan invisible because
//     project_uuid filter = B's UUID, orphan has NULL).
//  6. GET the orphan VNet UUID directly → 404 (tenant_uuid + project_uuid
//     filter excludes it).
//  7. Confirm project B's project_uuid differs from project A's.
//  8. Cleanup.
//
// No live KubeOVN calls are made — VNet rows are seeded via the DB repo.

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"github.com/wso2/dc-api/internal/models"
)

func TestProjectSlugRecycle(t *testing.T) {
	// Sequential: manipulates project rows by slug within a tenant. Running
	// in parallel with tests that share the same tenant slug would race.
	ctx := context.Background()

	tenantSlug := "test-proj-recycle-tenant"
	projectSlug := "proj-recycle"

	// ── Step 1a: Register the tenant ─────────────────────────────────────────

	if _, err := env.DB.UpsertTenant(ctx, tenantSlug, tenantSlug, "dc-tenant-"+tenantSlug, "proj-recycle-test"); err != nil {
		require.NoError(t, err, "UpsertTenant")
	}
	tenantUUID, err := env.DB.GetTenantUUIDBySlug(ctx, tenantSlug)
	require.NoError(t, err)
	require.NotEqual(t, uuid.Nil, tenantUUID)

	userSub := "sub-proj-recycle-owner"
	mustGrantOwner(t, tenantSlug, userSub)

	// ── Step 1b: Create project A ─────────────────────────────────────────────

	projectA, _, _, err := env.DB.CreateProject(ctx,
		models.Project{
			ID:         projectSlug,
			TenantID:   tenantSlug,
			TenantUUID: tenantUUID,
			Name:       "Project A",
			CPUCores:   10,
			MemoryGB:   32,
			StorageGB:  200,
			CreatedBy:  "proj-recycle-test",
		},
		models.ProjectQuota{MaxVNets: 5, MaxClusters: 1, MaxVolumes: 10, MaxPublicIPs: 2},
	)
	// Idempotent: if it already exists from a prior (crashed) run, look it up.
	if err != nil && strings.Contains(err.Error(), "already exists") {
		projectA = nil
		// Fall through — we'll fetch the UUID below.
	} else {
		require.NoError(t, err, "create project A")
	}

	var projectAUUID uuid.UUID
	if projectA != nil {
		projectAUUID = projectA.ProjectUUID
	} else {
		projectAUUID, err = env.DB.GetProjectUUIDByTenantAndSlug(ctx, tenantSlug, projectSlug)
		require.NoError(t, err, "lookup existing project A UUID")
		require.NotEqual(t, uuid.Nil, projectAUUID)
	}

	// ── Step 1c: Seed a VNet row tagged to project A ──────────────────────────

	vnetID := uuid.New()
	_, err = env.DB.Pool().Exec(ctx,
		`INSERT INTO vnets (id, tenant_id, tenant_uuid, project_id, project_uuid, name, region, address_space, status, provider_type)
		 VALUES ($1, $2, $3, $4, $5, $6, 'lk', ARRAY['10.91.0.0/16'], 'ACTIVE', 'kubeovn')
		 ON CONFLICT (id) DO NOTHING`,
		vnetID, tenantSlug, tenantUUID, projectSlug, projectAUUID,
		fmt.Sprintf("vnet-proj-a-%s", vnetID),
	)
	require.NoError(t, err, "seed VNet row for project A")

	// ── Step 2: Verify project A owner can list and GET the VNet ─────────────

	tokenA, err := env.JWT.MintToken(tenantSlug, userSub)
	require.NoError(t, err, "mint token for project A owner")
	clientA := NewAPIClientForProject(env.BaseURL, tokenA, tenantSlug, projectSlug)

	listResp, listStatus, err := clientA.ListVNets(ctx)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, listStatus, "project A owner must list VNets: 200")
	foundInA := false
	for _, v := range listResp {
		if v.ID == vnetID.String() {
			foundInA = true
		}
	}
	require.True(t, foundInA, "VNet must appear in project A's list")

	getResp, getStatus, err := clientA.GetVNet(ctx, vnetID.String())
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, getStatus, "GET VNet under project A must return 200")
	require.Equal(t, vnetID.String(), getResp.ID)

	// ── Step 3: Orphan the VNet and delete project A ──────────────────────────
	//
	// We simulate a hard platform-admin purge: NULL-out the project_uuid FK on
	// the VNet row so the project row can be deleted (FK is ON DELETE RESTRICT).
	// The VNet row remains — it becomes an orphan with NULL project_uuid.

	_, err = env.DB.Pool().Exec(ctx,
		`UPDATE vnets SET project_id = NULL, project_uuid = NULL WHERE id = $1`,
		vnetID,
	)
	require.NoError(t, err, "detach VNet from project A before project deletion")

	_, err = env.DB.Pool().Exec(ctx,
		`DELETE FROM projects WHERE tenant_id = $1 AND id = $2`,
		tenantSlug, projectSlug,
	)
	require.NoError(t, err, "delete project A row")

	// ── Step 4: Re-register the same slug → project B (fresh UUID) ───────────

	projectB, _, _, err := env.DB.CreateProject(ctx,
		models.Project{
			ID:         projectSlug,
			TenantID:   tenantSlug,
			TenantUUID: tenantUUID,
			Name:       "Project B (recycled slug)",
			CPUCores:   10,
			MemoryGB:   32,
			StorageGB:  200,
			CreatedBy:  "proj-recycle-test",
		},
		models.ProjectQuota{MaxVNets: 5, MaxClusters: 1, MaxVolumes: 10, MaxPublicIPs: 2},
	)
	require.NoError(t, err, "re-register project with same slug → project B")
	require.NotNil(t, projectB)

	// ── Step 7 (checked here): project B's UUID must differ from project A's ──
	require.NotEqual(t, projectAUUID, projectB.ProjectUUID,
		"re-registering a project slug must produce a fresh project_uuid (slug-recycle isolation)")

	// ── Step 5: List VNets under project B → empty ────────────────────────────

	// Re-mint the token — same user, same tenant, same project slug, but the
	// project_uuid behind the slug is now B's.
	tokenB, err := env.JWT.MintToken(tenantSlug, userSub)
	require.NoError(t, err, "mint token for project B")
	clientB := NewAPIClientForProject(env.BaseURL, tokenB, tenantSlug, projectSlug)

	listRespB, listStatusB, err := clientB.ListVNets(ctx)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, listStatusB, "project B owner must list VNets: 200")
	for _, v := range listRespB {
		require.NotEqual(t, vnetID.String(), v.ID,
			"project B must NOT see the orphaned VNet from project A (slug-recycle isolation failure)")
	}

	// ── Step 6: Direct GET of the orphan VNet → 404 ───────────────────────────

	_, orphanStatus, err := clientB.GetVNet(ctx, vnetID.String())
	require.NoError(t, err)
	require.Equal(t, http.StatusNotFound, orphanStatus,
		"GET of project-A's VNet UUID via project-B client must return 404 (slug-recycle isolation failure)")

	// ── Step 8: Cleanup ───────────────────────────────────────────────────────

	t.Cleanup(func() {
		ctx := context.Background()
		_, _ = env.DB.Pool().Exec(ctx, `DELETE FROM vnets WHERE id = $1`, vnetID)
		_, _ = env.DB.Pool().Exec(ctx,
			`DELETE FROM projects WHERE tenant_id = $1`, tenantSlug)
		_, _ = env.DB.Pool().Exec(ctx,
			`DELETE FROM role_assignments WHERE scope_id = $1 AND scope_type = 'tenant'`, tenantSlug)
		_, _ = env.DB.Pool().Exec(ctx, `DELETE FROM tenants WHERE id = $1`, tenantSlug)
	})
}
