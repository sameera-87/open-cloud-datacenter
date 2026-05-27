// Package keyvault — Key Vault Private Endpoint get command (M3 chunk 2).
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

func newEndpointGetCmd() *cobra.Command {
	var vault string
	var outputJSON bool

	cmd := &cobra.Command{
		Use:   "get <ep-id>",
		Short: "Get a Key Vault Private Endpoint by UUID",
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
			return runGetKeyVaultEndpoint(cmd.Context(), tenantID, projectID, vault, args[0], outputJSON)
		},
	}
	cmd.Flags().StringVar(&vault, "vault", "", "Parent vault ID (required)")
	cmd.Flags().BoolVar(&outputJSON, "json", false, "Output as JSON")
	cmd.MarkFlagRequired("vault") //nolint:errcheck
	return cmd
}

func runGetKeyVaultEndpoint(ctx context.Context, tenantID, projectID, vaultID, epID string, outputJSON bool) error {
	creds, err := dcconfig.LoadCredentials()
	if err != nil {
		return err
	}
	vID, err := uuid.Parse(vaultID)
	if err != nil {
		return fmt.Errorf("invalid Key Vault id %q: %w", vaultID, err)
	}
	eID, err := uuid.Parse(epID)
	if err != nil {
		return fmt.Errorf("invalid endpoint id %q: %w", epID, err)
	}

	apiClient := client.New(creds.AccessToken)
	resp, err := apiClient.Typed.GetKeyVaultPrivateEndpointWithResponse(ctx, tenantID, projectID, vID, eID)
	if err != nil {
		return fmt.Errorf("GET /v1/tenants/%s/keyvaults/%s/private-endpoints/%s: %w", tenantID, vaultID, epID, err)
	}
	if resp.StatusCode() != http.StatusOK || resp.JSON200 == nil {
		return cliutil.APIErrorf(resp.StatusCode(), resp.Body)
	}

	if outputJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(resp.JSON200)
	}

	printPrivateEndpoint(resp.JSON200)
	return nil
}

func printPrivateEndpoint(e *dcapi.PrivateEndpoint) {
	row(10, "ID", e.Id.String())
	row(10, "Name", e.Name)
	row(10, "Vault", e.TargetId.String())
	row(10, "VNet", e.VnetId.String())
	row(10, "Subnet", e.SubnetId.String())
	row(10, "IP", cliutil.Deref(e.IpAddress))
	row(10, "Hostname", cliutil.Deref(e.Hostname))
	row(10, "Status", string(e.Status))
	row(10, "Message", cliutil.Deref(e.Message))
	row(10, "Created", formatTime(e.CreatedAt))
}
