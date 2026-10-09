# `dcapi_route_table_association`

| | |
|---|---|
| Go file | `internal/acctest/route_table_association_test.go`, prefix `TestAccRouteTableAssociation_` |
| Argo template | `dcapi-acc-route-table-association` |
| Credentials | member |
| Creates | Its own VNet `10.203.0.0/16`, subnet and route tables |
| Go timeout | 90m |
| Estimated duration | 30–60 min |

## Facts from the code ([route_table_association.go](../../../internal/resources/route_table_association.go))

| Property | Value | Test consequence |
|---|---|---|
| Update | None. `vnet_id`, `route_table_id`, `subnet_id` are all ForceNew | Moving to another route table is a replacement |
| Computed | `association_id`, `created_at`, `warning` | `warning` is asserted to be present in state (can be empty) |
| Read | Fetches the parent route table and searches `associations[]`. Clears the ID if the route table or the association is gone | Both paths are tested |
| Importer | **Yes** (passthrough) | Import step |
| State ID | `tenant_id/project_id/vnet_id/route_table_id/association_id` (5 parts) | |

## Dependencies

Each test creates its own VNet `10.203.0.0/16`, a subnet `10.203.1.0/24` and two route tables
([04 §2](../04-test-isolation.md#2-cidr-plan)). The tests run one after another, so they reuse
the same ranges.

## Test cases

| Test | Steps | Proves |
|---|---|---|
| `TestAccRouteTableAssociation_basic` | 1. Create RT-A + subnet + association. 2. Import. 3. Disappears (delete the association only). | Create/Read, the 5-part import ID, 404 handling |
| `TestAccRouteTableAssociation_changeRouteTable` | 1. Associate subnet with RT-A. 2. Point `route_table_id` to RT-B → `DestroyBeforeCreate` | A subnet can be moved between route tables. Destroy-before-create avoids a "subnet already associated" 409 |
| `TestAccRouteTableAssociation_parentGone` | 1. Create. 2. Check deletes the association then RT-A out-of-band → `ExpectNonEmptyPlan` | Read's parent-missing branch |

## Config sketch

```hcl
resource "dcapi_vnet" "parent" {
  tenant_id     = local.tenant_id
  project_id    = local.project_id
  name          = "<name>-vnet"
  address_space = ["10.203.0.0/16"]
  region        = local.region
}

resource "dcapi_route_table" "a" {
  tenant_id  = local.tenant_id
  project_id = local.project_id
  vnet_id    = dcapi_vnet.parent.vnet_uuid
  name       = "<name>-a"
  routes {
    name             = "default"
    destination_cidr = "0.0.0.0/0"
    next_hop_type    = "internet"
  }
}

resource "dcapi_subnet" "test" {
  tenant_id  = local.tenant_id
  project_id = local.project_id
  vnet_id    = dcapi_vnet.parent.vnet_uuid
  name       = "<name>"
  cidr       = "10.203.1.0/24"
}

resource "dcapi_route_table_association" "test" {
  tenant_id      = local.tenant_id
  project_id     = local.project_id
  vnet_id        = dcapi_vnet.parent.vnet_uuid
  route_table_id = dcapi_route_table.a.route_table_id
  subnet_id      = dcapi_subnet.test.subnet_uuid
}
```

## Checks

- State: `association_id` set; `subnet_id` paired with the subnet; `route_table_id` paired with RT-A (RT-B after step 2).
- API: `GetRouteTable(A).Associations` contains the subnet in step 1, and is empty after step 2.
  `GetRouteTable(B)` contains it after step 2.

## Destroy verification

- `CheckDestroy`: parse the 5-part ID. The association must be absent from the route table's
  `associations[]`, or the route table itself must be gone.
- `Disappears`: `DeleteRouteTableAssociation`.

## Risks targeted

- Replacement ordering: if Terraform created the new association before deleting the old one,
  DC-API might reject it. `DestroyBeforeCreate` is the default for ForceNew, and the plan check pins it.
- `warning` changing between reads. It's Computed-only, so it doesn't cause a diff, but it's
  logged for visibility.
