# `dcapi_dns_record`

| | |
|---|---|
| Go file | `internal/acctest/dns_record_test.go`, prefix `TestAccDNSRecord_` |
| Argo template | `dcapi-acc-dns-record` |
| Credentials | member |
| Creates | Its own VNet `10.206.0.0/16` and zone |
| Go timeout | 60m |
| Estimated duration | 20–35 min |

## Facts from the code ([dns_record.go](../../../internal/resources/dns_record.go))

| Property | Value | Test consequence |
|---|---|---|
| Update | `values` (list) and `ttl` (default 300), through `UpdateDnsRecord` | Update step on both |
| ForceNew | `tenant_id`, `project_id`, `vnet_id`, `zone_id`, `name`, `type` | Replacement on `name` |
| Computed | `record_id`, `created_at` | |
| Async | None (sync) | |
| Importer | **Yes** (passthrough) | Import step appended to the basic test (`ImportState` + `ImportStateVerify`) |
| State ID | `tenant_id/project_id/vnet_id/zone_id/record_id` (5 parts) | |
| Data source | `dcapi_dns_record` by zone + `name` + `type` | |

## Dependencies

Each test creates its own VNet `10.206.0.0/16` and its own `dcapi_private_dns_zone`
([04 §2](../04-test-isolation.md#2-cidr-plan)). Nothing is shared with the private_dns_zone
tests, so a record failure can't be caused by, or cause, a zone test failure.

## Test cases

| Test | Steps | Proves |
|---|---|---|
| `TestAccDNSRecord_basic` | 1. Zone + `A` record (1 value, `ttl` omitted) + `CNAME` record. 2. Data source for the A record. 3. Import both records. 4. Disappears. | Default `ttl = 300` round-trips, both record types work, data source parity, the 5-part import ID reproduces state |
| `TestAccDNSRecord_update` | 1. A record `["10.206.0.5"]`, ttl 300. 2. `["10.206.0.5", "10.206.0.6"]`, ttl 60 → `Update`, same `record_id`. 3. Back to one value. | In-place update of `values` and `ttl` |
| `TestAccDNSRecord_valuesOrdering` | 1. A record `["10.206.0.20", "10.206.0.3", "10.206.0.100"]` (neither numeric nor lexical order) | G4. DNS back ends often sort record sets |
| `TestAccDNSRecord_forceNew` | 1. Create `name = "app"`. 2. `name = "app2"` → `DestroyBeforeCreate` | ForceNew on `name` |

## Config sketch

```hcl
resource "dcapi_vnet" "parent" {
  tenant_id     = local.tenant_id
  project_id    = local.project_id
  name          = "<name>-vnet"
  address_space = ["10.206.0.0/16"]
  region        = local.region
}

resource "dcapi_private_dns_zone" "z" {
  tenant_id  = local.tenant_id
  project_id = local.project_id
  vnet_id    = dcapi_vnet.parent.vnet_uuid
  name       = "<name>.acc.internal"
}

resource "dcapi_dns_record" "a" {
  tenant_id  = local.tenant_id
  project_id = local.project_id
  vnet_id    = dcapi_vnet.parent.vnet_uuid
  zone_id    = dcapi_private_dns_zone.z.zone_id
  name       = "app"
  type       = "A"
  values     = ["10.206.0.5"]
}

resource "dcapi_dns_record" "cname" {
  tenant_id  = local.tenant_id
  project_id = local.project_id
  vnet_id    = dcapi_vnet.parent.vnet_uuid
  zone_id    = dcapi_private_dns_zone.z.zone_id
  name       = "app-alias"
  type       = "CNAME"
  values     = ["app.${dcapi_private_dns_zone.z.name}"]
}
```

## Checks

- State: `ttl = 300` when omitted; `values.#` and each value; `record_id` set.
- Update: `plancheck` Update; `compare.ValuesSame()` on `record_id`.
- API: `GetDnsRecord` returns the same values and TTL after each step.
- Data source: pair on `record_id`, `values.#`, `ttl`.

## Import verification

The import ID is the state ID, `tenant_id/project_id/vnet_id/zone_id/record_id`. With only `ResourceName` set, the framework imports
using the ID from the previous step's state, so no `ImportStateIdFunc` is needed:

```go
{
	ResourceName:      "dcapi_dns_record.a",
	ImportState:       true,
	ImportStateVerify: true,
},
```

`ImportStateVerify` compares every attribute of the imported state with the state from the
create step. Any field Read doesn't set shows up as a mismatch. A second import step does the same for `dcapi_dns_record.cname`.

## Destroy verification

- `CheckDestroy`: `GetDnsRecord` is nil (or the zone is gone).
- `Disappears`: `DeleteDnsRecord`.

## Risks targeted

- Value reordering (G4). If it's caught, the fix is `TypeSet` for `values`.
- CNAME target normalisation (a trailing dot added by the DNS back end).
- `ttl` default mismatch, if the API default differs from the schema's `Default: 300`.
