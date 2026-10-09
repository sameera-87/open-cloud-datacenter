# `dcapi_private_dns_zone`

| | |
|---|---|
| Go file | `internal/acctest/private_dns_zone_test.go`, prefix `TestAccPrivateDNSZone_` |
| Argo template | `dcapi-acc-private-dns-zone` |
| Credentials | member |
| Creates | Its own VNet `10.205.0.0/16` |
| Go timeout | 40m |
| Estimated duration | 10–20 min |

## Facts from the code ([private_dns_zone.go](../../../internal/resources/private_dns_zone.go))

| Property | Value | Test consequence |
|---|---|---|
| Update | None. `vnet_id`, `name`, `description` are all ForceNew | Replacement test on `description` |
| `name` | A DNS name (e.g. `internal.example.com`), not a plain label | The random name is wrapped: `<name>.acc.internal` |
| Computed | `zone_id`, `status`, `provider_type`, `message`, `created_at`, `updated_at` | |
| Async | Create `PENDING → ACTIVE` (5m). Delete polls until 404 (5m) | |
| Importer | **Yes** (passthrough) | Import step appended to the basic test (`ImportState` + `ImportStateVerify`) |
| State ID | `tenant_id/project_id/vnet_id/zone_id` | |
| Data source | `dcapi_private_dns_zone` by `vnet_id` + `name` | |

## Dependencies

Each test creates its own VNet `10.205.0.0/16` with no subnet ([04 §2](../04-test-isolation.md#2-cidr-plan)).

## Test cases

| Test | Steps | Proves |
|---|---|---|
| `TestAccPrivateDNSZone_basic` | 1. Create zone. 2. Data source. 3. Import. 4. Disappears. | Create/Read, `status = ACTIVE`, data source parity, import parity, 404 handling |
| `TestAccPrivateDNSZone_forceNew` | 1. Create. 2. Change `description` → `DestroyBeforeCreate`, new `zone_id` | ForceNew, and the old zone is deleted before a new zone with the **same name** is created. This checks the API frees the name promptly |

## Config sketch

```hcl
resource "dcapi_vnet" "parent" {
  tenant_id     = local.tenant_id
  project_id    = local.project_id
  name          = "<name>-vnet"
  address_space = ["10.205.0.0/16"]
  region        = local.region
}

resource "dcapi_private_dns_zone" "test" {
  tenant_id   = local.tenant_id
  project_id  = local.project_id
  vnet_id     = dcapi_vnet.parent.vnet_uuid
  name        = "<name>.acc.internal"
  description = "acc zone"
}
```

## Checks

- State: `name`, `description`, `status = ACTIVE`, `zone_id` set.
- API: `GetPrivateDnsZone` returns `ACTIVE` and the same `name`. The API may return a trailing
  dot (`…internal.`); if so, the empty-plan check fails and flags a normalisation bug.
- Data source: pair on `zone_id`, `description`.

## Import verification

The import ID is the state ID, `tenant_id/project_id/vnet_id/zone_id`. With only `ResourceName` set, the framework imports
using the ID from the previous step's state, so no `ImportStateIdFunc` is needed:

```go
{
	ResourceName:      "dcapi_private_dns_zone.test",
	ImportState:       true,
	ImportStateVerify: true,
},
```

`ImportStateVerify` compares every attribute of the imported state with the state from the
create step. Any field Read doesn't set shows up as a mismatch.

## Destroy verification

- `CheckDestroy`: `GetPrivateDnsZone` is nil.
- `Disappears`: `DeletePrivateDnsZone`, then poll until 404.

## Risks targeted

- DNS name normalisation (trailing dot, letter case), which would cause a perpetual ForceNew diff.
- The name staying reserved after delete, which would fail the replacement step with 409.
