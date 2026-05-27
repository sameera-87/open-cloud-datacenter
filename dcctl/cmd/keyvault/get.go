// Package keyvault — Key Vault get command (M3 chunk 1).
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

func newGetCmd() *cobra.Command {
	var outputJSON bool

	cmd := &cobra.Command{
		Use:   "get <id>",
		Short: "Get a Key Vault by UUID",
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
			return runGetKeyVault(cmd.Context(), tenantID, projectID, args[0], outputJSON)
		},
	}
	cmd.Flags().BoolVar(&outputJSON, "json", false, "Output as JSON")
	return cmd
}

func runGetKeyVault(ctx context.Context, tenantID, projectID, id string, outputJSON bool) error {
	creds, err := dcconfig.LoadCredentials()
	if err != nil {
		return err
	}
	parsedID, err := uuid.Parse(id)
	if err != nil {
		return fmt.Errorf("invalid Key Vault id %q: %w", id, err)
	}

	apiClient := client.New(creds.AccessToken)
	resp, err := apiClient.Typed.GetKeyVaultWithResponse(ctx, tenantID, projectID, parsedID)
	if err != nil {
		return fmt.Errorf("GET /v1/tenants/%s/keyvaults/%s: %w", tenantID, id, err)
	}
	if resp.StatusCode() != http.StatusOK || resp.JSON200 == nil {
		return cliutil.APIErrorf(resp.StatusCode(), resp.Body)
	}

	if outputJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(resp.JSON200)
	}

	printKeyVault(resp.JSON200)
	return nil
}

func printKeyVault(k *dcapi.KeyVault) {
	row(17, "ID", k.Id.String())
	row(17, "Name", k.Name)
	row(17, "Tenant", k.TenantId)
	row(17, "Status", string(k.Status))
	row(17, "Soft-delete days", fmt.Sprintf("%d", k.SoftDeleteDays))
	row(17, "Created", formatTime(k.CreatedAt))
	row(17, "Updated", formatTime(k.UpdatedAt))
	row(17, "Message", cliutil.Deref(k.Message))
}
