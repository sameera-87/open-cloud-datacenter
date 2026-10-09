# 06 — Cleanup

The suite creates real VMs, clusters and networks. A leaked cluster holds quota and can make the
next run fail with `quota_exceeded` or a name conflict. Two mechanisms delete what a run
creates, and a third handles the rare case where both fail.

## 1. How resources get deleted

| # | Mechanism | Runs when | Deletes | Misses things when |
|---|---|---|---|---|
| 1 | **Framework destroy** (`terraform-plugin-testing`) | End of every test, pass or fail | Everything that test created | The Go binary is killed (pod eviction, `-test.timeout`, OOM), or the destroy itself errors |
| 2 | **Sweep step** (`onExit` of the master, and of each resource template when submitted on its own) | End of every run, pass or fail | Every object named `acc-<this run>-*`, children before parents | DC-API is down, or an object is stuck in `DELETING` |
| 3 | **Sweep by hand** (§3) | When someone sees a leftover | Every object named `acc-<given run>-*` | — |

In a normal run, mechanism 1 deletes everything and the sweep step finds nothing.

## 2. The sweepers in Go

The sweepers use `terraform-plugin-testing`'s sweeper framework, so they ship in the same
`acctest.test` binary and reuse the same client:

```go
// internal/acctest/sweep.go
func init() {
	resource.AddTestSweepers("dcapi_node_pool",  &resource.Sweeper{Name: "dcapi_node_pool",  F: sweepNodePools})
	resource.AddTestSweepers("dcapi_cluster",    &resource.Sweeper{Name: "dcapi_cluster",    F: sweepClusters,
		Dependencies: []string{"dcapi_node_pool"}})
	resource.AddTestSweepers("dcapi_subnet",     &resource.Sweeper{Name: "dcapi_subnet",     F: sweepSubnets,
		Dependencies: []string{"dcapi_virtual_machine", "dcapi_bastion", "dcapi_cluster",
			"dcapi_private_endpoint", "dcapi_nsg_attachment", "dcapi_route_table_association"}})
	// … one per resource type, full order in §2.2 …
}

// Every sweeper uses this filter.
func shouldSweep(name string) bool {
	run := os.Getenv("DCAPI_ACC_RUN_ID")
	if run == "" {
		return false // no run ID → sweep nothing (safe default)
	}
	return strings.HasPrefix(name, "acc-"+run+"-")
}
```

Run it:

```bash
acctest.test -test.run='^$' -sweep="$DCAPI_ACC_PROJECT_ID" -sweep-allow-failures
```

- **`-sweep` value.** The framework passes it to every sweeper as its `region` argument. We use it
  for the project slug instead, and the sweeper refuses to run if it doesn't equal
  `DCAPI_ACC_PROJECT_ID`. That guards against pointing it at the wrong project.
- **`-sweep-allow-failures`** keeps going after one sweeper fails. A stuck cluster shouldn't stop
  the VNet sweeper from cleaning everything else.
- **Only `acc-<run>-` objects.** The runner SAs (`tf-acc-runner`, `tf-acc-owner`) and anything
  else in the project can never match.

### 2.1 How nameless children are found

Some objects have no name of their own. They're swept through their named parent:

| Child | Found via |
|---|---|
| NSG attachments | `GET` each matching NSG → `attachments[]` → `DeleteNSGAttachment` |
| Route table associations | `GET` each matching route table → `associations[]` → `DeleteRouteTableAssociation` |
| DNS records | `ListDnsRecords` in each matching zone → `DeleteDnsRecord` |
| Node pools | `ListNodePools` of each matching cluster |
| Private endpoints | `ListPrivateEndpoints` of each matching key vault |

Secrets go with their key vault, so there is no separate secret sweeper.

### 2.2 Deletion order

A sweeper runs only after its `Dependencies` finish. Children always go before parents:

```
dcapi_node_pool ─► dcapi_cluster ─────────────────────────────┐
dcapi_virtual_machine ────────────────────────────────────────┤
dcapi_bastion ────────────────────────────────────────────────┤
dcapi_private_endpoint ─┬─► dcapi_key_vault                   │
                        └─────────────────────────────────────┤
dcapi_nsg_attachment ───┬─► dcapi_network_security_group      │
                        └─────────────────────────────────────┤
dcapi_route_table_association ─┬─► dcapi_route_table ─────────┤
                               └──────────────────────────────┼─► dcapi_subnet ─► dcapi_vnet
dcapi_dns_record ─► dcapi_private_dns_zone ───────────────────┤
dcapi_vnet_peering ───────────────────────────────────────────┘
dcapi_service_account   (independent)
```

Async deletes (VM, bastion, cluster, node pool, subnet, VNet, zone, peering) are polled until
404, reusing the timeouts from the resource schemas. Otherwise a VNet delete would race the
subnet deletes still in flight and fail with 409.

### 2.3 Client functions the sweepers need

[internal/client](../../internal/client) has every `Delete*` function, but no `List*` for these
collections. The API has the endpoints ([dc-api-reference.md](../dc-api-reference.md)); the client
doesn't expose them yet:

| New client function | Endpoint |
|---|---|
| `ListVMs(tenant, project)` | `GET /v1/tenants/{t}/projects/{p}/virtual-machines` |
| `ListBastions(tenant, project)` | `GET …/bastions` |
| `ListClusters(tenant, project)` | `GET …/clusters` |
| `ListNodePools(tenant, project, cluster)` | `GET …/clusters/{id}/node-pools` |
| `ListServiceAccounts(tenant, project)` | `GET …/service-accounts` |
| `ListPrivateEndpoints(tenant, project, kv)` | `GET …/keyvaults/{kv_id}/private-endpoints` |

Each needs a unit test against `httptest`, following the pattern in
[client_test.go](../../internal/client/client_test.go).

### 2.4 Special cases

| Case | Handling |
|---|---|
| **Key vault soft-delete** (`soft_delete_days` ≥ 7) | A deleted vault may keep its name reserved. Random names make that harmless. Tests use `soft_delete_days = 7`. Nothing in the provider can purge a soft-deleted vault, so the sweeper doesn't try. |
| **Key vault secret soft-delete** | `DELETE …/secrets/{key}` soft-deletes, and a later `GET` returns 410. Every test uses a random key, so a soft-deleted key never collides. |
| **Resource stuck in `FAILED`** | Deleting it is still attempted. If deletion errors, the sweep log shows it, and it is left for someone to delete by hand. The sweeper never loops forever. |

## 3. Cleaning up by hand

If the sweep log shows failures, or a run was cancelled before its exit step ran, sweep that run
again. The run ID is the last 5 characters of the workflow name (`dcapi-acc-suite-k3f9q` → `k3f9q`).

```bash
make sweep RUN_ID=k3f9q          # locally, see 03 §6
```

A sweep that deleted anything means a test didn't clean up after itself. Even if every test
passed, that's worth a look: often it's a Delete that returns before the object is really gone.
