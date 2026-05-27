// Package keyvault — Key Vaults list command (M3 chunk 1).
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
	dcconfig "github.com/wso2/dcctl/internal/config"
)

func newListCmd() *cobra.Command {
	var outputJSON bool

	cmd := &cobra.Command{
		Use:   "list",
		Short: "List Key Vaults for the active project",
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
			return runListKeyVaults(cmd.Context(), tenantID, projectID, outputJSON)
		},
	}
	cmd.Flags().BoolVar(&outputJSON, "json", false, "Output as JSON")
	return cmd
}

func runListKeyVaults(ctx context.Context, tenantID, projectID string, outputJSON bool) error {
	creds, err := dcconfig.LoadCredentials()
	if err != nil {
		return err
	}
	apiClient := client.New(creds.AccessToken)

	resp, err := apiClient.Typed.ListKeyVaultsWithResponse(ctx, tenantID, projectID)
	if err != nil {
		return fmt.Errorf("GET /v1/tenants/%s/keyvaults: %w", tenantID, err)
	}
	if resp.StatusCode() != http.StatusOK || resp.JSON200 == nil {
		return cliutil.APIErrorf(resp.StatusCode(), resp.Body)
	}
	vaults := *resp.JSON200

	if outputJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(vaults)
	}

	if len(vaults) == 0 {
		fmt.Println("No key vaults found.")
		return nil
	}

	fmt.Printf("%-38s  %-30s  %-10s  %-8s  %s\n", "ID", "NAME", "STATUS", "SDD", "CREATED")
	fmt.Printf("%-38s  %-30s  %-10s  %-8s  %s\n",
		"--------------------------------------",
		"------------------------------",
		"----------", "--------", "-------------------")

	for _, k := range vaults {
		fmt.Printf("%-38s  %-30s  %-10s  %-8d  %s\n",
			k.Id.String(), k.Name, string(k.Status), k.SoftDeleteDays,
			cliutil.TruncTime(k.CreatedAt.Format("2006-01-02T15:04:05Z07:00")))
	}
	return nil
}
