package vnet

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
		Short: "List all VNets",
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
			return runListVNets(cmd.Context(), tenantID, projectID, outputJSON)
		},
	}
	cmd.Flags().BoolVar(&outputJSON, "json", false, "Output as JSON")
	return cmd
}

func runListVNets(ctx context.Context, tenantID, projectID string, outputJSON bool) error {
	creds, err := dcconfig.LoadCredentials()
	if err != nil {
		return err
	}
	apiClient := client.New(creds.AccessToken)

	resp, err := apiClient.Typed.ListVNetsWithResponse(ctx, tenantID, projectID)
	if err != nil {
		return fmt.Errorf("GET /v1/tenants/%s/vnets: %w", tenantID, err)
	}
	if resp.StatusCode() != http.StatusOK || resp.JSON200 == nil {
		return cliutil.APIErrorf(resp.StatusCode(), resp.Body)
	}
	vnets := *resp.JSON200

	if outputJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(vnets)
	}

	if len(vnets) == 0 {
		fmt.Println("No VNets found.")
		return nil
	}

	fmt.Printf("%-38s  %-20s  %-6s  %-18s  %-10s  %s\n",
		"ID", "NAME", "REGION", "ADDRESS SPACE", "STATUS", "CREATED")
	fmt.Printf("%-38s  %-20s  %-6s  %-18s  %-10s  %s\n",
		"--------------------------------------", "--------------------",
		"------", "------------------", "----------", "-------------------")

	for _, v := range vnets {
		addrSpace := "-"
		if len(v.AddressSpace) > 0 {
			addrSpace = v.AddressSpace[0]
		}
		fmt.Printf("%-38s  %-20s  %-6s  %-18s  %-10s  %s\n",
			v.Id.String(), v.Name, v.Region, addrSpace, string(v.Status),
			cliutil.TruncTime(v.CreatedAt.Format("2006-01-02T15:04:05Z07:00")))
	}
	return nil
}
