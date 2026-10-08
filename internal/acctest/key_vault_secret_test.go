package acctest

// Plan: docs/testsuite/resources/key-vault-secret.md

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

// TestAccKeyVaultSecret_basic exercises the create/read/import/disappears lifecycle of a
// dcapi_key_vault_secret, including that the sensitive value round-trips through the API.
//
// PASSES when: the created secret is version 1 with the configured value and metadata, DC-API
// confirms the same value/version, import reproduces every attribute (including value, which Read
// pulls back from the API), and a secret soft-deleted out-of-band is detected as a non-empty plan.
// FAILS when: version/value/metadata don't match, the API disagrees, import drops a field, or Read
// ignores the 410 after soft-delete and reports no changes.
func TestAccKeyVaultSecret_basic(t *testing.T) {
	name := RandomName("kvs")
	addr := "dcapi_key_vault_secret.test"
	cfg := testAccKeyVaultSecretConfig(name, name, "acc-value-1", map[string]string{"env": "acc"})

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { PreCheck(t); preCheckSecretsAPI(t) },
		ProtoV5ProviderFactories: ProviderFactories,
		CheckDestroy:             testAccKeyVaultSecretCheckDestroy(),
		Steps: []resource.TestStep{
			{
				// Step 1 — Create: apply the config and assert the Create/Read round-trip.
				// Passes only if state shows version 1 with the configured value/metadata and
				// DC-API itself reports the same value and version.
				Config: cfg,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(addr, "version", "1"),
					resource.TestCheckResourceAttr(addr, "value", "acc-value-1"),
					resource.TestCheckResourceAttr(addr, "metadata.env", "acc"),
					CheckAPI(addr, checkSecretAPI("acc-value-1", 1)),
				),
			},
			{
				// Step 2 — Import: re-import by state ID and verify every attribute. Read returns
				// value from the API, so the sensitive value is verified too; a missing or changed
				// field fails ImportStateVerify.
				ResourceName:      addr,
				ImportState:       true,
				ImportStateVerify: true,
			},
			{
				// Step 3 — Disappears: DELETE soft-deletes the secret, so the next Read gets 410 and
				// must clear the ID, yielding a non-empty plan to recreate it. Fails if Read ignores
				// the 410 and reports no changes.
				Config:             cfg,
				Check:              Disappears(addr, DeleteKeyVaultSecret),
				ExpectNonEmptyPlan: true,
			},
		},
	})
}

// TestAccKeyVaultSecret_update verifies that changing a secret's value or metadata is an in-place
// update (not a replacement): the resource ID stays the same across every step while the version
// counter advances and metadata is written and cleared.
//
// PASSES when: each change plans an Update (not a replace), the value bump moves version 1→2 and
// DC-API agrees, metadata is set to the configured map, and removing metadata truly clears it
// server-side (metadata.% == 0) — all while the id is unchanged across steps.
// FAILS when: a change is planned as a replace, the version doesn't advance, metadata isn't
// written/cleared, or the id changes between steps.
func TestAccKeyVaultSecret_update(t *testing.T) {
	name := RandomName("kvs")
	addr := "dcapi_key_vault_secret.test"
	// sameID asserts the resource id is identical to the previous step (no replacement).
	sameID := statecheck.CompareValue(compare.ValuesSame())
	update := resource.ConfigPlanChecks{
		PreApply: []plancheck.PlanCheck{plancheck.ExpectResourceAction(addr, plancheck.ResourceActionUpdate)},
	}

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { PreCheck(t); preCheckSecretsAPI(t) },
		ProtoV5ProviderFactories: ProviderFactories,
		CheckDestroy:             testAccKeyVaultSecretCheckDestroy(),
		Steps: []resource.TestStep{
			{
				// Step 1 — Create the baseline secret at version 1 and record its id.
				Config:            testAccKeyVaultSecretConfig(name, name, "v1", nil),
				Check:             resource.TestCheckResourceAttr(addr, "version", "1"),
				ConfigStateChecks: []statecheck.StateCheck{sameID.AddStateValue(addr, tfjsonpath.New("id"))},
			},
			{
				// Step 2 — Change the value. Must be an in-place Update that moves version to 2,
				// with DC-API confirming the new value/version and the id unchanged.
				Config:           testAccKeyVaultSecretConfig(name, name, "v2", nil),
				ConfigPlanChecks: update,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(addr, "version", "2"),
					CheckAPI(addr, checkSecretAPI("v2", 2)),
				),
				ConfigStateChecks: []statecheck.StateCheck{sameID.AddStateValue(addr, tfjsonpath.New("id"))},
			},
			{
				// Step 3 — Add metadata (value unchanged). Update must write both keys; state must
				// show two entries including owner=tf, id still unchanged.
				Config:           testAccKeyVaultSecretConfig(name, name, "v2", map[string]string{"env": "acc", "owner": "tf"}),
				ConfigPlanChecks: update,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(addr, "metadata.%", "2"),
					resource.TestCheckResourceAttr(addr, "metadata.owner", "tf"),
				),
				ConfigStateChecks: []statecheck.StateCheck{sameID.AddStateValue(addr, tfjsonpath.New("id"))},
			},
			{
				// Step 4 — Remove metadata. Removing metadata must really clear it server-side, or
				// the next refresh wouldn't yield metadata.% == 0 and the plan wouldn't be empty.
				Config:            testAccKeyVaultSecretConfig(name, name, "v2", nil),
				ConfigPlanChecks:  update,
				Check:             resource.TestCheckResourceAttr(addr, "metadata.%", "0"),
				ConfigStateChecks: []statecheck.StateCheck{sameID.AddStateValue(addr, tfjsonpath.New("id"))},
			},
		},
	})
}

// TestAccKeyVaultSecret_forceNew verifies that the secret's key is ForceNew: changing it must
// destroy-then-recreate the secret rather than update in place. The IDSet records each id so
// CheckAllGone can confirm every replaced secret was really deleted.
//
// PASSES when: changing the key plans a DestroyBeforeCreate and, at teardown, every recorded
// secret id is gone from the API.
// FAILS when: the key change is treated as an in-place update, or a replaced secret survives.
func TestAccKeyVaultSecret_forceNew(t *testing.T) {
	name := RandomName("kvs")
	addr := "dcapi_key_vault_secret.test"
	ids := &IDSet{}

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { PreCheck(t); preCheckSecretsAPI(t) },
		ProtoV5ProviderFactories: ProviderFactories,
		CheckDestroy:             ids.CheckAllGone(KeyVaultSecretExists),
		Steps: []resource.TestStep{
			{
				// Step 1 — Create the secret with key "-k1" and record its id.
				Config: testAccKeyVaultSecretConfig(name, name+"-k1", "v", nil),
				Check:  ids.Record(addr),
			},
			{
				// Step 2 — Change the key to "-k2". ForceNew means this must replace the secret:
				// the plan check requires DestroyBeforeCreate and a new id is recorded.
				Config: testAccKeyVaultSecretConfig(name, name+"-k2", "v", nil),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{plancheck.ExpectResourceAction(addr, plancheck.ResourceActionDestroyBeforeCreate)},
				},
				Check: ids.Record(addr),
			},
		},
	})
}

// TestAccKeyVaultSecret_recreateAfterDelete reproduces what users will do: create a secret, remove
// it (leaving only the vault, which soft-deletes the key), then create a secret with the same key
// again. It verifies DC-API lets a soft-deleted key be rewritten.
//
// PASSES when: all three steps apply cleanly and the recreated secret holds the new value
// "v1-again".
// FAILS when: DC-API refuses to write a soft-deleted key, so step 3 errors — signalling the
// resource needs a restore or purge path.
func TestAccKeyVaultSecret_recreateAfterDelete(t *testing.T) {
	name := RandomName("kvs")

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { PreCheck(t); preCheckSecretsAPI(t) },
		ProtoV5ProviderFactories: ProviderFactories,
		CheckDestroy:             testAccKeyVaultSecretCheckDestroy(),
		Steps: []resource.TestStep{
			// Step 1 — Create the vault and a secret at the given key.
			{Config: testAccKeyVaultSecretConfig(name, name, "v1", nil)},
			// Step 2 — Drop the secret from config (vault only); Terraform deletes it, soft-deleting
			// the key server-side.
			{Config: ConfigBase() + ConfigKeyVault(name)},
			{
				// Step 3 — Recreate a secret with the same key. Passes only if DC-API accepts a
				// write to the soft-deleted key and state shows the new value.
				Config: testAccKeyVaultSecretConfig(name, name, "v1-again", nil),
				Check:  resource.TestCheckResourceAttr("dcapi_key_vault_secret.test", "value", "v1-again"),
			},
		},
	})
}

// TestAccKeyVaultSecret_validation checks that the key's ValidateFunc rejects a malformed key
// ("Bad Key!" violates ^[a-z0-9._-]{1,256}$) at plan time, before any API call.
//
// PASSES when: the plan-only step fails with an error matching "must match" (the pattern error).
// FAILS when: the invalid key is accepted, or the error text doesn't match.
func TestAccKeyVaultSecret_validation(t *testing.T) {
	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { PreCheck(t) },
		ProtoV5ProviderFactories: ProviderFactories,
		Steps: []resource.TestStep{
			{
				// Plan-only: a key with spaces and "!" must be rejected by the schema's regex
				// validator with a "must match" error; no resource is created.
				Config: ConfigBase() + `
resource "dcapi_key_vault_secret" "test" {
  tenant_id    = local.tenant_id
  project_id   = local.project_id
  key_vault_id = "00000000-0000-0000-0000-000000000000"
  key          = "Bad Key!"
  value        = "v"
}
`,
				PlanOnly:    true,
				ExpectError: ExpectErr("must match"),
			},
		},
	})
}

// testAccKeyVaultSecretConfig declares the test's own vault and a secret with the given key.
// Keys start with the run prefix and are lowercase, so they match ^[a-z0-9._-]{1,256}$.
func testAccKeyVaultSecretConfig(name, key, value string, metadata map[string]string) string {
	md := ""
	if len(metadata) > 0 {
		var pairs []string
		for k, v := range metadata {
			pairs = append(pairs, fmt.Sprintf("%s = %q", k, v))
		}
		md = "metadata     = { " + strings.Join(pairs, ", ") + " }"
	}
	return ConfigBase() + ConfigKeyVault(name) + fmt.Sprintf(`
resource "dcapi_key_vault_secret" "test" {
  tenant_id    = local.tenant_id
  project_id   = local.project_id
  key_vault_id = local.kv_uuid
  key          = %q
  value        = %q
  %s
}
`, key, value, md)
}

func testAccKeyVaultSecretCheckDestroy() resource.TestCheckFunc {
	return resource.ComposeTestCheckFunc(
		CheckDestroy("dcapi_key_vault_secret", KeyVaultSecretExists),
		CheckDestroy("dcapi_key_vault", KeyVaultExists),
	)
}

func checkSecretAPI(wantValue string, wantVersion int) APICheckFunc {
	return func(ctx context.Context, c *client.DCAPIClient, p []string) error {
		s, err := c.GetKeyVaultSecret(ctx, p[0], p[1], p[2], p[3])
		if err != nil {
			return err
		}
		if s == nil {
			return fmt.Errorf("secret not found")
		}
		if s.Value != wantValue || s.Version != wantVersion {
			return fmt.Errorf("value/version = %s/%d, want %s/%d", redact(s.Value), s.Version, redact(wantValue), wantVersion)
		}
		return nil
	}
}

// preCheckSecretsAPI skips the secret tests when the platform's KVI provisioner is disabled
// (secret routes return 501). It probes an existing vault in the project; with no vault to
// probe, the tests run and a 501 shows up as a clear failure instead.
func preCheckSecretsAPI(t *testing.T) {
	t.Helper()
	c, err := NewAPIClient()
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	kvs, err := c.ListKeyVaults(ctx, TenantID(), ProjectID())
	if err != nil || len(kvs) == 0 {
		return
	}
	_, err = c.GetKeyVaultSecret(ctx, TenantID(), ProjectID(), kvs[0].ID, "acc-probe")
	if err != nil && strings.Contains(err.Error(), "HTTP 501") {
		t.Skip("key vault secrets API returned 501 (KVI provisioner disabled); skipping secret tests")
	}
}
