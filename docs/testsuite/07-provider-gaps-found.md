# 07 — Provider Gaps Found During Planning

Planning the tests meant reading every resource's schema, Create, Read and Update function. That
turned up the issues below. Each one is either a **likely bug the suite will catch** (and the test
that catches it) or a **missing feature that limits what can be tested** (and the plan's
workaround).

Severity: **High**, users will hit it · **Medium**, wrong under specific conditions · **Low**, quality or ergonomics.

## G1 — Resources without an importer (Medium, mostly fixed)

**Status (2026-09-28):** passthrough importers were added to `vnet`, `subnet`, `vnet_peering`,
`private_dns_zone`, `dns_record`, `key_vault`, `key_vault_secret`, `private_endpoint` and
`bastion`. With the six that already had one, **15 of 17** in-scope resources are importable.
The import step in each resource's basic test is part of the plan (✓ in
[01 §6](01-scope-and-strategy.md#6-coverage-matrix-what-every-resource-gets)).

**Still without an importer, deliberately:** `virtual_machine` and `cluster`. See G2. Adding one
there would be worse than having none, because an imported VM or cluster would immediately plan
a destroy-and-recreate.

Import notes per resource:

| Resource | `ImportStateVerifyIgnore` | Why |
|---|---|---|
| `key_vault` | `secret_id`, `role_id`, `credentials_rotation` | Credentials are shown once. After import, set `credentials_rotation` to mint new ones |
| `bastion` | `private_key`, `console_password` | Shown once, never returned by `GET` |
| `service_account` (existing) | `token`, `last_used` | Token shown once; `last_used` changes on use |
| all others | none | Read sets every argument from the API or the state ID |

## G2 — Read can't refresh every configured field (Medium)

| Resource | Fields Read never sets | Why | Status |
|---|---|---|---|
| `virtual_machine` ([vm.go:245](../../internal/resources/vm.go#L245)) | `disk_gb`, `image_name`, `network_name`, `vnet_id`, `subnet_id` | `VMReadResponse` doesn't contain them. **DC-API doesn't return them** | Open. Needs a DC-API change |
| `cluster` ([cluster.go:314](../../internal/resources/cluster.go#L314)) | `k8s_version`, `image_name`, `worker_pools`, `network_name`, `vnet_id`, `subnet_id` (+ `project_id`) | `ClusterReadResponse` doesn't contain them. **DC-API doesn't return them** | Open. Needs a DC-API change |
| `bastion` ([bastion.go:185](../../internal/resources/bastion.go#L185)) | `project_id` | Parsed from the state ID | **Fixed.** Read now sets `tenant_id` and `project_id` from the state ID. This also removes bastion's instance of G5 |

- **Consequence:** normal tests still pass, because the values stay in state from Create. But
  out-of-band changes to these fields are invisible, and import is impossible without an
  immediate replacement.
- **Fix:** DC-API should include the create-time settings in `GET /virtual-machines/{id}` and
  `GET /clusters/{id}`. Then map them in Read and add the importers.

## G3 — `node_pool.disk_gb` and `image_name` are Optional but not Computed (High)

[node_pool.go:79-90](../../internal/resources/node_pool.go#L79) declares both as `Optional` +
`ForceNew` without `Computed`, but Read writes whatever the API returns
([node_pool.go:229-230](../../internal/resources/node_pool.go#L229)). If a user omits `disk_gb`
or `image_name` and DC-API fills in a default (for example the cluster's image), the next plan
sees config `null` against state `"rancher-infra/…"` on a **ForceNew** attribute. Terraform then
wants to **destroy and recreate the node pool on every apply**.

- **Caught by:** `TestAccCluster_withNodePool`, step 6 (a pool with neither field set; the
  post-apply plan must be empty). See [resources/node-pool.md](resources/node-pool.md).
- **Fix:** add `Computed: true` to both, as `cluster.system_pool.disk_gb` already does.

## G4 — List attributes that DC-API might reorder (Medium, needs verification)

These are `TypeList`, so order matters to Terraform. If DC-API returns them sorted differently
from how they were sent, every plan shows a diff:

| Attribute | File |
|---|---|
| `network_security_group.rules` | [nsg.go:59](../../internal/resources/nsg.go#L59). The API may sort by `priority` |
| `route_table.routes` | [route_table.go:66](../../internal/resources/route_table.go#L66) |
| `dns_record.values` | [dns_record.go:67](../../internal/resources/dns_record.go#L67). DNS servers often sort RRsets |
| `vnet.address_space` | [vnet.go:41](../../internal/resources/vnet.go#L41) |

- **Caught by:** the `*_ordering` tests in each resource doc. They deliberately send elements
  **out of natural order** (rules by descending priority, values reverse-sorted), so the
  empty-plan check fails if DC-API reorders them.
- **Fix, if caught:** switch to `TypeSet` where order has no meaning (DNS values), or sort
  consistently in both expand and flatten (NSG rules by priority).

## G5 — `tenant_id` overwritten from the API response (Medium, needs verification)

Read in `cluster` and `service_account` (bastion fixed, see G2) sets `tenant_id` from the response body
(`cluster.TenantID`, `bastion.TenantID`, `sa.TenantID`). Other resources set it from the state
ID. `tenant_id` is `ForceNew`. If the API returns the tenant **UUID** where the config has the
**slug**, every plan wants to replace the resource.

- **Caught by:** the empty-plan check in each of those resources' basic tests.
- **Fix, if caught:** set `tenant_id` from the parsed state ID, as the other resources do.

## G6 — Network-mode validation happens at apply time, not plan time (Low)

The rule "`network_name` XOR (`vnet_id` + `subnet_id`)" is checked inside Create for
`virtual_machine` ([vm.go:171](../../internal/resources/vm.go#L171)) and `cluster`
([cluster.go:240](../../internal/resources/cluster.go#L240)). `terraform plan` reports success,
and the error appears only during `apply`.

- **Plan:** `TestAccVirtualMachine_invalidNetwork` asserts the error message during apply. It
  makes no API call, because the check runs before the request. Once the fix lands, the test
  switches to `PlanOnly: true`.
- **Fix:** move it to `CustomizeDiff`, as `route_table` already does for `next_hop_ip`
  ([route_table.go:244](../../internal/resources/route_table.go#L244)).

## G7 — `dcapi_key_vault` exposes no vault UUID (Low)

Children (`key_vault_secret.key_vault_id`, `private_endpoint.kv_id`) need the vault's UUID, but
the resource has no computed attribute for it. The current workaround is
`element(split("/", dcapi_key_vault.x.id), 2)`
([examples/private_endpoint](../../examples/private_endpoint/main.tf)). It depends on the
internal ID format.

- **Plan:** tests use the same `split` so they match what users do today.
- **Fix:** add a computed `kv_uuid` (the name the data source already uses) and update the
  examples.

## G8 — `examples/node_pool/main.tf` is invalid (Low)

The example uses `count = 2` and `taints = [ { … } ]`:
- `count` is Terraform's **meta-argument**, not the schema's `node_count`.
- `taints` is a nested block (`TypeList` of `*schema.Resource` without `ConfigMode: attr`), so it
  must be written as `taints { … }` blocks.

`terraform validate` would reject the example.

- **Plan:** acceptance tests don't use the examples. A `terraform validate` check over every
  `examples/*` directory is listed as a later addition in [08](08-rollout-plan.md#later-if-needed).

## G9 — Node pool status vocabulary differs from other resources (Low, verify)

Node pool waiters use lowercase `provisioning` / `scaling` / `ready`
([node_pool.go:298](../../internal/resources/node_pool.go#L298)). Every other resource uses
`PENDING` / `ACTIVE`. That may be correct (it mirrors Rancher), but if DC-API actually returns
`ACTIVE`, the create waiter would time out after 15 minutes.

- **Caught by:** step 2 of `TestAccCluster_withNodePool`, which asserts `status = "ready"`. It
  will fail fast if the vocabulary is wrong.

## G10 — Subnet delete timeout is shorter than the documented last-subnet teardown (Medium, fixed)

**Status (2026-09-29):** fixed. The subnet delete default is now 15m.

[subnet.go](../../internal/resources/subnet.go) defaults to `Delete: 10m`. The API reference
recommends **15 min** when deleting the *last* subnet in a VNet, because DC-API also tears down
the per-VPC NAT gateway and CoreDNS
([dc-api-reference §2 and §9](../dc-api-reference.md#2-async-polling)). A user destroying a small
VPC (one subnet) can hit `timeout while waiting for state to become 'DELETED'` while DC-API is
still working. That leaves the VNet orphaned in state.

- **Caught by:** the subnet tests, which keep the **default** timeout on purpose. Every test with a
  subnet deletes the last subnet in its own VNet ([04 §3](04-test-isolation.md#3-the-last-subnet-delete)), so fix this before the first run.
- **Fix:** raise the subnet delete default to 15m. Users can still override it with a `timeouts` block.

## G11 — `private_endpoint` has a `status` but no waiter (Medium, verify)

[private_endpoint.go](../../internal/resources/private_endpoint.go) defines no `Timeouts` and
doesn't poll. The API reference says create is synchronous (201), yet the response carries
`status` and `message`. If DC-API provisions the VIP and hostname asynchronously, `apply`
reports success while the endpoint is still `PENDING`, and anything depending on `ip_address`
or `hostname` may get empty values.

- **Caught by:** `TestAccPrivateEndpoint_basic`, which asserts `status = ACTIVE` and a non-empty `ip_address` right after apply.
- **Fix, if caught:** add a `PENDING → ACTIVE` waiter like the other async resources.

## G12 — Removing `credentials_rotation` rotates the credentials (Low, decision needed)

`credentials_rotation` is Optional and not Computed. Deleting it from the config makes
`d.HasChange("credentials_rotation")` true (`"r2"` → `""`), so
[resourceKeyVaultUpdate](../../internal/resources/key_vault.go) calls rotate and **invalidates
the current `secret_id`**. Someone tidying up the config would break every client still using
the old secret.

- **Decide:** is removal a rotation, like `null_resource` triggers, or a no-op? A no-op is the
  safer default. Implement it by rotating only when the new value is non-empty.
- **Caught by:** `TestAccKeyVault_rotation` step 3. Until this is decided, the test pins today's
  behaviour (removal rotates, so `secret_id` changes).

## G13 — Delete fails when the object is already gone (Medium, fixed)

**Found by:** every `Disappears` step, when the suite was first run against an in-memory fake API.

Every resource's Delete returned an error when DC-API answered 404. So `terraform destroy` failed
for anything deleted outside Terraform unless a refresh ran first, and the test framework destroys
with `-refresh=false`, which made all 17 tests with a Disappears step fail.

- **Fix (2026-09-29):** every Delete now treats 404 as "already gone" (`isNotFound` in
  [helpers.go](../../internal/resources/helpers.go)), and `key_vault_secret` also treats 410 that
  way. Covered by `TestResourceDelete_NotFoundIsSuccess` in
  [delete_not_found_test.go](../../internal/resources/delete_not_found_test.go).

## G14 — Removing the last node-pool taint or label never reaches DC-API (High, fixed)

**Found by:** `TestAccCluster_withNodePool` step 3 (remove the taint), against the fake API.

`resourceNodePoolUpdate` deliberately sends `taints = []` and `labels = {}` to clear them
(full-replace semantics), but `NodePoolUpdateRequest` tagged both fields `omitempty`, so empty
values were dropped from the PATCH body. Removing the last taint looked successful in Terraform
while DC-API kept it, and the next plan showed the diff again.

- **Fix (2026-09-29):** `omitempty` removed from `Taints` and `Labels` in
  [client/node_pool.go](../../internal/client/node_pool.go).
- **Verify on the first real run:** that DC-API accepts `"taints": []` and `"labels": {}` on PATCH.

## G15 — Small import mismatches (Low)

Found by the import steps against the fake API. Neither causes a plan diff.

- `dcapi_route_table`: Create leaves `description` unset when it isn't configured, while Read sets
  it to `""`. The tests set a description, so the import comparison is stable.
- `dcapi_route_table_association`: `warning` comes only from the create response, so an import
  can't restore it. The import step ignores it.

## Summary

| ID | Severity | Blocks a test? | Test that surfaces it |
|---|---|---|---|
| G1 | Medium | Import for VM and cluster only (fixed for the other 9) | Import steps in each basic test |
| G2 | Medium | Import for VM and cluster (bastion fixed) | — (needs DC-API change) |
| G3 | High | No | `TestAccCluster_withNodePool` step 6 |
| G4 | Medium | No | `TestAccNSG_rulesOrdering`, `TestAccRouteTable_routesOrdering`, `TestAccDNSRecord_valuesOrdering`, `TestAccVNet_multiAddressSpace` |
| G5 | Medium | No | basic tests of cluster, bastion, service_account |
| G6 | Low | No | `TestAccVirtualMachine_invalidNetwork` |
| G7 | Low | No | — (workaround) |
| G8 | Low | No | — (a later `terraform validate` check) |
| G9 | Low | No | `TestAccCluster_withNodePool` step 2 |
| G10 | Medium | Fixed | `TestAccSubnet_*` |
| G11 | Medium | No | `TestAccPrivateEndpoint_basic` |
| G12 | Low | Needs a decision | `TestAccKeyVault_rotation` step 3 |
| G13 | Medium | Fixed | every Disappears step |
| G14 | High | Fixed | `TestAccCluster_withNodePool` step 3 |
| G15 | Low | No | import steps of route_table and route_table_association |
