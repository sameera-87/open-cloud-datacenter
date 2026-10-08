package acctest

// Plan: docs/testsuite/resources/cluster.md and resources/node-pool.md
//
// The cluster and its node pools are one chained test: a node pool can't exist without a
// cluster, and a second cluster would add about 50 minutes to the run. No cluster import step:
// the GET doesn't return k8s_version, image_name, the network fields or worker_pools (G2).

import (
	"fmt"
	"regexp"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/plancheck"
)

// TestAccCluster_withNodePool is the suite's one deliberate chained test: it creates a cluster
// once and then exercises node pools against it, because a node pool can't exist without a
// cluster and a second cluster would add ~50 minutes to the run. There is no cluster import step
// because the GET doesn't return k8s_version, image_name, the network fields or worker_pools (G2).
//
// PASSES when: the cluster reaches ACTIVE with a kubeconfig, correct node counts and no tenant_id
// drift (G5); node pool np1 creates ready with its label/taint round-tripping; scaling np1 is an
// in-place Update (cluster no-op) that applies the new count/label and drops the taint; np1
// imports cleanly by tenant/project/cluster/name; a refresh refreshes the cluster's counts and
// re-fetches the kubeconfig; np2 with disk_gb/image_name omitted leaves an empty plan (G3); and at
// teardown both pools and the cluster return 404.
// FAILS when: the cluster or a pool never reaches its ready state, kubeconfig is missing, a count
// is wrong, tenant_id drifts to the API UUID, a scale is planned as a replace instead of Update,
// the cluster is disturbed by a pool change, import drops a field, or omitting the optional pool
// fields produces a perpetual diff.
func TestAccCluster_withNodePool(t *testing.T) {
	name := RandomName("cl")
	clusterImage := RequireEnv(t, "DCAPI_ACC_CLUSTER_IMAGE")
	k8sVersion := RequireEnv(t, "DCAPI_ACC_K8S_VERSION")
	cl := "dcapi_cluster.test"
	np1 := "dcapi_node_pool.np1"

	cluster := testAccClusterConfig(name, clusterImage, k8sVersion)
	pool1 := testAccNodePool("np1", "np1", 1, clusterImage, true, "np1")
	pool1Scaled := testAccNodePool("np1", "np1", 2, clusterImage, false, "np1-scaled")
	// G3: disk_gb and image_name omitted. Read writes the API values, so the plan must still be empty.
	pool2 := `
resource "dcapi_node_pool" "np2" {
  tenant_id  = local.tenant_id
  project_id = local.project_id
  cluster_id = dcapi_cluster.test.cluster_id
  name       = "np2"
  size       = "small"
  node_count = 1
}
`

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { PreCheck(t) },
		ProtoV5ProviderFactories: ProviderFactories,
		// CheckDestroy fails the test unless, after teardown, the node pools, cluster, subnet and
		// VNet all return 404 — proving delete ordering (pools → cluster) and the delete waiters.
		CheckDestroy: resource.ComposeTestCheckFunc(
			CheckDestroy("dcapi_node_pool", NodePoolExists),
			CheckDestroy("dcapi_cluster", ClusterExists),
			CheckDestroy("dcapi_subnet", SubnetExists),
			CheckDestroy("dcapi_vnet", VNetExists),
		),
		Steps: []resource.TestStep{
			{
				// Step 1 — Cluster only: create the cluster and assert the Create/Read round-trip.
				// Passes only if status is ACTIVE, kubeconfig was fetched (contains apiVersion:),
				// the computed counts are 1 total / 0 worker pools, system_pool disk_gb is 40, and
				// tenant_id is not overwritten from the API response (G5).
				Config: cluster,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(cl, "status", "ACTIVE"),
					resource.TestMatchResourceAttr(cl, "kubeconfig", regexp.MustCompile(`apiVersion:`)),
					resource.TestCheckResourceAttr(cl, "total_node_count", "1"),
					resource.TestCheckResourceAttr(cl, "worker_pool_count", "0"),
					resource.TestCheckResourceAttr(cl, "system_pool.0.disk_gb", "40"),
					resource.TestCheckResourceAttr(cl, "tenant_id", TenantID()), // G5
				),
			},
			{
				// Step 2 — Add node pool np1 (disk, image, a label and a taint) against the live
				// cluster. Passes only if the pool reaches "ready" (G9 — its status vocabulary
				// differs from other resources), role and node_pool_id are computed, and the label
				// and NoSchedule taint round-trip through Create/Read.
				Config: cluster + pool1,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(np1, "status", "ready"), // G9
					resource.TestCheckResourceAttrSet(np1, "role"),
					resource.TestCheckResourceAttrSet(np1, "node_pool_id"),
					resource.TestCheckResourceAttr(np1, "labels.acc/pool", "np1"),
					resource.TestCheckResourceAttr(np1, "taints.#", "1"),
					resource.TestCheckResourceAttr(np1, "taints.0.key", "acc/dedicated"),
					resource.TestCheckResourceAttr(np1, "taints.0.effect", "NoSchedule"),
				),
			},
			{
				// Step 3 — Scale np1 to 2, change the label, remove the taint. This must be an
				// in-place Update of the pool (PATCH) with no change to the cluster: the plan check
				// requires Update on np1 and Noop on the cluster. Passes only if node_count is 2,
				// the new label is stored and the taint list is empty; fails if the change is
				// planned as a replace or disturbs the cluster.
				Config: cluster + pool1Scaled,
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{
						plancheck.ExpectResourceAction(np1, plancheck.ResourceActionUpdate),
						plancheck.ExpectResourceAction(cl, plancheck.ResourceActionNoop),
					},
				},
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(np1, "node_count", "2"),
					resource.TestCheckResourceAttr(np1, "labels.acc/pool", "np1-scaled"),
					resource.TestCheckResourceAttr(np1, "taints.#", "0"),
				),
			},
			{
				// Step 4 — Import np1 by its state ID (tenant/project/cluster/pool-name, keyed by
				// name not UUID). ImportStateVerify fails the step if any field Read sets differs
				// after a fresh import.
				ResourceName:      np1,
				ImportState:       true,
				ImportStateVerify: true,
			},
			{
				// Step 5 — Refresh only: Read must re-fetch the kubeconfig and refresh the cluster's
				// computed counts to 3 (1 system + 2 workers from the scaled pool). Fails if the
				// kubeconfig is dropped or the count isn't refreshed.
				RefreshState: true,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestMatchResourceAttr(cl, "kubeconfig", regexp.MustCompile(`apiVersion:`)),
					resource.TestCheckResourceAttr(cl, "total_node_count", "3"),
				),
			},
			{
				// Step 6 — Add node pool np2 with disk_gb and image_name omitted (G3): Read writes
				// the API values, so the post-apply plan must still be empty and the pool must reach
				// "ready". Placed last so a G3 perpetual-diff failure here doesn't hide steps 1–5.
				Config: cluster + pool1Scaled + pool2,
				Check:  resource.TestCheckResourceAttr("dcapi_node_pool.np2", "status", "ready"),
			},
		},
	})
}

// TestAccCluster_nodePoolValidation checks the dcapi_node_pool schema ValidateFunc without
// building anything: a plan-only config with disk_gb below the minimum must be rejected at plan
// time. It needs no real cluster because the cluster_id can be a dummy UUID in PlanOnly mode.
//
// PASSES when: planning disk_gb = 30 fails with an error matching "to be at least" (the
// IntAtLeast(40) validator firing).
// FAILS when: the config plans successfully, or the error message doesn't match.
func TestAccCluster_nodePoolValidation(t *testing.T) {
	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { PreCheck(t) },
		ProtoV5ProviderFactories: ProviderFactories,
		Steps: []resource.TestStep{
			{
				// Plan-only: disk_gb = 30 is below the IntAtLeast(40) minimum, so the plan must
				// error before any API call. Passes only when ExpectError matches "to be at least".
				Config: ConfigBase() + `
resource "dcapi_node_pool" "test" {
  tenant_id  = local.tenant_id
  project_id = local.project_id
  cluster_id = "00000000-0000-0000-0000-000000000000"
  name       = "np"
  size       = "small"
  node_count = 1
  disk_gb    = 30
}
`,
				PlanOnly:    true,
				ExpectError: ExpectErr("to be at least"),
			},
		},
	})
}

func testAccClusterConfig(name, image, k8sVersion string) string {
	return ConfigBase() + ConfigNetwork(name, CIDRClusterVNet, CIDRClusterSubnet) + fmt.Sprintf(`
resource "dcapi_cluster" "test" {
  tenant_id   = local.tenant_id
  project_id  = local.project_id
  name        = %q
  k8s_version = %q
  image_name  = %q
  vnet_id     = dcapi_vnet.parent.vnet_uuid
  subnet_id   = dcapi_subnet.parent.subnet_uuid

  system_pool {
    size    = "medium"
    count   = 1
    disk_gb = 40
  }
}
`, name, k8sVersion, image)
}

// testAccNodePool declares a small pool with disk_gb 40 and image_name set, a label and,
// if taint is set, a NoSchedule taint.
func testAccNodePool(key, poolName string, count int, image string, taint bool, label string) string {
	taintBlock := ""
	if taint {
		taintBlock = `
  taints {
    key    = "acc/dedicated"
    value  = "true"
    effect = "NoSchedule"
  }`
	}
	return fmt.Sprintf(`
resource "dcapi_node_pool" %q {
  tenant_id  = local.tenant_id
  project_id = local.project_id
  cluster_id = dcapi_cluster.test.cluster_id
  name       = %q
  size       = "small"
  node_count = %d
  disk_gb    = 40
  image_name = %q
  labels     = { "acc/pool" = %q }%s
}
`, key, poolName, count, image, label, taintBlock)
}
