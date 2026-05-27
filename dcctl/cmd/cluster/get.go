package cluster

import (
	"context"
	"encoding/json"
	"fmt"
	"os"

	"github.com/spf13/cobra"
	"github.com/wso2/dcctl/internal/client"
	dcconfig "github.com/wso2/dcctl/internal/config"
)

func newGetCmd() *cobra.Command {
	var outputJSON bool

	cmd := &cobra.Command{
		Use:   "get <id|name>",
		Short: "Get a cluster",
		Long: `Get the current status and configuration of a cluster.

The argument can be the cluster UUID or its name.

Examples:
  dcctl cluster get prod-k8s-01
  dcctl cluster get 3a1c2e5d-1234-5678-abcd-000000000002`,
		Args: cobra.ExactArgs(1),
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
			return runGetCluster(cmd.Context(), tenantID, projectID, args[0], outputJSON)
		},
	}
	cmd.Flags().BoolVar(&outputJSON, "json", false, "Output as JSON")
	return cmd
}

func runGetCluster(ctx context.Context, tenantID, projectID, idOrName string, outputJSON bool) error {
	creds, err := dcconfig.LoadCredentials()
	if err != nil {
		return err
	}
	apiClient := client.New(creds.AccessToken)

	// Resolve name → UUID when needed.
	id, err := apiClient.ResolveClusterID(ctx, tenantID, projectID, idOrName)
	if err != nil {
		return err
	}

	c, err := apiClient.GetClusterV2(ctx, tenantID, projectID, id)
	if err != nil {
		return err
	}

	if outputJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(c)
	}

	printClusterV2(c)
	return nil
}

// printClusterV2 renders a ClusterResponse in the detail view.
func printClusterV2(c *client.ClusterResponse) {
	row(14, "ID", c.ID)
	row(14, "Name", c.Name)
	row(14, "Status", c.Status)
	row(14, "Provider", c.ProviderType)
	row(14, "Tenant", c.TenantID)
	row(14, "Created", truncTime(c.CreatedAt))
	row(14, "Message", c.Message)

	fmt.Println()
	if c.SystemPool != nil {
		sp := c.SystemPool
		fmt.Printf("  System pool:   %d x %s  (status: %s)\n", sp.Count, sp.Size, sp.Status)
	}
	fmt.Printf("  Worker pools:  %d  (total nodes: %d)\n", c.WorkerPoolCount, c.TotalNodeCount)
	if c.WorkerPoolCount > 0 {
		fmt.Printf("  Pool details:  dcctl cluster node-pool list %s\n", c.Name)
	}
}

// truncTime shortens RFC3339 to the first 19 chars for consistent column width.
func truncTime(s string) string {
	if len(s) > 19 {
		return s[:19]
	}
	return s
}
