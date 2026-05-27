//go:build integration

package integration

import (
	"context"
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestPeering_FullLifecycle(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	client := clientForTenant(t, "tenant-peering-lc")
	// DELETE requires owner role (M1.5 Chunk 4).
	mustGrantOwnerForClient(t, "tenant-peering-lc")
	vnetA := mustCreateActiveVNet(t, client, randomName("vneta"), "10.230.0.0/16", "lk")
	vnetB := mustCreateActiveVNet(t, client, randomName("vnetb"), "10.231.0.0/16", "lk")

	pResp, body, status, err := client.CreatePeering(ctx, vnetA, map[string]interface{}{
		"name": randomName("peering"), "peer_vnet_id": vnetB,
	})
	require.NoError(t, err)
	require.Equal(t, http.StatusAccepted, status, "body: %s", ErrorBody(body))
	require.Equal(t, "PENDING", pResp.Resource.Status)
	peeringID := pResp.Resource.ID

	t.Cleanup(func() {
		ctx := context.Background()
		_, _ = client.DeletePeering(ctx, vnetA, peeringID)
		WaitPeeringGone(t, client, vnetA, peeringID)
	})

	WaitPeeringActive(t, client, vnetA, peeringID)

	peering, err := env.DB.GetPeering(ctx, uuidMust(peeringID))
	require.NoError(t, err)
	require.NotEmpty(t, peering.BackendUID)
	exists, err := VpcPeeringExists(ctx, env.KubeClient, peering.BackendUID)
	require.NoError(t, err)
	require.True(t, exists, "VpcPeering CRD must exist")

	uidA := dbGetVNetBackendUID(t, vnetA)
	uidB := dbGetVNetBackendUID(t, vnetB)
	routesA, _ := GetVpcStaticRoutes(ctx, env.KubeClient, uidA)
	routesB, _ := GetVpcStaticRoutes(ctx, env.KubeClient, uidB)
	require.NotEmpty(t, routesA, "VPC A must have static routes after peering")
	require.NotEmpty(t, routesB, "VPC B must have static routes after peering")

	// Item 4 — symmetric listing: the peering must appear when listed from vnetB too.
	peeringsFromB, listStatus, listErr := client.ListPeerings(ctx, vnetB)
	require.NoError(t, listErr)
	require.Equal(t, http.StatusOK, listStatus, "ListPeerings from vnetB must return 200")
	found := false
	for _, p := range peeringsFromB {
		if p.ID == peeringID {
			found = true
			break
		}
	}
	require.True(t, found,
		"peering %s must be visible when listing from peer VNet %s (symmetric listing fix)", peeringID, vnetB)

	status, err = client.DeletePeering(ctx, vnetA, peeringID)
	require.NoError(t, err)
	require.Equal(t, http.StatusAccepted, status)
	WaitPeeringGone(t, client, vnetA, peeringID)
}

// TestPeering_VisibleFromBothSides explicitly tests the symmetric listing
// invariant: a peering created by vnetA appears in both vnetA's and vnetB's
// listing response, with both sides showing the same ID.
func TestPeering_VisibleFromBothSides(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	client := clientForTenant(t, "tenant-peering-symm")
	mustGrantOwnerForClient(t, "tenant-peering-symm")

	vnetA := mustCreateActiveVNet(t, client, randomName("vneta"), "10.235.0.0/16", "lk")
	vnetB := mustCreateActiveVNet(t, client, randomName("vnetb"), "10.236.0.0/16", "lk")

	pResp, body, status, err := client.CreatePeering(ctx, vnetA, map[string]interface{}{
		"name": randomName("peering"), "peer_vnet_id": vnetB,
	})
	require.NoError(t, err)
	require.Equal(t, http.StatusAccepted, status, "CreatePeering must return 202: %s", ErrorBody(body))
	peeringID := pResp.Resource.ID

	t.Cleanup(func() {
		ctx := context.Background()
		_, _ = client.DeletePeering(ctx, vnetA, peeringID)
		WaitPeeringGone(t, client, vnetA, peeringID)
	})

	WaitPeeringActive(t, client, vnetA, peeringID)

	// List from initiator side.
	peeringsA, statusA, errA := client.ListPeerings(ctx, vnetA)
	require.NoError(t, errA)
	require.Equal(t, http.StatusOK, statusA)

	foundInA := false
	for _, p := range peeringsA {
		if p.ID == peeringID {
			foundInA = true
			break
		}
	}
	require.True(t, foundInA, "peering must appear in initiator VNet's listing")

	// List from peer side — the key assertion for the symmetry fix.
	peeringsB, statusB, errB := client.ListPeerings(ctx, vnetB)
	require.NoError(t, errB)
	require.Equal(t, http.StatusOK, statusB)

	foundInB := false
	for _, p := range peeringsB {
		if p.ID == peeringID {
			foundInB = true
			// Both sides must report the same pair of VNet IDs.
			require.Equal(t, vnetA, p.VNetID,
				"peering returned from vnetB side must still record vnet_id as vnetA")
			require.Equal(t, vnetB, p.PeerVNetID,
				"peering returned from vnetB side must still record peer_vnet_id as vnetB")
			break
		}
	}
	require.True(t, foundInB,
		"peering %s must be visible when listing from the peer VNet %s", peeringID, vnetB)
}

func TestPeering_RejectAddressSpaceOverlap(t *testing.T) {
	t.Parallel()
	client := clientForTenant(t, "tenant-peering-overlap")
	mustGrantOwnerForClient(t, "tenant-peering-overlap")
	vnetA := mustCreateActiveVNet(t, client, randomName("vneta"), "10.232.0.0/16", "lk")
	vnetB := mustCreateActiveVNet(t, client, randomName("vnetb"), "10.232.0.0/16", "lk")
	_, body, status, err := client.CreatePeering(context.Background(), vnetA, map[string]interface{}{
		"name": randomName("peering"), "peer_vnet_id": vnetB,
	})
	require.NoError(t, err)
	require.Equal(t, http.StatusBadRequest, status, "overlapping address spaces must return 400: %s", ErrorBody(body))
}

func TestPeering_RejectCrossRegion(t *testing.T) {
	t.Skip("cross-region peering rejection requires a second region — deferred to M3")
}

func TestPeering_AllowForwardedTrafficWarningPresent(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	client := clientForTenant(t, "tenant-peering-fwdwarn")
	mustGrantOwnerForClient(t, "tenant-peering-fwdwarn")
	vnetA := mustCreateActiveVNet(t, client, randomName("vneta"), "10.233.0.0/16", "lk")
	vnetB := mustCreateActiveVNet(t, client, randomName("vnetb"), "10.234.0.0/16", "lk")

	pResp, body, status, err := client.CreatePeering(ctx, vnetA, map[string]interface{}{
		"name": randomName("peering"), "peer_vnet_id": vnetB, "allow_forwarded_traffic": true,
	})
	require.NoError(t, err)
	require.Equal(t, http.StatusAccepted, status, "body: %s", ErrorBody(body))
	peeringID := pResp.Resource.ID
	t.Cleanup(func() {
		ctx := context.Background()
		WaitPeeringActive(t, client, vnetA, peeringID)
		_, _ = client.DeletePeering(ctx, vnetA, peeringID)
		WaitPeeringGone(t, client, vnetA, peeringID)
	})

	require.NotEmpty(t, pResp.Resource.Warning, "allow_forwarded_traffic=true must include a warning field per § 14")
}
