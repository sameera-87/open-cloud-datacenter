package vm

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"time"

	"github.com/google/uuid"
	"github.com/spf13/cobra"
	"github.com/wso2/dcctl/cmd/internal/cliutil"
	"github.com/wso2/dcctl/internal/client"
	dcapi "github.com/wso2/dcctl/internal/client/generated"
	dcconfig "github.com/wso2/dcctl/internal/config"
)

func newGetCmd() *cobra.Command {
	var outputJSON bool

	cmd := &cobra.Command{
		Use:   "get <id>",
		Short: "Get a virtual machine",
		Args:  cobra.ExactArgs(1),
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
			return runGetVM(cmd.Context(), tenantID, projectID, args[0], outputJSON)
		},
	}
	cmd.Flags().BoolVar(&outputJSON, "json", false, "Output as JSON")
	return cmd
}

func runGetVM(ctx context.Context, tenantID, projectID, id string, outputJSON bool) error {
	creds, err := dcconfig.LoadCredentials()
	if err != nil {
		return err
	}
	parsedID, err := uuid.Parse(id)
	if err != nil {
		return fmt.Errorf("invalid VM id %q: %w", id, err)
	}

	apiClient := client.New(creds.AccessToken)
	resp, err := apiClient.Typed.GetVirtualMachineWithResponse(ctx, tenantID, projectID, parsedID)
	if err != nil {
		return fmt.Errorf("GET /v1/tenants/%s/virtual-machines/%s: %w", tenantID, id, err)
	}
	if resp.StatusCode() != http.StatusOK || resp.JSON200 == nil {
		return cliutil.APIErrorf(resp.StatusCode(), resp.Body)
	}

	if outputJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(resp.JSON200)
	}

	printVirtualMachine(resp.JSON200)
	return nil
}

func printVirtualMachine(vm *dcapi.VirtualMachine) {
	size := ""
	if vm.Size != nil {
		size = string(*vm.Size)
	}
	row(10, "ID", vm.Id.String())
	row(10, "Name", vm.Name)
	row(10, "Size", size)
	row(10, "Status", string(vm.Status))
	row(10, "IP", cliutil.Deref(vm.IpAddress))
	row(10, "Provider", vm.ProviderType)
	row(10, "Tenant", vm.TenantId)
	row(10, "Created", formatTime(vm.CreatedAt))
	row(10, "Message", cliutil.Deref(vm.Message))
}

func row(labelWidth int, label, value string) {
	if value == "" {
		return
	}
	fmt.Printf("  %-*s %s\n", labelWidth, label+":", value)
}

func formatTime(t time.Time) string {
	return t.Format("2006-01-02T15:04:05Z07:00")
}
