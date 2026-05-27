package tenant

import (
	"context"
	"fmt"
	"net/http"

	"github.com/spf13/cobra"
	"github.com/wso2/dcctl/cmd/internal/cliutil"
	"github.com/wso2/dcctl/internal/client"
	dcapi "github.com/wso2/dcctl/internal/client/generated"
	dcconfig "github.com/wso2/dcctl/internal/config"
)

func newMemberCreateCmd() *cobra.Command {
	var role string

	cmd := &cobra.Command{
		Use:   "create <user_sub>",
		Short: "Invite a user to the tenant",
		Long: `Invite a user to the active (or specified) tenant and grant them a role.

Examples:
  dcctl tenant member create alice@example.com --role member
  dcctl tenant member create alice@example.com --role owner
  dcctl tenant member create alice@example.com --role viewer --tenant other-tenant`,
		Args:         cobra.ExactArgs(1),
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			tenantFlag, _ := cmd.Root().PersistentFlags().GetString("tenant")
			tenantID, err := dcconfig.GetTenantID(tenantFlag)
			if err != nil {
				return err
			}
			return runMemberCreate(cmd.Context(), args[0], role, tenantID)
		},
	}
	cmd.Flags().StringVar(&role, "role", "member", "Role to grant: owner, member, or viewer")
	_ = cmd.MarkFlagRequired("role")
	return cmd
}

func runMemberCreate(ctx context.Context, userSub, role, tenantID string) error {
	creds, err := dcconfig.LoadCredentials()
	if err != nil {
		return err
	}

	apiClient := client.New(creds.AccessToken)
	resp, err := apiClient.Typed.InviteMemberWithResponse(ctx, tenantID, dcapi.InviteMemberRequest{
		UserSub: userSub,
		Role:    dcapi.InviteMemberRequestRole(role),
	})
	if err != nil {
		return fmt.Errorf("POST /v1/tenants/%s/members: %w", tenantID, err)
	}
	if resp.StatusCode() != http.StatusCreated || resp.JSON201 == nil {
		return cliutil.APIErrorf(resp.StatusCode(), resp.Body)
	}

	fmt.Printf("Added %s to tenant %s with role %s.\n", userSub, tenantID, role)
	return nil
}
