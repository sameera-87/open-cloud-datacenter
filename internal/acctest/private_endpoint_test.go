package acctest

// Plan: docs/testsuite/resources/private-endpoint.md

import (
	"context"
	"fmt"
	"regexp"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/plancheck"

	"terraform-provider-dcapi/internal/client"
)

// TestAccPrivateEndpoint_basic exercises the full happy-path lifecycle of a dcapi_private_endpoint
// over its own VNet, subnet and key vault: create, import, and out-of-band deletion. Because the
// provider has no waiter (G11), it asserts the endpoint is already usable the instant apply
// returns.
//
// PASSES when: right after apply status is ACTIVE, ip_address is a VIP taken from the endpoint's
// subnet (matches ^10\.207\.1\.\d+$), hostname and target_type are populated, target_id equals the
// parent key vault's UUID (3rd part of its id), DC-API reports the same ip_address, import
// reproduces the state exactly, and an endpoint deleted behind Terraform's back yields a non-empty
// plan.
// FAILS when: apply returns while the endpoint is still PENDING (status != ACTIVE), the VIP is
// outside the subnet, hostname/target_type/target_id are wrong or empty, the API disagrees, import
// drops/changes a field, or Read doesn't notice the endpoint is gone.
func TestAccPrivateEndpoint_basic(t *testing.T) {
	name := RandomName("pe")
	addr := "dcapi_private_endpoint.test"
	cfg := testAccPrivateEndpointConfig(name, name)

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { PreCheck(t) },
		ProtoV5ProviderFactories: ProviderFactories,
		// CheckDestroy fails unless, after teardown, the endpoint and every dependency (vault,
		// subnet, VNet) return a real 404 — also catching an endpoint that blocks its parents' delete.
		CheckDestroy: testAccPrivateEndpointCheckDestroy(),
		Steps: []resource.TestStep{
			{
				// Step 1 — Create: apply the config and assert the Create/Read round-trip plus the
				// no-waiter guarantee. Passes only if state mirrors the config and the API confirms
				// the VIP, all right after apply.
				Config: cfg,
				Check: resource.ComposeAggregateTestCheckFunc(
					// G11: the provider has no waiter, so these must already hold right after apply.
					resource.TestCheckResourceAttr(addr, "status", "ACTIVE"),
					resource.TestMatchResourceAttr(addr, "ip_address", regexp.MustCompile(`^10\.207\.1\.\d+$`)),
					resource.TestCheckResourceAttrSet(addr, "hostname"),
					resource.TestCheckResourceAttrSet(addr, "target_type"),
					// target_id must equal the 3rd "/"-separated part of the vault's id (its UUID).
					CheckIDPart(addr, "target_id", "dcapi_key_vault.parent", 2),
					// Cross-check the provider's state against what DC-API itself reports.
					CheckAPI(addr, checkEndpointIP()),
				),
			},
			{
				// Step 2 — Import: re-import the endpoint by its state ID
				// (tenant/project/kv/endpoint_id) and verify every attribute. ImportStateVerify
				// fails if any field Read sets on create is missing or different after import.
				ResourceName:      addr,
				ImportState:       true,
				ImportStateVerify: true,
			},
			{
				// Step 3 — Disappears: delete the endpoint out-of-band, then re-plan. Read must drop
				// the 404'd endpoint from state and produce a non-empty plan to recreate it. Passes
				// only when ExpectNonEmptyPlan holds; fails if Read ignores the 404.
				Config:             cfg,
				Check:              Disappears(addr, DeletePrivateEndpoint),
				ExpectNonEmptyPlan: true,
			},
		},
	})
}

// TestAccPrivateEndpoint_forceNew verifies that name is ForceNew: renaming the endpoint must
// destroy-then-recreate it, releasing the old VIP in the process. The IDSet records every
// endpoint_id so CheckAllGone can confirm each replaced endpoint — and thus its VIP — really vanished.
//
// PASSES when: the rename plans a DestroyBeforeCreate and, at teardown, every recorded endpoint_id
// (and all dependencies) returns a 404.
// FAILS when: the rename is treated as an in-place update, or a replaced endpoint is left alive
// (its VIP never released).
func TestAccPrivateEndpoint_forceNew(t *testing.T) {
	name := RandomName("pe")
	addr := "dcapi_private_endpoint.test"
	ids := &IDSet{}

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { PreCheck(t) },
		ProtoV5ProviderFactories: ProviderFactories,
		// Every recorded endpoint_id must be gone, and the shared dependencies cleaned up, by the end.
		CheckDestroy: resource.ComposeTestCheckFunc(ids.CheckAllGone(PrivateEndpointExists), testAccPrivateEndpointCheckDestroy()),
		Steps: []resource.TestStep{
			{
				// Step 1 — Create the baseline endpoint and record its first endpoint_id.
				Config: testAccPrivateEndpointConfig(name, name),
				Check:  ids.Record(addr),
			},
			{
				// Step 2 — Rename the endpoint. ForceNew means this must replace it: the plan check
				// requires DestroyBeforeCreate. Records the new endpoint_id for the final sweep.
				Config: testAccPrivateEndpointConfig(name, name+"-2"),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{plancheck.ExpectResourceAction(addr, plancheck.ResourceActionDestroyBeforeCreate)},
				},
				Check: ids.Record(addr),
			},
		},
	})
}

// testAccPrivateEndpointConfig declares the test's own VNet, subnet and vault, and an endpoint
// named epName to the vault.
func testAccPrivateEndpointConfig(name, epName string) string {
	return ConfigBase() + ConfigNetwork(name, CIDRPrivateEndpointVNet, CIDRPrivateEndpointSubnet) +
		ConfigKeyVault(name) + fmt.Sprintf(`
resource "dcapi_private_endpoint" "test" {
  tenant_id  = local.tenant_id
  project_id = local.project_id
  kv_id      = local.kv_uuid
  name       = %q
  vnet_id    = dcapi_vnet.parent.vnet_uuid
  subnet_id  = dcapi_subnet.parent.subnet_uuid
}
`, epName)
}

func testAccPrivateEndpointCheckDestroy() resource.TestCheckFunc {
	return resource.ComposeTestCheckFunc(
		CheckDestroy("dcapi_private_endpoint", PrivateEndpointExists),
		CheckDestroy("dcapi_key_vault", KeyVaultExists),
		CheckDestroy("dcapi_subnet", SubnetExists),
		CheckDestroy("dcapi_vnet", VNetExists),
	)
}

func checkEndpointIP() APICheckFunc {
	return func(ctx context.Context, c *client.DCAPIClient, p []string) error {
		ep, err := c.GetPrivateEndpoint(ctx, p[0], p[1], p[2], p[3])
		if err != nil {
			return err
		}
		if ep == nil {
			return fmt.Errorf("endpoint not found")
		}
		if !regexp.MustCompile(`^10\.207\.1\.\d+$`).MatchString(ep.IPAddress) {
			return fmt.Errorf("ip_address = %q, want an address in %s", ep.IPAddress, CIDRPrivateEndpointSubnet)
		}
		return nil
	}
}
