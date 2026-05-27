package tenant

import (
	"bufio"
	"context"
	"fmt"
	"net/http"
	"os"
	"strings"

	"github.com/spf13/cobra"
	"github.com/wso2/dcctl/cmd/internal/cliutil"
	"github.com/wso2/dcctl/internal/client"
	dcconfig "github.com/wso2/dcctl/internal/config"
)

func newMemberDeleteCmd() *cobra.Command {
	var force bool

	cmd := &cobra.Command{
		Use:   "delete <user_sub>",
		Short: "Remove a user from the tenant",
		Long: `Remove a user from the active (or specified) tenant.

Examples:
  dcctl tenant member delete alice@example.com
  dcctl tenant member delete alice@example.com --yes
  dcctl tenant member delete alice@example.com --tenant other-tenant`,
		Args:         cobra.ExactArgs(1),
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			tenantFlag, _ := cmd.Root().PersistentFlags().GetString("tenant")
			tenantID, err := dcconfig.GetTenantID(tenantFlag)
			if err != nil {
				return err
			}
			return runMemberDelete(cmd.Context(), args[0], tenantID, force)
		},
	}
	cmd.Flags().BoolVarP(&force, "yes", "y", false, "Skip confirmation prompt")
	return cmd
}

func runMemberDelete(ctx context.Context, userSub, tenantID string, force bool) error {
	if !confirm(fmt.Sprintf("Remove %s from tenant %s? This cannot be undone. [y/N] ", userSub, tenantID), force) {
		fmt.Println("Cancelled.")
		return nil
	}

	creds, err := dcconfig.LoadCredentials()
	if err != nil {
		return err
	}

	apiClient := client.New(creds.AccessToken)
	resp, err := apiClient.Typed.RemoveMemberWithResponse(ctx, tenantID, userSub)
	if err != nil {
		return fmt.Errorf("DELETE /v1/tenants/%s/members/%s: %w", tenantID, userSub, err)
	}
	if resp.StatusCode() >= http.StatusMultipleChoices {
		return cliutil.APIErrorf(resp.StatusCode(), resp.Body)
	}

	fmt.Printf("Removed %s from tenant %s.\n", userSub, tenantID)
	return nil
}

// confirm prompts the user before a destructive action. Shared between the
// member-delete and service-account-delete commands in this package.
func confirm(prompt string, force bool) bool {
	if force {
		return true
	}
	fmt.Print(prompt)
	reader := bufio.NewReader(os.Stdin)
	line, _ := reader.ReadString('\n')
	return strings.EqualFold(strings.TrimSpace(line), "y")
}
