# `dcapi_network_security_group`

| | |
|---|---|
| Go file | `internal/acctest/network_security_group_test.go`, prefix `TestAccNSG_` |
| Argo template | `dcapi-acc-nsg` |
| Credentials | member |
| Creates | Nothing else. NSGs are project-level objects |
| Go timeout | 20m |
| Estimated duration | 3–6 min |

## Facts from the code ([nsg.go](../../../internal/resources/nsg.go))

| Property | Value | Test consequence |
|---|---|---|
| Update | `rules` only, through `PUT /rules` (**full replace**) | Update step must add, change and remove rules in one go, and also go to zero rules |
| ForceNew | `name`, `description`, `tenant_id`, `project_id` | Replacement test on `description` |
| Rule fields | `name`, `direction` (inbound\|outbound), `priority` (100–4096), `protocol` (tcp\|udp\|icmp\|\*), `source_address_prefix`, `source_port_range`, `destination_address_prefix`, `destination_port_range`, `action` (allow\|deny) | Every enum and range gets a plan-only negative test |
| Importer | **Yes** (passthrough) | Import step |
| Async | No (sync 201) | |
| State ID | `tenant_id/project_id/sg_id` | |
| Data source | `dcapi_network_security_group` by `name`, including `rules` and `attachments` | Checked in basic |
| API constraint | Name unique **within the tenant** | Random names are mandatory (another project in the same tenant may have `web-sg`) |

## Test cases

| Test | Steps | Proves |
|---|---|---|
| `TestAccNSG_basic` | 1. Create with 2 rules (inbound tcp 443 allow, outbound \* deny). 2. Import. 3. Add data source. 4. Disappears. | Mapping of every rule field, import parity, data source parity, 404 handling |
| `TestAccNSG_rulesUpdate` | 1. Create with rules A, B. 2. Change A's port, remove B, add C → `Update`, same `sg_id`. 3. `rules = []` → `Update`, `rules.# = 0`. 4. Back to one rule. | Full-replace semantics: nothing stale survives, and going to zero rules works |
| `TestAccNSG_rulesOrdering` | 1. Create 3 rules with priorities 300, 100, 200 in that order | [G4](../07-provider-gaps-found.md#g4--list-attributes-that-dc-api-might-reorder-medium-needs-verification): the API doesn't sort by priority, or if it does, the provider copes |
| `TestAccNSG_forceNew` | 1. Create. 2. Change `description` → `DestroyBeforeCreate`, new `sg_id` | ForceNew wiring |
| `TestAccNSG_validation` | Plan-only configs: `priority = 50`, `priority = 5000`, `direction = "sideways"`, `protocol = "gre"`, `action = "maybe"`. Each has `ExpectError` | Every `ValidateFunc` fires before any API call |

## Config sketch

```hcl
resource "dcapi_network_security_group" "test" {
  tenant_id   = local.tenant_id
  project_id  = local.project_id
  name        = "<name>"
  description = "acc"

  rules {
    name                       = "allow-https"
    direction                  = "inbound"
    priority                   = 100
    protocol                   = "tcp"
    source_address_prefix      = "10.0.0.0/8"
    source_port_range          = "*"
    destination_address_prefix = "*"
    destination_port_range     = "443"
    action                     = "allow"
  }
  rules {
    name                       = "deny-all-out"
    direction                  = "outbound"
    priority                   = 4096
    protocol                   = "*"
    source_address_prefix      = "*"
    source_port_range          = "*"
    destination_address_prefix = "*"
    destination_port_range     = "*"
    action                     = "deny"
  }
}
```

## Checks

- State: `rules.#`, then every field of `rules.0` and `rules.1`; `sg_id`, `status`, `provider_type` set.
- Update steps: `plancheck.ExpectResourceAction(addr, ResourceActionUpdate)`; `compare.ValuesSame()` on `id`.
- API: `GetNSG` rule count matches (independent of Terraform state, which proves the full replace
  really removed rule B server-side).
- Data source: pair on `sg_id`, `rules.#`, `rules.0.priority`; `attachments.# = 0`.

## Destroy verification

- `CheckDestroy`: `GetNSG` is nil.
- `Disappears`: `DeleteNSG`, then `ExpectNonEmptyPlan`.

## Risks targeted

- API reordering rules (G4).
- A full replace that only adds and never deletes (caught by the API rule-count check).
- Empty `rules` sent as `null` instead of `[]`. The update code sends `[]` explicitly, and step 3 proves it matters.
