# `dcapi_vnet_peering`

| | |
|---|---|
| Go file | `internal/acctest/vnet_peering_test.go`, prefix `TestAccVNetPeering_` |
| Argo template | `dcapi-acc-vnet-peering` |
| Credentials | member |
| Creates | Both VNets: `10.204.0.0/16` and `10.220.0.0/16` |
| Go timeout | 60m |
| Estimated duration | 20–40 min |

## Facts from the code ([vnet_peering.go](../../../internal/resources/vnet_peering.go))

| Property | Value | Test consequence |
|---|---|---|
| Update | None. `vnet_id`, `name`, `peer_vnet_id`, `allow_forwarded_traffic` (bool, default false) are all ForceNew | Replacement test on `allow_forwarded_traffic` |
| Computed | `peering_id`, `status`, `provider_type`, `message`, `warning`, `created_at`, `updated_at` | |
| Async | Create `PENDING → ACTIVE` (5m). Delete polls until 404 (5m) | |
| Directional | One resource = one direction ([dc-api-reference §8](../../dc-api-reference.md#8-peering-is-directional)) | A bidirectional test creates two |
| Importer | **Yes** (passthrough) | Import step appended to the basic test (`ImportState` + `ImportStateVerify`) |
| State ID | `tenant_id/project_id/vnet_id/peering_id` | |
| Data source | `dcapi_vnet_peering` by `vnet_id` + `name` | |

## Dependencies

Each test creates both VNets: side A `10.204.0.0/16` and side B `10.220.0.0/16`
([04 §2](../04-test-isolation.md#2-cidr-plan)). The two must not overlap. Neither has a subnet,
which keeps create and delete fast.

## Test cases

| Test | Steps | Proves |
|---|---|---|
| `TestAccVNetPeering_basic` | 1. Peer VNet + peering A→B. 2. Data source. 3. Import the peering. 4. Disappears. | Create/Read, `status = ACTIVE`, `allow_forwarded_traffic` default false round-trips, import parity, 404 handling |
| `TestAccVNetPeering_bidirectional` | 1. A→B and B→A. | Both directions reach `ACTIVE`, and destroying them together works (no ordering conflict) |
| `TestAccVNetPeering_forceNew` | 1. `allow_forwarded_traffic = false`. 2. `true` → `DestroyBeforeCreate`, new `peering_id` | ForceNew on the bool |
| `TestAccVNetPeering_overlapRejected` | 1. Peer VNet `10.204.128.0/17`, which overlaps side A → `ExpectError` | DC-API rejects overlapping peers, and the provider surfaces the API error text. The expected message regex is set after the first observed run |

## Config sketch

```hcl
resource "dcapi_vnet" "a" {
  tenant_id     = local.tenant_id
  project_id    = local.project_id
  name          = "<name>-a"
  address_space = ["10.204.0.0/16"]
  region        = local.region
}

resource "dcapi_vnet" "peer" {
  tenant_id     = local.tenant_id
  project_id    = local.project_id
  name          = "<name>-b"
  address_space = ["10.220.0.0/16"]
  region        = local.region
}

resource "dcapi_vnet_peering" "a_to_b" {
  tenant_id    = local.tenant_id
  project_id   = local.project_id
  vnet_id      = dcapi_vnet.a.vnet_uuid
  name         = "<name>-ab"
  peer_vnet_id = dcapi_vnet.peer.vnet_uuid
}
```

## Checks

- State: `status = ACTIVE`; `peer_vnet_id` paired with `dcapi_vnet.peer.vnet_uuid`;
  `allow_forwarded_traffic = false`; `peering_id` set.
- API: `ListVNetPeerings(side A)` contains the peering. In the bidirectional test,
  `ListVNetPeerings(peer vnet)` contains the reverse.
- `warning` is logged (not asserted). It's a Computed-only field the API uses for advisories.

## Import verification

The import ID is the state ID, `tenant_id/project_id/vnet_id/peering_id`. With only `ResourceName` set, the framework imports
using the ID from the previous step's state, so no `ImportStateIdFunc` is needed:

```go
{
	ResourceName:      "dcapi_vnet_peering.a_to_b",
	ImportState:       true,
	ImportStateVerify: true,
},
```

`ImportStateVerify` compares every attribute of the imported state with the state from the
create step. Any field Read doesn't set shows up as a mismatch.

## Destroy verification

- `CheckDestroy`: `GetVNetPeering(tenant, project, vnet, id)` is nil for each peering. Both VNets use the VNet CheckDestroy.
- `Disappears`: `DeleteVNetPeering`, then poll until 404.

## Risks targeted

- Peer VNet deletion racing peering deletion. Terraform orders them by reference, and the test
  would catch a 409 if DC-API needs longer.
- The create waiter targeting the wrong status.
