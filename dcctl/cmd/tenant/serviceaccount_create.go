package tenant

import (
	"context"
	"fmt"
	"net/http"
	"os"

	"github.com/spf13/cobra"
	"github.com/wso2/dcctl/cmd/internal/cliutil"
	"github.com/wso2/dcctl/internal/client"
	dcapi "github.com/wso2/dcctl/internal/client/generated"
	dcconfig "github.com/wso2/dcctl/internal/config"
)

func newServiceAccountCreateCmd() *cobra.Command {
	var role string
	var description string

	cmd := &cobra.Command{
		Use:   "create <name>",
		Short: "Create a new service account for the tenant",
		Long: `Create a new service account and print the one-time token.

The token is shown exactly ONCE. Store it immediately — it cannot be retrieved
again. Use it as a bearer token in the Authorization header:

  Authorization: Bearer <token>

Examples:
  dcctl tenant service-account create ci-deploy --role member
  dcctl tenant service-account create ci-deploy --role member --description "CI pipeline account"
  dcctl tenant service-account create ci-deploy --role member --tenant other-tenant`,
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
			return runServiceAccountCreate(cmd.Context(), args[0], role, description, tenantID, projectID)
		},
	}
	cmd.Flags().StringVar(&role, "role", "member", "Role to grant: owner, member, or viewer")
	cmd.Flags().StringVar(&description, "description", "", "Optional human-readable description")
	_ = cmd.MarkFlagRequired("role")
	return cmd
}

func runServiceAccountCreate(ctx context.Context, name, role, description, tenantID, projectID string) error {
	creds, err := dcconfig.LoadCredentials()
	if err != nil {
		return err
	}

	body := dcapi.CreateServiceAccountRequest{
		Name: name,
		Role: dcapi.CreateServiceAccountRequestRole(role),
	}
	if description != "" {
		body.Description = &description
	}

	apiClient := client.New(creds.AccessToken)
	resp, err := apiClient.Typed.CreateServiceAccountWithResponse(ctx, tenantID, projectID, body)
	if err != nil {
		return fmt.Errorf("POST /v1/tenants/%s/service-accounts: %w", tenantID, err)
	}
	if resp.StatusCode() != http.StatusCreated || resp.JSON201 == nil {
		return cliutil.APIErrorf(resp.StatusCode(), resp.Body)
	}
	sa := resp.JSON201

	if isTerminal(os.Stdout) {
		fmt.Fprintln(os.Stderr, "WARNING: The token below is shown ONCE and cannot be retrieved again. Store it securely.")
	}

	fmt.Printf("Service account %q created.\n\n", sa.Name)
	fmt.Printf("Token (shown ONCE — store it now):\n\n")
	fmt.Printf("  %s\n\n", sa.Token)
	fmt.Printf("ID:          %s\n", sa.Id)
	fmt.Printf("Role:        %s\n", sa.Role)
	if sa.Description != nil && *sa.Description != "" {
		fmt.Printf("Description: %s\n", *sa.Description)
	}
	fmt.Printf("Created at:  %s\n", sa.CreatedAt.Format("2006-01-02T15:04:05Z07:00"))
	return nil
}

func isTerminal(f *os.File) bool {
	fi, err := f.Stat()
	if err != nil {
		return false
	}
	return fi.Mode()&os.ModeCharDevice != 0
}
