package bastion

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
		Short: "Get a bastion host",
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
			return runGetBastion(cmd.Context(), tenantID, projectID, args[0], outputJSON)
		},
	}
	cmd.Flags().BoolVar(&outputJSON, "json", false, "Output as JSON")
	return cmd
}

func runGetBastion(ctx context.Context, tenantID, projectID, id string, outputJSON bool) error {
	creds, err := dcconfig.LoadCredentials()
	if err != nil {
		return err
	}
	parsedID, err := uuid.Parse(id)
	if err != nil {
		return fmt.Errorf("invalid bastion id %q: %w", id, err)
	}

	apiClient := client.New(creds.AccessToken)
	resp, err := apiClient.Typed.GetBastionWithResponse(ctx, tenantID, projectID, parsedID)
	if err != nil {
		return fmt.Errorf("GET /v1/tenants/%s/bastions/%s: %w", tenantID, id, err)
	}
	if resp.StatusCode() != http.StatusOK || resp.JSON200 == nil {
		return cliutil.APIErrorf(resp.StatusCode(), resp.Body)
	}

	if outputJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(resp.JSON200)
	}

	printBastion(resp.JSON200)
	return nil
}

func printBastion(b *dcapi.Bastion) {
	row(12, "ID", b.Id.String())
	row(12, "Name", b.Name)
	row(12, "Status", string(b.Status))
	row(12, "mgmt IP", cliutil.Deref(b.MgmtIp))
	row(12, "internal IP", cliutil.Deref(b.InternalIp))
	if b.VnetId != nil {
		row(12, "VNet", b.VnetId.String())
	}
	if b.SubnetId != nil {
		row(12, "Subnet", b.SubnetId.String())
	}
	row(12, "Provider", b.ProviderType)
	row(12, "Tenant", b.TenantId)
	row(12, "Description", cliutil.Deref(b.Description))
	row(12, "Created", formatTime(b.CreatedAt))
	row(12, "Message", cliutil.Deref(b.Message))
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
