// Package project — context.go
//
// Project-context sub-commands: set and current.
//
// These pin or show the active project for the current tenant, mirroring the
// 'dcctl tenant set/current' pattern. The active project is stored per-tenant
// in ~/.dcctl/context.yaml under the active_projects map so switching tenants
// automatically switches to that tenant's last-set project.
package project

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

func newContextCmds() []*cobra.Command {
	return []*cobra.Command{
		newProjectSetCmd(),
		newProjectCurrentCmd(),
	}
}

// ── project set ──────────────────────────────────────────────────────────────

func newProjectSetCmd() *cobra.Command {
	var noVerify bool

	cmd := &cobra.Command{
		Use:   "set <project-id>",
		Short: "Set the active project for the current tenant",
		Long: `Pin a project as the active default for all CLI commands within the current tenant.

The selection is saved to ~/.dcctl/context.yaml and takes effect immediately.
You can override it at any time with --project <id> or DCCTL_PROJECT env var.

By default the project ID is validated against the API before saving. Use
--no-verify to skip the API call (useful when offline or pre-configuring).

Examples:
  dcctl project set prod-infra
  dcctl project set prod-infra --no-verify
  dcctl project set dev --tenant other-tenant`,
		Args:         cobra.ExactArgs(1),
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			tenantFlag, _ := cmd.Root().PersistentFlags().GetString("tenant")
			tenantID, err := dcconfig.GetTenantID(tenantFlag)
			if err != nil {
				return err
			}
			return runProjectSet(cmd.Context(), tenantID, args[0], noVerify)
		},
	}
	cmd.Flags().BoolVar(&noVerify, "no-verify", false, "Skip API verification of the project ID")
	return cmd
}

func runProjectSet(ctx context.Context, tenantID, projectID string, noVerify bool) error {
	if !noVerify {
		creds, err := dcconfig.LoadCredentials()
		if err != nil {
			return err
		}
		apiClient := client.New(creds.AccessToken)
		resp, err := apiClient.Typed.GetProjectWithResponse(ctx, tenantID, projectID)
		if err != nil {
			return fmt.Errorf("GET /v1/tenants/%s/projects/%s: %w", tenantID, projectID, err)
		}
		if resp.StatusCode() != http.StatusOK || resp.JSON200 == nil {
			return apiErrorf(resp.StatusCode(), resp.Body)
		}
	}

	if err := dcconfig.SetActiveProject(tenantID, projectID); err != nil {
		return fmt.Errorf("save context: %w", err)
	}
	fmt.Printf("Active project set to %s (tenant: %s)\n", projectID, tenantID)
	return nil
}

// ── project current ───────────────────────────────────────────────────────────

func newProjectCurrentCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "current",
		Short: "Print the currently active project for the current tenant",
		Long: `Print the project that CLI commands currently run against.

The active project is read from ~/.dcctl/context.yaml (set with 'dcctl project set').
The lookup is per-tenant, so the result changes when you switch tenants.

Examples:
  dcctl project current
  dcctl project current --tenant other-tenant`,
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			tenantFlag, _ := cmd.Root().PersistentFlags().GetString("tenant")
			tenantID, err := dcconfig.GetTenantID(tenantFlag)
			if err != nil {
				return err
			}
			return runProjectCurrent(tenantID)
		},
	}
}

func runProjectCurrent(tenantID string) error {
	activeCtx, _ := dcconfig.LoadContext()
	if activeCtx != nil && activeCtx.ActiveProjects != nil {
		if pid := activeCtx.ActiveProjects[tenantID]; pid != "" {
			fmt.Println(pid)
			return nil
		}
	}
	fmt.Fprintf(os.Stderr,
		"no active project for tenant %s — run 'dcctl project set <id>' to choose one, "+
			"or 'dcctl project list' to see available projects\n", tenantID)
	os.Exit(1)
	return nil
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
