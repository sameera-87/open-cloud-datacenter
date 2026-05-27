package tenant

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"

	"github.com/spf13/cobra"
	"github.com/wso2/dcctl/cmd/internal/cliutil"
	"github.com/wso2/dcctl/internal/client"
	dcapi "github.com/wso2/dcctl/internal/client/generated"
	dcconfig "github.com/wso2/dcctl/internal/config"
)

func newListCmd() *cobra.Command {
	var outputJSON bool

	cmd := &cobra.Command{
		Use:   "list",
		Short: "List all tenants the authenticated user can access",
		Long: `List all tenants accessible to the authenticated user.

The active tenant (set with 'dcctl tenant set') is marked with '*'.
Use 'dcctl tenant set <id>' to switch the active tenant.

Examples:
  dcctl tenant list
  dcctl tenant list --json`,
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runListTenants(cmd.Context(), outputJSON)
		},
	}
	cmd.Flags().BoolVar(&outputJSON, "json", false, "Output as JSON")
	return cmd
}

func runListTenants(ctx context.Context, outputJSON bool) error {
	creds, err := dcconfig.LoadCredentials()
	if err != nil {
		return err
	}

	apiClient := client.New(creds.AccessToken)
	resp, err := apiClient.Typed.ListTenantsWithResponse(ctx)
	if err != nil {
		return fmt.Errorf("GET /v1/tenants: %w", err)
	}
	if resp.StatusCode() != http.StatusOK || resp.JSON200 == nil {
		return cliutil.APIErrorf(resp.StatusCode(), resp.Body)
	}
	tenants := *resp.JSON200

	// Resolve the active tenant to mark it in the table.
	activeCtx, _ := dcconfig.LoadContext()
	activeTenant := ""
	if activeCtx != nil {
		activeTenant = activeCtx.ActiveTenant
	}
	if activeTenant == "" {
		activeTenant = creds.TenantID
	}

	if outputJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(tenants)
	}

	if len(tenants) == 0 {
		fmt.Println("No tenants found.")
		return nil
	}

	fmt.Printf("%-2s  %-30s  %-30s  %s\n", "", "ID", "NAME", "ROLES")
	fmt.Printf("%-2s  %-30s  %-30s  %s\n",
		"--", strings.Repeat("-", 30), strings.Repeat("-", 30), "-----")
	for _, t := range tenants {
		marker := "  "
		if t.Id == activeTenant {
			marker = "* "
		}
		fmt.Printf("%-2s  %-30s  %-30s  %s\n",
			marker,
			cliutil.Truncate(t.Id, 30),
			cliutil.Truncate(t.Name, 30),
			formatTenantRoles(t.Roles),
		)
	}
	if activeTenant == "" {
		fmt.Printf("\nNo active tenant. Run 'dcctl tenant set <id>' to choose one.\n")
	}
	return nil
}

func formatTenantRoles(roles []dcapi.TenantSummaryRoles) string {
	if len(roles) == 0 {
		return ""
	}
	parts := make([]string, len(roles))
	for i, r := range roles {
		parts[i] = string(r)
	}
	return strings.Join(parts, ", ")
}
