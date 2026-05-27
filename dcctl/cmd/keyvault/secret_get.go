// Package keyvault — Key Vault secret get command.
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

func newSecretGetCmd() *cobra.Command {
	var version int
	var outputJSON bool
	var fieldValue bool

	cmd := &cobra.Command{
		Use:   "get <vault-id> <key>",
		Short: "Read a secret value from a Key Vault",
		Long: `Read a secret's latest value from a Key Vault. dc-api proxies to OpenBao
using its own backend token — the user never handles bao or AppRole creds.

By default, prints a human-readable summary. Use --value to print ONLY the
raw secret value (useful for shell embedding: API_KEY=$(dcctl keyvault
secret get <vault> api-key --value))

Examples:
  dcctl keyvault secret get <vault> db-password
  dcctl keyvault secret get <vault> db-password --version 3
  dcctl keyvault secret get <vault> api-key --value
  export DB_PASS=$(dcctl keyvault secret get <vault> db-password --value)`,
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
			if outputJSON && fieldValue {
				return fmt.Errorf("--json and --value are mutually exclusive")
			}
			return runGetKeyVaultSecret(cmd.Context(), tenantID, projectID, args[0], args[1], version, outputJSON, fieldValue)
		},
	}
	cmd.Flags().IntVar(&version, "version", 0, "Specific version (omit for latest)")
	cmd.Flags().BoolVar(&outputJSON, "json", false, "Output as JSON (includes value)")
	cmd.Flags().BoolVar(&fieldValue, "value", false, "Print only the secret value (no metadata, no newline)")
	return cmd
}

func runGetKeyVaultSecret(ctx context.Context, tenantID, projectID, vaultID, key string, version int, outputJSON, fieldValue bool) error {
	creds, err := dcconfig.LoadCredentials()
	if err != nil {
		return err
	}
	vID, err := uuid.Parse(vaultID)
	if err != nil {
		return fmt.Errorf("invalid Key Vault id %q: %w", vaultID, err)
	}
	apiClient := client.New(creds.AccessToken)

	params := &dcapi.GetKeyVaultSecretParams{}
	if version > 0 {
		v := version
		params.Version = &v
	}
	resp, err := apiClient.Typed.GetKeyVaultSecretWithResponse(ctx, tenantID, projectID, vID, key, params)
	if err != nil {
		return fmt.Errorf("GET /v1/.../keyvaults/%s/secrets/%s: %w", vaultID, key, err)
	}
	if resp.StatusCode() != http.StatusOK || resp.JSON200 == nil {
		return cliutil.APIErrorf(resp.StatusCode(), resp.Body)
	}
	out := resp.JSON200

	switch {
	case fieldValue:
		// raw value, no newline — friendly to $() embedding
		fmt.Print(out.Value)
		return nil
	case outputJSON:
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(out)
	default:
		row(11, "Key", out.Key)
		row(11, "Version", fmt.Sprintf("%d", out.Version))
		row(11, "Created", formatTime(out.CreatedAt))
		if out.DeletedAt != nil {
			row(11, "Deleted", formatTime(*out.DeletedAt))
		}
		row(11, "Value", out.Value)
		if out.Metadata != nil && len(*out.Metadata) > 0 {
			fmt.Println("Metadata:")
			for k, v := range *out.Metadata {
				fmt.Printf("  %s = %s\n", k, v)
			}
		}
		return nil
	}
}
