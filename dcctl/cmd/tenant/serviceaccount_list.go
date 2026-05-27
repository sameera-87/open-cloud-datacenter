package tenant

import (
	"context"
	"fmt"
	"net/http"
	"strings"

	"github.com/spf13/cobra"
	"github.com/wso2/dcctl/cmd/internal/cliutil"
	"github.com/wso2/dcctl/internal/client"
	dcconfig "github.com/wso2/dcctl/internal/config"
)

func newServiceAccountListCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List all service accounts for the tenant",
		Long: `List all service accounts belonging to the active (or specified) tenant.

Examples:
  dcctl tenant service-account list
  dcctl tenant service-account list --tenant other-tenant`,
		SilenceUsage: true,
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
			return runServiceAccountList(cmd.Context(), tenantID, projectID)
		},
	}
	return cmd
}

func runServiceAccountList(ctx context.Context, tenantID, projectID string) error {
	creds, err := dcconfig.LoadCredentials()
	if err != nil {
		return err
	}

	apiClient := client.New(creds.AccessToken)
	resp, err := apiClient.Typed.ListServiceAccountsWithResponse(ctx, tenantID, projectID)
	if err != nil {
		return fmt.Errorf("GET /v1/tenants/%s/service-accounts: %w", tenantID, err)
	}
	if resp.StatusCode() != http.StatusOK || resp.JSON200 == nil || resp.JSON200.ServiceAccounts == nil {
		return cliutil.APIErrorf(resp.StatusCode(), resp.Body)
	}
	sas := *resp.JSON200.ServiceAccounts

	if len(sas) == 0 {
		fmt.Println("No service accounts found.")
		return nil
	}

	fmt.Printf("%-30s  %-36s  %-8s  %-20s  %s\n",
		"NAME", "ID", "ROLE", "LAST_USED", "DESCRIPTION")
	fmt.Printf("%-30s  %-36s  %-8s  %-20s  %s\n",
		strings.Repeat("-", 30), strings.Repeat("-", 36),
		"--------", "--------------------", "--------------------")
	for _, sa := range sas {
		lastUsed := "never"
		if sa.LastUsed != nil {
			lastUsed = sa.LastUsed.Format("2006-01-02 15:04:05")
		}
		desc := ""
		if sa.Description != nil {
			desc = *sa.Description
		}
		fmt.Printf("%-30s  %-36s  %-8s  %-20s  %s\n",
			cliutil.Truncate(sa.Name, 30), sa.Id, sa.Role, cliutil.Truncate(lastUsed, 20), desc)
	}
	return nil
}
