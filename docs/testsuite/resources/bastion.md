# `dcapi_bastion`

| | |
|---|---|
| Go file | `internal/acctest/bastion_test.go`, prefix `TestAccBastion_` |
| Argo template | `dcapi-acc-bastion` |
| Credentials | member |
| Creates | Its own VNet `10.209.0.0/16` and `/28` subnet |
| Go timeout | 90m |
| Estimated duration | 45–75 min |

## Facts from the code ([bastion.go](../../../internal/resources/bastion.go))

| Property | Value | Test consequence |
|---|---|---|
| Update | None. `name`, `vnet_id`, `subnet_id`, `description` are all ForceNew | Replacement test on `description` |
| Size | Not user-configurable (platform-sized) | Assumed `small` in the quota budget. Confirm on the first run |
| Computed | `bastion_id`, `status`, `provider_type`, `mgmt_ip`, `internal_ip`, `message`, `created_at`, `private_key` + `console_password` (**sensitive, shown once**) | |
| Read | Sets `tenant_id` and `project_id` from the state ID (fixed; previously `tenant_id` came from the response body, [G5](../07-provider-gaps-found.md#g5--tenant_id-overwritten-from-the-api-response-medium-needs-verification), and `project_id` wasn't set); keeps the secrets from state | The empty-plan and import checks confirm it |
| Async | Create `PENDING → ACTIVE` (15m). Delete polls until 404 (10m) | |
| Importer | **Yes** (passthrough). Read now sets `tenant_id`/`project_id` from the state ID | Import step in the basic test with `ImportStateVerifyIgnore: ["private_key", "console_password"]` |
| State ID | `tenant_id/project_id/bastion_id` | |

## Dependencies

Each test creates its own VNet `10.209.0.0/16` and a small `/28` subnet `10.209.1.0/28`,
matching [examples/bastion](../../../examples/bastion/main.tf) ([04 §2](../04-test-isolation.md#2-cidr-plan)).

## Test cases

| Test | Steps | Proves |
|---|---|---|
| `TestAccBastion_basic` | 1. VNet + subnet + bastion. 2. `RefreshState: true`. 3. Import (ignore the one-time secrets). 4. Disappears. | Create polling, both IPs populated, `internal_ip` inside `10.209.1.0/28`, secrets stored and preserved, no `tenant_id` diff (G5), import restores `tenant_id` and `project_id`, 404 handling |
| `TestAccBastion_forceNew` | 1. Create. 2. Change `description` → `DestroyBeforeCreate` | ForceNew wiring. Old bastion deleted |

## Config sketch

```hcl
resource "dcapi_vnet" "parent" {
  tenant_id     = local.tenant_id
  project_id    = local.project_id
  name          = "<name>-vnet"
  address_space = ["10.209.0.0/16"]
  region        = local.region
}

resource "dcapi_subnet" "bastion" {
  tenant_id  = local.tenant_id
  project_id = local.project_id
  vnet_id    = dcapi_vnet.parent.vnet_uuid
  name       = "<name>-sn"
  cidr       = "10.209.1.0/28"
}

resource "dcapi_bastion" "test" {
  tenant_id   = local.tenant_id
  project_id  = local.project_id
  name        = "<name>"
  vnet_id     = dcapi_vnet.parent.vnet_uuid
  subnet_id   = dcapi_subnet.bastion.subnet_uuid
  description = "acc bastion"
}
```

## Checks

- State: `status = ACTIVE`; `mgmt_ip` set; `internal_ip` matches `^10\.209\.1\.\d+$`;
  `private_key`, `console_password` set; `bastion_id` set.
- After refresh: the secrets are unchanged; `tenant_id` still equals `local.tenant_id`.
- API: `GetBastion` returns `ACTIVE`, and the `vnet_id` / `subnet_id` match.

## Import verification

The import ID is the state ID, `tenant_id/project_id/bastion_id`. With only `ResourceName` set, the framework imports
using the ID from the previous step's state, so no `ImportStateIdFunc` is needed:

```go
{
	ResourceName:      "dcapi_bastion.test",
	ImportState:       true,
	ImportStateVerify: true,
	ImportStateVerifyIgnore: []string{"private_key", "console_password"},
},
```

`ImportStateVerify` compares every attribute of the imported state with the state from the
create step. Any field Read doesn't set shows up as a mismatch. The GET response never includes the one-time secrets. Everything else, including `tenant_id` and `project_id` (now parsed from the ID), must match.

## Destroy verification

- `CheckDestroy`: `GetBastion` is nil. The subnet is checked with the subnet CheckDestroy.
  Destroying the bastion before its subnet is ordered by reference.
- `Disappears`: `DeleteBastion`, then poll until 404.

## Risks targeted

- G5 (`tenant_id` slug vs UUID from the API response).
- Subnet deletion being blocked by a bastion that is still `DELETING`. The delete waiter must
  poll until 404; if it doesn't, the subnet destroy fails with 409.
