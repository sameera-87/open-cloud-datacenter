// Package keyvault — Key Vault Private Endpoint delete (M3 chunk 2).
package keyvault

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

func newEndpointDeleteCmd() *cobra.Command {
	var vault string
	var skipConfirm bool

	cmd := &cobra.Command{
		Use:   "delete <ep-id>",
		Short: "Delete a Key Vault Private Endpoint",
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
			return runDeleteKeyVaultEndpoint(cmd.Context(), tenantID, projectID, vault, args[0], skipConfirm)
		},
	}
	cmd.Flags().StringVar(&vault, "vault", "", "Parent vault ID (required)")
	cmd.Flags().BoolVarP(&skipConfirm, "yes", "y", false, "Skip confirmation prompt")
	cmd.MarkFlagRequired("vault") //nolint:errcheck
	return cmd
}

func runDeleteKeyVaultEndpoint(ctx context.Context, tenantID, projectID, vaultID, epID string, skipConfirm bool) error {
	if !confirm(fmt.Sprintf("Delete private endpoint %s? This cannot be undone. [y/N] ", epID), skipConfirm) {
		fmt.Println("Cancelled.")
		return nil
	}

	vID, err := uuid.Parse(vaultID)
	if err != nil {
		return fmt.Errorf("invalid Key Vault id %q: %w", vaultID, err)
	}
	eID, err := uuid.Parse(epID)
	if err != nil {
		return fmt.Errorf("invalid endpoint id %q: %w", epID, err)
	}

	creds, err := dcconfig.LoadCredentials()
	if err != nil {
		return err
	}
	apiClient := client.New(creds.AccessToken)

	resp, err := apiClient.Typed.DeleteKeyVaultPrivateEndpointWithResponse(ctx, tenantID, projectID, vID, eID)
	if err != nil {
		return fmt.Errorf("DELETE /v1/tenants/%s/keyvaults/%s/private-endpoints/%s: %w", tenantID, vaultID, epID, err)
	}
	if resp.StatusCode() >= http.StatusMultipleChoices {
		return cliutil.APIErrorf(resp.StatusCode(), resp.Body)
	}
	fmt.Printf("Private endpoint %s deletion initiated.\n", epID)
	return nil
}
