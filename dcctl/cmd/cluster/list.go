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

func newListCmd() *cobra.Command {
	var outputJSON bool

	cmd := &cobra.Command{
		Use:   "list",
		Short: "List all clusters",
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
			return runListClusters(cmd.Context(), tenantID, projectID, outputJSON)
		},
	}
	cmd.Flags().BoolVar(&outputJSON, "json", false, "Output as JSON")
	return cmd
}

func runListClusters(ctx context.Context, tenantID, projectID string, outputJSON bool) error {
	creds, err := dcconfig.LoadCredentials()
	if err != nil {
		return err
	}
	apiClient := client.New(creds.AccessToken)

	clusters, err := apiClient.ListClustersV2(ctx, tenantID, projectID)
	if err != nil {
		return err
	}

	if outputJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(clusters)
	}

	if len(clusters) == 0 {
		fmt.Println("No clusters found.")
		return nil
	}

	fmt.Printf("%-38s  %-20s  %-12s  %-14s  %-5s  %-10s  %s\n",
		"ID", "NAME", "SYSTEM_POOL", "WORKER_POOLS", "NODES", "STATUS", "CREATED")
	fmt.Printf("%-38s  %-20s  %-12s  %-14s  %-5s  %-10s  %s\n",
		"--------------------------------------", "--------------------",
		"------------", "--------------", "-----", "----------", "-------------------")

	for _, c := range clusters {
		sysPool := "-"
		if c.SystemPool != nil {
			sysPool = fmt.Sprintf("%dx%s", c.SystemPool.Count, c.SystemPool.Size)
		}
		fmt.Printf("%-38s  %-20s  %-12s  %-14d  %-5d  %-10s  %s\n",
			c.ID,
			truncate(c.Name, 20),
			sysPool,
			c.WorkerPoolCount,
			c.TotalNodeCount,
			c.Status,
			truncTime(c.CreatedAt))
	}
	return nil
}

func truncate(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	if maxLen <= 3 {
		return s[:maxLen]
	}
	return s[:maxLen-3] + "..."
}
