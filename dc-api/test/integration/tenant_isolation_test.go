//go:build integration

package integration

import (
	"context"
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestTenantIsolation_GetVNetCrossTenantReturns404(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	clientA := clientForTenant(t, "tenant-iso-A1")
	clientB := clientForTenant(t, "tenant-iso-B1")
	mustGrantOwnerForClient(t, "tenant-iso-A1")

	vnetID := mustCreateActiveVNet(t, clientA, randomName("vnet"), "10.160.0.0/16", "lk")

	_, status, err := clientB.GetVNet(ctx, vnetID)
	require.NoError(t, err)
	require.Equal(t, http.StatusNotFound, status, "cross-tenant GET must return 404")
}

func TestTenantIsolation_ListOnlyOwnVNets(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	clientA := clientForTenant(t, "tenant-iso-A2")
	clientB := clientForTenant(t, "tenant-iso-B2")
	mustGrantOwnerForClient(t, "tenant-iso-A2")

	vnetIDa := mustCreateActiveVNet(t, clientA, randomName("vnet"), "10.161.0.0/16", "lk")

	listB, status, err := clientB.ListVNets(ctx)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, status)
	for _, v := range listB {
		require.NotEqual(t, vnetIDa, v.ID, "tenant B must not see tenant A's VNet in list")
	}
}

func TestTenantIsolation_DeleteCrossTenantReturns404(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	clientA := clientForTenant(t, "tenant-iso-A3")
	clientB := clientForTenant(t, "tenant-iso-B3")
	// A needs owner for cleanup; B needs owner so the RBAC check passes and the
	// handler reaches the tenant-isolation check (which returns 404).
	mustGrantOwnerForClient(t, "tenant-iso-A3")
	mustGrantOwnerForClient(t, "tenant-iso-B3")

	vnetID := mustCreateActiveVNet(t, clientA, randomName("vnet"), "10.162.0.0/16", "lk")

	_, status, err := clientB.DeleteVNet(ctx, vnetID)
	require.NoError(t, err)
	require.Equal(t, http.StatusNotFound, status, "cross-tenant DELETE must return 404")
}

func TestTenantIsolation_PeeringRejectsForeignVNet(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	clientA := clientForTenant(t, "tenant-iso-A4")
	clientB := clientForTenant(t, "tenant-iso-B4")
	mustGrantOwnerForClient(t, "tenant-iso-A4")
	mustGrantOwnerForClient(t, "tenant-iso-B4")

	vnetA := mustCreateActiveVNet(t, clientA, randomName("vnet"), "10.163.0.0/16", "lk")
	vnetB := mustCreateActiveVNet(t, clientB, randomName("vnet"), "10.164.0.0/16", "lk")

	_, body, status, err := clientA.CreatePeering(ctx, vnetA, map[string]interface{}{
		"name": randomName("peering"), "peer_vnet_id": vnetB,
	})
	require.NoError(t, err)
	require.Equal(t, http.StatusNotFound, status, "cross-tenant peer VNet must return 404: %s", ErrorBody(body))
}
