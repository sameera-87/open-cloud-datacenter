package acctest

// Plan: docs/testsuite/resources/route-table-association.md

import (
	"context"
	"fmt"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/plancheck"
	"github.com/hashicorp/terraform-plugin-testing/terraform"

	"terraform-provider-dcapi/internal/client"
)

// TestAccRouteTableAssociation_basic exercises the happy-path lifecycle of a
// dcapi_route_table_association binding the test subnet to route table A: create, import, and
// out-of-band deletion of just the association.
//
// PASSES when: the association is created with a computed association_id, subnet_id and
// route_table_id match the subnet and route table A, DC-API's route table A actually lists the
// subnet among its associations, import reproduces the state exactly (ignoring the create-only
// warning field, which Read cannot restore), and deleting just the association behind Terraform's
// back is detected as a non-empty plan.
// FAILS when: association_id is empty, the id pairs are wrong, the API doesn't show the subnet on
// route table A, import drops/changes a non-ignored field, or Read doesn't notice the association
// is gone.
func TestAccRouteTableAssociation_basic(t *testing.T) {
	name := RandomName("rta")
	addr := "dcapi_route_table_association.test"
	cfg := testAccRouteTableAssociationConfig(name, "a")

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { PreCheck(t) },
		ProtoV5ProviderFactories: ProviderFactories,
		// CheckDestroy fails unless, after teardown, the association, route tables, subnet and VNet
		// are all gone (parsing the 5-part ID; the association must be absent from the route table's
		// associations[], or the route table itself gone).
		CheckDestroy: testAccRouteTableAssociationCheckDestroy(),
		Steps: []resource.TestStep{
			{
				// Step 1 — Create: apply the config and assert the Create/Read round-trip. Passes
				// only if state links the subnet to route table A and DC-API confirms the
				// association on route table A.
				Config: cfg,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttrSet(addr, "association_id"),
					resource.TestCheckResourceAttrPair(addr, "subnet_id", "dcapi_subnet.parent", "subnet_uuid"),
					resource.TestCheckResourceAttrPair(addr, "route_table_id", "dcapi_route_table.a", "route_table_id"),
					checkRouteTableHasSubnet("dcapi_route_table.a", true),
				),
			},
			{
				// Step 2 — Import: re-import by the 5-part state ID
				// (tenant/project/vnet/route_table_id/association_id) and verify every attribute.
				// warning comes only from the create response; Read can't restore it.
				// ImportStateVerify fails if any other field differs after import.
				ResourceName:            addr,
				ImportState:             true,
				ImportStateVerify:       true,
				ImportStateVerifyIgnore: []string{"warning"},
			},
			{
				// Step 3 — Disappears: delete the association only (not the route table) out-of-band,
				// then re-plan. Read must drop it from state and produce a non-empty plan to recreate
				// it. Passes only when ExpectNonEmptyPlan holds; fails if Read ignores the removal.
				Config:             cfg,
				Check:              Disappears(addr, DeleteRouteTableAssociation),
				ExpectNonEmptyPlan: true,
			},
		},
	})
}

// TestAccRouteTableAssociation_changeRouteTable verifies that pointing route_table_id at a
// different route table is a replacement (route_table_id is ForceNew): the subnet is moved from
// route table A to route table B. Because the association is destroyed before the new one is
// created, DC-API never sees the subnet associated twice.
//
// PASSES when: step 2 plans a DestroyBeforeCreate, state's route_table_id then matches route table
// B, and DC-API shows the subnet gone from A and present on B.
// FAILS when: the change is planned as create-before-destroy (risking a "subnet already associated"
// 409), route_table_id doesn't update, or the API still shows the subnet on A (or not on B).
func TestAccRouteTableAssociation_changeRouteTable(t *testing.T) {
	name := RandomName("rta")
	addr := "dcapi_route_table_association.test"

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { PreCheck(t) },
		ProtoV5ProviderFactories: ProviderFactories,
		CheckDestroy:             testAccRouteTableAssociationCheckDestroy(),
		Steps: []resource.TestStep{
			{
				// Step 1 — Associate the subnet with route table A; assert via the API that A lists
				// the subnet.
				Config: testAccRouteTableAssociationConfig(name, "a"),
				Check:  checkRouteTableHasSubnet("dcapi_route_table.a", true),
			},
			{
				// Step 2 — Repoint route_table_id to route table B. ForceNew forces a replacement;
				// the plan check pins DestroyBeforeCreate so the old association is dropped first.
				// Passes only if state now matches B and the API shows the subnet moved (gone from A,
				// present on B).
				Config: testAccRouteTableAssociationConfig(name, "b"),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{plancheck.ExpectResourceAction(addr, plancheck.ResourceActionDestroyBeforeCreate)},
				},
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttrPair(addr, "route_table_id", "dcapi_route_table.b", "route_table_id"),
					checkRouteTableHasSubnet("dcapi_route_table.a", false),
					checkRouteTableHasSubnet("dcapi_route_table.b", true),
				),
			},
		},
	})
}

// TestAccRouteTableAssociation_parentGone exercises Read's parent-missing branch: when the parent
// route table itself is deleted out-of-band (along with the association), Read must clear the
// association's ID from state rather than error.
//
// PASSES when: after deleting both the association and route table A behind Terraform's back, Read
// drops the association from state and the re-plan is non-empty (ExpectNonEmptyPlan).
// FAILS when: Read errors or fails to clear the ID when the parent route table is gone, so no
// recreate is planned.
func TestAccRouteTableAssociation_parentGone(t *testing.T) {
	name := RandomName("rta")

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { PreCheck(t) },
		ProtoV5ProviderFactories: ProviderFactories,
		CheckDestroy:             testAccRouteTableAssociationCheckDestroy(),
		Steps: []resource.TestStep{
			{
				// Single step — Create, then delete both the association and its parent route table A
				// out-of-band. Read's parent-missing branch must clear the ID; ExpectNonEmptyPlan
				// requires the follow-up plan to want a recreate.
				Config: testAccRouteTableAssociationConfig(name, "a"),
				Check: resource.ComposeTestCheckFunc(
					Disappears("dcapi_route_table_association.test", DeleteRouteTableAssociation),
					Disappears("dcapi_route_table.a", DeleteRouteTable),
				),
				ExpectNonEmptyPlan: true,
			},
		},
	})
}

// testAccRouteTableAssociationConfig declares a VNet, a subnet, route tables a and b, and an
// association from the subnet to route table `use`.
func testAccRouteTableAssociationConfig(name, use string) string {
	rt := func(key string) string {
		return fmt.Sprintf(`
resource "dcapi_route_table" %q {
  tenant_id  = local.tenant_id
  project_id = local.project_id
  vnet_id    = dcapi_vnet.parent.vnet_uuid
  name       = "%s-%s"
  routes {
    name             = "default"
    destination_cidr = "0.0.0.0/0"
    next_hop_type    = "internet"
  }
}
`, key, name, key)
	}
	return ConfigBase() + ConfigNetwork(name, CIDRRouteTableAssociationVNet, CIDRRouteTableAssociationSubnet) +
		rt("a") + rt("b") + fmt.Sprintf(`
resource "dcapi_route_table_association" "test" {
  tenant_id      = local.tenant_id
  project_id     = local.project_id
  vnet_id        = dcapi_vnet.parent.vnet_uuid
  route_table_id = dcapi_route_table.%s.route_table_id
  subnet_id      = dcapi_subnet.parent.subnet_uuid
}
`, use)
}

func testAccRouteTableAssociationCheckDestroy() resource.TestCheckFunc {
	return resource.ComposeTestCheckFunc(
		CheckDestroy("dcapi_route_table_association", RouteTableAssociationExists),
		CheckDestroy("dcapi_route_table", RouteTableExists),
		CheckDestroy("dcapi_subnet", SubnetExists),
		CheckDestroy("dcapi_vnet", VNetExists),
	)
}

// checkRouteTableHasSubnet asserts, through DC-API, whether rtAddr lists the test subnet
// among its associations.
func checkRouteTableHasSubnet(rtAddr string, want bool) resource.TestCheckFunc {
	return func(s *terraform.State) error {
		subnet, err := primary(s, "dcapi_subnet.parent")
		if err != nil {
			return err
		}
		subnetUUID := subnet.Attributes["subnet_uuid"]
		return CheckAPI(rtAddr, func(ctx context.Context, c *client.DCAPIClient, p []string) error {
			rt, err := c.GetRouteTable(ctx, p[0], p[1], p[2], p[3])
			if err != nil {
				return err
			}
			if rt == nil {
				return fmt.Errorf("route table not found")
			}
			found := false
			for _, a := range rt.Associations {
				if a.SubnetID == subnetUUID {
					found = true
				}
			}
			if found != want {
				return fmt.Errorf("subnet %s associated = %v, want %v", subnetUUID, found, want)
			}
			return nil
		})(s)
	}
}
