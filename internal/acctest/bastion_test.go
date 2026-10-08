package acctest

// Plan: docs/testsuite/resources/bastion.md

import (
	"context"
	"fmt"
	"regexp"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/plancheck"

	"terraform-provider-dcapi/internal/client"
)

// TestAccBastion_basic exercises the full happy-path lifecycle of a dcapi_bastion on top of its
// own VNet and /28 subnet: create (with PENDING→ACTIVE polling), refresh, import, and
// out-of-band deletion. It also captures the one-time secrets and verifies they are preserved.
//
// PASSES when: the bastion reaches ACTIVE with both IPs populated (internal_ip inside
// 10.209.1.0/28) and bastion_id set, DC-API confirms the vnet/subnet wiring, the sensitive
// private_key/console_password survive a refresh unchanged, tenant_id is not overwritten from
// the API response (G5), import reproduces every non-secret attribute, and a bastion deleted
// behind Terraform's back is detected as a non-empty plan.
// FAILS when: create never reaches ACTIVE, an IP is empty or internal_ip falls outside the
// subnet CIDR, the API disagrees on status/vnet/subnet, a secret changes on refresh, tenant_id
// drifts to the API UUID, import drops a field, or Read ignores the 404.
func TestAccBastion_basic(t *testing.T) {
	name := RandomName("bas")
	addr := "dcapi_bastion.test"
	cfg := testAccBastionConfig(name, "acc bastion")
	privateKey, password := &AttrSnapshot{}, &AttrSnapshot{}

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { PreCheck(t) },
		ProtoV5ProviderFactories: ProviderFactories,
		// CheckDestroy fails the test unless, after teardown, GetBastion, GetSubnet and GetVNet
		// all return 404 — proving each delete waiter polled until the object vanished.
		CheckDestroy: testAccBastionCheckDestroy(),
		Steps: []resource.TestStep{
			{
				// Step 1 — Create: apply the config and assert the Create/Read round-trip.
				// Passes only if the bastion is ACTIVE, both IPs are set (internal_ip within
				// 10.209.1.0/28), bastion_id is populated, and DC-API agrees. Snapshots the
				// one-time secrets for the refresh step.
				Config: cfg,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(addr, "status", "ACTIVE"),
					resource.TestCheckResourceAttrSet(addr, "mgmt_ip"),
					resource.TestMatchResourceAttr(addr, "internal_ip", regexp.MustCompile(`^10\.209\.1\.\d+$`)),
					resource.TestCheckResourceAttrSet(addr, "bastion_id"),
					privateKey.Save(addr, "private_key"),
					password.Save(addr, "console_password"),
					CheckAPI(addr, checkBastionAPI()),
				),
			},
			{
				// Step 2 — Refresh: re-run Read only. Secrets must survive a refresh (the GET
				// never returns them, so Read keeps them from state), and tenant_id isn't
				// overwritten from the API (G5). Fails if a secret changes or tenant_id drifts.
				RefreshState: true,
				Check: resource.ComposeAggregateTestCheckFunc(
					privateKey.Unchanged(addr, "private_key"),
					password.Unchanged(addr, "console_password"),
					resource.TestCheckResourceAttr(addr, "tenant_id", TenantID()),
				),
			},
			{
				// Step 3 — Import: re-import by the state ID (tenant/project/bastion_id) and
				// verify every attribute. The GET response never includes the one-time secrets,
				// so private_key/console_password are ignored; any other field Read doesn't
				// restore (including tenant_id/project_id parsed from the ID) fails the step.
				ResourceName:            addr,
				ImportState:             true,
				ImportStateVerify:       true,
				ImportStateVerifyIgnore: []string{"private_key", "console_password"},
			},
			{
				// Step 4 — Disappears: delete the bastion out-of-band, then re-plan. Read must
				// drop the gone resource from state and produce a non-empty recreate plan.
				// Passes only if ExpectNonEmptyPlan holds; fails if Read ignores the 404.
				Config:             cfg,
				Check:              Disappears(addr, DeleteBastion),
				ExpectNonEmptyPlan: true,
			},
		},
	})
}

// TestAccBastion_forceNew verifies that dcapi_bastion has no in-place update: description (like
// name, vnet_id and subnet_id) is ForceNew, so changing it must destroy-then-recreate the
// bastion. The IDSet records every bastion_id seen so CheckAllGone can confirm the replaced
// bastion was really deleted.
//
// PASSES when: changing description plans a DestroyBeforeCreate, and at teardown every recorded
// bastion_id returns a 404 (plus the subnet/vnet CheckDestroy).
// FAILS when: the change is treated as an in-place update, or a replaced bastion is left alive
// on the API.
func TestAccBastion_forceNew(t *testing.T) {
	name := RandomName("bas")
	addr := "dcapi_bastion.test"
	ids := &IDSet{}

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { PreCheck(t) },
		ProtoV5ProviderFactories: ProviderFactories,
		// Every recorded bastion_id must be gone (404), and the subnet/vnet must be destroyed too.
		CheckDestroy: resource.ComposeTestCheckFunc(ids.CheckAllGone(BastionExists), testAccBastionCheckDestroy()),
		Steps: []resource.TestStep{
			{
				// Step 1 — Create the baseline bastion and record its first bastion_id.
				Config: testAccBastionConfig(name, "first"),
				Check:  ids.Record(addr),
			},
			{
				// Step 2 — Change only description. ForceNew means this must replace the bastion:
				// the plan check requires DestroyBeforeCreate and a new bastion_id is recorded.
				Config: testAccBastionConfig(name, "second"),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{plancheck.ExpectResourceAction(addr, plancheck.ResourceActionDestroyBeforeCreate)},
				},
				Check: ids.Record(addr),
			},
		},
	})
}

func testAccBastionConfig(name, description string) string {
	return ConfigBase() + ConfigNetwork(name, CIDRBastionVNet, CIDRBastionSubnet) + fmt.Sprintf(`
resource "dcapi_bastion" "test" {
  tenant_id   = local.tenant_id
  project_id  = local.project_id
  name        = %q
  vnet_id     = dcapi_vnet.parent.vnet_uuid
  subnet_id   = dcapi_subnet.parent.subnet_uuid
  description = %q
}
`, name, description)
}

func testAccBastionCheckDestroy() resource.TestCheckFunc {
	return resource.ComposeTestCheckFunc(
		CheckDestroy("dcapi_bastion", BastionExists),
		CheckDestroy("dcapi_subnet", SubnetExists),
		CheckDestroy("dcapi_vnet", VNetExists),
	)
}

func checkBastionAPI() APICheckFunc {
	return func(ctx context.Context, c *client.DCAPIClient, p []string) error {
		b, err := c.GetBastion(ctx, p[0], p[1], p[2])
		if err != nil {
			return err
		}
		if b == nil {
			return fmt.Errorf("bastion not found")
		}
		if b.Status != "ACTIVE" || b.VNetID == "" || b.SubnetID == "" {
			return fmt.Errorf("status/vnet/subnet = %s/%s/%s, want ACTIVE with both IDs set", b.Status, b.VNetID, b.SubnetID)
		}
		return nil
	}
}
