# `dcapi_node_pool`

| | |
|---|---|
| Go file | `internal/acctest/cluster_node_pool_test.go` (shared with cluster) |
| Argo template | `dcapi-acc-node-pool`. **Submitted on its own only; not in the master** |
| Credentials | member |
| Creates | Its parent cluster, VNet and subnet (inside the cluster chain) |
| Go timeout | 120m |
| Estimated duration | runs inside the cluster chain (steps 2–6) |

## Where this resource is tested

Node pools are tested **inside `TestAccCluster_withNodePool`**, steps 2–6. The reasons are in
[cluster.md § Why cluster and node pool are one chained test](cluster.md#why-cluster-and-node-pool-are-one-chained-test).
In short, a second cluster just for node pools would double the most expensive part of the run.

There is still a per-resource WorkflowTemplate, `dcapi-acc-node-pool`, as with every other
resource. Its regex is `^TestAccCluster_withNodePool$`, so a developer working on node-pool
code can submit it on its own. It is **left out of `dcapi-acc-suite`**: the `cluster` task
already runs the same test, and listing both would build two clusters in every run.

## Facts from the code ([node_pool.go](../../../internal/resources/node_pool.go))

| Property | Value | Test consequence |
|---|---|---|
| Update | `node_count`, `taints`, `labels`, through `PATCH` | Step 3 of the chain |
| ForceNew | `tenant_id`, `project_id`, `cluster_id`, `name` (not `"system"`), `size`, `disk_gb` (≥ 40), `image_name` | |
| `disk_gb` / `image_name` | Optional **without** Computed, but Read writes the API value ([G3](../07-provider-gaps-found.md#g3--node_pooldisk_gb-and-image_name-are-optional-but-not-computed-high)) | Step 6 omits both and expects an empty plan |
| Computed | `node_pool_id`, `role`, `status`, `message`, `created_at` | |
| Status vocabulary | Waiter: `provisioning`/`scaling` → `ready`; delete: → 404 ([G9](../07-provider-gaps-found.md#g9--node-pool-status-vocabulary-differs-from-other-resources-low-verify)) | Step 2 asserts `status = ready` |
| Timeouts | Create 15m, Update 15m, Delete 10m | |
| Importer | **Yes** (passthrough) | Step 4 |
| State ID | `tenant_id/project_id/cluster_id/pool_name`. Keyed by **name**, not UUID | Import uses the name |

## Test cases (summary; full table in [cluster.md](cluster.md#testacccluster_withnodepool))

| Chain step | What it proves for node pools |
|---|---|
| 2 | Create against a live cluster; taint and label round-trip; `ready` status |
| 3 | In-place scale 1 → 2 and taint/label changes are an **Update** (not a replace); the waiter handles `scaling` |
| 4 | Import by `tenant/project/cluster/name` reproduces state |
| 6 | Omitting `disk_gb` / `image_name` doesn't cause a perpetual ForceNew diff (G3) |
| destroy | Pools are deleted before the cluster and each returns 404 |

`TestAccCluster_nodePoolValidation`: plan-only `disk_gb = 30` → `ExpectError`.

## Example fix needed

[examples/node_pool/main.tf](../../../examples/node_pool/main.tf) uses `count = 2` (Terraform's
meta-argument) instead of `node_count`, and attribute syntax for `taints`. It won't validate
([G8](../07-provider-gaps-found.md#g8--examplesnode_poolmaintf-is-invalid-low)). The test config
in cluster.md shows the correct form.
