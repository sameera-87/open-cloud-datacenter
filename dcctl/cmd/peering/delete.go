package peering

import (
	"context"
	"fmt"
	"net/http"

	"github.com/google/uuid"
	"github.com/spf13/cobra"
	"github.com/wso2/dcctl/cmd/internal/cliutil"
	"github.com/wso2/dcctl/internal/client"
	dcconfig "github.com/wso2/dcctl/internal/config"
)

func newDeleteCmd() *cobra.Command {
	var (
		vnet  string
		force bool
	)

	cmd := &cobra.Command{
		Use:   "delete <name-or-id>",
		Short: "Delete a VNet peering",
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
			return runDeletePeering(cmd.Context(), tenantID, projectID, vnet, args[0], force)
		},
	}
	cmd.Flags().StringVar(&vnet, "vnet", "", "Parent VNet name or ID (required)")
	cmd.Flags().BoolVarP(&force, "yes", "y", false, "Skip confirmation prompt")
	cmd.MarkFlagRequired("vnet") //nolint:errcheck
	return cmd
}

func runDeletePeering(ctx context.Context, tenantID, projectID, vnetIDOrName, idOrName string, force bool) error {
	if !confirm(fmt.Sprintf("Delete peering %s (in VNet %s)? This cannot be undone. [y/N] ", idOrName, vnetIDOrName), force) {
		fmt.Println("Cancelled.")
		return nil
	}

	creds, err := dcconfig.LoadCredentials()
	if err != nil {
		return err
	}
	apiClient := client.New(creds.AccessToken)

	vnetIDStr, err := apiClient.ResolveVNetID(tenantID, projectID, vnetIDOrName)
	if err != nil {
		return err
	}
	peeringIDStr, err := apiClient.ResolvePeeringID(tenantID, projectID, vnetIDStr, idOrName)
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

	resp, err := apiClient.Typed.DeletePeeringWithResponse(ctx, tenantID, projectID, vnetID, peeringID)
	if err != nil {
		return fmt.Errorf("DELETE /v1/tenants/%s/vnets/%s/peerings/%s: %w", tenantID, vnetIDStr, peeringIDStr, err)
	}
	if resp.StatusCode() >= http.StatusMultipleChoices {
		return cliutil.APIErrorf(resp.StatusCode(), resp.Body)
	}
	fmt.Printf("Peering %s deletion initiated (status -> DELETING).\n", idOrName)
	return nil
}
