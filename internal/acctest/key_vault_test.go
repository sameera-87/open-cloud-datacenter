package acctest

// Plan: docs/testsuite/resources/key-vault.md

import (
	"fmt"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/compare"
	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/plancheck"
	"github.com/hashicorp/terraform-plugin-testing/statecheck"
	"github.com/hashicorp/terraform-plugin-testing/tfjsonpath"
)

// TestAccKeyVault_basic exercises the full lifecycle of a dcapi_key_vault: create, refresh, data
// source parity, import, and out-of-band deletion. It also guards that one-time credentials
// (secret_id) survive a refresh.
//
// PASSES when: the vault is ACTIVE with soft_delete_days 7 and its computed endpoint/mount/role
// fields are populated; a bare refresh leaves secret_id unchanged; the data source's kv_uuid
// matches the UUID embedded in the resource ID (G7) and its mount_path/soft_delete_days agree;
// import reproduces state (ignoring the write-only secret_id/role_id/credentials_rotation); and a
// vault deleted behind Terraform's back produces a non-empty plan.
// FAILS when: a computed field is empty, a refresh wipes/rotates secret_id, the data source
// disagrees, import drops a verified field, or Read ignores the vault being gone.
func TestAccKeyVault_basic(t *testing.T) {
	name := RandomName("kv")
	addr := "dcapi_key_vault.test"
	cfg := testAccKeyVaultConfig(name, 7, "")
	secret := &AttrSnapshot{}

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { PreCheck(t) },
		ProtoV5ProviderFactories: ProviderFactories,
		CheckDestroy:             CheckDestroy("dcapi_key_vault", KeyVaultExists),
		Steps: []resource.TestStep{
			{
				// Step 1 — Create: assert the vault is ACTIVE, soft_delete_days matches, and every
				// computed credential/endpoint field is populated; snapshot secret_id for Step 2.
				Config: cfg,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(addr, "status", "ACTIVE"),
					resource.TestCheckResourceAttr(addr, "soft_delete_days", "7"),
					resource.TestCheckResourceAttrSet(addr, "mount_path"),
					resource.TestCheckResourceAttrSet(addr, "endpoint_address"),
					resource.TestCheckResourceAttrSet(addr, "endpoint_port"),
					resource.TestCheckResourceAttrSet(addr, "role_id"),
					secret.Save(addr, "secret_id"),
				),
			},
			{
				// Step 2 — Refresh: the credentials are shown once at create. A bare refresh must
				// not wipe or change secret_id; the step fails if the snapshot no longer matches.
				RefreshState: true,
				Check:        secret.Unchanged(addr, "secret_id"),
			},
			{
				// Step 3 — Data source parity: look the vault up by name and check it agrees with
				// the resource. Fails if the data source's kv_uuid doesn't equal the UUID inside the
				// resource ID (G7) or its mount_path/soft_delete_days diverge.
				Config: cfg + `
data "dcapi_key_vault" "test" {
  tenant_id  = local.tenant_id
  project_id = local.project_id
  name       = dcapi_key_vault.test.name
}
`,
				Check: resource.ComposeAggregateTestCheckFunc(
					// G7: the data source's kv_uuid must equal the UUID inside the resource ID.
					CheckIDPart("data.dcapi_key_vault.test", "kv_uuid", addr, 2),
					resource.TestCheckResourceAttrPair("data.dcapi_key_vault.test", "mount_path", addr, "mount_path"),
					resource.TestCheckResourceAttrPair("data.dcapi_key_vault.test", "soft_delete_days", addr, "soft_delete_days"),
				),
			},
			{
				// Step 4 — Import: verify the imported state matches, except for the write-only
				// fields that can't be read back — role_id/secret_id come from a one-time call and
				// can never be read again; credentials_rotation exists only in config.
				ResourceName:            addr,
				ImportState:             true,
				ImportStateVerify:       true,
				ImportStateVerifyIgnore: []string{"secret_id", "role_id", "credentials_rotation"},
			},
			{
				// Step 5 — Disappears: delete the vault out-of-band; Read must drop it from state
				// and produce a non-empty plan to recreate it. Fails if Read ignores the deletion.
				Config:             cfg,
				Check:              Disappears(addr, DeleteKeyVault),
				ExpectNonEmptyPlan: true,
			},
		},
	})
}

// TestAccKeyVault_rotation: changing credentials_rotation rotates secret_id but keeps role_id.
// Every step is an in-place Update that preserves the resource id and role_id while issuing a new
// secret_id.
//
// Step 3 removes credentials_rotation. Today that counts as a change and rotates again (G12).
// This test pins that current behaviour; if G12 is decided as "removal is a no-op", change the
// secret_id comparison in step 3 to ValuesSame.
//
// PASSES when: each change plans an Update, and across every step the id and role_id stay the same
// (ValuesSame) while secret_id differs from the prior step (ValuesDiffer).
// FAILS when: a change is planned as a replace, role_id/id change, or secret_id fails to rotate.
func TestAccKeyVault_rotation(t *testing.T) {
	name := RandomName("kv")
	addr := "dcapi_key_vault.test"
	sameID := statecheck.CompareValue(compare.ValuesSame())
	sameRole := statecheck.CompareValue(compare.ValuesSame())
	newSecret := statecheck.CompareValue(compare.ValuesDiffer())
	update := resource.ConfigPlanChecks{
		PreApply: []plancheck.PlanCheck{plancheck.ExpectResourceAction(addr, plancheck.ResourceActionUpdate)},
	}
	track := func() []statecheck.StateCheck {
		return []statecheck.StateCheck{
			sameID.AddStateValue(addr, tfjsonpath.New("id")),
			sameRole.AddStateValue(addr, tfjsonpath.New("role_id")),
			newSecret.AddStateValue(addr, tfjsonpath.New("secret_id")),
		}
	}

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { PreCheck(t) },
		ProtoV5ProviderFactories: ProviderFactories,
		CheckDestroy:             CheckDestroy("dcapi_key_vault", KeyVaultExists),
		Steps: []resource.TestStep{
			{
				// Step 1 — Create with credentials_rotation "r1"; capture the baseline id/role_id/secret_id.
				Config:            testAccKeyVaultConfig(name, 7, "r1"),
				ConfigStateChecks: track(),
			},
			{
				// Step 2 — Change rotation "r1"→"r2": an Update that keeps id/role_id but issues a
				// new secret_id.
				Config:            testAccKeyVaultConfig(name, 7, "r2"),
				ConfigPlanChecks:  update,
				ConfigStateChecks: track(),
			},
			{
				// Step 3 — Remove credentials_rotation. Per current G12 behaviour this is still an
				// Update that rotates secret_id again while id/role_id stay the same.
				Config:            testAccKeyVaultConfig(name, 7, ""),
				ConfigPlanChecks:  update,
				ConfigStateChecks: track(),
			},
		},
	})
}

// TestAccKeyVault_forceNew: soft_delete_days is ForceNew, so changing it must destroy-then-recreate
// the vault. Because the two vaults share a name, this also proves a new vault with the same name
// is creatable while the old one is soft-deleted.
//
// PASSES when: changing soft_delete_days plans a DestroyBeforeCreate, the replacement vault is
// created under the same name, and at teardown every recorded vault id is gone.
// FAILS when: the change is an in-place update, DC-API keeps the name reserved so step 2 can't
// create the replacement, or a replaced vault survives — all useful to know.
func TestAccKeyVault_forceNew(t *testing.T) {
	name := RandomName("kv")
	addr := "dcapi_key_vault.test"
	ids := &IDSet{}

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { PreCheck(t) },
		ProtoV5ProviderFactories: ProviderFactories,
		CheckDestroy:             ids.CheckAllGone(KeyVaultExists),
		Steps: []resource.TestStep{
			{
				// Step 1 — Create the baseline vault (soft_delete_days 7) and record its id.
				Config: testAccKeyVaultConfig(name, 7, ""),
				Check:  ids.Record(addr),
			},
			{
				// Step 2 — Change soft_delete_days 7→8. ForceNew requires a DestroyBeforeCreate; the
				// new vault reuses the name while the old one is soft-deleted, and its id is recorded.
				Config: testAccKeyVaultConfig(name, 8, ""),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{plancheck.ExpectResourceAction(addr, plancheck.ResourceActionDestroyBeforeCreate)},
				},
				Check: ids.Record(addr),
			},
		},
	})
}

// testAccKeyVaultConfig: rotation "" leaves credentials_rotation out of the config.
func testAccKeyVaultConfig(name string, softDeleteDays int, rotation string) string {
	rot := ""
	if rotation != "" {
		rot = fmt.Sprintf("credentials_rotation = %q", rotation)
	}
	return ConfigBase() + fmt.Sprintf(`
resource "dcapi_key_vault" "test" {
  tenant_id        = local.tenant_id
  project_id       = local.project_id
  name             = %q
  soft_delete_days = %d
  %s
}
`, name, softDeleteDays, rot)
}
