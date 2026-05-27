// Package keyvault — Key Vault secret restore (undelete) command.
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

func newSecretRestoreCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "restore <vault-id> <key>",
		Short: "Restore the latest soft-deleted version of a secret",
		Long: `Reverses a soft-delete by calling OpenBao's KV-v2 undelete on the
secret's latest deleted version. After restore, the key is readable again
under its latest version (the value is unchanged — undelete clears the
deletion marker, it doesn't write new data).

Only works while the vault's soft_delete_days window has not elapsed.
Once KV-v2 purges (destroys) a version, restore returns 410 Gone.

Examples:
  dcctl keyvault secret restore <vault> db-password`,
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
			return runRestoreKeyVaultSecret(cmd.Context(), tenantID, projectID, args[0], args[1])
		},
	}
	return cmd
}

func runRestoreKeyVaultSecret(ctx context.Context, tenantID, projectID, vaultID, key string) error {
	creds, err := dcconfig.LoadCredentials()
	if err != nil {
		return err
	}
	vID, err := uuid.Parse(vaultID)
	if err != nil {
		return fmt.Errorf("invalid Key Vault id %q: %w", vaultID, err)
	}
	apiClient := client.New(creds.AccessToken)
	resp, err := apiClient.Typed.RestoreKeyVaultSecretWithResponse(ctx, tenantID, projectID, vID, key)
	if err != nil {
		return fmt.Errorf("POST /v1/.../keyvaults/%s/secrets/%s/restore: %w", vaultID, key, err)
	}
	if resp.StatusCode() != http.StatusNoContent {
		return cliutil.APIErrorf(resp.StatusCode(), resp.Body)
	}
	fmt.Printf("Restored secret %q. Latest version is readable again.\n", key)
	return nil
}
