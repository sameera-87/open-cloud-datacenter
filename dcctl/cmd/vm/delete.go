package vm

import (
	"bufio"
	"context"
	"fmt"
	"net/http"
	"os"
	"strings"

	"github.com/google/uuid"
	"github.com/spf13/cobra"
	"github.com/wso2/dcctl/cmd/internal/cliutil"
	"github.com/wso2/dcctl/internal/client"
	dcconfig "github.com/wso2/dcctl/internal/config"
)

func newDeleteCmd() *cobra.Command {
	var force bool

	cmd := &cobra.Command{
		Use:   "delete <id>",
		Short: "Delete a virtual machine",
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
			return runDeleteVM(cmd.Context(), tenantID, projectID, args[0], force)
		},
	}
	cmd.Flags().BoolVarP(&force, "yes", "y", false, "Skip confirmation prompt")
	return cmd
}

func runDeleteVM(ctx context.Context, tenantID, projectID, id string, force bool) error {
	if !confirm(fmt.Sprintf("Delete VM %s? This cannot be undone. [y/N] ", id), force) {
		fmt.Println("Cancelled.")
		return nil
	}

	parsedID, err := uuid.Parse(id)
	if err != nil {
		return fmt.Errorf("invalid VM id %q: %w", id, err)
	}

	creds, err := dcconfig.LoadCredentials()
	if err != nil {
		return err
	}
	apiClient := client.New(creds.AccessToken)

	resp, err := apiClient.Typed.DeleteVirtualMachineWithResponse(ctx, tenantID, projectID, parsedID)
	if err != nil {
		return fmt.Errorf("DELETE /v1/tenants/%s/virtual-machines/%s: %w", tenantID, id, err)
	}
	if resp.StatusCode() >= http.StatusMultipleChoices {
		return cliutil.APIErrorf(resp.StatusCode(), resp.Body)
	}
	fmt.Printf("VM %s deletion initiated (status → DELETING).\n", id)
	fmt.Printf("Poll: dcctl vm get %s\n", id)
	return nil
}

// confirm prompts the user with a yes/no question. Returns true if the user
// types "y" (case-insensitive). The `force` flag bypasses the prompt.
func confirm(prompt string, force bool) bool {
	if force {
		return true
	}
	fmt.Print(prompt)
	reader := bufio.NewReader(os.Stdin)
	line, _ := reader.ReadString('\n')
	return strings.EqualFold(strings.TrimSpace(line), "y")
}
