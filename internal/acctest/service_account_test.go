package acctest

// Plan: docs/testsuite/resources/service-account.md
//
// These tests need an OWNER token in DCAPI_TOKEN: creating and deleting service accounts
// requires the owner role. In Argo, dcapi-acc-service-account mounts the dcapi-acc-owner Secret.
// Tests only create viewer and member SAs, never owner.

import (
	"fmt"
	"regexp"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/compare"
	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/plancheck"
	"github.com/hashicorp/terraform-plugin-testing/statecheck"
	"github.com/hashicorp/terraform-plugin-testing/tfjsonpath"
)

var tokenPattern = regexp.MustCompile(`^dcapi_sa_[A-Za-z0-9]+_.+`)

// TestAccServiceAccount_basic exercises the happy-path lifecycle of a dcapi_service_account with
// the viewer role: create and mint a token, import (ignoring the unreadable secret fields), and
// out-of-band deletion.
//
// PASSES when: the created SA exposes a token matching the dcapi_sa_ pattern, role is "viewer",
// sa_id is populated, tenant_id echoes the configured tenant, import reproduces every field except
// the ones it is told to ignore, and an SA deleted behind Terraform's back forces a non-empty plan.
// FAILS when: the token is malformed or empty, any attribute mapping is wrong, import diverges on a
// verified field, or Read doesn't notice the SA was deleted out-of-band.
func TestAccServiceAccount_basic(t *testing.T) {
	name := RandomName("sa")
	addr := "dcapi_service_account.test"
	cfg := testAccServiceAccountConfig(name, "viewer")

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { PreCheck(t) },
		ProtoV5ProviderFactories: ProviderFactories,
		// CheckDestroy fails unless, after teardown, the SA returns a 404 from the API.
		CheckDestroy: CheckDestroy("dcapi_service_account", ServiceAccountExists),
		Steps: []resource.TestStep{
			{
				// Step 1 — Create: mint the SA and assert the Create/Read round-trip.
				// Passes only if the token matches the pattern and role/sa_id/tenant_id are set.
				Config: cfg,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestMatchResourceAttr(addr, "token", tokenPattern),
					resource.TestCheckResourceAttr(addr, "role", "viewer"),
					resource.TestCheckResourceAttrSet(addr, "sa_id"),
					resource.TestCheckResourceAttr(addr, "tenant_id", TenantID()), // G5
				),
			},
			{
				// Step 2 — Import: re-import the SA and verify its attributes. token and last_used
				// are ignored because the token can't be recovered, and last_used changes whenever
				// the token is used; ImportStateVerify fails if any other field differs.
				ResourceName:            addr,
				ImportState:             true,
				ImportStateVerify:       true,
				ImportStateVerifyIgnore: []string{"token", "last_used"},
			},
			{
				// Step 3 — Disappears: delete the SA out-of-band, then re-plan. Read must drop it
				// from state and produce a non-empty plan to recreate it; fails if the 404 is
				// ignored and the plan comes back empty.
				Config:             cfg,
				Check:              Disappears(addr, DeleteServiceAccount),
				ExpectNonEmptyPlan: true,
			},
		},
	})
}

// TestAccServiceAccount_tokenWorks proves the minted token actually authenticates: a second,
// aliased provider configured with the new token reads the project, and DC-API then records
// last_used on the SA.
//
// PASSES when: the token saved at create is still valid and lets the aliased provider read the
// project (project_id matches), and a later refresh shows last_used populated while the token
// itself is unchanged.
// FAILS when: the token can't authenticate (the data source read errors), the project read
// disagrees, last_used is never set, or the token value changes across refresh.
//
// If the project-scoped viewer role turns out not to be allowed to read dcapi_project (403),
// switch step 2 to create a VNet in CIDRServiceAccountVNet with the default provider and read
// it back through dcapi.new_sa with data "dcapi_vnet" (docs/testsuite/resources/service-account.md).
func TestAccServiceAccount_tokenWorks(t *testing.T) {
	name := RandomName("sa")
	addr := "dcapi_service_account.test"
	token := &AttrSnapshot{}

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { PreCheck(t) },
		ProtoV5ProviderFactories: ProviderFactories,
		CheckDestroy:             CheckDestroy("dcapi_service_account", ServiceAccountExists),
		Steps: []resource.TestStep{
			{
				// Step 1 — Create the SA and snapshot its token for later comparison.
				Config: testAccServiceAccountConfig(name, "viewer"),
				Check:  token.Save(addr, "token"),
			},
			{
				// Step 2 — Authenticate with the token: an aliased provider uses it to read the
				// project. Passes only if the token is accepted and project_id matches; a bad or
				// unauthorized token fails the data source read.
				Config: testAccServiceAccountConfig(name, "viewer") + testAccNewSAProvider() + `
data "dcapi_project" "self" {
  provider   = dcapi.new_sa
  tenant_id  = local.tenant_id
  project_id = local.project_id
}
`,
				Check: resource.TestCheckResourceAttr("data.dcapi_project.self", "project_id", ProjectID()),
			},
			{
				// Step 3 — Refresh and confirm DC-API recorded the use: last_used must now be set,
				// and the token value must be the same one saved in Step 1.
				RefreshState: true,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttrSet(addr, "last_used"),
					token.Unchanged(addr, "token"),
				),
			},
		},
	})
}

// TestAccServiceAccount_forceNewRole verifies role is ForceNew: changing it cannot be an in-place
// update but must destroy-then-recreate the SA, which mints a brand-new token. The IDSet records
// every sa_id so CheckAllGone can confirm the replaced SA was really deleted.
//
// PASSES when: the role change plans a DestroyBeforeCreate, the post-change token differs from the
// original, and at teardown every recorded SA returns a 404.
// FAILS when: the change is treated as an in-place update, the token is reused, or a replaced SA is
// left alive on the API.
func TestAccServiceAccount_forceNewRole(t *testing.T) {
	name := RandomName("sa")
	addr := "dcapi_service_account.test"
	ids := &IDSet{}
	// ValuesDiffer asserts the token at this step differs from the one captured last step.
	newToken := statecheck.CompareValue(compare.ValuesDiffer())

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { PreCheck(t) },
		ProtoV5ProviderFactories: ProviderFactories,
		// Every sa_id recorded across the steps must be gone (404) by the end.
		CheckDestroy: ids.CheckAllGone(ServiceAccountExists),
		Steps: []resource.TestStep{
			{
				// Step 1 — Create the viewer SA and record its first sa_id and token.
				Config:            testAccServiceAccountConfig(name, "viewer"),
				Check:             ids.Record(addr),
				ConfigStateChecks: []statecheck.StateCheck{newToken.AddStateValue(addr, tfjsonpath.New("token"))},
			},
			{
				// Step 2 — Change role to member. ForceNew means this must replace the SA: the plan
				// check requires DestroyBeforeCreate and the new token must differ from Step 1's.
				Config: testAccServiceAccountConfig(name, "member"),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{plancheck.ExpectResourceAction(addr, plancheck.ResourceActionDestroyBeforeCreate)},
				},
				Check:             ids.Record(addr),
				ConfigStateChecks: []statecheck.StateCheck{newToken.AddStateValue(addr, tfjsonpath.New("token"))},
			},
		},
	})
}

// TestAccServiceAccount_viewerCannotWrite checks that a viewer token is read-only: DC-API must
// enforce the role, and the provider must surface the 403 clearly. Nothing should be created; the
// CheckDestroy sweep covers the NSG in case DC-API wrongly allowed it.
//
// PASSES when: Step 1 creates the viewer SA, and the write attempt in Step 2 via the aliased
// viewer provider fails with an HTTP 403 (so no NSG is created).
// FAILS when: the write succeeds, or it fails with a different error than a 403.
func TestAccServiceAccount_viewerCannotWrite(t *testing.T) {
	name := RandomName("sa")
	sa := testAccServiceAccountConfig(name, "viewer")

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { PreCheck(t) },
		ProtoV5ProviderFactories: ProviderFactories,
		CheckDestroy: resource.ComposeTestCheckFunc(
			CheckDestroy("dcapi_service_account", ServiceAccountExists),
			CheckDestroy("dcapi_network_security_group", NSGExists),
		),
		Steps: []resource.TestStep{
			// Step 1 — Create the viewer SA whose token Step 2 will use.
			{Config: sa},
			{
				// Step 2 — Attempt an NSG write through the viewer-token provider. Passes only if
				// the apply is rejected with an HTTP 403; any success or other error fails.
				Config: sa + testAccNewSAProvider() + fmt.Sprintf(`
resource "dcapi_network_security_group" "denied" {
  provider   = dcapi.new_sa
  tenant_id  = local.tenant_id
  project_id = local.project_id
  name       = "%s-nsg"
}
`, name),
				ExpectError: ExpectErr("HTTP 403"),
			},
		},
	})
}

// TestAccServiceAccount_validation checks the role schema validator rejects an unsupported value
// at plan time, before any API call.
//
// PASSES when: planning a config with role "admin" fails with a "to be one of" validation error.
// FAILS when: the invalid role is accepted, or the plan errors with a different message.
func TestAccServiceAccount_validation(t *testing.T) {
	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { PreCheck(t) },
		ProtoV5ProviderFactories: ProviderFactories,
		Steps: []resource.TestStep{
			{
				// Single step — PlanOnly: an invalid "admin" role must be rejected by the validator
				// during plan; passes only on the "to be one of" error.
				Config:      testAccServiceAccountConfig("acc-validation", "admin"),
				PlanOnly:    true,
				ExpectError: ExpectErr("to be one of"),
			},
		},
	})
}

func testAccServiceAccountConfig(name, role string) string {
	return ConfigBase() + fmt.Sprintf(`
resource "dcapi_service_account" "test" {
  tenant_id   = local.tenant_id
  project_id  = local.project_id
  name        = %q
  role        = %q
  description = "acc: safe to delete"
}
`, name, role)
}

// testAccNewSAProvider configures a second provider with the new SA's token. The endpoint
// still comes from DCAPI_ENDPOINT.
func testAccNewSAProvider() string {
	return `
provider "dcapi" {
  alias = "new_sa"
  token = dcapi_service_account.test.token
}
`
}
