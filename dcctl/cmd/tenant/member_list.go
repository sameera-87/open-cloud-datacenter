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
	dcconfig "github.com/wso2/dcctl/internal/config"
)

func newMemberListCmd() *cobra.Command {
	var outputJSON bool

	cmd := &cobra.Command{
		Use:   "list",
		Short: "List all human members of the tenant",
		Long: `List all human members of the active (or specified) tenant.

Examples:
  dcctl tenant member list
  dcctl tenant member list --json
  dcctl tenant member list --tenant other-tenant`,
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			tenantFlag, _ := cmd.Root().PersistentFlags().GetString("tenant")
			tenantID, err := dcconfig.GetTenantID(tenantFlag)
			if err != nil {
				return err
			}
			return runMemberList(cmd.Context(), tenantID, outputJSON)
		},
	}
	cmd.Flags().BoolVar(&outputJSON, "json", false, "Output as JSON")
	return cmd
}

func runMemberList(ctx context.Context, tenantID string, outputJSON bool) error {
	creds, err := dcconfig.LoadCredentials()
	if err != nil {
		return err
	}

	apiClient := client.New(creds.AccessToken)
	resp, err := apiClient.Typed.ListMembersWithResponse(ctx, tenantID)
	if err != nil {
		return fmt.Errorf("GET /v1/tenants/%s/members: %w", tenantID, err)
	}
	if resp.StatusCode() != http.StatusOK || resp.JSON200 == nil || resp.JSON200.Members == nil {
		return cliutil.APIErrorf(resp.StatusCode(), resp.Body)
	}
	members := *resp.JSON200.Members

	if outputJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(members)
	}

	if len(members) == 0 {
		fmt.Println("No members found.")
		return nil
	}

	fmt.Printf("%-40s  %-8s  %-20s  %s\n", "PRINCIPAL_ID", "ROLE", "GRANTED_AT", "GRANTED_BY")
	fmt.Printf("%-40s  %-8s  %-20s  %s\n",
		strings.Repeat("-", 40), "--------", "--------------------", "--------------------")
	for _, m := range members {
		fmt.Printf("%-40s  %-8s  %-20s  %s\n",
			m.PrincipalId, m.Role, m.GrantedAt.Format("2006-01-02 15:04:05"), m.GrantedBy)
	}
	return nil
}
