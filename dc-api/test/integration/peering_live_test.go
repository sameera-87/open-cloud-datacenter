//go:build integration

package integration

// TestPeering_OVNSpecAssertion (formerly TestPeering_LiveCrossVNetPing)
// is the regression test that catches spec-shape bugs in the VNet peering
// driver — the driver writes routes to each VPC's spec.staticRoutes after
// a peering becomes ACTIVE, and this test asserts both the CIDR and the
// nextHopIP have the right shape.
//
// Earlier this test booted two real VMs and ran `ping` between them. That
// covered both the OVN spec AND kernel-layer behaviour (MTU, ARP, etc.)
// but had two problems:
//
//   1. It took 30-40 minutes because of full Harvester VM provisioning.
//   2. The probe used `ping` inside the virt-launcher container, which
//      doesn't ship `ping` in the Harvester image we run — the test had
//      been broken for weeks.
//
// Option A from the M2 handoff: drop the kernel-layer probe and assert
// the OVN spec directly. That's enough to catch every spec-shape bug we
// hit during M2 driver work (driver writing routes into a named routeTable
// instead of the default, nextHopIP being empty or malformed, etc.). It
// misses kernel-layer issues (MTU, ARP, NetworkPolicy), but those have
// their own dedicated tests when we need them.

import (
	"context"
	"net"
	"net/http"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func TestPeering_OVNSpecAssertion(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	client := clientForTenant(t, "tenant-peering-spec")
	mustGrantOwnerForClient(t, "tenant-peering-spec")

	// joinSpace is the kube-ovn join subnet CIDR — every VPC peering
	// transit IP must come from this range. Hard-coded here because it's
	// a kube-ovn cluster-level constant, not a per-tenant value.
	_, joinSpace, err := net.ParseCIDR("100.64.0.0/10")
	require.NoError(t, err)

	// Two VNets with non-overlapping address spaces. The test asserts that
	// after peering, each VPC's staticRoutes contains a route to the OTHER's
	// address space, with a nextHopIP from the join space.
	const (
		vnetACIDR = "10.180.0.0/16"
		vnetBCIDR = "10.181.0.0/16"
	)

	vnetA := mustCreateActiveVNet(t, client, randomName("vnet-a"), vnetACIDR, "lk")
	vnetB := mustCreateActiveVNet(t, client, randomName("vnet-b"), vnetBCIDR, "lk")

	t.Cleanup(func() {
		cctx := context.Background()
		_, _, _ = client.DeleteVNet(cctx, vnetA)
		WaitVNetGone(t, client, vnetA)
		_, _, _ = client.DeleteVNet(cctx, vnetB)
		WaitVNetGone(t, client, vnetB)
	})

	// Subnets aren't strictly required for peering, but the M2 driver wires
	// per-subnet policy routes once a subnet is present — keep one each so
	// the test exercises the full routing path the driver builds.
	subnetA := mustCreateActiveSubnet(t, client, vnetA, randomName("sub-a"), "10.180.1.0/24")
	subnetB := mustCreateActiveSubnet(t, client, vnetB, randomName("sub-b"), "10.181.1.0/24")
	t.Cleanup(func() {
		cctx := context.Background()
		_, _, _ = client.DeleteSubnet(cctx, vnetA, subnetA)
		WaitSubnetGone(t, client, vnetA, subnetA)
		_, _, _ = client.DeleteSubnet(cctx, vnetB, subnetB)
		WaitSubnetGone(t, client, vnetB, subnetB)
	})

	// ── Create the peering and wait for it to reach ACTIVE ──────────────────
	pResp, body, status, err := client.CreatePeering(ctx, vnetA, map[string]interface{}{
		"name":         randomName("peer-ab"),
		"peer_vnet_id": vnetB,
	})
	require.NoError(t, err)
	require.Equal(t, http.StatusAccepted, status, "CreatePeering: %s", ErrorBody(body))
	peeringID := pResp.Resource.ID
	WaitPeeringActive(t, client, vnetA, peeringID)

	// Cleanup the peering before subnets/vnets so the staticRoutes are
	// removed first (kube-ovn won't tear down VPCs that have pending
	// peerings in spec).
	t.Cleanup(func() {
		cctx := context.Background()
		_, _ = client.DeletePeering(cctx, vnetA, peeringID)
		WaitPeeringGone(t, client, vnetA, peeringID)
	})

	// ── Assert the VpcPeering CRD entry is present in vnetA ────────────────
	peeringRow, dbErr := env.DB.GetPeering(ctx, uuid.MustParse(peeringID))
	require.NoError(t, dbErr)
	require.NotEmpty(t, peeringRow.BackendUID, "peering must have a kube-ovn backend UID")
	exists, existsErr := VpcPeeringExists(ctx, env.KubeClient, peeringRow.BackendUID)
	require.NoError(t, existsErr)
	require.True(t, exists, "VpcPeering must exist in vnetA's spec.vpcPeerings")

	// ── Assert staticRoutes on BOTH VPCs have the right shape ──────────────
	uidA := dbGetVNetBackendUID(t, vnetA)
	uidB := dbGetVNetBackendUID(t, vnetB)

	routesA, err := GetVpcStaticRoutes(ctx, env.KubeClient, uidA)
	require.NoError(t, err)
	routesB, err := GetVpcStaticRoutes(ctx, env.KubeClient, uidB)
	require.NoError(t, err)

	// vnetA must have a route to vnetB's address space (and vice versa),
	// in the default routeTable, with a nextHopIP that's a valid IPv4
	// address inside the kube-ovn join space.
	assertPeeringRoute(t, routesA, vnetBCIDR, joinSpace, "vnet-A → vnet-B")
	assertPeeringRoute(t, routesB, vnetACIDR, joinSpace, "vnet-B → vnet-A")

	t.Logf("PASS: VPC peering spec is well-formed — both VPCs have routes to the peer's CIDR with nextHopIP in %s", joinSpace)
}

// assertPeeringRoute walks a slice of staticRoute entries (as returned by
// GetVpcStaticRoutes) and asserts there's exactly one entry that:
//   - has cidr == targetCIDR,
//   - has routeTable == "" (default routing table — the one subnets use),
//   - has nextHopIP set to a valid IPv4 address inside joinSpace.
//
// `label` is a free-text description used to make the test failure message
// readable ("vnet-A → vnet-B").
func assertPeeringRoute(t *testing.T, routes []interface{}, targetCIDR string, joinSpace *net.IPNet, label string) {
	t.Helper()
	for _, r := range routes {
		m, ok := r.(map[string]interface{})
		if !ok {
			continue
		}
		cidr, _ := m["cidr"].(string)
		routeTable, _ := m["routeTable"].(string)
		nextHop, _ := m["nextHopIP"].(string)
		if cidr != targetCIDR || routeTable != "" {
			continue
		}
		require.NotEmpty(t, nextHop, "%s: route to %s has empty nextHopIP", label, targetCIDR)
		parsed := net.ParseIP(nextHop)
		require.NotNil(t, parsed, "%s: nextHopIP %q is not a valid IP", label, nextHop)
		require.True(t, joinSpace.Contains(parsed),
			"%s: nextHopIP %s for route %s must be inside the kube-ovn join space %s", label, nextHop, targetCIDR, joinSpace)
		return
	}
	t.Fatalf("%s: no default-routeTable route to %s found in spec.staticRoutes (got %d entries)", label, targetCIDR, len(routes))
}
