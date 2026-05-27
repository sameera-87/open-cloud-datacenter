package subnet

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
	var (
		vnet       string
		outputJSON bool
	)

	cmd := &cobra.Command{
		Use:   "get <name-or-id>",
		Short: "Get a subnet",
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
			return runGetSubnet(cmd.Context(), tenantID, projectID, vnet, args[0], outputJSON)
		},
	}
	cmd.Flags().StringVar(&vnet, "vnet", "", "Parent VNet name or ID (required)")
	cmd.Flags().BoolVar(&outputJSON, "json", false, "Output as JSON")
	cmd.MarkFlagRequired("vnet") //nolint:errcheck
	return cmd
}

func runGetSubnet(ctx context.Context, tenantID, projectID, vnetIDOrName, subnetIDOrName string, outputJSON bool) error {
	creds, err := dcconfig.LoadCredentials()
	if err != nil {
		return err
	}
	apiClient := client.New(creds.AccessToken)

	vnetIDStr, err := apiClient.ResolveVNetID(tenantID, projectID, vnetIDOrName)
	if err != nil {
		return err
	}
	subnetIDStr, err := apiClient.ResolveSubnetID(tenantID, projectID, vnetIDStr, subnetIDOrName)
	if err != nil {
		return err
	}
	vnetID, err := uuid.Parse(vnetIDStr)
	if err != nil {
		return fmt.Errorf("invalid VNet id %q: %w", vnetIDStr, err)
	}
	subnetID, err := uuid.Parse(subnetIDStr)
	if err != nil {
		return fmt.Errorf("invalid subnet id %q: %w", subnetIDStr, err)
	}

	resp, err := apiClient.Typed.GetSubnetWithResponse(ctx, tenantID, projectID, vnetID, subnetID)
	if err != nil {
		return fmt.Errorf("GET /v1/tenants/%s/vnets/%s/subnets/%s: %w", tenantID, vnetIDStr, subnetIDStr, err)
	}
	if resp.StatusCode() != http.StatusOK || resp.JSON200 == nil {
		return cliutil.APIErrorf(resp.StatusCode(), resp.Body)
	}

	if outputJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(resp.JSON200)
	}

	printSubnetDetail(resp.JSON200)
	return nil
}

func printSubnetDetail(s *dcapi.Subnet) {
	row(10, "ID", s.Id.String())
	row(10, "VNet", s.VnetId.String())
	row(10, "Name", s.Name)
	row(10, "CIDR", s.Cidr)
	row(10, "Gateway", cliutil.Deref(s.Gateway))
	row(10, "Status", string(s.Status))
	row(10, "Provider", s.ProviderType)
	row(10, "Tenant", s.TenantId)
	row(10, "Created", formatTime(s.CreatedAt))
	row(10, "Updated", formatTime(s.UpdatedAt))
	row(10, "Message", cliutil.Deref(s.Message))
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
