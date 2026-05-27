// Package keyvault — Key Vault creation command (M3 chunk 1).
//
// `dcctl keyvault create <name> [--soft-delete-days <n>]`
package keyvault

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"

	"github.com/spf13/cobra"
	"github.com/wso2/dcctl/cmd/internal/cliutil"
	"github.com/wso2/dcctl/internal/client"
	dcapi "github.com/wso2/dcctl/internal/client/generated"
	dcconfig "github.com/wso2/dcctl/internal/config"
)

func newCreateCmd() *cobra.Command {
	var softDeleteDays int
	var outputJSON bool

	cmd := &cobra.Command{
		Use:   "create <name>",
		Short: "Create a Key Vault (logical record)",
		Long: `Create a Key Vault as a logical container for secrets. Synchronous —
returns immediately with the new vault in ACTIVE state.

Chunk 1 stores only the vault metadata. Per-VPC Private Endpoints,
access policies, and the OpenBao-backed secret data plane land in
subsequent chunks.

Example:
  dcctl keyvault create billing-secrets
  dcctl keyvault create billing-secrets --soft-delete-days 14`,
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
			return runCreateKeyVault(cmd.Context(), tenantID, projectID, args[0], softDeleteDays, outputJSON)
		},
	}
	cmd.Flags().IntVar(&softDeleteDays, "soft-delete-days", 0, "Soft-delete window in days (7..90; default 30)")
	cmd.Flags().BoolVar(&outputJSON, "json", false, "Output as JSON")
	return cmd
}

func runCreateKeyVault(ctx context.Context, tenantID, projectID, name string, softDeleteDays int, outputJSON bool) error {
	creds, err := dcconfig.LoadCredentials()
	if err != nil {
		return err
	}
	apiClient := client.New(creds.AccessToken)

	body := dcapi.CreateKeyVaultRequest{Name: name}
	if softDeleteDays > 0 {
		body.SoftDeleteDays = &softDeleteDays
	}

	resp, err := apiClient.Typed.CreateKeyVaultWithResponse(ctx, tenantID, projectID, body)
	if err != nil {
		return fmt.Errorf("POST /v1/tenants/%s/keyvaults: %w", tenantID, err)
	}
	if resp.StatusCode() != http.StatusCreated || resp.JSON201 == nil {
		return cliutil.APIErrorf(resp.StatusCode(), resp.Body)
	}
	kv := resp.JSON201

	if outputJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(kv)
	}

	fmt.Printf("Key Vault %q created\n", name)
	fmt.Printf("  ID:               %s\n", kv.Id)
	fmt.Printf("  Status:           %s\n", kv.Status)
	fmt.Printf("  Soft-delete days: %d\n", kv.SoftDeleteDays)
	return nil
}
