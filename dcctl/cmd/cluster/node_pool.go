// Package cluster — node-pool subcommand group.
//
// `dcctl cluster node-pool <verb>`:
//
//	list   <cluster>               — list all pools in a cluster
//	get    <cluster> <pool>        — get a single pool
//	add    <cluster>               — add a new worker pool
//	scale  <cluster> <pool>        — change the node count of a pool
//	delete <cluster> <pool>        — drain and remove a worker pool
//
// The <cluster> argument accepts either the cluster UUID or its name.
// Pool names must match ^[a-z]([-a-z0-9]{0,38}[a-z0-9])?$ (DNS-label-safe,
// max 40 chars). The name "system" is reserved and will be rejected by the
// server; we catch it early here for a fast, friendly error.
package cluster

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"regexp"
	"strconv"
	"strings"

	"github.com/spf13/cobra"
	"github.com/wso2/dcctl/internal/client"
	dcconfig "github.com/wso2/dcctl/internal/config"
)

// poolNameRE is the server-side pattern from the openapi spec (≤40 chars, DNS-label-safe).
// We allow single-char names (e.g. "a") by anchoring both branches.
var poolNameRE = regexp.MustCompile(`^[a-z]([-a-z0-9]{0,38}[a-z0-9])?$|^[a-z]$`)

// validTaintEffects is the set of accepted Kubernetes taint effects.
var validTaintEffects = []string{"NoSchedule", "PreferNoSchedule", "NoExecute"}

// newNodePoolCmd returns the `dcctl cluster node-pool` parent command with
// all sub-subcommands attached.
func newNodePoolCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "node-pool",
		Short: "Manage node pools within a cluster",
		Long: `Manage worker node pools in an RKE2 cluster.

Every cluster has exactly one system pool (controlplane + etcd) that is
created when the cluster is first provisioned. Worker pools are created,
scaled, and deleted independently after the cluster is ACTIVE.

The <cluster> argument to each sub-command accepts either the cluster UUID
or its display name.

Examples:
  dcctl cluster node-pool list prod-k8s-01
  dcctl cluster node-pool add  prod-k8s-01 --name workers --size large --count 3
  dcctl cluster node-pool scale prod-k8s-01 workers --count 5
  dcctl cluster node-pool delete prod-k8s-01 workers`,
	}
	cmd.AddCommand(newNodePoolListCmd())
	cmd.AddCommand(newNodePoolGetCmd())
	cmd.AddCommand(newNodePoolAddCmd())
	cmd.AddCommand(newNodePoolScaleCmd())
	cmd.AddCommand(newNodePoolDeleteCmd())
	return cmd
}

// ── node-pool list ────────────────────────────────────────────────────────────

func newNodePoolListCmd() *cobra.Command {
	var outputJSON bool

	cmd := &cobra.Command{
		Use:   "list <cluster>",
		Short: "List all node pools in a cluster",
		Long: `List all node pools — system pool first, then worker pools.

The NAME column shows the pool name, ROLE shows system or worker,
SIZE is the node size, COUNT is the desired node count, STATUS is
the pool lifecycle status, and TAINTS shows the count of taints applied.

Examples:
  dcctl cluster node-pool list prod-k8s-01
  dcctl cluster node-pool list 3a1c2e5d-1234-5678-abcd-000000000002 --json`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			tenantID, projectID, err := resolveTenantProjectFromCmd(cmd)
			if err != nil {
				return err
			}
			return runNodePoolList(cmd.Context(), tenantID, projectID, args[0], outputJSON)
		},
	}
	cmd.Flags().BoolVar(&outputJSON, "json", false, "Output as JSON")
	return cmd
}

func runNodePoolList(ctx context.Context, tenantID, projectID, clusterArg string, outputJSON bool) error {
	creds, err := dcconfig.LoadCredentials()
	if err != nil {
		return err
	}
	apiClient := client.New(creds.AccessToken)

	clusterID, err := apiClient.ResolveClusterID(ctx, tenantID, projectID, clusterArg)
	if err != nil {
		return err
	}

	pools, err := apiClient.ListNodePools(ctx, tenantID, projectID, clusterID)
	if err != nil {
		return err
	}

	if outputJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(pools)
	}

	if len(pools) == 0 {
		fmt.Println("No node pools found.")
		return nil
	}

	fmt.Printf("%-20s  %-8s  %-8s  %-5s  %-12s  %s\n",
		"NAME", "ROLE", "SIZE", "COUNT", "STATUS", "TAINTS")
	fmt.Printf("%-20s  %-8s  %-8s  %-5s  %-12s  %s\n",
		"--------------------", "--------", "--------", "-----", "------------", "------")

	for _, p := range pools {
		taintStr := "-"
		if len(p.Taints) > 0 {
			taintStr = strconv.Itoa(len(p.Taints))
		}
		fmt.Printf("%-20s  %-8s  %-8s  %-5d  %-12s  %s\n",
			truncate(p.Name, 20), p.Role, p.Size, p.Count, p.Status, taintStr)
	}
	return nil
}

// ── node-pool get ─────────────────────────────────────────────────────────────

func newNodePoolGetCmd() *cobra.Command {
	var outputJSON bool

	cmd := &cobra.Command{
		Use:   "get <cluster> <pool-name>",
		Short: "Get a single node pool",
		Long: `Get the full details of a node pool including taints and labels.

Examples:
  dcctl cluster node-pool get prod-k8s-01 workers
  dcctl cluster node-pool get prod-k8s-01 system --json`,
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			tenantID, projectID, err := resolveTenantProjectFromCmd(cmd)
			if err != nil {
				return err
			}
			return runNodePoolGet(cmd.Context(), tenantID, projectID, args[0], args[1], outputJSON)
		},
	}
	cmd.Flags().BoolVar(&outputJSON, "json", false, "Output as JSON")
	return cmd
}

func runNodePoolGet(ctx context.Context, tenantID, projectID, clusterArg, poolName string, outputJSON bool) error {
	creds, err := dcconfig.LoadCredentials()
	if err != nil {
		return err
	}
	apiClient := client.New(creds.AccessToken)

	clusterID, err := apiClient.ResolveClusterID(ctx, tenantID, projectID, clusterArg)
	if err != nil {
		return err
	}

	pool, err := apiClient.GetNodePool(ctx, tenantID, projectID, clusterID, poolName)
	if err != nil {
		return err
	}

	if outputJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(pool)
	}

	printNodePool(pool)
	return nil
}

// ── node-pool add ─────────────────────────────────────────────────────────────

type addPoolFlags struct {
	name      string
	size      string
	count     int
	diskGB    int
	imageName string
	taints    []string
	labels    []string
}

func newNodePoolAddCmd() *cobra.Command {
	flags := &addPoolFlags{}

	cmd := &cobra.Command{
		Use:   "add <cluster>",
		Short: "Add a worker node pool to an ACTIVE cluster",
		Long: `Add a new worker node pool to an existing cluster that is in ACTIVE status.

Pool names must be lowercase alphanumeric (hyphens allowed inside), start
with a letter, and be at most 40 characters. The name 'system' is reserved.

Taints use the format:  key=value:effect  or  key=:effect  or  key:effect
Valid effects: NoSchedule, PreferNoSchedule, NoExecute

Labels use the format: key=value

Examples:
  # Add a GPU worker pool with a taint
  dcctl cluster node-pool add prod-k8s-01 \
    --name gpu-workers \
    --size xlarge \
    --count 2 \
    --taint nvidia.com/gpu=present:NoSchedule \
    --label accelerator=a100

  # Add a general-purpose worker pool
  dcctl cluster node-pool add prod-k8s-01 \
    --name workers \
    --size large \
    --count 3`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			tenantID, projectID, err := resolveTenantProjectFromCmd(cmd)
			if err != nil {
				return err
			}
			return runNodePoolAdd(cmd.Context(), tenantID, projectID, args[0], flags)
		},
	}

	cmd.Flags().StringVarP(&flags.name, "name", "n", "", "Pool name (required, DNS-label-safe, max 40 chars)")
	cmd.Flags().StringVarP(&flags.size, "size", "s", "", "Node size: small|medium|large|xlarge (required)")
	cmd.Flags().IntVarP(&flags.count, "count", "c", 0, "Number of nodes 1-50 (required)")
	cmd.Flags().IntVarP(&flags.diskGB, "disk-gb", "d", 0, "Root disk override in GiB (0 = use size default, min 40)")
	cmd.Flags().StringVarP(&flags.imageName, "image", "i", "", "VM image override (namespace/name; omit to reuse cluster's image)")
	cmd.Flags().StringArrayVar(&flags.taints, "taint", nil, "Node taint (repeatable): key=value:effect or key:effect")
	cmd.Flags().StringArrayVar(&flags.labels, "label", nil, "Node label (repeatable): key=value")

	cmd.MarkFlagRequired("name")  //nolint:errcheck
	cmd.MarkFlagRequired("size")  //nolint:errcheck
	cmd.MarkFlagRequired("count") //nolint:errcheck
	return cmd
}

func runNodePoolAdd(ctx context.Context, tenantID, projectID, clusterArg string, flags *addPoolFlags) error {
	// ── Local validation ──────────────────────────────────────────────────────
	if flags.name == "system" {
		return fmt.Errorf("pool name 'system' is reserved; choose a different name")
	}
	if !poolNameRE.MatchString(flags.name) {
		return fmt.Errorf("--name %q is invalid: must be 1-40 lowercase alphanumeric characters or hyphens, starting with a letter", flags.name)
	}
	if !isValidSize(flags.size) {
		return fmt.Errorf("--size %q is not valid; must be one of: %s", flags.size, strings.Join(validSizes, ", "))
	}
	if flags.count < 1 || flags.count > 50 {
		return fmt.Errorf("--count %d is out of range; must be 1-50", flags.count)
	}

	taints, err := parseTaints(flags.taints)
	if err != nil {
		return err
	}
	labels, err := parseLabels(flags.labels)
	if err != nil {
		return err
	}

	creds, err := dcconfig.LoadCredentials()
	if err != nil {
		return err
	}
	apiClient := client.New(creds.AccessToken)

	clusterID, err := apiClient.ResolveClusterID(ctx, tenantID, projectID, clusterArg)
	if err != nil {
		return err
	}

	req := &client.AddNodePoolRequest{
		Name:      flags.name,
		Size:      flags.size,
		Count:     flags.count,
		ImageName: flags.imageName,
		Taints:    taints,
		Labels:    labels,
	}
	if flags.diskGB > 0 {
		req.DiskGB = flags.diskGB
	}

	pool, err := apiClient.AddNodePool(ctx, tenantID, projectID, clusterID, req)
	if err != nil {
		return err
	}

	fmt.Printf("Node pool %q accepted (status: %s).\n", pool.Name, pool.Status)
	fmt.Printf("Pool ID: %s\n", pool.ID)
	fmt.Printf("Track:   dcctl cluster node-pool get %s %s\n", clusterArg, pool.Name)
	return nil
}

// ── node-pool scale ───────────────────────────────────────────────────────────

func newNodePoolScaleCmd() *cobra.Command {
	var (
		count      int
		force      bool
		outputJSON bool
	)

	cmd := &cobra.Command{
		Use:   "scale <cluster> <pool-name>",
		Short: "Scale a node pool to a new node count",
		Long: `Change the desired node count of a node pool.

For the system pool: only growth transitions 1→3 and 3→5 are allowed.
Shrinking the system pool is refused (etcd quorum requirement).

For worker pools: any value between 1 and 50.

Scaling down drains nodes before removing them. For large reductions
(e.g. 10 → 1) this can take several minutes while workloads reschedule.

Examples:
  dcctl cluster node-pool scale prod-k8s-01 workers --count 5
  dcctl cluster node-pool scale prod-k8s-01 workers --count 1 --yes`,
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			tenantID, projectID, err := resolveTenantProjectFromCmd(cmd)
			if err != nil {
				return err
			}
			return runNodePoolScale(cmd.Context(), tenantID, projectID, args[0], args[1], count, force, outputJSON)
		},
	}

	cmd.Flags().IntVarP(&count, "count", "c", 0, "New node count (required)")
	cmd.Flags().BoolVarP(&force, "yes", "y", false, "Skip confirmation prompt")
	cmd.Flags().BoolVar(&outputJSON, "json", false, "Output as JSON")
	cmd.MarkFlagRequired("count") //nolint:errcheck
	return cmd
}

func runNodePoolScale(ctx context.Context, tenantID, projectID, clusterArg, poolName string, newCount int, force, outputJSON bool) error {
	if newCount < 1 || newCount > 50 {
		return fmt.Errorf("--count %d is out of range; must be 1-50", newCount)
	}

	creds, err := dcconfig.LoadCredentials()
	if err != nil {
		return err
	}
	apiClient := client.New(creds.AccessToken)

	clusterID, err := apiClient.ResolveClusterID(ctx, tenantID, projectID, clusterArg)
	if err != nil {
		return err
	}

	// Fetch current count so we can print a meaningful confirmation.
	current, err := apiClient.GetNodePool(ctx, tenantID, projectID, clusterID, poolName)
	if err != nil {
		return fmt.Errorf("get pool before scale: %w", err)
	}

	// Build confirmation prompt.
	direction := "up"
	warning := ""
	if newCount < current.Count {
		direction = "down"
		delta := current.Count - newCount
		warning = fmt.Sprintf(" WARNING: this drains %d node(s).", delta)
	}
	prompt := fmt.Sprintf("Scale pool %q %s from %d to %d?%s [y/N] ",
		poolName, direction, current.Count, newCount, warning)

	if !confirm(prompt, force) {
		fmt.Println("Cancelled.")
		return nil
	}

	updated, err := apiClient.ScaleOrUpdateNodePool(ctx, tenantID, projectID, clusterID, poolName,
		&client.PatchNodePoolRequest{Count: newCount})
	if err != nil {
		return err
	}

	if outputJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(updated)
	}

	fmt.Printf("Pool %q scaling from %d → %d (status: %s).\n",
		poolName, current.Count, newCount, updated.Status)
	fmt.Printf("Track: dcctl cluster node-pool get %s %s\n", clusterArg, poolName)
	return nil
}

// ── node-pool delete ──────────────────────────────────────────────────────────

func newNodePoolDeleteCmd() *cobra.Command {
	var force bool

	cmd := &cobra.Command{
		Use:   "delete <cluster> <pool-name>",
		Short: "Delete a worker node pool (drains nodes first)",
		Long: `Remove a worker node pool from a cluster.

All nodes in the pool are drained and removed before the pool is deleted.
This can take several minutes depending on the number of nodes and running
workloads. The system pool cannot be removed — delete the cluster instead.

Examples:
  dcctl cluster node-pool delete prod-k8s-01 workers
  dcctl cluster node-pool delete prod-k8s-01 gpu-workers --yes`,
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			tenantID, projectID, err := resolveTenantProjectFromCmd(cmd)
			if err != nil {
				return err
			}
			return runNodePoolDelete(cmd.Context(), tenantID, projectID, args[0], args[1], force)
		},
	}
	cmd.Flags().BoolVarP(&force, "yes", "y", false, "Skip confirmation prompt")
	return cmd
}

func runNodePoolDelete(ctx context.Context, tenantID, projectID, clusterArg, poolName string, force bool) error {
	creds, err := dcconfig.LoadCredentials()
	if err != nil {
		return err
	}
	apiClient := client.New(creds.AccessToken)

	clusterID, err := apiClient.ResolveClusterID(ctx, tenantID, projectID, clusterArg)
	if err != nil {
		return err
	}

	// Fetch pool first so we can show a meaningful confirmation.
	pool, err := apiClient.GetNodePool(ctx, tenantID, projectID, clusterID, poolName)
	if err != nil {
		return fmt.Errorf("get pool before delete: %w", err)
	}

	prompt := fmt.Sprintf(
		"Delete pool %q from cluster %q? This drains all %d node(s). [y/N] ",
		poolName, clusterArg, pool.Count)
	if !confirm(prompt, force) {
		fmt.Println("Cancelled.")
		return nil
	}

	if err := apiClient.DeleteNodePool(ctx, tenantID, projectID, clusterID, poolName); err != nil {
		return err
	}

	fmt.Printf("Pool %q deletion initiated (status → DELETING).\n", poolName)
	fmt.Printf("Track: dcctl cluster node-pool list %s\n", clusterArg)
	return nil
}

// ── Output helpers ────────────────────────────────────────────────────────────

// printNodePool renders a NodePoolResponse in detail view.
func printNodePool(p *client.NodePoolResponse) {
	diskStr := "-"
	if p.DiskGB != nil {
		diskStr = fmt.Sprintf("%d GiB", *p.DiskGB)
	}

	row(12, "ID", p.ID)
	row(12, "Name", p.Name)
	row(12, "Role", p.Role)
	row(12, "Size", p.Size)
	row(12, "Count", strconv.Itoa(p.Count))
	row(12, "Disk", diskStr)
	row(12, "Status", p.Status)
	row(12, "Created", truncTime(p.CreatedAt))
	row(12, "Message", p.Message)

	if len(p.Taints) > 0 {
		fmt.Printf("  %-12s\n", "Taints:")
		for _, t := range p.Taints {
			val := t.Value
			if val == "" {
				fmt.Printf("    %s:%s\n", t.Key, t.Effect)
			} else {
				fmt.Printf("    %s=%s:%s\n", t.Key, val, t.Effect)
			}
		}
	}
	if len(p.Labels) > 0 {
		fmt.Printf("  %-12s\n", "Labels:")
		for k, v := range p.Labels {
			fmt.Printf("    %s=%s\n", k, v)
		}
	}
}

// ── Parse helpers ─────────────────────────────────────────────────────────────

// parseTaints parses the --taint flag values in the form:
//
//	key=value:effect   (value present)
//	key=:effect        (empty value, explicit separator)
//	key:effect         (no value — shorthand)
func parseTaints(raw []string) ([]client.NodePoolTaint, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	taints := make([]client.NodePoolTaint, 0, len(raw))
	for _, s := range raw {
		t, err := parseSingleTaint(s)
		if err != nil {
			return nil, err
		}
		taints = append(taints, t)
	}
	return taints, nil
}

func parseSingleTaint(s string) (client.NodePoolTaint, error) {
	// The last colon separates the effect from the rest.
	lastColon := strings.LastIndex(s, ":")
	if lastColon < 0 {
		return client.NodePoolTaint{}, fmt.Errorf("invalid --taint %q: must be in the form key=value:effect or key:effect", s)
	}

	effect := s[lastColon+1:]
	if !isValidTaintEffect(effect) {
		return client.NodePoolTaint{}, fmt.Errorf("invalid taint effect %q in %q: must be one of %s",
			effect, s, strings.Join(validTaintEffects, ", "))
	}

	keyValue := s[:lastColon]
	// Split on the FIRST '=' only — values may contain '=' themselves.
	eqIdx := strings.Index(keyValue, "=")
	var key, value string
	if eqIdx < 0 {
		// key:effect form (no value)
		key = keyValue
	} else {
		key = keyValue[:eqIdx]
		value = keyValue[eqIdx+1:]
	}

	if key == "" {
		return client.NodePoolTaint{}, fmt.Errorf("invalid --taint %q: key must not be empty", s)
	}

	return client.NodePoolTaint{Key: key, Value: value, Effect: effect}, nil
}

// parseLabels parses the --label flag values in the form key=value.
func parseLabels(raw []string) (map[string]string, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	labels := make(map[string]string, len(raw))
	for _, s := range raw {
		eqIdx := strings.Index(s, "=")
		if eqIdx < 0 {
			return nil, fmt.Errorf("invalid --label %q: must be in the form key=value", s)
		}
		key := s[:eqIdx]
		value := s[eqIdx+1:]
		if key == "" {
			return nil, fmt.Errorf("invalid --label %q: key must not be empty", s)
		}
		labels[key] = value
	}
	return labels, nil
}

// isValidTaintEffect returns true if effect is one of the Kubernetes taint effects.
func isValidTaintEffect(effect string) bool {
	for _, v := range validTaintEffects {
		if v == effect {
			return true
		}
	}
	return false
}

// ── Shared context helper ─────────────────────────────────────────────────────

// resolveTenantProjectFromCmd reads the --tenant and --project flags from root.
func resolveTenantProjectFromCmd(cmd *cobra.Command) (tenantID, projectID string, err error) {
	tenantFlag, _ := cmd.Root().PersistentFlags().GetString("tenant")
	tenantID, err = dcconfig.GetTenantID(tenantFlag)
	if err != nil {
		return
	}
	projectFlag, _ := cmd.Root().PersistentFlags().GetString("project")
	projectID, err = dcconfig.GetProjectID(projectFlag, tenantID)
	return
}
