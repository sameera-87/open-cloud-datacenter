package acctest

// Plan: docs/testsuite/resources/private-dns-zone.md

import (
	"context"
	"fmt"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/plancheck"

	"terraform-provider-dcapi/internal/client"
)

// TestAccPrivateDNSZone_basic exercises the full happy-path lifecycle of a dcapi_private_dns_zone
// over its own VNet: create, read back through the data source, import, and out-of-band deletion.
//
// PASSES when: the zone is created and reaches ACTIVE, the stored name is exactly the configured
// "<name>.acc.internal" (no trailing dot or case change), description matches, the computed
// zone_id is populated, DC-API reports the same ACTIVE name, the data source returns the same
// zone_id/description, import reproduces the state exactly, and a zone deleted behind Terraform's
// back is detected as a non-empty plan.
// FAILS when: the name is normalised (trailing dot / different case) and diverges from the config,
// any attribute mapping is wrong, zone_id is empty, the API disagrees, the data source diverges,
// import drops/changes a field, or Read doesn't notice the zone is gone.
func TestAccPrivateDNSZone_basic(t *testing.T) {
	name := RandomName("dz")
	addr := "dcapi_private_dns_zone.test"
	cfg := testAccPrivateDNSZoneConfig(name, "acc zone")

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { PreCheck(t) },
		ProtoV5ProviderFactories: ProviderFactories,
		// CheckDestroy fails unless, after teardown, both the zone and its parent VNet return a
		// real 404 — proving delete waited for each to actually vanish.
		CheckDestroy: resource.ComposeTestCheckFunc(
			CheckDestroy("dcapi_private_dns_zone", PrivateDNSZoneExists),
			CheckDestroy("dcapi_vnet", VNetExists),
		),
		Steps: []resource.TestStep{
			{
				// Step 1 — Create: apply the config and assert the Create/Read round-trip.
				// Passes only if state mirrors the config (name wrapped as "<name>.acc.internal",
				// description, zone_id set) and the API confirms the zone is ACTIVE under that name.
				Config: cfg,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(addr, "name", name+".acc.internal"),
					resource.TestCheckResourceAttr(addr, "description", "acc zone"),
					resource.TestCheckResourceAttr(addr, "status", "ACTIVE"),
					resource.TestCheckResourceAttrSet(addr, "zone_id"),
					// Cross-check the provider's state against what DC-API itself reports.
					CheckAPI(addr, checkZoneAPI(name+".acc.internal")),
				),
			},
			{
				// Step 2 — Data source parity: add a data "dcapi_private_dns_zone" that looks the
				// zone up by vnet_id + name and assert it returns the same key values as the managed
				// resource. Fails if the data source's Read diverges from the resource's Read.
				Config: cfg + `
data "dcapi_private_dns_zone" "test" {
  tenant_id  = local.tenant_id
  project_id = local.project_id
  vnet_id    = dcapi_vnet.parent.vnet_uuid
  name       = dcapi_private_dns_zone.test.name
}
`,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttrPair("data.dcapi_private_dns_zone.test", "zone_id", addr, "zone_id"),
					resource.TestCheckResourceAttrPair("data.dcapi_private_dns_zone.test", "description", addr, "description"),
				),
			},
			{
				// Step 3 — Import: re-import the zone by its state ID
				// (tenant/project/vnet/zone_id) and verify every attribute. ImportStateVerify fails
				// the step if any field Read sets on create is missing or different after import.
				ResourceName:      addr,
				ImportState:       true,
				ImportStateVerify: true,
			},
			{
				// Step 4 — Disappears: delete the zone out-of-band, then re-plan. The resource is
				// gone from the API, so Read must drop it from state and produce a non-empty plan to
				// recreate it. Passes only when ExpectNonEmptyPlan holds; fails if Read ignores the
				// 404 and reports no changes.
				Config:             cfg,
				Check:              Disappears(addr, DeletePrivateDNSZone),
				ExpectNonEmptyPlan: true,
			},
		},
	})
}

// TestAccPrivateDNSZone_forceNew verifies that description is ForceNew: changing it must
// destroy-then-recreate the zone. Because the name stays the same across both steps, the old zone
// must be deleted before the new one is created, which checks DC-API frees the reserved name
// promptly. The IDSet records every zone_id so CheckAllGone can confirm each replaced zone really
// vanished.
//
// PASSES when: the description change plans a DestroyBeforeCreate and, at teardown, every recorded
// zone_id returns a 404.
// FAILS when: the change is treated as an in-place update, or the create of the replacement hits a
// 409 because DC-API has not released the old name, or a replaced zone is left alive.
func TestAccPrivateDNSZone_forceNew(t *testing.T) {
	name := RandomName("dz")
	addr := "dcapi_private_dns_zone.test"
	ids := &IDSet{}

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { PreCheck(t) },
		ProtoV5ProviderFactories: ProviderFactories,
		// Every zone_id recorded across the steps must be gone (404) by the end.
		CheckDestroy: ids.CheckAllGone(PrivateDNSZoneExists),
		Steps: []resource.TestStep{
			{
				// Step 1 — Create the baseline zone and record its first zone_id.
				Config: testAccPrivateDNSZoneConfig(name, "first"),
				Check:  ids.Record(addr),
			},
			{
				// Step 2 — Change only description. ForceNew means this must replace the zone: the
				// plan check requires DestroyBeforeCreate so the same name is freed first. Records
				// the new zone_id for the final CheckAllGone sweep.
				Config: testAccPrivateDNSZoneConfig(name, "second"),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{plancheck.ExpectResourceAction(addr, plancheck.ResourceActionDestroyBeforeCreate)},
				},
				Check: ids.Record(addr),
			},
		},
	})
}

func testAccPrivateDNSZoneConfig(name, description string) string {
	return ConfigBase() + ConfigNetwork(name, CIDRPrivateDNSZoneVNet, "") + fmt.Sprintf(`
resource "dcapi_private_dns_zone" "test" {
  tenant_id   = local.tenant_id
  project_id  = local.project_id
  vnet_id     = dcapi_vnet.parent.vnet_uuid
  name        = "%s.acc.internal"
  description = %q
}
`, name, description)
}

// checkZoneAPI asserts DC-API reports the zone ACTIVE under exactly the configured name.
// A trailing dot or a case change would also show up as a non-empty plan.
func checkZoneAPI(wantName string) APICheckFunc {
	return func(ctx context.Context, c *client.DCAPIClient, p []string) error {
		z, err := c.GetPrivateDnsZone(ctx, p[0], p[1], p[2], p[3])
		if err != nil {
			return err
		}
		if z == nil {
			return fmt.Errorf("zone not found")
		}
		if z.Status != "ACTIVE" || z.Name != wantName {
			return fmt.Errorf("status/name = %s/%s, want ACTIVE/%s", z.Status, z.Name, wantName)
		}
		return nil
	}
}
