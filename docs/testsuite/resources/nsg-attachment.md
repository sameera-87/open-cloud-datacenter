# `dcapi_nsg_attachment`

| | |
|---|---|
| Go file | `internal/acctest/nsg_attachment_test.go`, prefix `TestAccNSGAttachment_` |
| Argo template | `dcapi-acc-nsg-attachment` |
| Credentials | member |
| Creates | Its own NSG, VNet `10.201.0.0/16` and subnet |
| Go timeout | 90m |
| Estimated duration | 30–60 min |

## Facts from the code ([nsg_attachment.go](../../../internal/resources/nsg_attachment.go))

| Property | Value | Test consequence |
|---|---|---|
| Update | None. `sg_id`, `target_type`, `target_id` are all ForceNew | |
| `target_type` | ValidateFunc allows only `"subnet"` | Plan-only negative test |
| Computed | `attachment_id`, `created_at` | |
| Importer | **Yes** (passthrough) | Import step |
| Read | Fetches the parent NSG and searches `attachments[]` for the ID. Clears the ID if the NSG *or* the attachment is gone | Both disappearance paths are tested |
| State ID | `tenant_id/project_id/sg_id/attachment_id` | |
| Ordering rule | The attachment must be deleted before the NSG or subnet ([dc-api-reference §11](../../dc-api-reference.md#11-nsg-attachment-ordering)) | Destroy ordering is verified |

## Dependencies

Each test creates an NSG, its own VNet `10.201.0.0/16` and a subnet `10.201.1.0/24`
([04 §2](../04-test-isolation.md#2-cidr-plan)). The tests run one after another, so they reuse
the same ranges.

## Test cases

| Test | Steps | Proves |
|---|---|---|
| `TestAccNSGAttachment_basic` | 1. Create NSG + subnet + attachment. 2. Import. 3. Add `data "dcapi_network_security_group"` → `attachments.# = 1`. 4. Disappears (delete the attachment only). | Create/Read, import, the data source shows the attachment, 404 handling |
| `TestAccNSGAttachment_parentNSGGone` | 1. Create. 2. Check deletes the **attachment then the NSG** out-of-band → `ExpectNonEmptyPlan` | Read's "parent NSG gone" branch clears the ID instead of erroring |
| `TestAccNSGAttachment_destroyOrder` | 1. Create all three. 2. Framework destroy. `CheckDestroy` asserts NSG, subnet and attachment are all gone | Terraform's dependency order (attachment → NSG/subnet) is honoured, and DC-API accepts it without a 409 |
| `TestAccNSGAttachment_validation` | Plan-only: `target_type = "vm"` → `ExpectError` | ValidateFunc |

## Config sketch

```hcl
resource "dcapi_network_security_group" "test" {
  tenant_id  = local.tenant_id
  project_id = local.project_id
  name       = "<name>"
  rules {
    name                       = "allow-ssh"
    direction                  = "inbound"
    priority                   = 100
    protocol                   = "tcp"
    source_address_prefix      = "10.0.0.0/8"
    source_port_range          = "*"
    destination_address_prefix = "*"
    destination_port_range     = "22"
    action                     = "allow"
  }
}

resource "dcapi_vnet" "parent" {
  tenant_id     = local.tenant_id
  project_id    = local.project_id
  name          = "<name>-vnet"
  address_space = ["10.201.0.0/16"]
  region        = local.region
}

resource "dcapi_subnet" "test" {
  tenant_id  = local.tenant_id
  project_id = local.project_id
  vnet_id    = dcapi_vnet.parent.vnet_uuid
  name       = "<name>"
  cidr       = "10.201.1.0/24"
}

resource "dcapi_nsg_attachment" "test" {
  tenant_id   = local.tenant_id
  project_id  = local.project_id
  sg_id       = dcapi_network_security_group.test.sg_id
  target_type = "subnet"
  target_id   = dcapi_subnet.test.subnet_uuid
}
```

## Checks

- State: `attachment_id` set; `target_type = subnet`; `target_id` paired with `dcapi_subnet.test.subnet_uuid`.
- API: `GetNSG(...).Attachments` contains exactly one entry with the right `target_id`.

## Destroy verification

- `CheckDestroy`: the NSG is gone (`GetNSG` nil). If a parent NSG still existed, its
  `attachments[]` would have to be empty. Subnet CheckDestroy is reused from the subnet tests.
- `Disappears`: `DeleteNSGAttachment(tenant, project, sg, id)`.

## Risks targeted

- Read treating a missing parent NSG as an error, which would break every plan after someone deletes the NSG.
- Delete ordering in the provider, if DC-API rejects deleting a subnet that still has an attachment.
