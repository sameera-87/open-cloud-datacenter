# Per-Resource Test Plans

One document per resource. Each follows the same structure, so they can be compared side by side:

1. **Summary card:** Go test file, Argo template, credentials, what the tests create, Go timeout, estimated duration.
2. **Facts from the code:** the schema properties that decide which test steps apply
   (updatable vs ForceNew fields, importer, async timeouts, sensitive fields, state ID format),
   taken from `internal/resources/<name>.go`.
3. **Dependencies:** the parent objects each test creates for itself, and its CIDR range ([04](../04-test-isolation.md)).
4. **Test cases:** each `TestAcc*` function and its step-by-step purpose.
5. **Config sketch:** the HCL the test's config function produces.
6. **Checks:** Terraform-state assertions and independent API assertions.
7. **Destroy verification:** how `CheckDestroy` and `Disappears` are implemented.
8. **Risks targeted:** which likely bugs this test exists to catch (links to [07](../07-provider-gaps-found.md)).

Every test follows the standard anatomy in [03 §5](../03-acceptance-test-framework.md#5-the-standard-test-anatomy).
Every resource name comes from `acctest.RandomName(<short>)`, so the config sketches write it as
`<name>`. `local.*` refers to the `ConfigBase()` locals, and `dcapi_vnet.parent` /
`dcapi_subnet.parent` come from `ConfigNetwork()` ([03 §4.4](../03-acceptance-test-framework.md#44-config-composition--configgo)).

## Index

| Resource | Doc | Short name | Argo template | Est. duration |
|---|---|---|---|---|
| `dcapi_vnet` | [vnet.md](vnet.md) | `vnet` | `dcapi-acc-vnet` | 15–25 min |
| `dcapi_subnet` | [subnet.md](subnet.md) | `sn` | `dcapi-acc-subnet` | 30–60 min |
| `dcapi_network_security_group` | [network-security-group.md](network-security-group.md) | `nsg` | `dcapi-acc-nsg` | 3–6 min |
| `dcapi_nsg_attachment` | [nsg-attachment.md](nsg-attachment.md) | `nsga` | `dcapi-acc-nsg-attachment` | 30–60 min |
| `dcapi_route_table` | [route-table.md](route-table.md) | `rt` | `dcapi-acc-route-table` | 15–30 min |
| `dcapi_route_table_association` | [route-table-association.md](route-table-association.md) | `rta` | `dcapi-acc-route-table-association` | 30–60 min |
| `dcapi_vnet_peering` | [vnet-peering.md](vnet-peering.md) | `peer` | `dcapi-acc-vnet-peering` | 20–40 min |
| `dcapi_private_dns_zone` | [private-dns-zone.md](private-dns-zone.md) | `dz` | `dcapi-acc-private-dns-zone` | 10–20 min |
| `dcapi_dns_record` | [dns-record.md](dns-record.md) | `dr` | `dcapi-acc-dns-record` | 20–35 min |
| `dcapi_key_vault` | [key-vault.md](key-vault.md) | `kv` | `dcapi-acc-key-vault` | 10–20 min |
| `dcapi_key_vault_secret` | [key-vault-secret.md](key-vault-secret.md) | `kvs` | `dcapi-acc-key-vault-secret` | 15–25 min |
| `dcapi_private_endpoint` | [private-endpoint.md](private-endpoint.md) | `pe` | `dcapi-acc-private-endpoint` | 25–45 min |
| `dcapi_virtual_machine` | [virtual-machine.md](virtual-machine.md) | `vm` | `dcapi-acc-virtual-machine` | 40–70 min |
| `dcapi_bastion` | [bastion.md](bastion.md) | `bas` | `dcapi-acc-bastion` | 45–75 min |
| `dcapi_cluster` | [cluster.md](cluster.md) | `cl` | `dcapi-acc-cluster` | 60–90 min |
| `dcapi_node_pool` | [node-pool.md](node-pool.md) | `np` | `dcapi-acc-node-pool` (not in the master) | inside the cluster chain |
| `dcapi_service_account` | [service-account.md](service-account.md) | `sa` | `dcapi-acc-service-account` | 3–8 min |
| data sources | [data-sources.md](data-sources.md) | — | `dcapi-acc-data-sources` | 1–2 min |

Durations are estimates until the first real run. All resources run in parallel, so a full run
takes about as long as the slowest one.
