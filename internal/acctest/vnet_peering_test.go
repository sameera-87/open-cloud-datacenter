package acctest

// Plan: docs/testsuite/resources/vnet-peering.md

import (
	"context"
	"fmt"
	"regexp"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/plancheck"
	"github.com/hashicorp/terraform-plugin-testing/terraform"

	"terraform-provider-dcapi/internal/client"
)

// TestAccVNetPeering_basic exercises the full happy-path lifecycle of a single-direction
// dcapi_vnet_peering (A→B) between two VNets it declares: create, read back through the data
// source, import, and out-of-band deletion.
//
// PASSES when: the peering is created and reaches ACTIVE, peer_vnet_id points at the peer VNet,
// allow_forwarded_traffic is false, peering_id is set, DC-API lists the peering under VNet A
// (checkPeeringListed), the data source returns the same values by name, import reproduces state
// exactly, and a peering deleted out-of-band forces a non-empty plan.
// FAILS when: any attribute mapping is wrong, the API doesn't list the peering, the data source
// diverges, import drops/changes a field, or Read doesn't notice the peering is gone.
func TestAccVNetPeering_basic(t *testing.T) {
	name := RandomName("peer")
	addr := "dcapi_vnet_peering.a_to_b"
	cfg := testAccVNetPeeringConfig(name, CIDRVNetPeeringB, false, false)

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { PreCheck(t) },
		ProtoV5ProviderFactories: ProviderFactories,
		// CheckDestroy fails unless both the peering and its VNets return a 404 after teardown.
		CheckDestroy: testAccVNetPeeringCheckDestroy(),
		Steps: []resource.TestStep{
			{
				// Step 1 — Create: apply the config and assert the Create/Read round-trip. Passes
				// only if state mirrors the config, the peering is ACTIVE, and DC-API itself lists
				// the peering on VNet A (checkPeeringListed).
				Config: cfg,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(addr, "status", "ACTIVE"),
					resource.TestCheckResourceAttrPair(addr, "peer_vnet_id", "dcapi_vnet.peer", "vnet_uuid"),
					resource.TestCheckResourceAttr(addr, "allow_forwarded_traffic", "false"),
					resource.TestCheckResourceAttrSet(addr, "peering_id"),
					checkPeeringListed("dcapi_vnet.a", addr),
				),
			},
			{
				// Step 2 — Data source parity: look the peering up by name within VNet A and assert
				// the key values match the managed resource. Fails if the data source Read diverges.
				Config: cfg + `
data "dcapi_vnet_peering" "test" {
  tenant_id  = local.tenant_id
  project_id = local.project_id
  vnet_id    = dcapi_vnet.a.vnet_uuid
  name       = dcapi_vnet_peering.a_to_b.name
}
`,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttrPair("data.dcapi_vnet_peering.test", "peering_id", addr, "peering_id"),
					resource.TestCheckResourceAttrPair("data.dcapi_vnet_peering.test", "peer_vnet_id", addr, "peer_vnet_id"),
					resource.TestCheckResourceAttrPair("data.dcapi_vnet_peering.test", "status", addr, "status"),
				),
			},
			{
				// Step 3 — Import: re-import the peering by its state ID and verify every attribute.
				// ImportStateVerify fails if any field set on create is missing or different.
				ResourceName:      addr,
				ImportState:       true,
				ImportStateVerify: true,
			},
			{
				// Step 4 — Disappears: delete the peering out-of-band, then re-plan. Read must drop
				// it from state and produce a non-empty plan; fails if the 404 is ignored.
				Config:             cfg,
				Check:              Disappears(addr, DeleteVNetPeering),
				ExpectNonEmptyPlan: true,
			},
		},
	})
}

// TestAccVNetPeering_bidirectional verifies that a full two-way peering is modeled as two separate
// directional resources (A→B and B→A), since peering is directional.
//
// PASSES when: both the a_to_b and b_to_a peerings reach ACTIVE and DC-API lists each one under its
// own originating VNet (a_to_b on VNet A, b_to_a on the peer VNet).
// FAILS when: either direction isn't ACTIVE, or either peering isn't listed on its source VNet.
func TestAccVNetPeering_bidirectional(t *testing.T) {
	name := RandomName("peer")

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { PreCheck(t) },
		ProtoV5ProviderFactories: ProviderFactories,
		CheckDestroy:             testAccVNetPeeringCheckDestroy(),
		Steps: []resource.TestStep{
			{
				// Single step — Create both directions (reverse=true) and assert each is ACTIVE and
				// listed on its originating VNet.
				Config: testAccVNetPeeringConfig(name, CIDRVNetPeeringB, false, true),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("dcapi_vnet_peering.a_to_b", "status", "ACTIVE"),
					resource.TestCheckResourceAttr("dcapi_vnet_peering.b_to_a", "status", "ACTIVE"),
					checkPeeringListed("dcapi_vnet.a", "dcapi_vnet_peering.a_to_b"),
					checkPeeringListed("dcapi_vnet.peer", "dcapi_vnet_peering.b_to_a"),
				),
			},
		},
	})
}

// TestAccVNetPeering_forceNew verifies allow_forwarded_traffic is ForceNew: flipping it cannot be
// an in-place update but must destroy-then-recreate the peering. The IDSet records every peering_id
// so CheckAllGone can confirm the replaced peering was really deleted.
//
// PASSES when: toggling allow_forwarded_traffic from false to true plans a DestroyBeforeCreate, the
// new value lands in state, and at teardown every recorded peering returns a 404.
// FAILS when: the change is treated as an in-place update, or a replaced peering is left alive.
func TestAccVNetPeering_forceNew(t *testing.T) {
	name := RandomName("peer")
	addr := "dcapi_vnet_peering.a_to_b"
	ids := &IDSet{}

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { PreCheck(t) },
		ProtoV5ProviderFactories: ProviderFactories,
		// Every peering_id recorded across the steps must be gone (404) by the end.
		CheckDestroy: ids.CheckAllGone(VNetPeeringExists),
		Steps: []resource.TestStep{
			{
				// Step 1 — Create the peering with allow_forwarded_traffic=false and record its id.
				Config: testAccVNetPeeringConfig(name, CIDRVNetPeeringB, false, false),
				Check:  ids.Record(addr),
			},
			{
				// Step 2 — Flip allow_forwarded_traffic to true. ForceNew means this must replace the
				// peering: the plan check requires DestroyBeforeCreate and the new value must be in
				// state.
				Config: testAccVNetPeeringConfig(name, CIDRVNetPeeringB, true, false),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{plancheck.ExpectResourceAction(addr, plancheck.ResourceActionDestroyBeforeCreate)},
				},
				Check: resource.ComposeAggregateTestCheckFunc(
					ids.Record(addr),
					resource.TestCheckResourceAttr(addr, "allow_forwarded_traffic", "true"),
				),
			},
		},
	})
}

// TestAccVNetPeering_overlapRejected checks that overlapping address spaces can't be peered: the
// peer's CIDR overlaps side A, so DC-API must reject the peering and the provider must surface the
// API error. Tighten the regexp once the real message is known.
//
// PASSES when: applying the overlapping config fails with an HTTP 4xx error from DC-API.
// FAILS when: the overlapping peering is accepted, or the apply fails with a non-4xx error.
func TestAccVNetPeering_overlapRejected(t *testing.T) {
	name := RandomName("peer")

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { PreCheck(t) },
		ProtoV5ProviderFactories: ProviderFactories,
		CheckDestroy:             testAccVNetPeeringCheckDestroy(),
		Steps: []resource.TestStep{
			{
				// Single step — Create a peering whose peer CIDR overlaps side A; passes only if the
				// apply is rejected with an HTTP 4xx error.
				Config:      testAccVNetPeeringConfig(name, CIDRVNetPeeringOverlap, false, false),
				ExpectError: regexp.MustCompile(`HTTP 4\d\d`),
			},
		},
	})
}

// testAccVNetPeeringConfig declares side A, a peer VNet with peerCIDR, the A→B peering and,
// if reverse is set, the B→A peering.
func testAccVNetPeeringConfig(name, peerCIDR string, allowForwarded, reverse bool) string {
	cfg := ConfigBase() + fmt.Sprintf(`
resource "dcapi_vnet" "a" {
  tenant_id     = local.tenant_id
  project_id    = local.project_id
  name          = "%[1]s-a"
  address_space = [%[2]q]
  region        = local.region
}

resource "dcapi_vnet" "peer" {
  tenant_id     = local.tenant_id
  project_id    = local.project_id
  name          = "%[1]s-b"
  address_space = [%[3]q]
  region        = local.region
}

resource "dcapi_vnet_peering" "a_to_b" {
  tenant_id               = local.tenant_id
  project_id              = local.project_id
  vnet_id                 = dcapi_vnet.a.vnet_uuid
  name                    = "%[1]s-ab"
  peer_vnet_id            = dcapi_vnet.peer.vnet_uuid
  allow_forwarded_traffic = %[4]t
}
`, name, CIDRVNetPeeringA, peerCIDR, allowForwarded)

	if reverse {
		cfg += fmt.Sprintf(`
resource "dcapi_vnet_peering" "b_to_a" {
  tenant_id    = local.tenant_id
  project_id   = local.project_id
  vnet_id      = dcapi_vnet.peer.vnet_uuid
  name         = "%s-ba"
  peer_vnet_id = dcapi_vnet.a.vnet_uuid
}
`, name)
	}
	return cfg
}

func testAccVNetPeeringCheckDestroy() resource.TestCheckFunc {
	return resource.ComposeTestCheckFunc(
		CheckDestroy("dcapi_vnet_peering", VNetPeeringExists),
		CheckDestroy("dcapi_vnet", VNetExists),
	)
}

// checkPeeringListed asserts DC-API lists peeringAddr among vnetAddr's peerings.
func checkPeeringListed(vnetAddr, peeringAddr string) resource.TestCheckFunc {
	return func(s *terraform.State) error {
		peering, err := primary(s, peeringAddr)
		if err != nil {
			return err
		}
		want := peering.Attributes["peering_id"]
		return CheckAPI(vnetAddr, func(ctx context.Context, c *client.DCAPIClient, p []string) error {
			list, err := c.ListVNetPeerings(ctx, p[0], p[1], p[2])
			if err != nil {
				return err
			}
			for _, pr := range list {
				if pr.ID == want {
					return nil
				}
			}
			return fmt.Errorf("peering %s not listed on VNet %s", want, p[2])
		})(s)
	}
}
