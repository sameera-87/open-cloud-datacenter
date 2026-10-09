package acctest

// Plan: docs/testsuite/resources/nsg-attachment.md

import (
	"context"
	"fmt"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"

	"terraform-provider-dcapi/internal/client"
)

// TestAccNSGAttachment_basic exercises the lifecycle of a dcapi_nsg_attachment that binds an NSG to
// a subnet: create, import, data source parity, and out-of-band deletion.
//
// PASSES when: the attachment is created with a computed attachment_id, target_type "subnet", and
// target_id equal to the parent subnet's subnet_uuid, and DC-API lists exactly one attachment on
// the NSG; import reproduces state; the NSG data source reports attachments.# == 1; and deleting
// just the attachment out-of-band yields a non-empty plan.
// FAILS when: target_type/target_id are wrong, the API attachment count disagrees, import drops a
// field, the data source doesn't see the attachment, or Read ignores the attachment being gone.
func TestAccNSGAttachment_basic(t *testing.T) {
	name := RandomName("nsga")
	addr := "dcapi_nsg_attachment.test"
	cfg := testAccNSGAttachmentConfig(name)

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { PreCheck(t) },
		ProtoV5ProviderFactories: ProviderFactories,
		CheckDestroy:             testAccNSGAttachmentCheckDestroy(),
		Steps: []resource.TestStep{
			{
				// Step 1 — Create: assert the attachment targets the parent subnet and DC-API lists
				// exactly one attachment on the NSG.
				Config: cfg,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttrSet(addr, "attachment_id"),
					resource.TestCheckResourceAttr(addr, "target_type", "subnet"),
					resource.TestCheckResourceAttrPair(addr, "target_id", "dcapi_subnet.parent", "subnet_uuid"),
					CheckAPI("dcapi_network_security_group.test", checkNSGAttachmentCount(1)),
				),
			},
			{
				// Step 2 — Import: re-import by state ID and verify every attribute matches.
				ResourceName:      addr,
				ImportState:       true,
				ImportStateVerify: true,
			},
			{
				// Step 3 — Data source parity: the NSG data source must report one attachment,
				// proving the attachment is visible through the NSG's own read path.
				Config: cfg + `
data "dcapi_network_security_group" "test" {
  tenant_id  = local.tenant_id
  project_id = local.project_id
  name       = dcapi_network_security_group.test.name
}
`,
				Check: resource.TestCheckResourceAttr("data.dcapi_network_security_group.test", "attachments.#", "1"),
			},
			{
				// Step 4 — Disappears: delete the attachment only (NSG and subnet remain); Read must
				// drop it from state and produce a non-empty plan to recreate it.
				Config:             cfg,
				Check:              Disappears(addr, DeleteNSGAttachment),
				ExpectNonEmptyPlan: true,
			},
		},
	})
}

// TestAccNSGAttachment_parentNSGGone checks the attachment's Read handles its parent NSG
// disappearing gracefully: when both the attachment and its NSG are deleted out-of-band, Read must
// clear the ID (treat it as gone) rather than error out and break every later plan.
//
// PASSES when: after deleting both the attachment and the parent NSG behind Terraform's back, Read
// drops the attachment from state and the re-plan is non-empty.
// FAILS when: Read returns an error (e.g. on the missing parent) instead of clearing the ID, so the
// plan can't be produced.
func TestAccNSGAttachment_parentNSGGone(t *testing.T) {
	name := RandomName("nsga")

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { PreCheck(t) },
		ProtoV5ProviderFactories: ProviderFactories,
		CheckDestroy:             testAccNSGAttachmentCheckDestroy(),
		Steps: []resource.TestStep{
			{
				// Delete both the attachment and its parent NSG out-of-band; Read must still clear
				// the attachment's ID and yield a non-empty plan rather than erroring.
				Config: testAccNSGAttachmentConfig(name),
				Check: resource.ComposeTestCheckFunc(
					Disappears("dcapi_nsg_attachment.test", DeleteNSGAttachment),
					Disappears("dcapi_network_security_group.test", DeleteNSG),
				),
				ExpectNonEmptyPlan: true,
			},
		},
	})
}

// TestAccNSGAttachment_destroyOrder verifies clean teardown of the dependency chain. With just a
// create step, the test's real work happens at CheckDestroy: Terraform must delete the attachment
// before the NSG and the subnet, and DC-API must accept that order without a 409.
//
// PASSES when: the single create step applies and the final teardown deletes attachment → NSG →
// subnet → vnet so that CheckDestroy confirms all four resources are gone.
// FAILS when: the dependency ordering is wrong or DC-API rejects a delete (e.g. 409 because the
// attachment still references the NSG/subnet), leaving a resource behind.
func TestAccNSGAttachment_destroyOrder(t *testing.T) {
	name := RandomName("nsga")

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { PreCheck(t) },
		ProtoV5ProviderFactories: ProviderFactories,
		CheckDestroy:             testAccNSGAttachmentCheckDestroy(),
		Steps: []resource.TestStep{
			// Create the full chain (vnet → subnet → NSG → attachment); teardown order is what's
			// under test via CheckDestroy.
			{Config: testAccNSGAttachmentConfig(name)},
		},
	})
}

// TestAccNSGAttachment_validation checks that target_type is validated at plan time: only "subnet"
// is accepted, so a config with target_type "vm" must be rejected before any API call.
//
// PASSES when: the plan-only step fails with an error matching `must be "subnet"`.
// FAILS when: target_type "vm" is accepted, or the error text doesn't match.
func TestAccNSGAttachment_validation(t *testing.T) {
	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { PreCheck(t) },
		ProtoV5ProviderFactories: ProviderFactories,
		Steps: []resource.TestStep{
			{
				// Plan-only: target_type "vm" must be rejected by the schema validator; no resource
				// is created.
				Config: ConfigBase() + `
resource "dcapi_nsg_attachment" "test" {
  tenant_id   = local.tenant_id
  project_id  = local.project_id
  sg_id       = "00000000-0000-0000-0000-000000000000"
  target_type = "vm"
  target_id   = "00000000-0000-0000-0000-000000000000"
}
`,
				PlanOnly:    true,
				ExpectError: ExpectErr(`must be "subnet"`),
			},
		},
	})
}

func testAccNSGAttachmentConfig(name string) string {
	return ConfigBase() + ConfigNetwork(name, CIDRNSGAttachmentVNet, CIDRNSGAttachmentSubnet) +
		testAccNSGResource(name, "acc", nsgRule{name: "allow-ssh", priority: 100, port: "22"}) + `
resource "dcapi_nsg_attachment" "test" {
  tenant_id   = local.tenant_id
  project_id  = local.project_id
  sg_id       = dcapi_network_security_group.test.sg_id
  target_type = "subnet"
  target_id   = dcapi_subnet.parent.subnet_uuid
}
`
}

func testAccNSGAttachmentCheckDestroy() resource.TestCheckFunc {
	return resource.ComposeTestCheckFunc(
		CheckDestroy("dcapi_nsg_attachment", NSGAttachmentExists),
		CheckDestroy("dcapi_network_security_group", NSGExists),
		CheckDestroy("dcapi_subnet", SubnetExists),
		CheckDestroy("dcapi_vnet", VNetExists),
	)
}

// checkNSGAttachmentCount asserts how many attachments DC-API lists on the NSG.
func checkNSGAttachmentCount(want int) APICheckFunc {
	return func(ctx context.Context, c *client.DCAPIClient, p []string) error {
		n, err := c.GetNSG(ctx, p[0], p[1], p[2])
		if err != nil {
			return err
		}
		if n == nil {
			return fmt.Errorf("NSG not found")
		}
		if len(n.Attachments) != want {
			return fmt.Errorf("DC-API lists %d attachments, want %d", len(n.Attachments), want)
		}
		for _, a := range n.Attachments {
			if a.TargetType != "subnet" {
				return fmt.Errorf("attachment target_type = %q, want subnet", a.TargetType)
			}
		}
		return nil
	}
}
