// Package keyvault — Key Vault Private Endpoint creation (M3 chunk 2).
//
// `dcctl keyvault endpoint create <vault-id> --name <ep-name>
//   --vnet <vnet> --subnet <subnet>`
package keyvault

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

func newEndpointCreateCmd() *cobra.Command {
	var name, vnet, subnet string
	var outputJSON bool

	cmd := &cobra.Command{
		Use:   "create <vault-id>",
		Short: "Create a Private Endpoint into a tenant VPC for a Key Vault",
		Long: `Stand up a dual-NIC nginx proxy on Harvester with one NIC pinned to a
VIP in the tenant subnet and the other reaching the Key Vault's backend
(OpenBao). Updates the tenant's per-VPC CoreDNS so the resulting hostname
resolves only from inside that VPC.

Example:
  dcctl keyvault endpoint create <vault-id> --name billing-vault \
      --vnet prod-vnet --subnet app-subnet`,
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
			return runCreateKeyVaultEndpoint(cmd.Context(), tenantID, projectID, args[0], name, vnet, subnet, outputJSON)
		},
	}
	cmd.Flags().StringVar(&name, "name", "", "Endpoint name (required)")
	cmd.Flags().StringVar(&vnet, "vnet", "", "VNet name or ID (required)")
	cmd.Flags().StringVar(&subnet, "subnet", "", "Subnet name or ID (required)")
	cmd.Flags().BoolVar(&outputJSON, "json", false, "Output as JSON")
	cmd.MarkFlagRequired("name")   //nolint:errcheck
	cmd.MarkFlagRequired("vnet")   //nolint:errcheck
	cmd.MarkFlagRequired("subnet") //nolint:errcheck
	return cmd
}

func runCreateKeyVaultEndpoint(ctx context.Context, tenantID, projectID, vaultID, name, vnet, subnet string, outputJSON bool) error {
	creds, err := dcconfig.LoadCredentials()
	if err != nil {
		return err
	}
	apiClient := client.New(creds.AccessToken)

	vID, err := uuid.Parse(vaultID)
	if err != nil {
		return fmt.Errorf("invalid Key Vault id %q: %w", vaultID, err)
	}
	vnetIDStr, err := apiClient.ResolveVNetID(tenantID, projectID, vnet)
	if err != nil {
		return err
	}
	subnetIDStr, err := apiClient.ResolveSubnetID(tenantID, projectID, vnetIDStr, subnet)
	if err != nil {
		return err
	}
	vnetID, err := uuid.Parse(vnetIDStr)
	if err != nil {
		return fmt.Errorf("invalid VNet id %q: %w", vnetIDStr, err)
	}
	subnetID, err := uuid.Parse(subnetIDStr)
	if err != nil {
		return fmt.Errorf("invalid subnet id %q: %w", subnetIDStr, err)
	}

	resp, err := apiClient.Typed.CreateKeyVaultPrivateEndpointWithResponse(ctx, tenantID, projectID, vID, dcapi.CreatePrivateEndpointRequest{
		Name:     name,
		VnetId:   vnetID,
		SubnetId: subnetID,
	})
	if err != nil {
		return fmt.Errorf("POST /v1/tenants/%s/keyvaults/%s/private-endpoints: %w", tenantID, vaultID, err)
	}
	if resp.StatusCode() != http.StatusCreated || resp.JSON201 == nil {
		return cliutil.APIErrorf(resp.StatusCode(), resp.Body)
	}
	ep := resp.JSON201

	if outputJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(ep)
	}

	fmt.Printf("Key Vault Endpoint %q created\n", name)
	fmt.Printf("  ID:        %s\n", ep.Id)
	fmt.Printf("  IP:        %s\n", cliutil.DerefOrDash(ep.IpAddress))
	fmt.Printf("  Hostname:  %s\n", cliutil.DerefOrDash(ep.Hostname))
	fmt.Printf("  Status:    %s\n", ep.Status)
	return nil
}
