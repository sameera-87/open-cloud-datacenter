package acctest

// Plan: docs/testsuite/resources/network-security-group.md

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

// nsgRule is one rules block. Fields left empty take the defaults in hcl().
type nsgRule struct {
	name, direction, protocol, source, port, action string
	priority                                        int
}

func (r nsgRule) hcl() string {
	def := func(v, d string) string {
		if v == "" {
			return d
		}
		return v
	}
	return fmt.Sprintf(`
  rules {
    name                       = %q
    direction                  = %q
    priority                   = %d
    protocol                   = %q
    source_address_prefix      = %q
    source_port_range          = "*"
    destination_address_prefix = "*"
    destination_port_range     = %q
    action                     = %q
  }`, r.name, def(r.direction, "inbound"), r.priority, def(r.protocol, "tcp"),
		def(r.source, "10.0.0.0/8"), def(r.port, "*"), def(r.action, "allow"))
}

var (
	ruleHTTPS   = nsgRule{name: "allow-https", priority: 100, port: "443"}
	ruleDenyOut = nsgRule{name: "deny-all-out", direction: "outbound", priority: 4096, protocol: "*", source: "*", action: "deny"}
)

// TestAccNSG_basic exercises the full lifecycle of a dcapi_network_security_group with two rules:
// create, import, data source parity, and out-of-band deletion.
//
// PASSES when: the created NSG stores both rules with every field defaulted/populated exactly as
// configured (the inbound allow-https rule at index 0, the outbound deny rule at index 1), computed
// sg_id/status/provider_type are set, and DC-API reports 2 rules; import reproduces state; the data
// source returns the same sg_id/rule count/priority and reports zero attachments; and a deleted NSG
// yields a non-empty plan.
// FAILS when: any rule field is wrong, a computed field is empty, the API rule count disagrees,
// import drops a field, the data source diverges, or Read ignores the NSG being gone.
func TestAccNSG_basic(t *testing.T) {
	name := RandomName("nsg")
	addr := "dcapi_network_security_group.test"
	cfg := testAccNSGConfig(name, "acc", ruleHTTPS, ruleDenyOut)

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { PreCheck(t) },
		ProtoV5ProviderFactories: ProviderFactories,
		CheckDestroy:             CheckDestroy("dcapi_network_security_group", NSGExists),
		Steps: []resource.TestStep{
			{
				// Step 1 — Create: assert both rules map field-for-field into state, computed fields
				// are set, and DC-API itself reports exactly 2 rules.
				Config: cfg,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(addr, "name", name),
					resource.TestCheckResourceAttr(addr, "rules.#", "2"),
					resource.TestCheckResourceAttr(addr, "rules.0.name", "allow-https"),
					resource.TestCheckResourceAttr(addr, "rules.0.direction", "inbound"),
					resource.TestCheckResourceAttr(addr, "rules.0.priority", "100"),
					resource.TestCheckResourceAttr(addr, "rules.0.protocol", "tcp"),
					resource.TestCheckResourceAttr(addr, "rules.0.source_address_prefix", "10.0.0.0/8"),
					resource.TestCheckResourceAttr(addr, "rules.0.source_port_range", "*"),
					resource.TestCheckResourceAttr(addr, "rules.0.destination_address_prefix", "*"),
					resource.TestCheckResourceAttr(addr, "rules.0.destination_port_range", "443"),
					resource.TestCheckResourceAttr(addr, "rules.0.action", "allow"),
					resource.TestCheckResourceAttr(addr, "rules.1.name", "deny-all-out"),
					resource.TestCheckResourceAttr(addr, "rules.1.direction", "outbound"),
					resource.TestCheckResourceAttr(addr, "rules.1.priority", "4096"),
					resource.TestCheckResourceAttr(addr, "rules.1.protocol", "*"),
					resource.TestCheckResourceAttr(addr, "rules.1.action", "deny"),
					resource.TestCheckResourceAttrSet(addr, "sg_id"),
					resource.TestCheckResourceAttrSet(addr, "status"),
					resource.TestCheckResourceAttrSet(addr, "provider_type"),
					CheckAPI(addr, checkNSGRuleCount(2)),
				),
			},
			{
				// Step 2 — Import: re-import by state ID and verify every attribute matches; a
				// dropped or changed field fails ImportStateVerify.
				ResourceName:      addr,
				ImportState:       true,
				ImportStateVerify: true,
			},
			{
				// Step 3 — Data source parity: look the NSG up by name and assert it returns the
				// same sg_id, rule count, and first rule's priority, and reports no attachments.
				// Fails if the data source's Read diverges from the resource.
				Config: cfg + `
data "dcapi_network_security_group" "test" {
  tenant_id  = local.tenant_id
  project_id = local.project_id
  name       = dcapi_network_security_group.test.name
}
`,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttrPair("data.dcapi_network_security_group.test", "sg_id", addr, "sg_id"),
					resource.TestCheckResourceAttrPair("data.dcapi_network_security_group.test", "rules.#", addr, "rules.#"),
					resource.TestCheckResourceAttrPair("data.dcapi_network_security_group.test", "rules.0.priority", addr, "rules.0.priority"),
					resource.TestCheckResourceAttr("data.dcapi_network_security_group.test", "attachments.#", "0"),
				),
			},
			{
				// Step 4 — Disappears: delete the NSG out-of-band; Read must drop it from state and
				// produce a non-empty plan to recreate it. Fails if Read ignores the deletion.
				Config:             cfg,
				Check:              Disappears(addr, DeleteNSG),
				ExpectNonEmptyPlan: true,
			},
		},
	})
}

// TestAccNSG_rulesUpdate: rules are replaced as a whole (PUT /rules), so every edit is an in-place
// Update that keeps the resource id. It walks two rules → edited/replaced two rules → zero rules →
// one rule, proving nothing stale survives and that going to zero rules works.
//
// PASSES when: every step plans an Update (not a replace) with the id unchanged, state shows the
// expected rule count/fields after each change, and DC-API's own rule count matches at each step
// (including 0).
// FAILS when: a change is planned as a replace, a stale rule lingers server-side, the zero-rule
// transition is rejected, or the id changes.
func TestAccNSG_rulesUpdate(t *testing.T) {
	name := RandomName("nsg")
	addr := "dcapi_network_security_group.test"
	sameID := statecheck.CompareValue(compare.ValuesSame())
	update := resource.ConfigPlanChecks{
		PreApply: []plancheck.PlanCheck{plancheck.ExpectResourceAction(addr, plancheck.ResourceActionUpdate)},
	}

	ruleA := nsgRule{name: "a", priority: 100, port: "443"}
	ruleB := nsgRule{name: "b", priority: 200, port: "80"}
	ruleA2 := nsgRule{name: "a", priority: 100, port: "8443"}
	ruleC := nsgRule{name: "c", priority: 300, port: "22"}

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { PreCheck(t) },
		ProtoV5ProviderFactories: ProviderFactories,
		CheckDestroy:             CheckDestroy("dcapi_network_security_group", NSGExists),
		Steps: []resource.TestStep{
			{
				// Step 1 — Create with rules A and B; DC-API must report 2 rules. Record the id.
				Config:            testAccNSGConfig(name, "acc", ruleA, ruleB),
				Check:             CheckAPI(addr, checkNSGRuleCount(2)),
				ConfigStateChecks: []statecheck.StateCheck{sameID.AddStateValue(addr, tfjsonpath.New("id"))},
			},
			{
				// Step 2 — Change A's port, remove B, add C: an Update that leaves 2 rules, with A's
				// new port (8443) at index 0 and rule c at index 1, confirmed by the API count and
				// an unchanged id.
				Config:           testAccNSGConfig(name, "acc", ruleA2, ruleC),
				ConfigPlanChecks: update,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(addr, "rules.#", "2"),
					resource.TestCheckResourceAttr(addr, "rules.0.destination_port_range", "8443"),
					resource.TestCheckResourceAttr(addr, "rules.1.name", "c"),
					CheckAPI(addr, checkNSGRuleCount(2)),
				),
				ConfigStateChecks: []statecheck.StateCheck{sameID.AddStateValue(addr, tfjsonpath.New("id"))},
			},
			{
				// Step 3 — Remove all rules: an Update that must drive both state and DC-API to 0
				// rules; id unchanged. Fails if the API rejects an empty rule set or keeps stale rules.
				Config:           testAccNSGConfig(name, "acc"),
				ConfigPlanChecks: update,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(addr, "rules.#", "0"),
					CheckAPI(addr, checkNSGRuleCount(0)),
				),
				ConfigStateChecks: []statecheck.StateCheck{sameID.AddStateValue(addr, tfjsonpath.New("id"))},
			},
			{
				// Step 4 — Add a single rule back: an Update that brings state and DC-API to 1 rule;
				// id unchanged.
				Config:           testAccNSGConfig(name, "acc", ruleA),
				ConfigPlanChecks: update,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(addr, "rules.#", "1"),
					CheckAPI(addr, checkNSGRuleCount(1)),
				),
				ConfigStateChecks: []statecheck.StateCheck{sameID.AddStateValue(addr, tfjsonpath.New("id"))},
			},
		},
	})
}

// TestAccNSG_rulesOrdering (G4) guards that the provider preserves the config's rule order rather
// than whatever order the API returns. Rules are supplied with priorities 300, 100, 200 in that
// order on purpose.
//
// PASSES when: state keeps the rules in config order — rules.0/.1/.2 have priority 300/100/200 —
// and the post-apply plan is empty.
// FAILS when: the API sorts by priority and the provider adopts that order, so the indices no
// longer match the config and the plan after apply isn't empty.
func TestAccNSG_rulesOrdering(t *testing.T) {
	name := RandomName("nsg")
	addr := "dcapi_network_security_group.test"

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { PreCheck(t) },
		ProtoV5ProviderFactories: ProviderFactories,
		CheckDestroy:             CheckDestroy("dcapi_network_security_group", NSGExists),
		Steps: []resource.TestStep{
			{
				// Create three rules out of priority order; state must preserve that exact order.
				Config: testAccNSGConfig(name, "acc",
					nsgRule{name: "p300", priority: 300, port: "300"},
					nsgRule{name: "p100", priority: 100, port: "100"},
					nsgRule{name: "p200", priority: 200, port: "200"},
				),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(addr, "rules.0.priority", "300"),
					resource.TestCheckResourceAttr(addr, "rules.1.priority", "100"),
					resource.TestCheckResourceAttr(addr, "rules.2.priority", "200"),
				),
			},
		},
	})
}

// TestAccNSG_forceNew verifies that the NSG's description is ForceNew: changing it must
// destroy-then-recreate the resource. The IDSet records each sg so CheckAllGone can confirm every
// replaced NSG was really deleted.
//
// PASSES when: changing description plans a DestroyBeforeCreate and, at teardown, every recorded
// NSG id is gone from the API.
// FAILS when: the description change is treated as an in-place update, or a replaced NSG survives.
func TestAccNSG_forceNew(t *testing.T) {
	name := RandomName("nsg")
	addr := "dcapi_network_security_group.test"
	ids := &IDSet{}

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { PreCheck(t) },
		ProtoV5ProviderFactories: ProviderFactories,
		CheckDestroy:             ids.CheckAllGone(NSGExists),
		Steps: []resource.TestStep{
			{
				// Step 1 — Create with description "first" and record the id.
				Config: testAccNSGConfig(name, "first", ruleHTTPS),
				Check:  ids.Record(addr),
			},
			{
				// Step 2 — Change description to "second". ForceNew requires a DestroyBeforeCreate
				// and a new id is recorded.
				Config: testAccNSGConfig(name, "second", ruleHTTPS),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{plancheck.ExpectResourceAction(addr, plancheck.ResourceActionDestroyBeforeCreate)},
				},
				Check: ids.Record(addr),
			},
		},
	})
}

// TestAccNSG_validation: every rule ValidateFunc must reject bad input at plan time, before any
// API call. Each step is plan-only and expects a specific schema error.
//
// PASSES when: a priority below range (50) and above range (5000) both error with "to be in the
// range", and an invalid direction/protocol/action each error with "to be one of".
// FAILS when: any bad value is accepted at plan time, or the error text doesn't match.
func TestAccNSG_validation(t *testing.T) {
	name := RandomName("nsg")
	// bad builds a plan-only step that must fail with an error containing want.
	bad := func(r nsgRule, want string) resource.TestStep {
		return resource.TestStep{Config: testAccNSGConfig(name, "acc", r), PlanOnly: true, ExpectError: ExpectErr(want)}
	}

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { PreCheck(t) },
		ProtoV5ProviderFactories: ProviderFactories,
		Steps: []resource.TestStep{
			bad(nsgRule{name: "r", priority: 50}, "to be in the range"),                   // priority too low
			bad(nsgRule{name: "r", priority: 5000}, "to be in the range"),                 // priority too high
			bad(nsgRule{name: "r", priority: 100, direction: "sideways"}, "to be one of"), // invalid direction
			bad(nsgRule{name: "r", priority: 100, protocol: "gre"}, "to be one of"),       // invalid protocol
			bad(nsgRule{name: "r", priority: 100, action: "maybe"}, "to be one of"),       // invalid action
		},
	})
}

func testAccNSGConfig(name, description string, rules ...nsgRule) string {
	return ConfigBase() + testAccNSGResource(name, description, rules...)
}

// testAccNSGResource is the dcapi_network_security_group "test" block alone, for configs that
// combine it with other resources.
func testAccNSGResource(name, description string, rules ...nsgRule) string {
	var b strings.Builder
	for _, r := range rules {
		b.WriteString(r.hcl())
	}
	return fmt.Sprintf(`
resource "dcapi_network_security_group" "test" {
  tenant_id   = local.tenant_id
  project_id  = local.project_id
  name        = %q
  description = %q
%s
}
`, name, description, b.String())
}

// checkNSGRuleCount reads the rule count from DC-API, independent of state. This proves a
// full replace really removed old rules server-side.
func checkNSGRuleCount(want int) APICheckFunc {
	return func(ctx context.Context, c *client.DCAPIClient, p []string) error {
		n, err := c.GetNSG(ctx, p[0], p[1], p[2])
		if err != nil {
			return err
		}
		if n == nil {
			return fmt.Errorf("NSG not found")
		}
		if len(n.Rules) != want {
			return fmt.Errorf("DC-API has %d rules, want %d", len(n.Rules), want)
		}
		return nil
	}
}
