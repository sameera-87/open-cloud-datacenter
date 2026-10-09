# Data Sources

| | |
|---|---|
| Go file | Resource-backed data sources are tested **inside their resource's test file**. The standalone ones are in `internal/acctest/data_sources_test.go`, prefix `TestAccDataSource_` |
| Argo template | `dcapi-acc-data-sources` |
| Credentials | member |
| Go timeout | 10m |
| Estimated duration | 1–2 min |

## Two kinds, two strategies

**Resource-backed data sources** read something the suite creates (VNet, subnet, NSG, …). They're
tested in the same `TestCase` that creates the object: a later step adds a `data` block that
looks the object up **by name**, then `TestCheckResourceAttrPair` compares it with the managed
resource. This is the strongest check available. It proves the data source finds the right
object and maps every field the same way the resource does, without creating anything extra.

**Platform data sources** (`dcapi_region`, `dcapi_image`) and **scope data sources**
(`dcapi_project`, `dcapi_tenant`) read things that exist before the suite runs. They're tested
standalone and create nothing.

## Coverage

| Data source | Lookup | Tested in | Assertions |
|---|---|---|---|
| `dcapi_vnet` | `name` | [vnet.md](vnet.md) `TestAccVNet_basic` step 2 | Pair `vnet_uuid`, `address_space.0`, `region`, `description` |
| `dcapi_subnet` | `vnet_id` + `name` | [subnet.md](subnet.md) `TestAccSubnet_basic` step 2 | Pair `subnet_uuid`, `cidr`, `gateway` |
| `dcapi_network_security_group` | `name` | [network-security-group.md](network-security-group.md) basic, and [nsg-attachment.md](nsg-attachment.md) basic | Pair `sg_id`, `rules.#`; `attachments.#` 0 then 1 |
| `dcapi_route_table` | `vnet_id` + `name` | [route-table.md](route-table.md) basic | Pair `route_table_id`, `routes.#` |
| `dcapi_vnet_peering` | `vnet_id` + `name` | [vnet-peering.md](vnet-peering.md) basic | Pair `peering_id`, `peer_vnet_id`, `status` |
| `dcapi_private_dns_zone` | `vnet_id` + `name` | [private-dns-zone.md](private-dns-zone.md) basic | Pair `zone_id`, `description` |
| `dcapi_dns_record` | zone + `name` + `type` | [dns-record.md](dns-record.md) basic | Pair `record_id`, `values.#`, `ttl` |
| `dcapi_key_vault` | `name` | [key-vault.md](key-vault.md) basic | `kv_uuid` equals the vault ID segment; pair `mount_path`, `soft_delete_days` |
| `dcapi_region` | `name` | `TestAccDataSource_region` | `name = local.region`; `status` set; `zones.#` > 0 |
| `dcapi_image` | `display_name` (tenant-scoped) | `TestAccDataSource_image` | Looks up `DCAPI_ACC_VM_IMAGE_DISPLAY_NAME`. `image_id` is set, and `namespace` equals the namespace part of `DCAPI_ACC_VM_IMAGE` (`rancher-infra`). The lookup key is a *display name*, while resources take `namespace/name`, so both env values are needed |
| `dcapi_project` | `tenant_id` + `project_id` | `TestAccDataSource_project` | `project_id`, `project_uuid` set; quota fields readable. Skipped with a reason if the member SA gets 403 |
| `dcapi_tenant` | `id` | `TestAccDataSource_tenant` | `id = local.tenant_id`. **Expected to be skipped**: a project-scoped SA probably can't read the tenant. The test records the 403 and skips, so the permission model stays visible |

## Standalone tests

| Test | What it does |
|---|---|
| `TestAccDataSource_region` | Reads `DCAPI_ACC_REGION`. Also acts as a cheap end-to-end auth check |
| `TestAccDataSource_image` | Reads the VM and cluster images that the compute tests depend on. If this fails, expect the VM and cluster tests in the same run to fail too, and fix the image parameters first |
| `TestAccDataSource_project` | See table. Also logs the project's quota fields, which helps explain a `quota_exceeded` elsewhere in the run |
| `TestAccDataSource_tenant` | See table |
| `TestAccDataSource_notFound` | `data "dcapi_vnet" { name = "acc-does-not-exist-<rand>" }` → `ExpectError` with a "not found" message. Proves lookups fail loudly instead of returning an empty object |

## Config sketch

```hcl
data "dcapi_region" "test" {
  name = local.region
}

data "dcapi_image" "vm" {
  tenant_id    = local.tenant_id
  display_name = "<DCAPI_ACC_VM_IMAGE_DISPLAY_NAME>"
}
```

## Risks targeted

- Lookup by name matching more than one object, or the wrong one. Paired checks against the managed resource catch a wrong match.
- Field mapping drifting between a resource and its data source. They use different flatten code paths, and paired checks catch the drift.
- A permission-model regression. The project and tenant read tests record 403s explicitly, so a change in what a project SA may read shows up as a test-status change.
