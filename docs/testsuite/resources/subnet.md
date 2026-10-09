# `dcapi_subnet`

| | |
|---|---|
| Go file | `internal/acctest/subnet_test.go`, prefix `TestAccSubnet_` |
| Argo template | `dcapi-acc-subnet` |
| Credentials | member |
| Creates | Its own VNet `10.200.0.0/16` |
| Go timeout | 90m |
| Estimated duration | 30–60 min |

## Facts from the code ([subnet.go](../../../internal/resources/subnet.go))

| Property | Value | Test consequence |
|---|---|---|
| Update | None. Every argument is ForceNew | Changes are tested as replacements |
| Arguments | `name`, `cidr`, `gateway` (Optional + Computed), `description`, `tenant_id`, `project_id`, `vnet_id` | `gateway` has to be tested both omitted and explicit |
| Computed | `subnet_uuid`, `status`, `provider_type`, `message`, `created_at`, `updated_at` | |
| Importer | **Yes** (passthrough) | Import step appended to the basic test (`ImportState` + `ImportStateVerify`) |
| Async | Create `PENDING → ACTIVE` (5m). Delete polls until 404 (10m) | |
| State ID | `tenant_id/project_id/vnet_id/subnet_uuid` | |
| Data source | `dcapi_subnet` by `vnet_id` + `name` | Checked in basic |

## Dependencies

Each test creates its own VNet `10.200.0.0/16` and a subnet in it
([04 §2](../04-test-isolation.md#2-cidr-plan)). The tests run one after another, so they reuse
the same ranges.

CIDRs: `10.200.10.0/24`, `10.200.11.0/24` (replacement target), `10.200.12.0/24` (explicit gateway).

Each subnet is the only one in its VNet, so every destroy takes the slow last-subnet path
([04 §3](../04-test-isolation.md#3-the-last-subnet-delete)). These tests keep the provider's
**default** delete timeout on purpose, so they catch [G10](../07-provider-gaps-found.md#g10--subnet-delete-timeout-is-shorter-than-the-documented-last-subnet-teardown-medium-fixed).

## Test cases

| Test | Steps | Proves |
|---|---|---|
| `TestAccSubnet_basic` | 1. Create `10.200.10.0/24` with no `gateway`. 2. Add `data "dcapi_subnet"`. 3. Import. 4. Disappears. | Computed gateway is filled in (`10.200.10.1`, first usable IP) and doesn't cause a diff. Data source parity. Import parity, including the computed `gateway`. 404 handling |
| `TestAccSubnet_explicitGateway` | 1. Create `10.200.12.0/24` with `gateway = "10.200.12.10"` | A user-set gateway survives the round trip unchanged |
| `TestAccSubnet_forceNewCIDR` | 1. Create `10.200.10.0/24`. 2. Change `cidr` to `10.200.11.0/24` → `DestroyBeforeCreate`, new `subnet_uuid` | ForceNew on `cidr`. The old subnet is deleted before the new one is created |

## Config sketch

```hcl
resource "dcapi_vnet" "parent" {
  tenant_id     = local.tenant_id
  project_id    = local.project_id
  name          = "<name>-vnet"
  address_space = ["10.200.0.0/16"]
  region        = local.region
}

resource "dcapi_subnet" "test" {
  tenant_id  = local.tenant_id
  project_id = local.project_id
  vnet_id    = dcapi_vnet.parent.vnet_uuid
  name       = "<name>"
  cidr       = "10.200.10.0/24"
  # gateway omitted → Computed
}

data "dcapi_subnet" "test" {
  tenant_id  = local.tenant_id
  project_id = local.project_id
  vnet_id    = dcapi_vnet.parent.vnet_uuid
  name       = dcapi_subnet.test.name
}
```

## Checks

- State: `cidr`; `gateway = 10.200.10.1` when omitted (the API's documented default);
  `status = ACTIVE`; `subnet_uuid` is set; `vnet_id` paired with `dcapi_vnet.parent.vnet_uuid`.
- Data source: pair on `subnet_uuid`, `cidr`, `gateway`.
- API: `GetSubnet` returns the same `cidr` and `gateway`.

## Import verification

The import ID is the state ID, `tenant_id/project_id/vnet_id/subnet_uuid`. With only `ResourceName` set, the framework imports
using the ID from the previous step's state, so no `ImportStateIdFunc` is needed:

```go
{
	ResourceName:      "dcapi_subnet.test",
	ImportState:       true,
	ImportStateVerify: true,
},
```

`ImportStateVerify` compares every attribute of the imported state with the state from the
create step. Any field Read doesn't set shows up as a mismatch.

## Destroy verification

- `CheckDestroy`: `GetSubnet(tenant, project, vnet, id)` is nil.
- `Disappears`: `DeleteSubnet`, then poll until 404 (max 10m).

## Risks targeted

- `gateway` being Optional + Computed + ForceNew: a wrong combination produces a replacement on every plan.
- The API returning `cidr` in a normalised form (e.g. `10.200.10.0/24` vs `10.200.10.00/24`).
- Subnet deletion returning 202 while the object is still around, so the VNet delete that follows fails.
- The last-subnet delete taking longer than the default timeout (G10).
