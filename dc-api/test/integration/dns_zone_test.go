//go:build integration

package integration

import (
	"context"
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestPrivateDnsZone_FullLifecycle(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	client := clientForTenant(t, "tenant-dns-lc")
	// DELETE requires owner role (M1.5 Chunk 4).
	mustGrantOwnerForClient(t, "tenant-dns-lc")
	vnetID := mustCreateActiveVNet(t, client, randomName("vnet"), "10.250.0.0/16", "lk")

	zResp, body, status, err := client.CreateDNSZone(ctx, vnetID, map[string]string{
		"name":        "test.internal.lk",
		"description": "lifecycle test zone",
	})
	require.NoError(t, err)
	require.Equal(t, http.StatusAccepted, status, "body: %s", ErrorBody(body))
	require.Equal(t, "PENDING", zResp.Resource.Status)
	zoneID := zResp.Resource.ID

	t.Cleanup(func() {
		ctx := context.Background()
		_, _ = client.DeleteDNSZone(ctx, vnetID, zoneID)
		WaitDNSZoneGone(t, client, vnetID, zoneID)
	})

	WaitDNSZoneActive(t, client, vnetID, zoneID)

	getResp, status, err := client.GetDNSZone(ctx, vnetID, zoneID)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, status)
	require.Equal(t, "ACTIVE", getResp.Status)
	require.Equal(t, "test.internal.lk", getResp.Name)

	recResp, body, status, err := client.CreateDNSRecord(ctx, vnetID, zoneID, map[string]interface{}{
		"name": "api", "type": "A", "ttl": 300, "values": []string{"10.250.1.5"},
	})
	require.NoError(t, err)
	require.Equal(t, http.StatusCreated, status, "create record: %s", ErrorBody(body))
	require.Equal(t, "api", recResp.Name)
	require.Equal(t, "A", recResp.Type)
	recordID := recResp.ID

	status, err = client.DeleteDNSRecord(ctx, vnetID, zoneID, recordID)
	require.NoError(t, err)
	require.Equal(t, http.StatusNoContent, status)

	status, err = client.DeleteDNSZone(ctx, vnetID, zoneID)
	require.NoError(t, err)
	require.Equal(t, http.StatusAccepted, status)
	WaitDNSZoneGone(t, client, vnetID, zoneID)
}

func TestDnsZone_CrossTenantCollisionAllowed(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	clientA := clientForTenant(t, "tenant-dns-colA")
	clientB := clientForTenant(t, "tenant-dns-colB")
	mustGrantOwnerForClient(t, "tenant-dns-colA")
	mustGrantOwnerForClient(t, "tenant-dns-colB")
	vnetA := mustCreateActiveVNet(t, clientA, randomName("vnet"), "10.251.0.0/16", "lk")
	vnetB := mustCreateActiveVNet(t, clientB, randomName("vnet"), "10.252.0.0/16", "lk")

	respA, _, statusA, _ := clientA.CreateDNSZone(ctx, vnetA, map[string]string{"name": "shared.internal.lk"})
	respB, _, statusB, _ := clientB.CreateDNSZone(ctx, vnetB, map[string]string{"name": "shared.internal.lk"})
	require.Equal(t, http.StatusAccepted, statusA, "tenant A zone create must succeed")
	require.Equal(t, http.StatusAccepted, statusB, "tenant B zone create must succeed (same name allowed across tenants)")

	t.Cleanup(func() {
		ctx := context.Background()
		WaitDNSZoneActive(t, clientA, vnetA, respA.Resource.ID)
		_, _ = clientA.DeleteDNSZone(ctx, vnetA, respA.Resource.ID)
		WaitDNSZoneGone(t, clientA, vnetA, respA.Resource.ID)
		WaitDNSZoneActive(t, clientB, vnetB, respB.Resource.ID)
		_, _ = clientB.DeleteDNSZone(ctx, vnetB, respB.Resource.ID)
		WaitDNSZoneGone(t, clientB, vnetB, respB.Resource.ID)
	})
}
