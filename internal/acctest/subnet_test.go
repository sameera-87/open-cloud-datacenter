package acctest

// Plan: docs/testsuite/resources/subnet.md
//
// Every subnet here is the only one in its VNet, so each destroy takes the slow last-subnet
// path. These tests keep the provider's DEFAULT delete timeout on purpose, to catch G10.

import (
	"context"
	"fmt"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/compare"
	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/plancheck"
	"github.com/hashicorp/terraform-plugin-testing/statecheck"
	"github.com/hashicorp/terraform-plugin-testing/tfjsonpath"

	"terraform-provider-dcapi/internal/client"
)

// TestAccSubnet_basic exercises the full happy-path lifecycle of a dcapi_subnet inside its own
// VNet: create with the gateway omitted, read back through the data source, import, and out-of-band
// deletion.
//
// PASSES when: the subnet is created and reaches ACTIVE, cidr matches the config, the omitted
// gateway is auto-filled to the first usable IP with no diff, subnet_uuid is set, vnet_id points at
// the parent VNet, the data source returns the same values by name, import reproduces state exactly,
// and a subnet deleted out-of-band forces a non-empty plan.
// FAILS when: any attribute mapping is wrong, the API disagrees (CheckAPI), the data source
// diverges, import drops/changes a field, or Read doesn't notice the subnet is gone.
func TestAccSubnet_basic(t *testing.T) {
	name := RandomName("sn")
	addr := "dcapi_subnet.test"
	cfg := testAccSubnetConfig(name, CIDRSubnetBasic, "")

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { PreCheck(t) },
		ProtoV5ProviderFactories: ProviderFactories,
		// CheckDestroy fails unless both the subnet and its parent VNet return a 404 after teardown.
		CheckDestroy: resource.ComposeTestCheckFunc(
			CheckDestroy("dcapi_subnet", SubnetExists),
			CheckDestroy("dcapi_vnet", VNetExists),
		),
		Steps: []resource.TestStep{
			{
				// Step 1 — Create: apply the config and assert the Create/Read round-trip. Passes
				// only if state mirrors the config, the gateway is auto-filled, and the API confirms
				// the subnet matches (CheckAPI).
				Config: cfg,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(addr, "cidr", CIDRSubnetBasic),
					// gateway omitted → the API fills in the first usable IP, with no diff.
					resource.TestCheckResourceAttr(addr, "gateway", "10.200.10.1"),
					resource.TestCheckResourceAttr(addr, "status", "ACTIVE"),
					resource.TestCheckResourceAttrSet(addr, "subnet_uuid"),
					resource.TestCheckResourceAttrPair(addr, "vnet_id", "dcapi_vnet.parent", "vnet_uuid"),
					CheckAPI(addr, checkSubnetAPI(CIDRSubnetBasic, "10.200.10.1")),
				),
			},
			{
				// Step 2 — Data source parity: look the subnet up by name within its VNet and assert
				// the key values match the managed resource. Fails if the data source's Read diverges.
				Config: cfg + `
data "dcapi_subnet" "test" {
  tenant_id  = local.tenant_id
  project_id = local.project_id
  vnet_id    = dcapi_vnet.parent.vnet_uuid
  name       = dcapi_subnet.test.name
}
`,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttrPair("data.dcapi_subnet.test", "subnet_uuid", addr, "subnet_uuid"),
					resource.TestCheckResourceAttrPair("data.dcapi_subnet.test", "cidr", addr, "cidr"),
					resource.TestCheckResourceAttrPair("data.dcapi_subnet.test", "gateway", addr, "gateway"),
				),
			},
			{
				// Step 3 — Import: re-import the subnet by its state ID and verify every attribute.
				// ImportStateVerify fails if any field set on create is missing or different.
				ResourceName:      addr,
				ImportState:       true,
				ImportStateVerify: true,
			},
			{
				// Step 4 — Disappears: delete the subnet out-of-band, then re-plan. Read must drop it
				// from state and produce a non-empty plan; fails if the 404 is ignored.
				Config:             cfg,
				Check:              Disappears(addr, DeleteSubnet),
				ExpectNonEmptyPlan: true,
			},
		},
	})
}

// TestAccSubnet_explicitGateway verifies that an explicitly configured gateway is honored rather
// than overwritten by the API's auto-assignment.
//
// PASSES when: the subnet's gateway in state equals the configured 10.200.12.10 and DC-API reports
// the same cidr/gateway pair (CheckAPI).
// FAILS when: the gateway is ignored or replaced, or the API disagrees with the configured value.
func TestAccSubnet_explicitGateway(t *testing.T) {
	name := RandomName("sn")
	addr := "dcapi_subnet.test"

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { PreCheck(t) },
		ProtoV5ProviderFactories: ProviderFactories,
		CheckDestroy:             CheckDestroy("dcapi_subnet", SubnetExists),
		Steps: []resource.TestStep{
			{
				// Single step — Create with an explicit gateway and assert both state and the API
				// reflect that exact gateway.
				Config: testAccSubnetConfig(name, CIDRSubnetGateway, "10.200.12.10"),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(addr, "gateway", "10.200.12.10"),
					CheckAPI(addr, checkSubnetAPI(CIDRSubnetGateway, "10.200.12.10")),
				),
			},
		},
	})
}

// TestAccSubnet_forceNewCIDR verifies cidr is ForceNew: changing it cannot be an in-place update
// but must destroy-then-recreate the subnet with a new subnet_uuid. The IDSet records every
// subnet_uuid so CheckAllGone can confirm the replaced subnet was really deleted.
//
// PASSES when: the cidr change plans a DestroyBeforeCreate, the new cidr lands in state, the
// subnet_uuid differs from the previous step, and at teardown every recorded UUID returns a 404.
// FAILS when: the change is treated as an in-place update, the UUID is reused, or a replaced subnet
// is left alive on the API.
func TestAccSubnet_forceNewCIDR(t *testing.T) {
	name := RandomName("sn")
	addr := "dcapi_subnet.test"
	ids := &IDSet{}
	// ValuesDiffer asserts the subnet_uuid at this step differs from the one captured last step.
	differentID := statecheck.CompareValue(compare.ValuesDiffer())

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { PreCheck(t) },
		ProtoV5ProviderFactories: ProviderFactories,
		// Every subnet_uuid recorded across the steps must be gone (404) by the end.
		CheckDestroy: ids.CheckAllGone(SubnetExists),
		Steps: []resource.TestStep{
			{
				// Step 1 — Create the baseline subnet and record its first subnet_uuid.
				Config:            testAccSubnetConfig(name, CIDRSubnetBasic, ""),
				Check:             ids.Record(addr),
				ConfigStateChecks: []statecheck.StateCheck{differentID.AddStateValue(addr, tfjsonpath.New("subnet_uuid"))},
			},
			{
				// Step 2 — Change cidr to a new range. ForceNew means this must replace the subnet:
				// the plan check requires DestroyBeforeCreate, the new cidr must be in state, and the
				// subnet_uuid must differ from Step 1's.
				Config: testAccSubnetConfig(name, CIDRSubnetForceNewTarget, ""),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{plancheck.ExpectResourceAction(addr, plancheck.ResourceActionDestroyBeforeCreate)},
				},
				Check: resource.ComposeAggregateTestCheckFunc(
					ids.Record(addr),
					resource.TestCheckResourceAttr(addr, "cidr", CIDRSubnetForceNewTarget),
				),
				ConfigStateChecks: []statecheck.StateCheck{differentID.AddStateValue(addr, tfjsonpath.New("subnet_uuid"))},
			},
		},
	})
}

// testAccSubnetConfig declares its own VNet, then the subnet under test. gateway "" is omitted.
func testAccSubnetConfig(name, cidr, gateway string) string {
	gw := ""
	if gateway != "" {
		gw = fmt.Sprintf("gateway    = %q", gateway)
	}
	return ConfigBase() + ConfigNetwork(name, CIDRSubnetVNet, "") + fmt.Sprintf(`
resource "dcapi_subnet" "test" {
  tenant_id  = local.tenant_id
  project_id = local.project_id
  vnet_id    = dcapi_vnet.parent.vnet_uuid
  name       = %q
  cidr       = %q
  %s
}
`, name, cidr, gw)
}

func checkSubnetAPI(wantCIDR, wantGateway string) APICheckFunc {
	return func(ctx context.Context, c *client.DCAPIClient, p []string) error {
		s, err := c.GetSubnet(ctx, p[0], p[1], p[2], p[3])
		if err != nil {
			return err
		}
		if s == nil {
			return fmt.Errorf("subnet not found")
		}
		if s.CIDR != wantCIDR || s.Gateway != wantGateway {
			return fmt.Errorf("cidr/gateway = %s/%s, want %s/%s", s.CIDR, s.Gateway, wantCIDR, wantGateway)
		}
		return nil
	}
}
