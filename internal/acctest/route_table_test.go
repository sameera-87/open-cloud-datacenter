package acctest

// Plan: docs/testsuite/resources/route-table.md

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/compare"
	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/plancheck"
	"github.com/hashicorp/terraform-plugin-testing/statecheck"
	"github.com/hashicorp/terraform-plugin-testing/tfjsonpath"

	"terraform-provider-dcapi/internal/client"
)

type route struct {
	name, cidr, hopType, hopIP string
}

func (r route) hcl() string {
	ip := ""
	if r.hopIP != "" {
		ip = fmt.Sprintf("\n    next_hop_ip      = %q", r.hopIP)
	}
	return fmt.Sprintf(`
  routes {
    name             = %q
    destination_cidr = %q
    next_hop_type    = %q%s
  }`, r.name, r.cidr, r.hopType, ip)
}

var (
	routeDefault  = route{"default", "0.0.0.0/0", "internet", ""}
	routeFirewall = route{"to-peer-via-fw", "10.220.0.0/16", "virtual_appliance", "10.202.0.10"}
)

// TestAccRouteTable_basic exercises the full happy-path lifecycle of a dcapi_route_table carrying
// two routes (an internet default route and a virtual_appliance route): create, import, data source
// read-back, and out-of-band deletion.
//
// PASSES when: both routes map round-trip at their given indices — including the empty next_hop_ip
// on the non-appliance route (the flatten function writes "", so no perpetual diff) and the
// next_hop_ip on the appliance route — route_table_id and status are populated, DC-API reports 2
// routes, the data source returns the same route_table_id and route count, and a table deleted
// behind Terraform's back is detected as a non-empty plan.
// FAILS when: a route field is mis-mapped, next_hop_ip comes back null/missing instead of "" for
// the internet route, the route count disagrees with the API, the data source diverges, import
// drops/changes a field, or Read doesn't notice the table is gone.
func TestAccRouteTable_basic(t *testing.T) {
	name := RandomName("rt")
	addr := "dcapi_route_table.test"
	cfg := testAccRouteTableConfig(name, routeDefault, routeFirewall)

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { PreCheck(t) },
		ProtoV5ProviderFactories: ProviderFactories,
		// CheckDestroy fails unless, after teardown, both the route table and its parent VNet return
		// a real 404.
		CheckDestroy: resource.ComposeTestCheckFunc(
			CheckDestroy("dcapi_route_table", RouteTableExists),
			CheckDestroy("dcapi_vnet", VNetExists),
		),
		Steps: []resource.TestStep{
			{
				// Step 1 — Create: apply the config and assert the Create/Read round-trip of every
				// route field. Passes only if state mirrors the config and DC-API reports 2 routes.
				Config: cfg,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(addr, "routes.#", "2"),
					resource.TestCheckResourceAttr(addr, "routes.0.name", "default"),
					resource.TestCheckResourceAttr(addr, "routes.0.destination_cidr", "0.0.0.0/0"),
					resource.TestCheckResourceAttr(addr, "routes.0.next_hop_type", "internet"),
					// Non-appliance routes round-trip an empty next_hop_ip without a diff.
					resource.TestCheckResourceAttr(addr, "routes.0.next_hop_ip", ""),
					resource.TestCheckResourceAttr(addr, "routes.1.next_hop_type", "virtual_appliance"),
					resource.TestCheckResourceAttr(addr, "routes.1.next_hop_ip", "10.202.0.10"),
					resource.TestCheckResourceAttrSet(addr, "route_table_id"),
					resource.TestCheckResourceAttrSet(addr, "status"),
					// Cross-check the route count against what DC-API itself reports.
					CheckAPI(addr, checkRouteCount(2)),
				),
			},
			{
				// Step 2 — Import: re-import by the state ID (tenant/project/vnet/route_table_id) and
				// verify every attribute. ImportStateVerify fails if any field Read sets on create is
				// missing or different after import.
				ResourceName:      addr,
				ImportState:       true,
				ImportStateVerify: true,
			},
			{
				// Step 3 — Data source parity: add a data "dcapi_route_table" that looks the table up
				// by vnet_id + name and assert it returns the same route_table_id and route count as
				// the managed resource. Fails if the data source's Read diverges.
				Config: cfg + `
data "dcapi_route_table" "test" {
  tenant_id  = local.tenant_id
  project_id = local.project_id
  vnet_id    = dcapi_vnet.parent.vnet_uuid
  name       = dcapi_route_table.test.name
}
`,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttrPair("data.dcapi_route_table.test", "route_table_id", addr, "route_table_id"),
					resource.TestCheckResourceAttrPair("data.dcapi_route_table.test", "routes.#", addr, "routes.#"),
				),
			},
			{
				// Step 4 — Disappears: delete the table out-of-band, then re-plan. Read must drop the
				// 404'd table from state and produce a non-empty plan to recreate it. Passes only
				// when ExpectNonEmptyPlan holds; fails if Read ignores the 404.
				Config:             cfg,
				Check:              Disappears(addr, DeleteRouteTable),
				ExpectNonEmptyPlan: true,
			},
		},
	})
}

// TestAccRouteTable_routesUpdate verifies the in-place routes update (PUT full-replace, like NSG
// rules): the routes block can be changed and emptied without replacing the table. sameID asserts
// the resource id is unchanged across all three steps, proving these are updates and not
// replacements.
//
// PASSES when: every route change plans a ResourceActionUpdate (not a replacement), the id stays
// the same each step, the final route set (count, CIDRs, next_hop_type) matches the config, and
// DC-API's route count agrees after each step — including going to zero routes.
// FAILS when: a change is planned as a replacement, the id changes, the full-replace leaves stale
// routes behind, or the API's route count disagrees with the config.
func TestAccRouteTable_routesUpdate(t *testing.T) {
	name := RandomName("rt")
	addr := "dcapi_route_table.test"
	sameID := statecheck.CompareValue(compare.ValuesSame())
	update := resource.ConfigPlanChecks{
		PreApply: []plancheck.PlanCheck{plancheck.ExpectResourceAction(addr, plancheck.ResourceActionUpdate)},
	}

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { PreCheck(t) },
		ProtoV5ProviderFactories: ProviderFactories,
		CheckDestroy:             CheckDestroy("dcapi_route_table", RouteTableExists),
		Steps: []resource.TestStep{
			{
				// Step 1 — Create the baseline table with two routes and capture its id for the
				// sameID comparison.
				Config:            testAccRouteTableConfig(name, routeDefault, routeFirewall),
				ConfigStateChecks: []statecheck.StateCheck{sameID.AddStateValue(addr, tfjsonpath.New("id"))},
			},
			{
				// Step 2 — Change one route's CIDR, remove the other, add a blackhole ("none") route.
				// The plan check requires an in-place Update and sameID requires the id to be
				// unchanged. Passes only if the new route set round-trips and the API shows 2 routes.
				Config: testAccRouteTableConfig(name,
					route{"default", "10.0.0.0/8", "internet", ""},
					route{"blackhole", "10.99.0.0/16", "none", ""},
				),
				ConfigPlanChecks: update,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(addr, "routes.#", "2"),
					resource.TestCheckResourceAttr(addr, "routes.0.destination_cidr", "10.0.0.0/8"),
					resource.TestCheckResourceAttr(addr, "routes.1.next_hop_type", "none"),
					CheckAPI(addr, checkRouteCount(2)),
				),
				ConfigStateChecks: []statecheck.StateCheck{sameID.AddStateValue(addr, tfjsonpath.New("id"))},
			},
			{
				// Step 3 — Remove all routes (routes = []). Still an in-place Update with the same id.
				// Passes only if state shows zero routes and the API confirms zero — proving the
				// full-replace leaves nothing behind.
				Config:           testAccRouteTableConfig(name),
				ConfigPlanChecks: update,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(addr, "routes.#", "0"),
					CheckAPI(addr, checkRouteCount(0)),
				),
				ConfigStateChecks: []statecheck.StateCheck{sameID.AddStateValue(addr, tfjsonpath.New("id"))},
			},
		},
	})
}

// TestAccRouteTable_routesOrdering guards the G4 ordering concern: routes are supplied with
// destinations in neither numeric nor lexical order, so it catches the API silently reordering the
// routes list (which would leave a perpetual diff).
//
// PASSES when: each route stays at the exact index it was configured at (index 0 = 10.230.0.0/16,
// index 1 = 0.0.0.0/0, index 2 = 10.220.0.0/16).
// FAILS when: the API returns the routes reordered, so the destination_cidr at any index no longer
// matches the config.
func TestAccRouteTable_routesOrdering(t *testing.T) {
	name := RandomName("rt")
	addr := "dcapi_route_table.test"

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { PreCheck(t) },
		ProtoV5ProviderFactories: ProviderFactories,
		CheckDestroy:             CheckDestroy("dcapi_route_table", RouteTableExists),
		Steps: []resource.TestStep{
			{
				// Single step — Create with three deliberately unordered routes and assert each
				// destination_cidr lands at its configured index.
				Config: testAccRouteTableConfig(name,
					route{"r1", "10.230.0.0/16", "none", ""},
					route{"r2", "0.0.0.0/0", "internet", ""},
					route{"r3", "10.220.0.0/16", "none", ""},
				),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(addr, "routes.0.destination_cidr", "10.230.0.0/16"),
					resource.TestCheckResourceAttr(addr, "routes.1.destination_cidr", "0.0.0.0/0"),
					resource.TestCheckResourceAttr(addr, "routes.2.destination_cidr", "10.220.0.0/16"),
				),
			},
		},
	})
}

// TestAccRouteTable_forceNew verifies that name is ForceNew: renaming the table must
// destroy-then-recreate it with a new route_table_id. The IDSet records every id so CheckAllGone
// can confirm each replaced table really vanished.
//
// PASSES when: the rename plans a DestroyBeforeCreate and, at teardown, every recorded id returns a 404.
// FAILS when: the rename is treated as an in-place update, or a replaced table is left alive on the API.
func TestAccRouteTable_forceNew(t *testing.T) {
	name := RandomName("rt")
	addr := "dcapi_route_table.test"
	ids := &IDSet{}

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { PreCheck(t) },
		ProtoV5ProviderFactories: ProviderFactories,
		// Every id recorded across the steps must be gone (404) by the end.
		CheckDestroy: ids.CheckAllGone(RouteTableExists),
		Steps: []resource.TestStep{
			{
				// Step 1 — Create the baseline table and record its first id.
				Config: testAccRouteTableConfigNamed(name, name, routeDefault),
				Check:  ids.Record(addr),
			},
			{
				// Step 2 — Rename the table (VNet prefix unchanged, so the VNet is not replaced).
				// ForceNew means this must replace the table: the plan check requires
				// DestroyBeforeCreate. Records the new id for the final CheckAllGone sweep.
				Config: testAccRouteTableConfigNamed(name, name+"-2", routeDefault),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{plancheck.ExpectResourceAction(addr, plancheck.ResourceActionDestroyBeforeCreate)},
				},
				Check: ids.Record(addr),
			},
		},
	})
}

// TestAccRouteTable_validation checks that CustomizeDiff and the field ValidateFuncs reject bad
// routes at plan time, before any API call. Every step is PlanOnly and expects a specific error.
//
// PASSES when: each plan fails with the matching error — a virtual_appliance route without
// next_hop_ip, an internet route that sets next_hop_ip, an invalid destination_cidr ("10.0.0.0/33"),
// and an unknown next_hop_type ("gateway").
// FAILS when: any of these configs plans cleanly, or fails with a different error than expected.
func TestAccRouteTable_validation(t *testing.T) {
	cfg := func(r route) string {
		return ConfigBase() + fmt.Sprintf(`
resource "dcapi_route_table" "test" {
  tenant_id  = local.tenant_id
  project_id = local.project_id
  vnet_id    = "00000000-0000-0000-0000-000000000000"
  name       = "acc-validation"
%s
}
`, r.hcl())
	}

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { PreCheck(t) },
		ProtoV5ProviderFactories: ProviderFactories,
		Steps: []resource.TestStep{
			// Step 1 — virtual_appliance route with no next_hop_ip: CustomizeDiff must reject it.
			{Config: cfg(route{"r", "10.0.0.0/8", "virtual_appliance", ""}), PlanOnly: true, ExpectError: ExpectErr("next_hop_ip is required")},
			// Step 2 — internet route that sets next_hop_ip: CustomizeDiff must forbid it.
			{Config: cfg(route{"r", "0.0.0.0/0", "internet", "10.0.0.1"}), PlanOnly: true, ExpectError: ExpectErr("next_hop_ip must not be set")},
			// Step 3 — invalid CIDR: the destination_cidr IsCIDR ValidateFunc must reject /33.
			{Config: cfg(route{"r", "10.0.0.0/33", "internet", ""}), PlanOnly: true, ExpectError: ExpectErr("destination_cidr")},
			// Step 4 — unknown next_hop_type: the enum ValidateFunc must reject "gateway".
			{Config: cfg(route{"r", "10.0.0.0/8", "gateway", ""}), PlanOnly: true, ExpectError: ExpectErr("to be one of")},
		},
	})
}

func testAccRouteTableConfig(name string, routes ...route) string {
	return testAccRouteTableConfigNamed(name, name, routes...)
}

// testAccRouteTableConfigNamed separates the VNet name prefix from the route table name,
// so the ForceNew test can rename the table without replacing its VNet.
func testAccRouteTableConfigNamed(prefix, rtName string, routes ...route) string {
	var b strings.Builder
	for _, r := range routes {
		b.WriteString(r.hcl())
	}
	return ConfigBase() + ConfigNetwork(prefix, CIDRRouteTableVNet, "") + fmt.Sprintf(`
resource "dcapi_route_table" "test" {
  tenant_id  = local.tenant_id
  project_id = local.project_id
  vnet_id     = dcapi_vnet.parent.vnet_uuid
  name        = %q
  description = "acc"
%s
}
`, rtName, b.String())
}

func checkRouteCount(want int) APICheckFunc {
	return func(ctx context.Context, c *client.DCAPIClient, p []string) error {
		rt, err := c.GetRouteTable(ctx, p[0], p[1], p[2], p[3])
		if err != nil {
			return err
		}
		if rt == nil {
			return fmt.Errorf("route table not found")
		}
		if len(rt.Routes) != want {
			return fmt.Errorf("DC-API has %d routes, want %d", len(rt.Routes), want)
		}
		return nil
	}
}
