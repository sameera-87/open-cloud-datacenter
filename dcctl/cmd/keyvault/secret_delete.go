// Package keyvault — Key Vault secret delete command (soft-delete).
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

func newSecretDeleteCmd() *cobra.Command {
	var yes bool

	cmd := &cobra.Command{
		Use:   "delete <vault-id> <key>",
		Short: "Soft-delete a secret from a Key Vault",
		Long: `Soft-delete a secret. The vault's soft_delete_days governs how long
the secret is recoverable before KV-v2 permanently purges it.

Examples:
  dcctl keyvault secret delete <vault> db-password
  dcctl keyvault secret delete <vault> db-password --yes`,
		Args: cobra.ExactArgs(2),
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
			if !confirm(fmt.Sprintf("Soft-delete secret %q from vault %s? [y/N]: ", args[1], args[0]), yes) {
				fmt.Println("Aborted.")
				return nil
			}
			return runDeleteKeyVaultSecret(cmd.Context(), tenantID, projectID, args[0], args[1])
		},
	}
	cmd.Flags().BoolVar(&yes, "yes", false, "Skip confirmation prompt")
	return cmd
}

func runDeleteKeyVaultSecret(ctx context.Context, tenantID, projectID, vaultID, key string) error {
	creds, err := dcconfig.LoadCredentials()
	if err != nil {
		return err
	}
	vID, err := uuid.Parse(vaultID)
	if err != nil {
		return fmt.Errorf("invalid Key Vault id %q: %w", vaultID, err)
	}
	apiClient := client.New(creds.AccessToken)
	resp, err := apiClient.Typed.DeleteKeyVaultSecretWithResponse(ctx, tenantID, projectID, vID, key)
	if err != nil {
		return fmt.Errorf("DELETE /v1/.../keyvaults/%s/secrets/%s: %w", vaultID, key, err)
	}
	if resp.StatusCode() != http.StatusNoContent {
		return cliutil.APIErrorf(resp.StatusCode(), resp.Body)
	}
	fmt.Printf("Soft-deleted secret %q. Recoverable until soft_delete_days elapses.\n", key)
	return nil
}
