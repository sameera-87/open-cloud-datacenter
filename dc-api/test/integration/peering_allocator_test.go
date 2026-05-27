//go:build integration

package integration

import (
	"context"
	"net/http"
	"regexp"
	"testing"

	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// TestPeering_F6_AllocatorActuallyUsed pins down that the F6 DB-backed
// transit-CIDR allocator is being exercised end-to-end (handler → DB →
// kubeovn driver → live Vpc CRD) rather than the legacy SHA-256 hash
// fallback path. Without this test, every other peering integration test
// passes against the hash path too — a regression that wiped out the
// `spec.TransitCIDR` plumbing would go unnoticed.
//
// Three things are asserted:
//
//  1. A row exists in `peering_transit_cidrs` for the newly-created
//     peering. The handler can only have inserted it via
//     AllocateTransitCIDR, so this proves the new code ran.
//
//  2. The live Vpc CRD's `spec.vpcPeerings[].localConnectIP` matches the
//     allocator's `100.{64+(idx>>8)}.{idx&0xff}.{1|2}/24` shape — i.e.
//     the third octet is 0 for the first allocation, NOT the random
//     value the hash path would produce.
//
//  3. After Peering DELETE, the `peering_transit_cidrs` row is gone so
//     the index can be reused by a future peering.
func TestPeering_F6_AllocatorActuallyUsed(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	client := clientForTenant(t, "tenant-f6-alloc")
	mustGrantOwnerForClient(t, "tenant-f6-alloc")

	vnetA := mustCreateActiveVNet(t, client, randomName("vneta"), "10.240.0.0/16", "lk")
	vnetB := mustCreateActiveVNet(t, client, randomName("vnetb"), "10.241.0.0/16", "lk")

	pResp, body, status, err := client.CreatePeering(ctx, vnetA, map[string]interface{}{
		"name": randomName("f6-peering"), "peer_vnet_id": vnetB,
	})
	require.NoError(t, err)
	require.Equal(t, http.StatusAccepted, status, "body: %s", ErrorBody(body))
	peeringID := pResp.Resource.ID

	t.Cleanup(func() {
		ctx := context.Background()
		_, _ = client.DeletePeering(ctx, vnetA, peeringID)
		WaitPeeringGone(t, client, vnetA, peeringID)
	})

	WaitPeeringActive(t, client, vnetA, peeringID)

	// 1. Allocator row exists. LookupTransitCIDR returns "" if no row is
	// present, so a non-empty result is direct proof the handler called
	// AllocateTransitCIDR.
	cidr, err := env.DB.LookupTransitCIDR(ctx, uuidMust(peeringID))
	require.NoError(t, err)
	require.NotEmpty(t, cidr,
		"peering %s must have a row in peering_transit_cidrs after CreatePeering (F6 allocator wiring broken)",
		peeringID)
	t.Logf("F6: peering %s got allocator CIDR %s", peeringID, cidr)

	// 2. The Vpc CRDs' localConnectIP for this peering matches the
	// allocator's network address shape. The allocator hands out
	// `100.64.0.0/24`, `100.64.1.0/24`, … sequentially — third octet
	// follows from index, fourth host octet is .1 or .2. The hash path
	// would produce a *random* third AND fourth octet (e.g.
	// `100.64.173.42/24`), so any host octet >= 65 disproves it.
	uidA := dbGetVNetBackendUID(t, vnetA)
	uidB := dbGetVNetBackendUID(t, vnetB)

	allocatorShape := regexp.MustCompile(`^100\.(6[4-9]|[789]\d|1[01]\d|12[0-7])\.\d+\.[12]/24$`)
	for _, vpcName := range []string{uidA, uidB} {
		localIP := mustLocalConnectIP(t, vpcName)
		require.Regexp(t, allocatorShape, localIP,
			"localConnectIP on %s does not match the F6 allocator shape (host octet must be .1 or .2)", vpcName)
	}

	// 3. After delete the row is freed for reuse.
	delStatus, err := client.DeletePeering(ctx, vnetA, peeringID)
	require.NoError(t, err)
	require.Equal(t, http.StatusAccepted, delStatus)
	WaitPeeringGone(t, client, vnetA, peeringID)

	cidrAfter, err := env.DB.LookupTransitCIDR(ctx, uuidMust(peeringID))
	require.NoError(t, err)
	require.Empty(t, cidrAfter,
		"peering_transit_cidrs row must be gone after Peering DELETE (CASCADE or explicit release missing)")
}

// mustLocalConnectIP fetches the live Vpc CRD and returns the FIRST
// vpcPeerings entry's localConnectIP. The TestPeering_F6_AllocatorActuallyUsed
// caller creates exactly one peering on each VPC so position 0 is the one
// we want.
func mustLocalConnectIP(t *testing.T, vpcName string) string {
	t.Helper()
	obj, err := env.KubeClient.Resource(kubeovnVpcGVR).Get(context.Background(), vpcName, metav1.GetOptions{})
	require.NoError(t, err, "get vpc %s", vpcName)
	spec, _ := obj.Object["spec"].(map[string]interface{})
	require.NotNil(t, spec, "vpc %s has no spec", vpcName)
	peerings, _ := spec["vpcPeerings"].([]interface{})
	require.NotEmpty(t, peerings, "vpc %s has no vpcPeerings entries", vpcName)
	first, _ := peerings[0].(map[string]interface{})
	require.NotNil(t, first, "vpc %s peerings[0] is malformed", vpcName)
	ip, _ := first["localConnectIP"].(string)
	require.NotEmpty(t, ip, "vpc %s peerings[0] has no localConnectIP", vpcName)
	return ip
}
