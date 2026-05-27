// Package keyvault — Key Vault delete command (M3 chunk 1).
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

func newDeleteCmd() *cobra.Command {
	var skipConfirm bool

	cmd := &cobra.Command{
		Use:   "delete <id>",
		Short: "Delete a Key Vault",
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
			return runDeleteKeyVault(cmd.Context(), tenantID, projectID, args[0], skipConfirm)
		},
	}
	cmd.Flags().BoolVarP(&skipConfirm, "yes", "y", false, "Skip confirmation prompt")
	return cmd
}

func runDeleteKeyVault(ctx context.Context, tenantID, projectID, id string, skipConfirm bool) error {
	if !confirm(fmt.Sprintf("Delete key vault %s? This cannot be undone. [y/N] ", id), skipConfirm) {
		fmt.Println("Cancelled.")
		return nil
	}

	parsedID, err := uuid.Parse(id)
	if err != nil {
		return fmt.Errorf("invalid Key Vault id %q: %w", id, err)
	}

	creds, err := dcconfig.LoadCredentials()
	if err != nil {
		return err
	}
	apiClient := client.New(creds.AccessToken)

	resp, err := apiClient.Typed.DeleteKeyVaultWithResponse(ctx, tenantID, projectID, parsedID)
	if err != nil {
		return fmt.Errorf("DELETE /v1/tenants/%s/keyvaults/%s: %w", tenantID, id, err)
	}
	if resp.StatusCode() >= http.StatusMultipleChoices {
		return cliutil.APIErrorf(resp.StatusCode(), resp.Body)
	}
	fmt.Printf("Key Vault %s deletion initiated.\n", id)
	return nil
}
