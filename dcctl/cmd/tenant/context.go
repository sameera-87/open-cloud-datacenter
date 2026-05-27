// Package tenant — context.go
//
// Tenant-context sub-commands for `dcctl tenant`.
//
// These commands manage which tenant the CLI operates against by default,
// mirroring the `az account set/show` pattern.
//
// Command tree:
//
//	dcctl tenant set <id>       — pin a tenant as the active default
//	dcctl tenant current        — print the currently active tenant
//
// Use `dcctl list tenants` to enumerate available tenants.
//
// The active tenant is persisted to ~/.dcctl/context.yaml so every subsequent
// command picks it up without needing --tenant. Resolution order is documented
// in internal/config/config.go::GetTenantID.
package tenant

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"

	"github.com/spf13/cobra"
	"github.com/wso2/dcctl/internal/client"
	dcconfig "github.com/wso2/dcctl/internal/config"
)

// NewContextCmds returns the tenant-context sub-commands.
func NewContextCmds() []*cobra.Command {
	return []*cobra.Command{
		newTenantSetCmd(),
		newTenantCurrentCmd(),
	}
}

// ── tenant set ────────────────────────────────────────────────────────────────

func newTenantSetCmd() *cobra.Command {
	var noVerify bool

	cmd := &cobra.Command{
		Use:   "set <tenant-id>",
		Short: "Set the active tenant for subsequent commands",
		Long: `Set the active tenant that all CLI commands run against by default.

The selection is saved to ~/.dcctl/context.yaml and takes effect immediately.
You can override it any time with --tenant <id> or DCCTL_TENANT env var.

By default the tenant ID is validated against the API before saving. Use
--no-verify to skip the API call (useful when offline or pre-configuring).

Examples:
  dcctl tenant set hiran
  dcctl tenant set hiran --no-verify`,
		Args:         cobra.ExactArgs(1),
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runTenantSet(cmd.Context(), args[0], noVerify)
		},
	}
	cmd.Flags().BoolVar(&noVerify, "no-verify", false, "Skip API verification of the tenant ID")
	return cmd
}

func runTenantSet(ctx context.Context, tenantID string, noVerify bool) error {
	if !noVerify {
		// Verify the tenant exists and is accessible.
		creds, err := dcconfig.LoadCredentials()
		if err != nil {
			return err
		}
		apiClient := client.New(creds.AccessToken)
		resp, err := apiClient.Typed.ListTenantsWithResponse(ctx)
		if err != nil {
			return fmt.Errorf("GET /v1/tenants: %w", err)
		}
		if resp.StatusCode() != http.StatusOK || resp.JSON200 == nil {
			return apiErrorf(resp.StatusCode(), resp.Body)
		}
		found := false
		for _, t := range *resp.JSON200 {
			if t.Id == tenantID {
				found = true
				break
			}
		}
		if !found {
			return fmt.Errorf("no such tenant %q — run 'dcctl list tenants' to see accessible tenants", tenantID)
		}
	}

	// Load existing context so we don't wipe ActiveProjects when changing tenant.
	c, err := dcconfig.LoadContext()
	if err != nil {
		c = &dcconfig.Context{}
	}
	c.ActiveTenant = tenantID
	if err := dcconfig.SaveContext(c); err != nil {
		return fmt.Errorf("save context: %w", err)
	}
	fmt.Printf("Active tenant set to %s\n", tenantID)
	return nil
}

// ── tenant current ────────────────────────────────────────────────────────────

func newTenantCurrentCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "current",
		Short: "Print the currently active tenant",
		Long: `Print the tenant that CLI commands currently run against.

The active tenant is read from ~/.dcctl/context.yaml (set with 'dcctl tenant set').
If no context is set, the tenant from credentials.json is shown as the fallback.

Examples:
  dcctl tenant current`,
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runTenantCurrent()
		},
	}
}

// apiErrorf builds a friendly error from a non-2xx response body.
func apiErrorf(status int, body []byte) error {
	var parsed struct {
		Error string `json:"error"`
	}
	if json.Unmarshal(body, &parsed) == nil && parsed.Error != "" {
		return fmt.Errorf("DC-API error (%d): %s", status, parsed.Error)
	}
	return fmt.Errorf("DC-API returned HTTP %d: %s", status, string(body))
}

func runTenantCurrent() error {
	activeCtx, _ := dcconfig.LoadContext()
	if activeCtx != nil && activeCtx.ActiveTenant != "" {
		fmt.Println(activeCtx.ActiveTenant)
		return nil
	}

	// Fall back to credentials.json so users see something useful before
	// they run 'tenant set'.
	creds, err := dcconfig.LoadCredentials()
	if err == nil && creds.TenantID != "" {
		fmt.Printf("%s  (from credentials.json — pin with 'dcctl tenant set %s')\n",
			creds.TenantID, creds.TenantID)
		return nil
	}

	fmt.Fprintln(os.Stderr, "no active tenant — run 'dcctl tenant set <id>' to choose one, or 'dcctl list tenants' to see available tenants")
	os.Exit(1)
	return nil
}
