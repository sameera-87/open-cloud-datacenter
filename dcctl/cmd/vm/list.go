package vm

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
		Short: "List all virtual machines",
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
			return runListVMs(cmd.Context(), tenantID, projectID, outputJSON)
		},
	}
	cmd.Flags().BoolVar(&outputJSON, "json", false, "Output as JSON")
	return cmd
}

func runListVMs(ctx context.Context, tenantID, projectID string, outputJSON bool) error {
	creds, err := dcconfig.LoadCredentials()
	if err != nil {
		return err
	}
	apiClient := client.New(creds.AccessToken)

	resp, err := apiClient.Typed.ListVirtualMachinesWithResponse(ctx, tenantID, projectID)
	if err != nil {
		return fmt.Errorf("GET /v1/tenants/%s/virtual-machines: %w", tenantID, err)
	}
	if resp.StatusCode() != http.StatusOK || resp.JSON200 == nil {
		return cliutil.APIErrorf(resp.StatusCode(), resp.Body)
	}
	vms := *resp.JSON200

	if outputJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(vms)
	}

	if len(vms) == 0 {
		fmt.Println("No virtual machines found.")
		return nil
	}

	fmt.Printf("%-38s  %-20s  %-8s  %-10s  %-16s  %s\n", "ID", "NAME", "SIZE", "STATUS", "IP", "CREATED")
	fmt.Printf("%-38s  %-20s  %-8s  %-10s  %-16s  %s\n",
		"--------------------------------------", "--------------------",
		"--------", "----------", "----------------", "-------------------")

	for _, vm := range vms {
		size := "-"
		if vm.Size != nil {
			size = string(*vm.Size)
		}
		fmt.Printf("%-38s  %-20s  %-8s  %-10s  %-16s  %s\n",
			vm.Id.String(), vm.Name, size, string(vm.Status),
			cliutil.DerefOrDash(vm.IpAddress), cliutil.TruncTime(vm.CreatedAt.Format("2006-01-02T15:04:05Z07:00")))
	}
	return nil
}
