package cluster

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"github.com/wso2/dcctl/internal/client"
	dcconfig "github.com/wso2/dcctl/internal/config"
)

// validSizes is the ordered set of allowed node sizes.
var validSizes = []string{"small", "medium", "large", "xlarge"}

// validSystemCounts is the allowed set of system pool counts (etcd quorum).
var validSystemCounts = []int{1, 3, 5}

type createClusterFlags struct {
	name        string
	k8sVersion  string
	imageName   string
	systemSize  string
	systemCount int
	systemDisk  int
	networkName string
	vnet        string
	subnet      string
	workerPools []string // raw --worker-pool flag values
	outputJSON  bool
	noWait      bool
}

func newCreateCmd() *cobra.Command {
	flags := &createClusterFlags{}

	cmd := &cobra.Command{
		Use:   "create",
		Short: "Create an RKE2 cluster",
		Long: `Create an RKE2 Kubernetes cluster via DC-API.

The cluster is provisioned with a dedicated system node pool that runs the
Kubernetes control-plane and etcd. One or more worker pools can optionally
be created at the same time using --worker-pool (repeatable, max 10).
Worker pools can also be added after the cluster is ACTIVE using
'dcctl cluster node-pool add'.

By default the command returns immediately (202 Accepted) and prints the
cluster ID. Poll for status with:
  dcctl cluster get <id|name>

Once ACTIVE, retrieve the kubeconfig with:
  dcctl cluster kubeconfig <id> --file ~/.kube/<name>.yaml

Node sizes:
  small   2 vCPU /  4 GiB RAM /  40 GiB default disk
  medium  4 vCPU /  8 GiB RAM /  40 GiB default disk
  large   8 vCPU / 16 GiB RAM /  80 GiB default disk
  xlarge 16 vCPU / 32 GiB RAM / 160 GiB default disk

System pool count must be 1, 3, or 5 (etcd quorum constraints).
Use 1 for dev/test; 3 or 5 for production HA.

Network (mutually exclusive — exactly one required):
  --network <namespace/name>               Legacy Harvester bridge network.
  --vnet <name|uuid> --subnet <name|uuid>  VPC placement on a KubeOVN subnet.

Worker pool format (--worker-pool, repeatable):
  "name=<name>,size=<size>,count=<n>"
  Sub-keys: name (required), size (required), count (required),
            disk-gb (optional), image (optional),
            taint=<key=value:effect> (repeatable), label=<key=value> (repeatable)
  Valid taint effects: NoSchedule, PreferNoSchedule, NoExecute
  Pool name rules: lowercase alphanumeric + hyphens, start with a letter,
                   max 40 chars, not "system" (reserved).

EXAMPLES
  # HA cluster on a VPC subnet (recommended for production)
  dcctl cluster create \
    --name prod-k8s-01 \
    --k8s-version v1.33.10+rke2r3 \
    --image rancher-infra/ubuntu-22-04 \
    --system-size large --system-count 3 \
    --vnet prod-vnet --subnet app-subnet

  # Cluster with system pool + 1 worker pool created at the same time
  dcctl cluster create \
    --name k1 \
    --image rancher-infra/ubuntu-22-04 \
    --system-size large --system-count 3 \
    --vnet prod-vnet --subnet app-subnet \
    --worker-pool "name=workers,size=large,count=3"

  # Cluster with multiple worker pools, one with a taint and label
  dcctl cluster create \
    --name k1 \
    --image rancher-infra/ubuntu-22-04 \
    --system-size large --system-count 3 \
    --vnet prod-vnet --subnet app-subnet \
    --worker-pool "name=app,size=large,count=3" \
    --worker-pool "name=gpu,size=xlarge,count=2,taint=nvidia.com/gpu=:NoSchedule,label=accelerator=a100"

  # Single-node dev cluster on a legacy bridge (no worker pool)
  dcctl cluster create \
    --name dev-k8s \
    --image default/image-rflb5 \
    --system-size small --system-count 1 \
    --network default/vm-net-100`,
		RunE: func(cmd *cobra.Command, args []string) error {
			tenantFlag, _ := cmd.Root().PersistentFlags().GetString("tenant")
			tenantID, err := dcconfig.GetTenantID(tenantFlag)
			if err != nil {
				return err
			}
			projectFlag, _ := cmd.Root().PersistentFlags().GetString("project")
			projectID, err := dcconfig.GetProjectID(projectFlag, tenantID)
			if err != nil {
				return err
			}
			return runCreateCluster(cmd.Context(), tenantID, projectID, flags)
		},
	}

	cmd.Flags().StringVarP(&flags.name, "name", "n", "", "Cluster name (required)")
	cmd.Flags().StringVar(&flags.k8sVersion, "k8s-version", "v1.33.10+rke2r3", "Kubernetes/RKE2 version")
	cmd.Flags().StringVarP(&flags.imageName, "image", "i", "", "Node VM image (namespace/name, required)")
	cmd.Flags().StringVar(&flags.systemSize, "system-size", "", "System pool node size: small|medium|large|xlarge (required)")
	cmd.Flags().IntVar(&flags.systemCount, "system-count", 1, "System pool node count: 1, 3, or 5 (etcd quorum)")
	cmd.Flags().IntVar(&flags.systemDisk, "system-disk-gb", 0, "System pool disk override in GiB (0 = use size default)")
	cmd.Flags().StringVar(&flags.networkName, "network", "", "Legacy bridge network (namespace/name). Mutually exclusive with --vnet/--subnet.")
	cmd.Flags().StringVar(&flags.vnet, "vnet", "", "VNet name or ID for VPC placement. Requires --subnet.")
	cmd.Flags().StringVar(&flags.subnet, "subnet", "", "Subnet name or ID within the VNet. Requires --vnet.")
	cmd.Flags().StringArrayVar(&flags.workerPools, "worker-pool", nil,
		`Worker pool spec (repeatable, max 10): "name=<n>,size=<s>,count=<c>[,disk-gb=<gb>][,image=<img>][,taint=<k=v:effect>][,label=<k=v>]"`)
	cmd.Flags().BoolVar(&flags.outputJSON, "json", false, "Output as JSON")
	cmd.Flags().BoolVar(&flags.noWait, "no-wait", false, "Return immediately without waiting for the cluster to become active")

	cmd.MarkFlagRequired("name")        //nolint:errcheck
	cmd.MarkFlagRequired("image")       //nolint:errcheck
	cmd.MarkFlagRequired("system-size") //nolint:errcheck
	return cmd
}

func runCreateCluster(ctx context.Context, tenantID, projectID string, flags *createClusterFlags) error {
	// ── Local validation ──────────────────────────────────────────────────────

	if !isValidSize(flags.systemSize) {
		return fmt.Errorf("--system-size %q is not valid; must be one of: %s",
			flags.systemSize, strings.Join(validSizes, ", "))
	}
	if !isValidSystemCount(flags.systemCount) {
		return fmt.Errorf("--system-count %d is not valid; must be 1, 3, or 5 (etcd quorum constraints)",
			flags.systemCount)
	}
	if flags.networkName != "" && (flags.vnet != "" || flags.subnet != "") {
		return fmt.Errorf("--network and --vnet/--subnet are mutually exclusive; choose one network attachment method")
	}
	if flags.networkName == "" && flags.vnet == "" {
		return fmt.Errorf("exactly one of --network (legacy) or --vnet + --subnet (VPC) is required")
	}
	if (flags.vnet == "") != (flags.subnet == "") {
		return fmt.Errorf("--vnet and --subnet must be provided together")
	}

	// ── Worker pool parsing ───────────────────────────────────────────────────

	workerPools, err := parseWorkerPools(flags.workerPools)
	if err != nil {
		return err
	}

	creds, err := dcconfig.LoadCredentials()
	if err != nil {
		return err
	}
	apiClient := client.New(creds.AccessToken)

	// ── Build request body ────────────────────────────────────────────────────

	sysPool := &client.SystemPoolSpec{
		Size:  flags.systemSize,
		Count: flags.systemCount,
	}
	if flags.systemDisk > 0 {
		sysPool.DiskGB = flags.systemDisk
	}

	body := &client.CreateClusterRequest{
		Name:        flags.name,
		K8sVersion:  flags.k8sVersion,
		ImageName:   flags.imageName,
		SystemPool:  sysPool,
		WorkerPools: workerPools,
	}

	if flags.vnet != "" {
		// Resolve name → UUID for both VNet and Subnet.
		vnetID, err := apiClient.ResolveVNetID(tenantID, projectID, flags.vnet)
		if err != nil {
			return fmt.Errorf("resolve --vnet: %w", err)
		}
		subnetID, err := apiClient.ResolveSubnetID(tenantID, projectID, vnetID, flags.subnet)
		if err != nil {
			return fmt.Errorf("resolve --subnet: %w", err)
		}
		body.VNetID = vnetID
		body.SubnetID = subnetID
	} else {
		body.NetworkName = flags.networkName
	}

	// ── Call API ──────────────────────────────────────────────────────────────

	if len(workerPools) > 0 {
		fmt.Printf("Creating cluster %q (%d × %s system nodes, %d worker pool(s))...\n",
			flags.name, flags.systemCount, flags.systemSize, len(workerPools))
	} else {
		fmt.Printf("Creating cluster %q (%d × %s nodes)...\n", flags.name, flags.systemCount, flags.systemSize)
	}

	resp, err := apiClient.CreateClusterV2(ctx, tenantID, projectID, body)
	if err != nil {
		return err
	}

	if flags.outputJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(resp)
	}

	fmt.Printf("\nCluster %q accepted!\n", resp.Resource.Name)
	fmt.Printf("  ID:     %s\n", resp.Resource.ID)
	fmt.Printf("  Status: %s\n", resp.Resource.Status)

	if flags.noWait {
		fmt.Printf("\nCheck status:    dcctl cluster get %s\n", resp.Resource.ID)
		fmt.Printf("Get kubeconfig:  dcctl cluster kubeconfig %s --file ~/.kube/%s.yaml\n",
			resp.Resource.ID, flags.name)
		return nil
	}

	fmt.Printf("\nWaiting for cluster to become active (this takes 5-15 minutes)")
	final, err := pollClusterV2UntilDone(ctx, tenantID, projectID, apiClient, resp.Resource.ID)
	if err != nil {
		return err
	}

	fmt.Println()
	printClusterV2(final)
	fmt.Printf("\nGet kubeconfig:  dcctl cluster kubeconfig %s --file ~/.kube/%s.yaml\n",
		final.ID, flags.name)
	return nil
}

// pollClusterV2UntilDone polls until the cluster leaves PENDING / PROVISIONING.
// Timeout: 20 minutes.
func pollClusterV2UntilDone(ctx context.Context, tenantID, projectID string, apiClient *client.Client, id string) (*client.ClusterResponse, error) {
	const (
		pollInterval = 15 * time.Second
		timeout      = 20 * time.Minute
	)

	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		time.Sleep(pollInterval)
		fmt.Print(".")

		c, err := apiClient.GetClusterV2(ctx, tenantID, projectID, id)
		if err != nil {
			continue
		}
		switch strings.ToUpper(c.Status) {
		case "ACTIVE":
			fmt.Println(" done!")
			return c, nil
		case "FAILED":
			fmt.Println()
			return nil, fmt.Errorf("cluster provisioning failed: %s", c.Message)
		}
	}

	fmt.Println()
	return nil, fmt.Errorf("timed out after 20m — check status with: dcctl cluster get %s", id)
}

// ── Validation helpers ────────────────────────────────────────────────────────

func isValidSize(s string) bool {
	for _, v := range validSizes {
		if v == s {
			return true
		}
	}
	return false
}

func isValidSystemCount(n int) bool {
	for _, v := range validSystemCounts {
		if v == n {
			return true
		}
	}
	return false
}
