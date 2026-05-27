//go:build integration

package integration

// M3 chunk 1 — Key Vault CRUD integration tests.
//
// Coverage:
//   - Happy-path Create → Get → List → Delete
//   - Duplicate-name (per tenant) → 409
//   - Cross-tenant Get → 404 (no leak)
//   - Out-of-range soft_delete_days → 400

import (
	"context"
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestKeyVault_FullLifecycle(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	client := clientForTenant(t, "tenant-kv-lc")
	mustGrantOwnerForClient(t, "tenant-kv-lc")

	name := randomName("kv")

	// ── Create ────────────────────────────────────────────────────────────────
	createResp, body, status, err := client.CreateKeyVault(ctx, CreateKeyVaultRequest{
		Name:           name,
		SoftDeleteDays: 14,
	})
	require.NoError(t, err)
	require.Equal(t, http.StatusCreated, status, "body: %s", ErrorBody(body))
	require.NotEmpty(t, createResp.ID)
	require.Equal(t, name, createResp.Name)
	require.Equal(t, 14, createResp.SoftDeleteDays)
	// When KVI is wired the vault starts PENDING; poll until ACTIVE.
	// In fallback (no KVI) it's already ACTIVE on create.
	require.Contains(t, []string{"PENDING", "ACTIVE"}, createResp.Status,
		"initial status must be PENDING (KVI mode) or ACTIVE (fallback mode)")

	kvID := createResp.ID
	t.Cleanup(func() {
		_, _, _ = client.DeleteKeyVault(context.Background(), kvID)
	})

	// Wait for the KVI operator to provision OpenBao and flip the vault to ACTIVE.
	WaitKeyVaultActive(t, client, kvID)

	// ── Get ───────────────────────────────────────────────────────────────────
	getResp, _, status, err := client.GetKeyVault(ctx, kvID)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, status)
	require.Equal(t, kvID, getResp.ID)
	require.Equal(t, name, getResp.Name)
	require.Equal(t, "ACTIVE", getResp.Status)

	// ── List ──────────────────────────────────────────────────────────────────
	listResp, _, status, err := client.ListKeyVaults(ctx)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, status)
	found := false
	for _, kv := range listResp {
		if kv.ID == kvID {
			found = true
			break
		}
	}
	require.True(t, found, "newly-created vault must appear in tenant list")

	// ── Delete ────────────────────────────────────────────────────────────────
	_, status, err = client.DeleteKeyVault(ctx, kvID)
	require.NoError(t, err)
	require.Equal(t, http.StatusNoContent, status)

	_, _, status, _ = client.GetKeyVault(ctx, kvID)
	require.Equal(t, http.StatusNotFound, status, "deleted vault must 404 on subsequent get")
}

func TestKeyVault_RejectDuplicateNameWithinTenant(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	client := clientForTenant(t, "tenant-kv-dup")
	mustGrantOwnerForClient(t, "tenant-kv-dup")

	name := randomName("kv")

	first, body, status, err := client.CreateKeyVault(ctx, CreateKeyVaultRequest{Name: name})
	require.NoError(t, err)
	require.Equal(t, http.StatusCreated, status, "body: %s", ErrorBody(body))
	t.Cleanup(func() {
		_, _, _ = client.DeleteKeyVault(context.Background(), first.ID)
	})

	_, body, status, err = client.CreateKeyVault(ctx, CreateKeyVaultRequest{Name: name})
	require.NoError(t, err)
	require.Equal(t, http.StatusConflict, status, "duplicate name must return 409: %s", ErrorBody(body))
}

func TestKeyVault_RejectOutOfRangeSoftDeleteDays(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	client := clientForTenant(t, "tenant-kv-sdd")
	mustGrantOwnerForClient(t, "tenant-kv-sdd")

	_, body, status, err := client.CreateKeyVault(ctx, CreateKeyVaultRequest{
		Name:           randomName("kv"),
		SoftDeleteDays: 999, // > 90
	})
	require.NoError(t, err)
	require.Equal(t, http.StatusBadRequest, status, "body: %s", ErrorBody(body))

	_, body, status, err = client.CreateKeyVault(ctx, CreateKeyVaultRequest{
		Name:           randomName("kv"),
		SoftDeleteDays: 1, // < 7
	})
	require.NoError(t, err)
	require.Equal(t, http.StatusBadRequest, status, "body: %s", ErrorBody(body))
}

func TestKeyVault_CrossTenantGetReturns404(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	clientA := clientForTenant(t, "tenant-kv-iso-a")
	mustGrantOwnerForClient(t, "tenant-kv-iso-a")

	respA, body, status, err := clientA.CreateKeyVault(ctx, CreateKeyVaultRequest{Name: randomName("kv")})
	require.NoError(t, err)
	require.Equal(t, http.StatusCreated, status, "body: %s", ErrorBody(body))
	t.Cleanup(func() {
		_, _, _ = clientA.DeleteKeyVault(context.Background(), respA.ID)
	})

	clientB := clientForTenant(t, "tenant-kv-iso-b")
	mustGrantOwnerForClient(t, "tenant-kv-iso-b")

	_, _, status, _ = clientB.GetKeyVault(ctx, respA.ID)
	require.Equal(t, http.StatusNotFound, status,
		"tenant B must get 404 (not 403) when looking up tenant A's vault — no existence leak")
}
