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

func newAdminTenantCreateCmd() *cobra.Command {
	var (
		name         string
		description  string
		cpuCoresCap  int
		memoryGBCap  int
		storageGBCap int
		outputJSON   bool
	)

	cmd := &cobra.Command{
		Use:   "create <tenant-id>",
		Short: "Register a new tenant (platform-admin only)",
		Long: `Register a new tenant in the dc-api registry with an optional capacity cap.

Tenant id must match ^[a-z][a-z0-9-]{0,30}[a-z0-9]$ (lowercase, starts with a letter,
2-32 chars).

Cap flags set the tenant ceiling — the tenant owner distributes this budget across
projects via per-project quotas (managed with 'dcctl project create/update').
Omitted/zero cap flags accept the schema defaults (80 vCPU / 256 GiB RAM / 2 TiB).

Examples:
  dcctl admin tenant create cs-team
  dcctl admin tenant create cs-team --name "Customer Success" --description "SRE-managed"
  dcctl admin tenant create big-team --cpu-cores-cap 500 --memory-gb-cap 2000 --storage-gb-cap 10000`,
		Args:         cobra.ExactArgs(1),
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runAdminTenantCreate(cmd.Context(), args[0], name, description, cpuCoresCap, memoryGBCap, storageGBCap, outputJSON)
		},
	}

	cmd.Flags().StringVar(&name, "name", "", "Display name (defaults to <tenant-id>)")
	cmd.Flags().StringVar(&description, "description", "", "Optional free-form description")
	cmd.Flags().IntVar(&cpuCoresCap, "cpu-cores-cap", 0, "Tenant CPU cap (0 = server default of 80)")
	cmd.Flags().IntVar(&memoryGBCap, "memory-gb-cap", 0, "Tenant memory cap GiB (0 = server default of 256)")
	cmd.Flags().IntVar(&storageGBCap, "storage-gb-cap", 0, "Tenant storage cap GiB (0 = server default of 2000)")
	cmd.Flags().BoolVar(&outputJSON, "json", false, "Output as JSON")
	return cmd
}

func runAdminTenantCreate(ctx context.Context, id, name, description string, cpuCap, memCap, stoCap int, outputJSON bool) error {
	creds, err := dcconfig.LoadCredentials()
	if err != nil {
		return err
	}
	apiClient := client.New(creds.AccessToken)

	body := dcapi.AdminCreateTenantJSONRequestBody{Id: id}
	if name != "" {
		body.Name = &name
	}
	if description != "" {
		body.Description = &description
	}
	if cpuCap > 0 {
		body.CpuCoresCap = &cpuCap
	}
	if memCap > 0 {
		body.MemoryGbCap = &memCap
	}
	if stoCap > 0 {
		body.StorageGbCap = &stoCap
	}

	resp, err := apiClient.Typed.AdminCreateTenantWithResponse(ctx, body)
	if err != nil {
		return fmt.Errorf("POST /v1/admin/tenants: %w", err)
	}
	if resp.StatusCode() != http.StatusCreated || resp.JSON201 == nil {
		return apiErrorf(resp.StatusCode(), resp.Body)
	}
	t := resp.JSON201

	if outputJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(t)
	}

	fmt.Printf("Tenant %q registered\n", t.Id)
	fmt.Printf("  Name:           %s\n", t.Name)
	// CpuCoresCap etc. are *int in the generated client (spec marks them
	// omitempty). Safely dereference — the server always populates these
	// thanks to the schema NOT NULL default, but be defensive.
	fmt.Printf("  Capacity caps:  %d vCPU / %d GiB RAM / %d GiB storage\n",
		intPtr(t.CpuCoresCap), intPtr(t.MemoryGbCap), intPtr(t.StorageGbCap))
	fmt.Printf("\nSet as active tenant with:\n  dcctl tenant set %s\n", t.Id)
	return nil
}

// intPtr returns the value of an optional *int or 0 if nil. Used for
// formatting fields that the spec marks omitempty but the server always
// populates.
func intPtr(p *int) int {
	if p == nil {
		return 0
	}
	return *p
}
