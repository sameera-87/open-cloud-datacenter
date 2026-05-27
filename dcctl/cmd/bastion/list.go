package bastion

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
		Short: "List all bastion hosts",
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
			return runListBastions(cmd.Context(), tenantID, projectID, outputJSON)
		},
	}
	cmd.Flags().BoolVar(&outputJSON, "json", false, "Output as JSON")
	return cmd
}

func runListBastions(ctx context.Context, tenantID, projectID string, outputJSON bool) error {
	creds, err := dcconfig.LoadCredentials()
	if err != nil {
		return err
	}
	apiClient := client.New(creds.AccessToken)

	resp, err := apiClient.Typed.ListBastionsWithResponse(ctx, tenantID, projectID)
	if err != nil {
		return fmt.Errorf("GET /v1/tenants/%s/bastions: %w", tenantID, err)
	}
	if resp.StatusCode() != http.StatusOK || resp.JSON200 == nil {
		return cliutil.APIErrorf(resp.StatusCode(), resp.Body)
	}
	bastions := *resp.JSON200

	if outputJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(bastions)
	}

	if len(bastions) == 0 {
		fmt.Println("No bastions found.")
		return nil
	}

	fmt.Printf("%-38s  %-24s  %-10s  %-16s  %-16s  %s\n",
		"ID", "NAME", "STATUS", "MGMT IP", "INTERNAL IP", "CREATED")
	fmt.Printf("%-38s  %-24s  %-10s  %-16s  %-16s  %s\n",
		"--------------------------------------", "------------------------",
		"----------", "----------------", "----------------", "-------------------")

	for _, b := range bastions {
		fmt.Printf("%-38s  %-24s  %-10s  %-16s  %-16s  %s\n",
			b.Id.String(), b.Name, string(b.Status),
			cliutil.DerefOrDash(b.MgmtIp), cliutil.DerefOrDash(b.InternalIp),
			cliutil.TruncTime(b.CreatedAt.Format("2006-01-02T15:04:05Z07:00")))
	}
	return nil
}
