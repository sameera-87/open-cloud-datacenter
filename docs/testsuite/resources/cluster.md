# `dcapi_cluster` (with the `dcapi_node_pool` chain)

| | |
|---|---|
| Go file | `internal/acctest/cluster_node_pool_test.go`, prefix `TestAccCluster_` |
| Argo template | `dcapi-acc-cluster` |
| Credentials | member |
| Creates | Its own VNet `10.215.0.0/16` and subnet |
| Go timeout | 120m |
| Estimated duration | 60–90 min (the longest resource in a run) |

## Why cluster and node pool are one chained test

This is the suite's one deliberate "reuse the previous resource" chain, the idea from the
original proposal, used where it pays off:

- **A node pool can't exist without a cluster,** and cluster create (up to 30 min) plus delete
  (up to 20 min) is the single most expensive thing in the suite.
- A separate node pool test would need a **second cluster**. That adds roughly 50 minutes to
  the longest resource in the run, and doubles the biggest quota item.

So one `TestCase` does it all, in steps, inside one Terraform state. The framework destroys the
node pools before the cluster, and destroys everything even if a middle step fails. The cost
of chaining is that a cluster failure also skips the node-pool steps. That's acceptable here:
if the cluster can't be created, node pools can't be tested anyway.

## Facts from the code

[cluster.go](../../../internal/resources/cluster.go):

| Property | Value | Test consequence |
|---|---|---|
| Update | None. Every argument is ForceNew, including inline `worker_pools` | Post-create pool changes go through `dcapi_node_pool` |
| Required | `name` (DNS label, ≤ 32 chars), `k8s_version`, `image_name`, `system_pool { size, count (1\|3\|5), disk_gb }` | `count = 1` keeps it small |
| Network | `network_name` **or** `vnet_id` + `subnet_id`, validated in Create (G6) | |
| Computed | `cluster_id`, `status`, `provider_type`, `worker_pool_count`, `total_node_count`, `message`, `created_at`, `kubeconfig` (**sensitive**, fetched after `ACTIVE`) | |
| Read | Sets `tenant_id` from the response body (G5); doesn't refresh `project_id`, `k8s_version`, `image_name`, the network fields or `worker_pools` (G2); re-fetches `kubeconfig` | |
| Async | Create `PENDING → ACTIVE` (30m). Delete polls until 404, with `FAILED` as pending (20m) | |
| Importer | **None, deliberately** | `GET` doesn't return `k8s_version`, `image_name`, the network fields or `worker_pools`. An imported cluster would plan a replacement (G2). Blocked on DC-API |

[node_pool.go](../../../internal/resources/node_pool.go): see [node-pool.md](node-pool.md).

## Dependencies

Its own VNet `10.215.0.0/16` and subnet `10.215.1.0/24`, from `acctest.ConfigNetwork`
([04 §2](../04-test-isolation.md#2-cidr-plan)). Sizes are chosen for the quota budget
([02 §5](../02-authentication-and-test-environment.md#5-quota-budget)): system pool 1 × `medium`
/ 40 GB, node pool `np1` 1 → 2 × `small` / 40 GB, node pool `np2` 1 × `small`.

## Test cases

### `TestAccCluster_withNodePool`

| Step | Config change | Assertions | Proves |
|---|---|---|---|
| 1 | Cluster only | `status = ACTIVE`; `kubeconfig` contains `apiVersion:`; `total_node_count = 1`; `worker_pool_count = 0`; `system_pool.0.disk_gb = 40`; no `tenant_id` diff | Cluster create, polling, kubeconfig fetch, G5 |
| 2 | + `dcapi_node_pool.np1` (1 × small, `disk_gb = 40`, `image_name` set, a label, a taint) | `status = "ready"` ([G9](../07-provider-gaps-found.md#g9--node-pool-status-vocabulary-differs-from-other-resources-low-verify)); `role` set; `node_pool_id` set; taint and label round-trip | Node pool create against a live cluster |
| 3 | `node_count = 2`, change the label, remove the taint | `plancheck` **Update** on the pool, **no-op** on the cluster; `node_count = 2`; `taints.# = 0` | In-place scale, taint and label update (waiter handles `scaling → ready`) |
| 4 | Import `dcapi_node_pool.np1` | `ImportStateVerify` | Import ID `tenant/project/cluster/pool-name` |
| 5 | `RefreshState: true` | `kubeconfig` still set; cluster `total_node_count = 3` (1 system + 2 workers) | Read re-fetches kubeconfig; computed counts refresh |
| 6 | + `dcapi_node_pool.np2` with **no** `disk_gb` and **no** `image_name` | Post-apply plan must be **empty** | [G3](../07-provider-gaps-found.md#g3--node_pooldisk_gb-and-image_name-are-optional-but-not-computed-high). Placed last so a failure here doesn't hide steps 1–5 |
| destroy | (framework) | `CheckDestroy`: both pools and the cluster return 404 | Delete ordering (pools → cluster), delete waiters |

### Other tests

| Test | Steps | Proves |
|---|---|---|
| `TestAccCluster_nodePoolValidation` | Plan-only `dcapi_node_pool` with `disk_gb = 30` → `ExpectError` (`IntAtLeast(40)`) | ValidateFunc. Needs no cluster, because the `cluster_id` can be a dummy in plan-only mode |

Inline `worker_pools` on `dcapi_cluster` are not tested. That would need a second cluster,
which adds about 50 minutes to the run. Add it later as a `TestAccCluster_inlineWorkerPools`
test if inline pools become important.

## Config sketch (step 2)

```hcl
resource "dcapi_cluster" "test" {
  tenant_id   = local.tenant_id
  project_id  = local.project_id
  name        = "<name>"                       # ≤ 30 chars from RandomName
  k8s_version = "<DCAPI_ACC_K8S_VERSION>"
  image_name  = "<DCAPI_ACC_CLUSTER_IMAGE>"
  vnet_id     = dcapi_vnet.parent.vnet_uuid        # from acctest.ConfigNetwork
  subnet_id   = dcapi_subnet.parent.subnet_uuid

  system_pool {
    size    = "medium"
    count   = 1
    disk_gb = 40
  }
}

resource "dcapi_node_pool" "np1" {
  tenant_id  = local.tenant_id
  project_id = local.project_id
  cluster_id = dcapi_cluster.test.cluster_id
  name       = "np1"
  size       = "small"
  node_count = 1
  disk_gb    = 40
  image_name = "<DCAPI_ACC_CLUSTER_IMAGE>"
  labels     = { "acc/pool" = "np1" }
  taints {
    key    = "acc/dedicated"
    value  = "true"
    effect = "NoSchedule"
  }
}
```

## Destroy verification

- `CheckDestroy`: `GetNodePool(tenant, project, cluster, name)` and `GetCluster` are nil.
- There's no Disappears step. An out-of-band delete of a cluster costs another 20 minutes, and
  proves the same 404 handling that cheaper resources already prove.

## Risks targeted

- G3 (perpetual ForceNew replacement of node pools), G5, G9.
- Cluster delete finishing before its node pools, if pools were created outside Terraform. Not
  tested; documented as out of scope.
- `kubeconfig` fetch failing on a cluster that just became `ACTIVE` (a race).
