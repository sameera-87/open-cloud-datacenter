package acctest

// Plan: docs/testsuite/resources/vnet.md

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

// TestAccVNet_basic exercises the full happy-path lifecycle of a dcapi_vnet: create, read back
// through both the resource and the data source, import, and out-of-band deletion.
//
// PASSES when: the VNet is created and reaches ACTIVE, every state attribute matches the config
// (name, region, address_space, description) and the computed fields are populated, the data
// source returns the same values by name, import reproduces the state exactly, and a VNet deleted
// behind Terraform's back is detected as a non-empty plan.
// FAILS when: any attribute mapping is wrong, a computed field is empty, the data source disagrees
// with the resource, import drops/changes a field, or Read doesn't notice the VNet is gone.
func TestAccVNet_basic(t *testing.T) {
	name := RandomName("vnet")
	addr := "dcapi_vnet.test"
	cfg := testAccVNetConfig(name, "acc basic", CIDRVNetBasic)

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { PreCheck(t) },
		ProtoV5ProviderFactories: ProviderFactories,
		// CheckDestroy fails the test unless, after teardown, GetVNet returns a real 404 for
		// every VNet that was in state — proving delete waited for the VNet to actually vanish.
		CheckDestroy: CheckDestroy("dcapi_vnet", VNetExists),
		Steps: []resource.TestStep{
			{
				// Step 1 — Create: apply the config and assert the Create/Read round-trip.
				// Passes only if state mirrors the config and the API confirms the VNet is ACTIVE.
				Config: cfg,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(addr, "name", name),
					resource.TestCheckResourceAttr(addr, "region", Region()),
					resource.TestCheckResourceAttr(addr, "address_space.#", "1"),
					resource.TestCheckResourceAttr(addr, "address_space.0", CIDRVNetBasic),
					resource.TestCheckResourceAttr(addr, "description", "acc basic"),
					resource.TestCheckResourceAttr(addr, "status", "ACTIVE"),
					// Computed fields must be populated by Read; an empty value fails the step.
					resource.TestCheckResourceAttrSet(addr, "vnet_uuid"),
					resource.TestCheckResourceAttrSet(addr, "provider_type"),
					resource.TestCheckResourceAttrSet(addr, "created_at"),
					// Cross-check the provider's state against what DC-API itself reports.
					CheckAPI(addr, checkVNetAPI([]string{CIDRVNetBasic})),
				),
			},
			{
				// Step 2 — Data source parity: add a data "dcapi_vnet" that looks the VNet up by
				// name and assert it returns the same key values as the managed resource.
				// Fails if the data source's Read diverges from the resource's Read.
				Config: cfg + testAccVNetDataSource(),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttrPair("data.dcapi_vnet.test", "vnet_uuid", addr, "vnet_uuid"),
					resource.TestCheckResourceAttrPair("data.dcapi_vnet.test", "address_space.0", addr, "address_space.0"),
					resource.TestCheckResourceAttrPair("data.dcapi_vnet.test", "region", addr, "region"),
					resource.TestCheckResourceAttrPair("data.dcapi_vnet.test", "description", addr, "description"),
				),
			},
			{
				// Step 3 — Import: re-import the VNet by its state ID and verify every attribute.
				// ImportStateVerify fails the step if any field Read sets on create is missing or
				// different after a fresh import.
				ResourceName:      addr,
				ImportState:       true,
				ImportStateVerify: true,
			},
			{
				// Step 4 — Disappears: delete the VNet out-of-band, then re-plan. The resource is
				// gone from the API, so Read must drop it from state and produce a non-empty plan
				// to recreate it. Passes only when ExpectNonEmptyPlan holds; fails if Read ignores
				// the 404 and reports no changes.
				Config:             cfg,
				Check:              Disappears(addr, DeleteVNet),
				ExpectNonEmptyPlan: true,
			},
		},
	})
}

// TestAccVNet_multiAddressSpace creates a VNet whose address_space holds two CIDRs supplied in
// descending order on purpose (G4). It guards against the API silently reordering a list
// attribute, which would make the stored order differ from the config and leave a perpetual diff.
//
// PASSES when: both CIDRs land at the exact indices given in the config (index 0 = the first
// listed CIDR) and DC-API reports the same address space in the same order.
// FAILS when: the API returns the list reordered, so address_space.0/.1 no longer match the
// config — the symptom this test is designed to catch.
func TestAccVNet_multiAddressSpace(t *testing.T) {
	name := RandomName("vnet")
	addr := "dcapi_vnet.test"
	spaces := []string{CIDRVNetMultiB, CIDRVNetMultiA}

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { PreCheck(t) },
		ProtoV5ProviderFactories: ProviderFactories,
		CheckDestroy:             CheckDestroy("dcapi_vnet", VNetExists),
		Steps: []resource.TestStep{
			{
				// Create with two CIDRs; assert the count, each position, and API order.
				Config: ConfigBase() + fmt.Sprintf(`
resource "dcapi_vnet" "test" {
  tenant_id     = local.tenant_id
  project_id    = local.project_id
  name          = %q
  address_space = [%q, %q]
  region        = local.region
}
`, name, spaces[0], spaces[1]),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(addr, "address_space.#", "2"),
					resource.TestCheckResourceAttr(addr, "address_space.0", spaces[0]),
					resource.TestCheckResourceAttr(addr, "address_space.1", spaces[1]),
					CheckAPI(addr, checkVNetAPI(spaces)),
				),
			},
		},
	})
}

// TestAccVNet_forceNew verifies that dcapi_vnet has no in-place update: because every argument is
// ForceNew, changing any of them must destroy-then-recreate the resource with a brand-new
// vnet_uuid. It changes a non-identity field (description) and then an identity field
// (address_space), asserting a replacement each time. The IDSet records every vnet_uuid seen so
// CheckAllGone can confirm each replaced VNet was really deleted, not just orphaned.
//
// PASSES when: each change plans a DestroyBeforeCreate and yields a vnet_uuid different from the
// previous step, and at teardown every recorded UUID returns a 404.
// FAILS when: a change is treated as an in-place update, the UUID is reused, or a replaced VNet is
// left alive on the API.
func TestAccVNet_forceNew(t *testing.T) {
	name := RandomName("vnet")
	addr := "dcapi_vnet.test"
	ids := &IDSet{}
	// ValuesDiffer asserts the vnet_uuid at this step differs from the one captured last step.
	differentID := statecheck.CompareValue(compare.ValuesDiffer())

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { PreCheck(t) },
		ProtoV5ProviderFactories: ProviderFactories,
		// Every vnet_uuid recorded across the steps must be gone (404) by the end.
		CheckDestroy: ids.CheckAllGone(VNetExists),
		Steps: []resource.TestStep{
			{
				// Step 1 — Create the baseline VNet and record its first vnet_uuid.
				Config:            testAccVNetConfig(name, "first", CIDRVNetBasic),
				Check:             ids.Record(addr),
				ConfigStateChecks: []statecheck.StateCheck{differentID.AddStateValue(addr, tfjsonpath.New("vnet_uuid"))},
			},
			{
				// Step 2 — Change only description. ForceNew means this must replace the VNet:
				// the plan check requires DestroyBeforeCreate and the UUID must change.
				Config: testAccVNetConfig(name, "second", CIDRVNetBasic),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{plancheck.ExpectResourceAction(addr, plancheck.ResourceActionDestroyBeforeCreate)},
				},
				Check:             ids.Record(addr),
				ConfigStateChecks: []statecheck.StateCheck{differentID.AddStateValue(addr, tfjsonpath.New("vnet_uuid"))},
			},
			{
				// Step 3 — Change address_space to a new CIDR. Same expectation: a replacement
				// (DestroyBeforeCreate) and yet another distinct vnet_uuid.
				Config: testAccVNetConfig(name, "second", CIDRVNetForceNewTarget),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{plancheck.ExpectResourceAction(addr, plancheck.ResourceActionDestroyBeforeCreate)},
				},
				Check:             ids.Record(addr),
				ConfigStateChecks: []statecheck.StateCheck{differentID.AddStateValue(addr, tfjsonpath.New("vnet_uuid"))},
			},
		},
	})
}

func testAccVNetConfig(name, description, cidr string) string {
	return ConfigBase() + fmt.Sprintf(`
resource "dcapi_vnet" "test" {
  tenant_id     = local.tenant_id
  project_id    = local.project_id
  name          = %q
  address_space = [%q]
  region        = local.region
  description   = %q
}
`, name, cidr, description)
}

func testAccVNetDataSource() string {
	return `
data "dcapi_vnet" "test" {
  tenant_id  = local.tenant_id
  project_id = local.project_id
  name       = dcapi_vnet.test.name
}
`
}

// checkVNetAPI asserts DC-API itself reports the VNet ACTIVE with the same address space, in order.
func checkVNetAPI(wantSpaces []string) APICheckFunc {
	return func(ctx context.Context, c *client.DCAPIClient, p []string) error {
		v, err := c.GetVNet(ctx, p[0], p[1], p[2])
		if err != nil {
			return err
		}
		if v == nil {
			return fmt.Errorf("VNet not found")
		}
		if v.Status != "ACTIVE" {
			return fmt.Errorf("status = %q, want ACTIVE", v.Status)
		}
		if fmt.Sprint(v.AddressSpace) != fmt.Sprint(wantSpaces) {
			return fmt.Errorf("address_space = %v, want %v", v.AddressSpace, wantSpaces)
		}
		if v.Region != Region() {
			return fmt.Errorf("region = %q, want %q", v.Region, Region())
		}
		return nil
	}
}
