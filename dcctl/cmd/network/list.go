package network

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"

	"github.com/spf13/cobra"
	"github.com/wso2/dcctl/cmd/internal/cliutil"
	"github.com/wso2/dcctl/internal/client"
	dcconfig "github.com/wso2/dcctl/internal/config"
)

func newListCmd() *cobra.Command {
	var outputJSON bool

	cmd := &cobra.Command{
		Use:   "list",
		Short: "List available VM networks (legacy bridges)",
		Long: `List all VM networks available in the datacenter.

The ID column is what you pass to --network when creating a VM or cluster
on a legacy bridge. New workloads should use VPCs (` + "`dcctl vnet`" + `).

Example:
  dcctl network list
  dcctl vm create --name web-01 --network default/vm-net-100 ...`,
		RunE: func(cmd *cobra.Command, args []string) error {
			tenantFlag, _ := cmd.Root().PersistentFlags().GetString("tenant")
			tenantID, err := dcconfig.GetTenantID(tenantFlag)
			if err != nil {
				return err
			}
			return runListNetworks(cmd.Context(), tenantID, outputJSON)
		},
	}
	cmd.Flags().BoolVar(&outputJSON, "json", false, "Output as JSON")
	return cmd
}

func runListNetworks(ctx context.Context, tenantID string, outputJSON bool) error {
	creds, err := dcconfig.LoadCredentials()
	if err != nil {
		return err
	}
	apiClient := client.New(creds.AccessToken)

	resp, err := apiClient.Typed.ListNetworksWithResponse(ctx, tenantID)
	if err != nil {
		return fmt.Errorf("GET /v1/tenants/%s/networks: %w", tenantID, err)
	}
	if resp.StatusCode() != http.StatusOK || resp.JSON200 == nil {
		return cliutil.APIErrorf(resp.StatusCode(), resp.Body)
	}
	networks := *resp.JSON200

	if outputJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(networks)
	}

	if len(networks) == 0 {
		fmt.Println("No networks found.")
		return nil
	}

	fmt.Printf("  %-40s  %s\n", "ID", "DISPLAY NAME")
	fmt.Printf("  %-40s  %s\n", "──────────────────────────────────────────", "────────────────────────")
	for _, n := range networks {
		fmt.Printf("  %-40s  %s\n", n.Id, n.DisplayName)
	}
	return nil
}
