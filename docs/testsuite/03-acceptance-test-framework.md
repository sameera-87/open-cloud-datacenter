# 03 — Acceptance Test Framework

## 1. Why `terraform-plugin-testing`

It is HashiCorp's official harness for provider acceptance tests, and it works with SDKv2
providers like this one. For each `TestStep` it runs the real Terraform CLI against the
provider, which is served **in-process** through a provider factory. That means:
- no `go build`
- no `~/.terraformrc` `dev_overrides`
- no `terraform init` or registry lookup

What it does for free:
| Behaviour | Why it matters here |
|---|---|
| Re-plans after every apply and fails on a non-empty plan | Catches perpetual diffs, e.g. an API that reorders NSG rules |
| Always destroys at the end of a `TestCase`, even after a failed step | The main cleanup mechanism ([06](06-cleanup.md)) |
| `CheckDestroy` runs after destroy | Proves the object is really gone from DC-API, not just from state |
| `ImportState` + `ImportStateVerify` | Proves import reproduces state field-for-field |
| `ConfigPlanChecks` (`plancheck`) | Asserts *update* versus *replace* before apply, which is essential for ForceNew bugs |
| `ConfigStateChecks` + `compare` | Asserts an ID stayed the same (update) or changed (replace) across steps |
| `ExpectError`, `PlanOnly` | Validation tests with zero API calls |
| Skips everything unless `TF_ACC=1` | `go test ./...` in PR CI stays offline and fast |

## 2. Dependencies to add

```bash
go get github.com/hashicorp/terraform-plugin-testing@latest   # v1.x
go get github.com/hashicorp/terraform-plugin-go@latest        # tfprotov5 types for the factory
go mod tidy
```

Check that the `terraform-plugin-testing` version you pick supports the pinned Terraform CLI in
the image (Terraform `1.15.x` locally today; pin the same version in the image, see [05 §1](05-argo-workflows.md#1-the-test-image)).

## 3. Repository layout

```
internal/acctest/                    # one package = one test binary (acctest.test)
  acctest.go                         # provider factory, PreCheck, RequireEnv, ExpectErr
  names.go                           # RandomName(), RunPrefix()
  apiclient.go                       # out-of-band *client.DCAPIClient, WaitGone
  api.go                             # per-resource Exists/Delete functions (checks + sweepers)
  config.go                          # ConfigBase(), ConfigNetwork(), ConfigKeyVault(), CIDR constants
  checks.go                          # CheckDestroy, Disappears, CheckAPI, IDSet, AttrSnapshot
  sweep.go                           # sweeper registration (see 06)
  main_test.go                       # TestMain → resource.TestMain (enables -sweep)
  vnet_test.go                       # one *_test.go per resource
  subnet_test.go
  network_security_group_test.go
  nsg_attachment_test.go
  route_table_test.go
  route_table_association_test.go
  vnet_peering_test.go
  private_dns_zone_test.go
  dns_record_test.go
  key_vault_test.go
  key_vault_secret_test.go
  private_endpoint_test.go
  virtual_machine_test.go
  bastion_test.go
  cluster_node_pool_test.go          # the one deliberate chain (see resources/cluster.md)
  service_account_test.go
  data_sources_test.go               # region, image, project, tenant (standalone reads)
test/acc/
  env.example                        # template for .env.acc (02 §4)
  Dockerfile                         # test image (05 §1), added with the Argo phase
argo/dcapi-acc/                      # all Argo manifests (05 §2)
```

All acceptance tests live in **one package**, which gives:
- **One test binary.** The image ships a single precompiled `acctest.test`. Every Argo step runs
  that binary with a different `-test.run` regex, so no Go compilation happens at run time.
- **No import cycle.** `internal/provider` imports `internal/resources`. Putting acceptance tests
  in `internal/resources` would need them to import `provider`, which Go only allows from an
  external `_test` package. A dedicated package is cleaner.
- **Separate unit tests.** Unit tests stay where they are ([project_test.go](../../internal/resources/project_test.go)),
  in the package they test.

### Naming convention (Argo depends on it)

`TestAcc<Resource>_<scenario>`, e.g. `TestAccNSG_rulesUpdate`, `TestAccNSGAttachment_basic`.

Every Argo template selects its tests with an anchored regex that ends at the underscore,
`^TestAccNSG_`. Because of the underscore, `^TestAccNSG_` does **not** match
`TestAccNSGAttachment_basic`. Keep the underscore mandatory, or templates will start running
each other's tests.

| Resource | Go prefix |
|---|---|
| vnet | `TestAccVNet_` |
| subnet | `TestAccSubnet_` |
| network_security_group | `TestAccNSG_` |
| nsg_attachment | `TestAccNSGAttachment_` |
| route_table | `TestAccRouteTable_` |
| route_table_association | `TestAccRouteTableAssociation_` |
| vnet_peering | `TestAccVNetPeering_` |
| private_dns_zone | `TestAccPrivateDNSZone_` |
| dns_record | `TestAccDNSRecord_` |
| key_vault | `TestAccKeyVault_` |
| key_vault_secret | `TestAccKeyVaultSecret_` |
| private_endpoint | `TestAccPrivateEndpoint_` |
| virtual_machine | `TestAccVirtualMachine_` |
| bastion | `TestAccBastion_` |
| cluster + node_pool | `TestAccCluster_` (the chain is `TestAccCluster_withNodePool`) |
| service_account | `TestAccServiceAccount_` |
| standalone data sources | `TestAccDataSource_` |

## 4. Helpers (`internal/acctest`)

### 4.1 Provider factory and PreCheck — `acctest.go`

```go
package acctest

import (
	"os"
	"testing"

	"github.com/hashicorp/terraform-plugin-go/tfprotov5"
	"github.com/hashicorp/terraform-plugin-sdk/v2/helper/schema"
	"terraform-provider-dcapi/internal/provider"
)

// ProviderFactories serves the provider in-process — no build/install/init.
var ProviderFactories = map[string]func() (tfprotov5.ProviderServer, error){
	"dcapi": func() (tfprotov5.ProviderServer, error) {
		return schema.NewGRPCProviderServer(provider.New()), nil
	},
}

var requiredEnv = []string{
	"DCAPI_ENDPOINT", "DCAPI_TOKEN", "DCAPI_ACC_TENANT_ID", "DCAPI_ACC_PROJECT_ID",
	"DCAPI_ACC_REGION", "DCAPI_ACC_RUN_ID",
}

// PreCheck fails fast with an actionable message instead of a cryptic 401 mid-apply.
func PreCheck(t *testing.T) {
	t.Helper()
	for _, k := range requiredEnv {
		if os.Getenv(k) == "" {
			t.Fatalf("%s must be set for acceptance tests (see docs/testsuite/02)", k)
		}
	}
}

func TenantID() string  { return os.Getenv("DCAPI_ACC_TENANT_ID") }
func ProjectID() string { return os.Getenv("DCAPI_ACC_PROJECT_ID") }
```

### 4.2 Names — `names.go`

```go
// RandomName returns "acc-<run>-<short>-<rand5>", e.g. "acc-k3f9q-nsg-x7a2m".
//   acc-   : cleanup prefix (06). NEVER reuse this prefix for anything long-lived.
//   <run>  : DCAPI_ACC_RUN_ID (5 chars) — lets the cleanup step find exactly this run's leftovers.
//   <short>: resource abbreviation, for humans reading DC-API listings.
//   <rand5>: uniqueness across tests and retries within a run.
func RandomName(short string) string
```

Why these constraints:
- **Lowercase, starts with a letter, at most 30 characters.** That fits the strictest rules in
  the API: cluster names are a DNS label of at most 32 characters, VNet names must match
  `[a-z][a-z0-9-]{0,61}[a-z0-9]`, key vaults start with a letter, and SA names allow at most 63
  characters.
- **Random suffix.** NSG names are unique across the *tenant*, key vault and secret names can be
  held by soft-delete, and a retried test must never collide with the object from its first attempt.

### 4.3 Out-of-band API client — `apiclient.go`

```go
// APIClient returns a *client.DCAPIClient built from DCAPI_ENDPOINT/DCAPI_TOKEN.
// Used by CheckDestroy, Disappears and the sweepers to talk to DC-API *without* Terraform,
// so checks don't depend on the code they are testing.
func APIClient(t *testing.T) *client.DCAPIClient
```

### 4.4 Config composition — `config.go`

Every test config starts with the same `locals`. That way tenant and project IDs and the region
are never hard-coded, and the HCL reads like the examples:

```go
func ConfigBase() string {
	return fmt.Sprintf(`
locals {
  tenant_id  = %q
  project_id = %q
  region     = %q
}
`, TenantID(), ProjectID(), Region())
}
```

Many tests need a VNet and subnet as parents. `ConfigNetwork` returns them, so each test
declares its own in one line and destroys them with everything else:

```go
// ConfigNetwork returns a dcapi_vnet "parent" and, if subnetCIDR != "", a dcapi_subnet "parent".
// Tests refer to dcapi_vnet.parent.vnet_uuid and dcapi_subnet.parent.subnet_uuid.
func ConfigNetwork(name, vnetCIDR, subnetCIDR string) string
```

The CIDRs come from named constants, one range per resource ([04 §2](04-test-isolation.md#2-cidr-plan)).

Each resource test file has small `testAcc<Res>Config_<scenario>(name, ...) string` functions
that return `ConfigBase() + ConfigNetwork(...) + fmt.Sprintf(<resource HCL>)`.

### 4.5 Checks — `checks.go`

```go
// CheckDestroy builds a resource.TestCheckFunc that, for every resource of `resType` left in
// the final state, parses its ID and calls `get`; it fails if the object still exists.
// All client Get* functions return (nil, nil) on HTTP 404, which is exactly "gone".
func CheckDestroy(resType string, get func(ctx context.Context, c *client.DCAPIClient, id string) (bool, error)) resource.TestCheckFunc

// Disappears deletes the object behind `addr` directly through the client (and waits for
// the 404 where the API is async), simulating deletion outside Terraform.
// Used with ExpectNonEmptyPlan: true to prove Read clears the ID on 404.
func Disappears(addr string, del func(ctx context.Context, c *client.DCAPIClient, id string) error) resource.TestCheckFunc

// CheckAPIAttr fetches the object via the client and asserts a field — an independent
// source of truth next to TestCheckResourceAttr (which only reads Terraform state).
func CheckAPIAttr(addr string, fetch func(...) (map[string]any, error), key, want string) resource.TestCheckFunc
```

### 4.6 `TestMain`

```go
func TestMain(m *testing.M) {
	resource.TestMain(m) // enables the `-sweep=<project>` flag (06)
}
```

## 5. The standard test anatomy

Every resource test file follows the same shape. Steps are dropped where the coverage matrix
([01 §6](01-scope-and-strategy.md#6-coverage-matrix-what-every-resource-gets)) says a step doesn't apply.

```go
func TestAccNSG_basic(t *testing.T) {
	name := acctest.RandomName("nsg")
	addr := "dcapi_network_security_group.test"
	sameID := statecheck.CompareValue(compare.ValuesSame()) // collects "id" across steps

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { acctest.PreCheck(t) },
		ProtoV5ProviderFactories: acctest.ProviderFactories,
		CheckDestroy:             acctest.CheckDestroy("dcapi_network_security_group", nsgExists),
		Steps: []resource.TestStep{
			// 1. CREATE — assert user-set + computed attributes, and cross-check via the API.
			{
				Config: testAccNSGConfig(name, rulesA),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(addr, "name", name),
					resource.TestCheckResourceAttr(addr, "rules.#", "2"),
					resource.TestCheckResourceAttrSet(addr, "sg_id"),
					resource.TestCheckResourceAttr(addr, "status", "ACTIVE"),
				),
				ConfigStateChecks: []statecheck.StateCheck{ sameID.AddStateValue(addr, tfjsonpath.New("id")) },
				// (implicit) empty-plan check after apply
			},
			// 2. UPDATE IN PLACE — assert "update", not "replace", and the ID is unchanged.
			{
				Config: testAccNSGConfig(name, rulesB),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{
						plancheck.ExpectResourceAction(addr, plancheck.ResourceActionUpdate),
					},
				},
				ConfigStateChecks: []statecheck.StateCheck{ sameID.AddStateValue(addr, tfjsonpath.New("id")) },
			},
			// 3. IMPORT — every resource except virtual_machine and cluster (08 G2).
			{ResourceName: addr, ImportState: true, ImportStateVerify: true},
			// 4. DISAPPEARS — delete out-of-band, expect Terraform to want to recreate it.
			{
				Config:             testAccNSGConfig(name, rulesB),
				Check:              acctest.Disappears(addr, deleteNSG),
				ExpectNonEmptyPlan: true,
			},
		},
	})
}
```

### Step types used across the suite

| Step | How | Proves |
|---|---|---|
| Create | `Config` + `Check` | Create works, Read maps every field, the plan is empty afterwards |
| Update in place | new `Config` + `plancheck.ResourceActionUpdate` + `compare.ValuesSame()` on `id` | `UpdateContext` is wired, the field is really updatable, no hidden ForceNew |
| ForceNew replace | new `Config` + `plancheck.ResourceActionDestroyBeforeCreate` + `compare.ValuesDiffer()` on `id` | The immutable field really triggers replacement, and the old object is deleted |
| Import | `ImportState: true, ImportStateVerify: true` (+ `ImportStateVerifyIgnore` for write-only or one-time fields). Placed **before** any Disappears step, because Disappears deletes the object | The importer and Read reproduce state. Used by 15 of 17 resources; the ignore lists are in each resource doc and in [08 G1](07-provider-gaps-found.md#g1--resources-without-an-importer-medium-mostly-fixed) |
| Disappears | `Check: acctest.Disappears(...)`, `ExpectNonEmptyPlan: true` | Read treats 404 as drift (`d.SetId("")`) instead of erroring |
| Data source | add a `data` block in a later step + `TestCheckResourceAttrPair` | The data source finds the object by name and maps the same fields |
| Validation | `PlanOnly: true, ExpectError: regexp.MustCompile(...)` | `ValidateFunc` / `CustomizeDiff` reject bad input before any API call |

Every test uses `resource.Test`, not `resource.ParallelTest`. The tests of one resource run one
after another inside its pod, so tests in the same file can reuse the same CIDR range. The
parallelism comes from Argo running all resources at once.

### Timeouts

Go's `-test.timeout` is per binary invocation. Each resource template sets it to roughly the
sum of that resource's tests, plus a margin. The values are in [05 §3.3](05-argo-workflows.md#33-values-per-resource).

## 6. Running locally

```bash
# one-time: copy and fill (file is gitignored)
cp test/acc/env.example .env.acc && $EDITOR .env.acc

# run one resource, exactly like its Argo template does
make testacc TESTARGS='-run ^TestAccNSG_ -timeout 30m'

# delete leftovers of a run whose tests were interrupted (06 §3)
make sweep RUN_ID=<run id>
```

`GNUmakefile` additions:

```make
testacc:
	set -a; . ./.env.acc; set +a; \
	TF_ACC=1 go test ./internal/acctest -v $(TESTARGS)

sweep:
	set -a; . ./.env.acc; set +a; \
	DCAPI_ACC_RUN_ID=$(RUN_ID) go test ./internal/acctest -v -run '^$$' \
	  -sweep=$${DCAPI_ACC_PROJECT_ID} -sweep-allow-failures -timeout 60m
```

Add `.env.acc*` to `.gitignore`.
