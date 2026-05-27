// Package keyvault — Key Vault secret list command.
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

func newSecretListCmd() *cobra.Command {
	var limit int
	var outputJSON bool

	cmd := &cobra.Command{
		Use:   "list <vault-id>",
		Short: "List secret names in a Key Vault (values not included)",
		Long: `List the names + metadata of all secrets in a Key Vault. Values are NOT
returned — use 'dcctl keyvault secret get' for that. Viewers can list names;
member-or-higher is required to read values.

Pagination is automatic — this command walks the cursor until all pages are
fetched.

Examples:
  dcctl keyvault secret list <vault>
  dcctl keyvault secret list <vault> --json
  dcctl keyvault secret list <vault> --limit 500`,
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
			return runListKeyVaultSecrets(cmd.Context(), tenantID, projectID, args[0], limit, outputJSON)
		},
	}
	cmd.Flags().IntVar(&limit, "limit", 100, "Per-page limit (max 500)")
	cmd.Flags().BoolVar(&outputJSON, "json", false, "Output as JSON")
	return cmd
}

func runListKeyVaultSecrets(ctx context.Context, tenantID, projectID, vaultID string, limit int, outputJSON bool) error {
	creds, err := dcconfig.LoadCredentials()
	if err != nil {
		return err
	}
	vID, err := uuid.Parse(vaultID)
	if err != nil {
		return fmt.Errorf("invalid Key Vault id %q: %w", vaultID, err)
	}
	apiClient := client.New(creds.AccessToken)

	var all []dcapi.KeyVaultSecretSummary
	cursor := ""
	for {
		params := &dcapi.ListKeyVaultSecretsParams{}
		if limit > 0 {
			l := limit
			params.Limit = &l
		}
		if cursor != "" {
			c := cursor
			params.Cursor = &c
		}
		resp, err := apiClient.Typed.ListKeyVaultSecretsWithResponse(ctx, tenantID, projectID, vID, params)
		if err != nil {
			return fmt.Errorf("GET /v1/.../keyvaults/%s/secrets: %w", vaultID, err)
		}
		if resp.StatusCode() != http.StatusOK || resp.JSON200 == nil {
			return cliutil.APIErrorf(resp.StatusCode(), resp.Body)
		}
		all = append(all, resp.JSON200.Items...)
		if resp.JSON200.NextCursor == nil || *resp.JSON200.NextCursor == "" {
			break
		}
		cursor = *resp.JSON200.NextCursor
	}

	if outputJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(all)
	}

	if len(all) == 0 {
		fmt.Println("No secrets in this key vault.")
		return nil
	}

	fmt.Printf("    %-40s  %-8s  %-20s  %-20s  %s\n", "NAME", "VERSION", "CREATED", "UPDATED", "DELETED")
	for _, s := range all {
		deleted := ""
		if s.DeletedAt != nil {
			deleted = formatTime(*s.DeletedAt)
		}
		fmt.Printf("    %-40s  %-8d  %-20s  %-20s  %s\n",
			truncate(s.Name, 40),
			s.LatestVersion,
			formatTime(s.CreatedAt),
			formatTime(s.UpdatedAt),
			deleted,
		)
	}
	fmt.Printf("\n%d secret(s)\n", len(all))
	return nil
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n-1] + "…"
}
