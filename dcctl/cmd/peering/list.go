package peering

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"

	"github.com/google/uuid"
	"github.com/spf13/cobra"
	"github.com/wso2/dcctl/cmd/internal/cliutil"
	"github.com/wso2/dcctl/internal/client"
	dcconfig "github.com/wso2/dcctl/internal/config"
)

func newListCmd() *cobra.Command {
	var (
		vnet       string
		outputJSON bool
	)

	cmd := &cobra.Command{
		Use:   "list",
		Short: "List peerings for a VNet",
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
			return runListPeerings(cmd.Context(), tenantID, projectID, vnet, outputJSON)
		},
	}
	cmd.Flags().StringVar(&vnet, "vnet", "", "Parent VNet name or ID (required)")
	cmd.Flags().BoolVar(&outputJSON, "json", false, "Output as JSON")
	cmd.MarkFlagRequired("vnet") //nolint:errcheck
	return cmd
}

func runListPeerings(ctx context.Context, tenantID, projectID, vnetIDOrName string, outputJSON bool) error {
	creds, err := dcconfig.LoadCredentials()
	if err != nil {
		return err
	}
	apiClient := client.New(creds.AccessToken)

	vnetIDStr, err := apiClient.ResolveVNetID(tenantID, projectID, vnetIDOrName)
	if err != nil {
		return err
	}
	vnetID, err := uuid.Parse(vnetIDStr)
	if err != nil {
		return fmt.Errorf("invalid VNet id %q: %w", vnetIDStr, err)
	}

	resp, err := apiClient.Typed.ListPeeringsWithResponse(ctx, tenantID, projectID, vnetID)
	if err != nil {
		return fmt.Errorf("GET /v1/tenants/%s/vnets/%s/peerings: %w", tenantID, vnetIDStr, err)
	}
	if resp.StatusCode() != http.StatusOK || resp.JSON200 == nil {
		return cliutil.APIErrorf(resp.StatusCode(), resp.Body)
	}
	peerings := *resp.JSON200

	if outputJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(peerings)
	}

	if len(peerings) == 0 {
		fmt.Println("No peerings found.")
		return nil
	}

	fmt.Printf("%-38s  %-20s  %-38s  %-10s  %s\n",
		"ID", "NAME", "PEER VNET ID", "STATUS", "CREATED")
	fmt.Printf("%-38s  %-20s  %-38s  %-10s  %s\n",
		"--------------------------------------", "--------------------",
		"--------------------------------------", "----------", "-------------------")

	for _, p := range peerings {
		fmt.Printf("%-38s  %-20s  %-38s  %-10s  %s\n",
			p.Id.String(), p.Name, p.PeerVnetId.String(), string(p.Status),
			cliutil.TruncTime(p.CreatedAt.Format("2006-01-02T15:04:05Z07:00")))
	}
	return nil
}
