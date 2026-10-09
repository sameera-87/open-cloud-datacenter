# `dcapi_route_table`

| | |
|---|---|
| Go file | `internal/acctest/route_table_test.go`, prefix `TestAccRouteTable_` |
| Argo template | `dcapi-acc-route-table` |
| Credentials | member |
| Creates | Its own VNet `10.202.0.0/16` |
| Go timeout | 45m |
| Estimated duration | 15–30 min |

## Facts from the code ([route_table.go](../../../internal/resources/route_table.go))

| Property | Value | Test consequence |
|---|---|---|
| Update | `routes` only, through `PUT` (**full replace**) | Same update strategy as NSG rules |
| ForceNew | `name`, `description`, `vnet_id`, `tenant_id`, `project_id` | Replacement test on `name` |
| Route fields | `name`, `destination_cidr` (IsCIDR), `next_hop_type` (vnet_local\|internet\|virtual_appliance\|none), `next_hop_ip` (IsIPAddress, optional) | |
| CustomizeDiff | `next_hop_ip` is required for `virtual_appliance` and forbidden otherwise | Two plan-only negative tests |
| Importer | **Yes** (passthrough) | Import step |
| State ID | `tenant_id/project_id/vnet_id/route_table_id` | |
| Data source | `dcapi_route_table` by `vnet_id` + `name` | |

## Dependencies

Each test creates its own VNet `10.202.0.0/16` with no subnet ([04 §2](../04-test-isolation.md#2-cidr-plan)).
Associations are tested separately ([route-table-association.md](route-table-association.md)).

## Test cases

| Test | Steps | Proves |
|---|---|---|
| `TestAccRouteTable_basic` | 1. Create with `internet` default route + `virtual_appliance` route. 2. Import. 3. Data source. 4. Disappears. | All route fields map round-trip, including an empty `next_hop_ip` for non-appliance routes |
| `TestAccRouteTable_routesUpdate` | 1. Two routes. 2. Change one's CIDR, remove the other, add a `none` (blackhole) route → `Update`, same ID. 3. `routes = []`. | Full-replace semantics, and going to zero routes |
| `TestAccRouteTable_routesOrdering` | 1. Routes with destinations `10.230.0.0/16`, `0.0.0.0/0`, `10.220.0.0/16` in that order | G4 ordering |
| `TestAccRouteTable_forceNew` | 1. Create. 2. Rename → `DestroyBeforeCreate` | ForceNew on `name` |
| `TestAccRouteTable_validation` | Plan-only: `virtual_appliance` without IP; `internet` with IP; `destination_cidr = "10.0.0.0/33"`; `next_hop_type = "gateway"` | CustomizeDiff and ValidateFuncs |

## Config sketch

```hcl
resource "dcapi_vnet" "parent" {
  tenant_id     = local.tenant_id
  project_id    = local.project_id
  name          = "<name>-vnet"
  address_space = ["10.202.0.0/16"]
  region        = local.region
}

resource "dcapi_route_table" "test" {
  tenant_id  = local.tenant_id
  project_id = local.project_id
  vnet_id    = dcapi_vnet.parent.vnet_uuid
  name       = "<name>"

  routes {
    name             = "default"
    destination_cidr = "0.0.0.0/0"
    next_hop_type    = "internet"
  }
  routes {
    name             = "to-peer-via-fw"
    destination_cidr = "10.220.0.0/16"
    next_hop_type    = "virtual_appliance"
    next_hop_ip      = "10.202.0.10"
  }
}
```

## Checks

- State: `routes.#`, every field per route; `routes.0.next_hop_ip = ""` for the internet route
  (the flatten function writes `""`, so the plan must stay empty); `route_table_id`, `status` set.
- API: `GetRouteTable` route count matches after every update step.
- Data source: pair on `route_table_id`, `routes.#`.

## Destroy verification

- `CheckDestroy`: `GetRouteTable` is nil.
- `Disappears`: `DeleteRouteTable`.

## Risks targeted

- `next_hop_ip` round-trip: the API returning `null` vs `""`, or dropping the field for non-appliance routes.
- Route reordering (G4).
- A full replace that leaves old routes behind.
