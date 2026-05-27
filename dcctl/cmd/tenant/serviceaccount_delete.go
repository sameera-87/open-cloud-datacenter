package tenant

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

func newServiceAccountDeleteCmd() *cobra.Command {
	var force bool

	cmd := &cobra.Command{
		Use:   "delete <name-or-id>",
		Short: "Delete a service account from the tenant",
		Long: `Delete a service account. If a name is given, the ID is resolved via a list
call first, then the SA is deleted by ID.

Any active sessions using the SA token will be rejected (401) immediately after
deletion — tokens are validated against the database on every request.

Examples:
  dcctl tenant service-account delete ci-deploy
  dcctl tenant service-account delete ci-deploy --yes
  dcctl tenant service-account delete <uuid> --tenant other-tenant`,
		Args:         cobra.ExactArgs(1),
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
			return runServiceAccountDelete(cmd.Context(), args[0], tenantID, projectID, force)
		},
	}
	cmd.Flags().BoolVarP(&force, "yes", "y", false, "Skip confirmation prompt")
	return cmd
}

func runServiceAccountDelete(ctx context.Context, nameOrID, tenantID, projectID string, force bool) error {
	creds, err := dcconfig.LoadCredentials()
	if err != nil {
		return err
	}

	apiClient := client.New(creds.AccessToken)

	saID, err := uuid.Parse(nameOrID)
	if err != nil {
		resolved, err := resolveServiceAccountIDByName(ctx, apiClient, tenantID, projectID, nameOrID)
		if err != nil {
			return err
		}
		saID = resolved
	}

	if !confirm(fmt.Sprintf("Delete service account %q from tenant %s? This cannot be undone. [y/N] ",
		nameOrID, tenantID), force) {
		fmt.Println("Cancelled.")
		return nil
	}

	resp, err := apiClient.Typed.DeleteServiceAccountWithResponse(ctx, tenantID, projectID, saID)
	if err != nil {
		return fmt.Errorf("DELETE /v1/tenants/%s/service-accounts/%s: %w", tenantID, saID, err)
	}
	if resp.StatusCode() >= http.StatusMultipleChoices {
		return cliutil.APIErrorf(resp.StatusCode(), resp.Body)
	}

	fmt.Printf("Service account %q deleted from tenant %s.\n", nameOrID, tenantID)
	return nil
}

// resolveServiceAccountIDByName lists service accounts and returns the ID of
// the one whose name matches exactly. Returns an error if not found.
func resolveServiceAccountIDByName(ctx context.Context, apiClient *client.Client, tenantID, projectID, name string) (uuid.UUID, error) {
	resp, err := apiClient.Typed.ListServiceAccountsWithResponse(ctx, tenantID, projectID)
	if err != nil {
		return uuid.Nil, fmt.Errorf("list service accounts: %w", err)
	}
	if resp.StatusCode() != http.StatusOK || resp.JSON200 == nil || resp.JSON200.ServiceAccounts == nil {
		return uuid.Nil, cliutil.APIErrorf(resp.StatusCode(), resp.Body)
	}
	for _, sa := range *resp.JSON200.ServiceAccounts {
		if sa.Name == name {
			return sa.Id, nil
		}
	}
	return uuid.Nil, fmt.Errorf("service account %q not found in tenant %s", name, tenantID)
}
