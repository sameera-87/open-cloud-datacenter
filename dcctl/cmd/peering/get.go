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
	dcapi "github.com/wso2/dcctl/internal/client/generated"
	dcconfig "github.com/wso2/dcctl/internal/config"
)

func newGetCmd() *cobra.Command {
	var (
		vnet       string
		outputJSON bool
	)

	cmd := &cobra.Command{
		Use:   "get <name-or-id>",
		Short: "Get a VNet peering",
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
			return runGetPeering(cmd.Context(), tenantID, projectID, vnet, args[0], outputJSON)
		},
	}
	cmd.Flags().StringVar(&vnet, "vnet", "", "Parent VNet name or ID (required)")
	cmd.Flags().BoolVar(&outputJSON, "json", false, "Output as JSON")
	cmd.MarkFlagRequired("vnet") //nolint:errcheck
	return cmd
}

func runGetPeering(ctx context.Context, tenantID, projectID, vnetIDOrName, peeringIDOrName string, outputJSON bool) error {
	creds, err := dcconfig.LoadCredentials()
	if err != nil {
		return err
	}
	apiClient := client.New(creds.AccessToken)

	vnetIDStr, err := apiClient.ResolveVNetID(tenantID, projectID, vnetIDOrName)
	if err != nil {
		return err
	}
	peeringIDStr, err := apiClient.ResolvePeeringID(tenantID, projectID, vnetIDStr, peeringIDOrName)
	if err != nil {
		return err
	}
	vnetID, err := uuid.Parse(vnetIDStr)
	if err != nil {
		return fmt.Errorf("invalid VNet id %q: %w", vnetIDStr, err)
	}
	peeringID, err := uuid.Parse(peeringIDStr)
	if err != nil {
		return fmt.Errorf("invalid peering id %q: %w", peeringIDStr, err)
	}

	resp, err := apiClient.Typed.GetPeeringWithResponse(ctx, tenantID, projectID, vnetID, peeringID)
	if err != nil {
		return fmt.Errorf("GET /v1/tenants/%s/vnets/%s/peerings/%s: %w", tenantID, vnetIDStr, peeringIDStr, err)
	}
	if resp.StatusCode() != http.StatusOK || resp.JSON200 == nil {
		return cliutil.APIErrorf(resp.StatusCode(), resp.Body)
	}

	if outputJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(resp.JSON200)
	}

	printPeering(resp.JSON200)
	return nil
}

func printPeering(p *dcapi.Peering) {
	row(12, "ID", p.Id.String())
	row(12, "VNet", p.VnetId.String())
	row(12, "Peer VNet", p.PeerVnetId.String())
	row(12, "Name", p.Name)
	row(12, "Status", string(p.Status))
	row(12, "Provider", p.ProviderType)
	row(12, "Tenant", p.TenantId)
	row(12, "Created", formatTime(p.CreatedAt))
	row(12, "Updated", formatTime(p.UpdatedAt))
	row(12, "Message", cliutil.Deref(p.Message))
	row(12, "Warning", cliutil.Deref(p.Warning))
}
