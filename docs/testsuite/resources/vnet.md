# `dcapi_vnet`

| | |
|---|---|
| Go file | `internal/acctest/vnet_test.go`, prefix `TestAccVNet_` |
| Argo template | `dcapi-acc-vnet` |
| Credentials | member |
| Creates | Its own VNets only |
| Go timeout | 60m |
| Estimated duration | 15–25 min |

## Facts from the code ([vnet.go](../../../internal/resources/vnet.go))

| Property | Value | Test consequence |
|---|---|---|
| Update | No `UpdateContext`. Every argument is ForceNew | No update step. Changes are tested as replacements |
| Arguments | `name`, `address_space` (list), `region`, `description` (optional), `tenant_id`, `project_id` | |
| Computed | `vnet_uuid`, `status`, `provider_type`, `message`, `created_at`, `updated_at` | Asserted after create |
| Importer | **Yes** (passthrough) | Import step appended to the basic test (`ImportState` + `ImportStateVerify`) |
| Async | Create polls `PENDING → ACTIVE` (5m). Delete polls until 404 (5m) | CheckDestroy must see a real 404 |
| State ID | `tenant_id/project_id/vnet_uuid` | Parsed by CheckDestroy |
| Data source | `dcapi_vnet` by `name` | Checked in the basic test |

## Dependencies

None beyond the project.

CIDRs: `10.210.0.0/16` (basic), `10.211.0.0/16` (replacement target), `10.212.0.0/16` +
`10.213.0.0/16` (multi-space), per [04 §2](../04-test-isolation.md#2-cidr-plan).

The slow last-subnet delete ([G10](../07-provider-gaps-found.md#g10--subnet-delete-timeout-is-shorter-than-the-documented-last-subnet-teardown-medium-fixed))
needs no separate VNet test: every test that has a subnet deletes the last subnet in its own VNet.

## Test cases

| Test | Steps | Proves |
|---|---|---|
| `TestAccVNet_basic` | 1. Create VNet with description. 2. Add `data "dcapi_vnet"` by name. 3. Import. 4. Disappears. | Create/Read mapping, empty plan, data source parity, import parity, Read handles 404 |
| `TestAccVNet_multiAddressSpace` | 1. Create with `address_space = ["10.213.0.0/16", "10.212.0.0/16"]` (descending order on purpose) | [G4](../07-provider-gaps-found.md#g4--list-attributes-that-dc-api-might-reorder-medium-needs-verification): the list isn't reordered by the API |
| `TestAccVNet_forceNew` | 1. Create. 2. Change `description` → assert `DestroyBeforeCreate`, new `vnet_uuid`. 3. Change `address_space` to `10.211.0.0/16` → replace again. | ForceNew wiring. The old VNet is really deleted (CheckDestroy covers every ID seen) |

## Config sketch

```hcl
resource "dcapi_vnet" "test" {
  tenant_id     = local.tenant_id
  project_id    = local.project_id
  name          = "<name>"
  address_space = ["10.210.0.0/16"]
  region        = local.region
  description   = "acc basic"
}

# step 2 adds:
data "dcapi_vnet" "test" {
  tenant_id  = local.tenant_id
  project_id = local.project_id
  name       = dcapi_vnet.test.name
}
```

## Checks

- State: `name`, `region`, `address_space.# = 1`, `address_space.0 = 10.210.0.0/16`,
  `description`; `status = ACTIVE`; `vnet_uuid`, `provider_type` and `created_at` are set.
- Data source: `TestCheckResourceAttrPair` on `vnet_uuid`, `address_space.0`, `region`, `description`.
- API: `GetVNet` returns `status = ACTIVE`, the same `address_space`, and the same `region`.

## Import verification

The import ID is the state ID, `tenant_id/project_id/vnet_uuid`. With only `ResourceName` set, the framework imports
using the ID from the previous step's state, so no `ImportStateIdFunc` is needed:

```go
{
	ResourceName:      "dcapi_vnet.test",
	ImportState:       true,
	ImportStateVerify: true,
},
```

`ImportStateVerify` compares every attribute of the imported state with the state from the
create step. Any field Read doesn't set shows up as a mismatch.

## Destroy verification

- `CheckDestroy`: for every `dcapi_vnet` in state, `GetVNet(...)` must return `(nil, nil)`.
- `Disappears`: `DeleteVNet`, then poll `GetVNet` until nil (max 5m), then
  `ExpectNonEmptyPlan: true`.

## Risks targeted

- `address_space` reordering (G4).
- Delete returning before the VNet is really gone, which would show up as 409s in later tests.
- `description` normalisation, e.g. the API trimming whitespace, which would cause a perpetual diff.
