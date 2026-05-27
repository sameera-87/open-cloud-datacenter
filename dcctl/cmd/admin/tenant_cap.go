package admin

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"

	"github.com/spf13/cobra"
	"github.com/wso2/dcctl/internal/client"
	dcapi "github.com/wso2/dcctl/internal/client/generated"
	dcconfig "github.com/wso2/dcctl/internal/config"
)

func newAdminTenantCapCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "cap",
		Short: "Show or adjust a tenant's capacity cap",
	}
	cmd.AddCommand(newAdminTenantCapShowCmd(), newAdminTenantCapSetCmd())
	return cmd
}

// ── admin tenant cap show ────────────────────────────────────────────────────

func newAdminTenantCapShowCmd() *cobra.Command {
	var outputJSON bool

	cmd := &cobra.Command{
		Use:   "show <tenant-id>",
		Short: "Show the current capacity cap for a tenant",
		Long: `Print the tenant's CPU/memory/storage cap. Requires platform-admin or tenant
membership (anyone in the tenant can read the cap; only admin can change it).

Examples:
  dcctl admin tenant cap show cs-team
  dcctl admin tenant cap show cs-team --json`,
		Args:         cobra.ExactArgs(1),
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runAdminTenantCapShow(cmd.Context(), args[0], outputJSON)
		},
	}
	cmd.Flags().BoolVar(&outputJSON, "json", false, "Output as JSON")
	return cmd
}

func runAdminTenantCapShow(ctx context.Context, tenantID string, outputJSON bool) error {
	creds, err := dcconfig.LoadCredentials()
	if err != nil {
		return err
	}
	apiClient := client.New(creds.AccessToken)

	resp, err := apiClient.Typed.GetTenantCapUsageWithResponse(ctx, tenantID)
	if err != nil {
		return fmt.Errorf("GET /v1/tenants/%s/cap-usage: %w", tenantID, err)
	}
	if resp.StatusCode() != http.StatusOK || resp.JSON200 == nil {
		return apiErrorf(resp.StatusCode(), resp.Body)
	}
	u := resp.JSON200

	if outputJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(u)
	}

	fmt.Printf("Tenant: %s\n", tenantID)
	fmt.Printf("  %-12s %6s   %6s   %6s\n", "", "CPU", "RAM(GiB)", "DISK(GiB)")
	fmt.Printf("  %-12s %6d   %6d   %6d\n", "Cap:", u.Cap.CpuCores, u.Cap.MemoryGb, u.Cap.StorageGb)
	fmt.Printf("  %-12s %6d   %6d   %6d\n", "Allocated:", u.Allocated.CpuCores, u.Allocated.MemoryGb, u.Allocated.StorageGb)
	fmt.Printf("  %-12s %6d   %6d   %6d\n", "Available:", u.Available.CpuCores, u.Available.MemoryGb, u.Available.StorageGb)
	return nil
}

// ── admin tenant cap set ─────────────────────────────────────────────────────

func newAdminTenantCapSetCmd() *cobra.Command {
	var (
		cpuCores   int
		memoryGB   int
		storageGB  int
		outputJSON bool
	)

	cmd := &cobra.Command{
		Use:   "set <tenant-id>",
		Short: "Adjust a tenant's capacity cap (platform-admin only)",
		Long: `Update one or more cap dimensions on a tenant. Flags omitted keep their
current value.

The server refuses to shrink any dimension below the sum of project
allocations already in that dimension — to shrink further, resize or delete
projects first.

Examples:
  dcctl admin tenant cap set cs-team --cpu-cores 120
  dcctl admin tenant cap set cs-team --cpu-cores 120 --memory-gb 384 --storage-gb 3000`,
		Args:         cobra.ExactArgs(1),
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			if cpuCores == 0 && memoryGB == 0 && storageGB == 0 {
				return fmt.Errorf("at least one of --cpu-cores, --memory-gb, --storage-gb is required")
			}
			return runAdminTenantCapSet(cmd.Context(), args[0], cpuCores, memoryGB, storageGB, outputJSON)
		},
	}

	cmd.Flags().IntVar(&cpuCores, "cpu-cores", 0, "New CPU cap")
	cmd.Flags().IntVar(&memoryGB, "memory-gb", 0, "New memory cap GiB")
	cmd.Flags().IntVar(&storageGB, "storage-gb", 0, "New storage cap GiB")
	cmd.Flags().BoolVar(&outputJSON, "json", false, "Output as JSON")
	return cmd
}

func runAdminTenantCapSet(ctx context.Context, tenantID string, cpuCores, memoryGB, storageGB int, outputJSON bool) error {
	creds, err := dcconfig.LoadCredentials()
	if err != nil {
		return err
	}
	apiClient := client.New(creds.AccessToken)

	body := dcapi.UpdateTenantCapRequest{}
	if cpuCores > 0 {
		body.CpuCoresCap = &cpuCores
	}
	if memoryGB > 0 {
		body.MemoryGbCap = &memoryGB
	}
	if storageGB > 0 {
		body.StorageGbCap = &storageGB
	}

	resp, err := apiClient.Typed.AdminUpdateTenantCapWithResponse(ctx, tenantID, body)
	if err != nil {
		return fmt.Errorf("PATCH /v1/admin/tenants/%s: %w", tenantID, err)
	}
	if resp.StatusCode() != http.StatusOK || resp.JSON200 == nil {
		return apiErrorf(resp.StatusCode(), resp.Body)
	}
	t := resp.JSON200

	if outputJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(t)
	}

	fmt.Printf("Tenant %q cap updated\n", t.Id)
	fmt.Printf("  Caps: %d vCPU / %d GiB RAM / %d GiB storage\n",
		intPtr(t.CpuCoresCap), intPtr(t.MemoryGbCap), intPtr(t.StorageGbCap))
	return nil
}
