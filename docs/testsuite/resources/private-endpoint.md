# `dcapi_private_endpoint`

| | |
|---|---|
| Go file | `internal/acctest/private_endpoint_test.go`, prefix `TestAccPrivateEndpoint_` |
| Argo template | `dcapi-acc-private-endpoint` |
| Credentials | member |
| Creates | Its own VNet `10.207.0.0/16`, subnet and key vault |
| Go timeout | 60m |
| Estimated duration | 25–45 min |

## Facts from the code ([private_endpoint.go](../../../internal/resources/private_endpoint.go))

| Property | Value | Test consequence |
|---|---|---|
| Update | None. `kv_id`, `name`, `vnet_id`, `subnet_id` are all ForceNew | Replacement on `name` |
| Computed | `endpoint_id`, `target_type`, `target_id`, `ip_address`, `hostname`, `status`, `message`, `created_at`, `updated_at` | |
| Async | **None in the provider**: no timeouts and no waiter. The API returns 201, but the resource has a `status` field | The test asserts `status = ACTIVE` right after apply, to find out whether a waiter is needed ([G11](../07-provider-gaps-found.md#g11--private_endpoint-has-a-status-but-no-waiter-medium-verify)) |
| Importer | **Yes** (passthrough) | Import step appended to the basic test (`ImportState` + `ImportStateVerify`) |
| State ID | `tenant_id/project_id/kv_id/endpoint_id` | |

## Dependencies

Each test creates its own VNet `10.207.0.0/16`, subnet `10.207.1.0/24` and key vault
([04 §2](../04-test-isolation.md#2-cidr-plan)). The endpoint takes one IP (VIP) from that subnet.

## Test cases

| Test | Steps | Proves |
|---|---|---|
| `TestAccPrivateEndpoint_basic` | 1. Create VNet, subnet, key vault and an endpoint to the vault. 2. Import. 3. Disappears. | Create/Read, the VIP comes from the right subnet, the target is the vault, import parity, 404 handling |
| `TestAccPrivateEndpoint_forceNew` | 1. Create. 2. Rename → `DestroyBeforeCreate`, new `endpoint_id` | ForceNew, and the old VIP is released |

## Config sketch

```hcl
# acctest.ConfigNetwork(name, "10.207.0.0/16", "10.207.1.0/24") adds
# dcapi_vnet.parent and dcapi_subnet.parent.

resource "dcapi_key_vault" "parent" {
  tenant_id        = local.tenant_id
  project_id       = local.project_id
  name             = "<name>-kv"
  soft_delete_days = 7
}

resource "dcapi_private_endpoint" "test" {
  tenant_id  = local.tenant_id
  project_id = local.project_id
  kv_id      = element(split("/", dcapi_key_vault.parent.id), 2)   # G7
  name       = "<name>"
  vnet_id    = dcapi_vnet.parent.vnet_uuid
  subnet_id  = dcapi_subnet.parent.subnet_uuid
}
```

## Checks

- State: `ip_address` matches `^10\.207\.1\.\d+$` (`TestMatchResourceAttr`); `hostname` set;
  `target_id` equals the vault UUID; `target_type` is set (value recorded on first run, then pinned);
  `status = ACTIVE`.
- API: `GetPrivateEndpoint` returns the same `ip_address`.

## Import verification

The import ID is the state ID, `tenant_id/project_id/kv_id/endpoint_id`. With only `ResourceName` set, the framework imports
using the ID from the previous step's state, so no `ImportStateIdFunc` is needed:

```go
{
	ResourceName:      "dcapi_private_endpoint.test",
	ImportState:       true,
	ImportStateVerify: true,
},
```

`ImportStateVerify` compares every attribute of the imported state with the state from the
create step. Any field Read doesn't set shows up as a mismatch.

## Destroy verification

- `CheckDestroy`: `GetPrivateEndpoint(tenant, project, kv, id)` is nil.
- `Disappears`: `DeletePrivateEndpoint`.

## Risks targeted

- G11: apply finishing while the endpoint is still `PENDING`, so users see "success" before the endpoint works.
- The endpoint blocking deletion of its key vault or subnet, if the provider returns from Delete
  before DC-API has released it. This would show up as a 409 when the test destroys them.
