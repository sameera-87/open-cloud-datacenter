package cluster

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"
	"github.com/wso2/dcctl/internal/client"
	dcconfig "github.com/wso2/dcctl/internal/config"
)

func newDeleteCmd() *cobra.Command {
	var force bool

	cmd := &cobra.Command{
		Use:   "delete <id|name>",
		Short: "Delete a cluster",
		Args:  cobra.ExactArgs(1),
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
			return runDeleteCluster(cmd.Context(), tenantID, projectID, args[0], force)
		},
	}
	cmd.Flags().BoolVarP(&force, "yes", "y", false, "Skip confirmation prompt")
	return cmd
}

func runDeleteCluster(ctx context.Context, tenantID, projectID, idOrName string, force bool) error {
	creds, err := dcconfig.LoadCredentials()
	if err != nil {
		return err
	}
	apiClient := client.New(creds.AccessToken)

	id, err := apiClient.ResolveClusterID(ctx, tenantID, projectID, idOrName)
	if err != nil {
		return err
	}

	if !confirm(fmt.Sprintf("Delete cluster %s? This cannot be undone. [y/N] ", idOrName), force) {
		fmt.Println("Cancelled.")
		return nil
	}

	if err := apiClient.DeleteClusterV2(ctx, tenantID, projectID, id); err != nil {
		return err
	}
	fmt.Printf("Cluster %s deletion initiated (status → DELETING).\n", idOrName)
	fmt.Printf("Poll: dcctl cluster get %s\n", idOrName)
	return nil
}

func confirm(prompt string, force bool) bool {
	if force {
		return true
	}
	fmt.Print(prompt)
	reader := bufio.NewReader(os.Stdin)
	line, _ := reader.ReadString('\n')
	return strings.EqualFold(strings.TrimSpace(line), "y")
}

func row(labelWidth int, label, value string) {
	if value == "" {
		return
	}
	fmt.Printf("  %-*s %s\n", labelWidth, label+":", value)
}
