// Package keyvault — Key Vault Private Endpoints list (M3 chunk 2).
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
	dcconfig "github.com/wso2/dcctl/internal/config"
)

func newEndpointListCmd() *cobra.Command {
	var outputJSON bool

	cmd := &cobra.Command{
		Use:   "list <vault-id>",
		Short: "List Private Endpoints for a Key Vault",
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
			return runListKeyVaultEndpoints(cmd.Context(), tenantID, projectID, args[0], outputJSON)
		},
	}
	cmd.Flags().BoolVar(&outputJSON, "json", false, "Output as JSON")
	return cmd
}

func runListKeyVaultEndpoints(ctx context.Context, tenantID, projectID, vaultID string, outputJSON bool) error {
	creds, err := dcconfig.LoadCredentials()
	if err != nil {
		return err
	}
	vID, err := uuid.Parse(vaultID)
	if err != nil {
		return fmt.Errorf("invalid Key Vault id %q: %w", vaultID, err)
	}

	apiClient := client.New(creds.AccessToken)
	resp, err := apiClient.Typed.ListKeyVaultPrivateEndpointsWithResponse(ctx, tenantID, projectID, vID)
	if err != nil {
		return fmt.Errorf("GET /v1/tenants/%s/keyvaults/%s/private-endpoints: %w", tenantID, vaultID, err)
	}
	if resp.StatusCode() != http.StatusOK || resp.JSON200 == nil {
		return cliutil.APIErrorf(resp.StatusCode(), resp.Body)
	}
	endpoints := *resp.JSON200

	if outputJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(endpoints)
	}

	if len(endpoints) == 0 {
		fmt.Println("No private endpoints found.")
		return nil
	}

	fmt.Printf("%-38s  %-20s  %-15s  %-30s  %-10s\n", "ID", "NAME", "IP", "HOSTNAME", "STATUS")
	fmt.Printf("%-38s  %-20s  %-15s  %-30s  %-10s\n",
		"--------------------------------------",
		"--------------------",
		"---------------",
		"------------------------------",
		"----------")
	for _, e := range endpoints {
		fmt.Printf("%-38s  %-20s  %-15s  %-30s  %-10s\n",
			e.Id.String(), e.Name, cliutil.DerefOrDash(e.IpAddress), cliutil.DerefOrDash(e.Hostname),
			string(e.Status))
	}
	return nil
}
