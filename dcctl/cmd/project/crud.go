// Package project — crud.go
//
// Project CRUD sub-commands: create, list, get, delete.
package project

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"

	"github.com/spf13/cobra"
	"github.com/wso2/dcctl/internal/client"
	dcapi "github.com/wso2/dcctl/internal/client/generated"
	dcconfig "github.com/wso2/dcctl/internal/config"
)

func newCRUDCmds() []*cobra.Command {
	return []*cobra.Command{
		newProjectCreateCmd(),
		newProjectListCmd(),
		newProjectGetCmd(),
		newProjectUpdateCmd(),
		newProjectDeleteCmd(),
	}
}

// ── project update ────────────────────────────────────────────────────────────

func newProjectUpdateCmd() *cobra.Command {
	var (
		cpuCores   int
		memoryGB   int
		storageGB  int
		outputJSON bool
	)

	cmd := &cobra.Command{
		Use:   "update <project-id>",
		Short: "Update a project's capacity quotas",
		Long: `Update one or more capacity quotas on a project. Tenant-owner or platform-admin only.

Flags omitted keep their current value; pass any subset.

Server-side guards:
  - new quota must be >= resources already in use in this project
  - new quota + other projects' allocations must be <= tenant cap

Examples:
  dcctl project update prod-infra --cpu-cores 40
  dcctl project update prod-infra --cpu-cores 40 --memory-gb 128 --storage-gb 1000
  dcctl project update prod-infra --memory-gb 128 --tenant choreo-sre`,
		Args:         cobra.ExactArgs(1),
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			if cpuCores == 0 && memoryGB == 0 && storageGB == 0 {
				return fmt.Errorf("at least one of --cpu-cores, --memory-gb, --storage-gb is required")
			}
			tenantFlag, _ := cmd.Root().PersistentFlags().GetString("tenant")
			tenantID, err := dcconfig.GetTenantID(tenantFlag)
			if err != nil {
				return err
			}
			return runProjectUpdate(cmd.Context(), tenantID, args[0], cpuCores, memoryGB, storageGB, outputJSON)
		},
	}

	cmd.Flags().IntVar(&cpuCores, "cpu-cores", 0, "New vCPU quota (omit to keep current)")
	cmd.Flags().IntVar(&memoryGB, "memory-gb", 0, "New memory quota GiB (omit to keep current)")
	cmd.Flags().IntVar(&storageGB, "storage-gb", 0, "New storage quota GiB (omit to keep current)")
	cmd.Flags().BoolVar(&outputJSON, "json", false, "Output as JSON")
	return cmd
}

func runProjectUpdate(ctx context.Context, tenantID, projectID string, cpuCores, memoryGB, storageGB int, outputJSON bool) error {
	creds, err := dcconfig.LoadCredentials()
	if err != nil {
		return err
	}
	apiClient := client.New(creds.AccessToken)

	body := dcapi.UpdateProjectQuotaRequest{}
	if cpuCores > 0 {
		body.CpuCores = &cpuCores
	}
	if memoryGB > 0 {
		body.MemoryGb = &memoryGB
	}
	if storageGB > 0 {
		body.StorageGb = &storageGB
	}

	resp, err := apiClient.Typed.UpdateProjectQuotaWithResponse(ctx, tenantID, projectID, body)
	if err != nil {
		return fmt.Errorf("PATCH /v1/tenants/%s/projects/%s: %w", tenantID, projectID, err)
	}
	if resp.StatusCode() != http.StatusOK || resp.JSON200 == nil {
		return apiErrorf(resp.StatusCode(), resp.Body)
	}
	p := resp.JSON200

	if outputJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(p)
	}

	fmt.Printf("Project %q updated\n", p.Id)
	printProject(p)
	return nil
}

// ── project create ────────────────────────────────────────────────────────────

func newProjectCreateCmd() *cobra.Command {
	var (
		name        string
		description string
		cpuCores    int
		memoryGB    int
		storageGB   int
		outputJSON  bool
	)

	cmd := &cobra.Command{
		Use:   "create <project-id>",
		Short: "Create a new project in the active tenant",
		Long: `Create a new project. The project-id is the URL-safe slug used in API paths
([a-z0-9-], starts with a letter, max 48 chars). It cannot be changed after creation.

Quota flags set resource limits for the project. Omitted flags use server defaults
(20 vCPU / 64 GiB RAM / 500 GiB storage).

Examples:
  dcctl project create prod-infra
  dcctl project create prod-infra --name "Production Infrastructure" --cpu-cores 40 --memory-gb 128
  dcctl project create dev --description "Developer sandbox" --tenant choreo-sre`,
		Args:         cobra.ExactArgs(1),
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			tenantFlag, _ := cmd.Root().PersistentFlags().GetString("tenant")
			tenantID, err := dcconfig.GetTenantID(tenantFlag)
			if err != nil {
				return err
			}
			return runProjectCreate(cmd.Context(), tenantID, args[0], name, description, cpuCores, memoryGB, storageGB, outputJSON)
		},
	}

	cmd.Flags().StringVar(&name, "name", "", "Human-readable display name (defaults to the project-id)")
	cmd.Flags().StringVar(&description, "description", "", "Optional free-text description (max 512 chars)")
	cmd.Flags().IntVar(&cpuCores, "cpu-cores", 0, "vCPU quota (default: server default of 20)")
	cmd.Flags().IntVar(&memoryGB, "memory-gb", 0, "Memory quota GiB (default: server default of 64)")
	cmd.Flags().IntVar(&storageGB, "storage-gb", 0, "Storage quota GiB (default: server default of 500)")
	cmd.Flags().BoolVar(&outputJSON, "json", false, "Output as JSON")
	return cmd
}

func runProjectCreate(ctx context.Context, tenantID, id, name, description string, cpuCores, memoryGB, storageGB int, outputJSON bool) error {
	creds, err := dcconfig.LoadCredentials()
	if err != nil {
		return err
	}
	apiClient := client.New(creds.AccessToken)

	body := dcapi.CreateProjectRequest{Id: id}
	if name != "" {
		body.Name = &name
	}
	if description != "" {
		body.Description = &description
	}
	if cpuCores > 0 {
		body.CpuCores = &cpuCores
	}
	if memoryGB > 0 {
		body.MemoryGb = &memoryGB
	}
	if storageGB > 0 {
		body.StorageGb = &storageGB
	}

	resp, err := apiClient.Typed.CreateProjectWithResponse(ctx, tenantID, body)
	if err != nil {
		return fmt.Errorf("POST /v1/tenants/%s/projects: %w", tenantID, err)
	}
	if resp.StatusCode() != http.StatusCreated || resp.JSON201 == nil {
		return apiErrorf(resp.StatusCode(), resp.Body)
	}
	p := resp.JSON201

	if outputJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(p)
	}

	fmt.Printf("Project %q created in tenant %s\n", p.Id, p.TenantId)
	printProject(p)
	fmt.Printf("\nSet as active project with:\n  dcctl project set %s\n", p.Id)
	return nil
}

// ── project list ──────────────────────────────────────────────────────────────

func newProjectListCmd() *cobra.Command {
	var outputJSON bool

	cmd := &cobra.Command{
		Use:   "list",
		Short: "List all projects in the active tenant",
		Long: `List all projects the authenticated user can access within the active tenant.

The active project (set with 'dcctl project set') is marked with '*'.

Examples:
  dcctl project list
  dcctl project list --tenant other-tenant
  dcctl project list --json`,
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			tenantFlag, _ := cmd.Root().PersistentFlags().GetString("tenant")
			tenantID, err := dcconfig.GetTenantID(tenantFlag)
			if err != nil {
				return err
			}
			return runProjectList(cmd.Context(), tenantID, outputJSON)
		},
	}
	cmd.Flags().BoolVar(&outputJSON, "json", false, "Output as JSON")
	return cmd
}

func runProjectList(ctx context.Context, tenantID string, outputJSON bool) error {
	creds, err := dcconfig.LoadCredentials()
	if err != nil {
		return err
	}
	apiClient := client.New(creds.AccessToken)

	resp, err := apiClient.Typed.ListProjectsWithResponse(ctx, tenantID)
	if err != nil {
		return fmt.Errorf("GET /v1/tenants/%s/projects: %w", tenantID, err)
	}
	if resp.StatusCode() != http.StatusOK || resp.JSON200 == nil {
		return apiErrorf(resp.StatusCode(), resp.Body)
	}
	projects := *resp.JSON200

	if outputJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(projects)
	}

	if len(projects) == 0 {
		fmt.Println("No projects found.")
		return nil
	}

	// Mark the active project.
	activeCtx, _ := dcconfig.LoadContext()
	activeProject := ""
	if activeCtx != nil && activeCtx.ActiveProjects != nil {
		activeProject = activeCtx.ActiveProjects[tenantID]
	}

	fmt.Printf("%-2s  %-30s  %-30s  %-6s  %-8s  %-10s\n",
		"", "ID", "NAME", "vCPU", "RAM(GiB)", "DISK(GiB)")
	fmt.Printf("%-2s  %-30s  %-30s  %-6s  %-8s  %-10s\n",
		"--", strings.Repeat("-", 30), strings.Repeat("-", 30),
		"------", "--------", "----------")

	for i := range projects {
		p := &projects[i]
		marker := "  "
		if p.Id == activeProject {
			marker = "* "
		}
		fmt.Printf("%-2s  %-30s  %-30s  %-6d  %-8d  %-10d\n",
			marker, truncate(p.Id, 30), truncate(p.Name, 30),
			p.CpuCores, p.MemoryGb, p.StorageGb)
	}
	if activeProject == "" {
		fmt.Printf("\nNo active project. Run 'dcctl project set <id>' to choose one.\n")
	}
	return nil
}

// ── project get ───────────────────────────────────────────────────────────────

func newProjectGetCmd() *cobra.Command {
	var outputJSON bool

	cmd := &cobra.Command{
		Use:   "get <project-id>",
		Short: "Get a project by ID",
		Long: `Get detailed information about a single project.

Examples:
  dcctl project get prod-infra
  dcctl project get prod-infra --json`,
		Args:         cobra.ExactArgs(1),
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			tenantFlag, _ := cmd.Root().PersistentFlags().GetString("tenant")
			tenantID, err := dcconfig.GetTenantID(tenantFlag)
			if err != nil {
				return err
			}
			return runProjectGet(cmd.Context(), tenantID, args[0], outputJSON)
		},
	}
	cmd.Flags().BoolVar(&outputJSON, "json", false, "Output as JSON")
	return cmd
}

func runProjectGet(ctx context.Context, tenantID, projectID string, outputJSON bool) error {
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

	if outputJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(resp.JSON200)
	}

	printProject(resp.JSON200)
	return nil
}

// ── project delete ────────────────────────────────────────────────────────────

func newProjectDeleteCmd() *cobra.Command {
	var skipConfirm bool

	cmd := &cobra.Command{
		Use:   "delete <project-id>",
		Short: "Delete a project",
		Long: `Delete a project. The project must be empty (no VMs, clusters, VNets, or other
resources). Delete all resources within the project before deleting the project.

This action cannot be undone.

Examples:
  dcctl project delete old-project
  dcctl project delete old-project --yes`,
		Args:         cobra.ExactArgs(1),
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			tenantFlag, _ := cmd.Root().PersistentFlags().GetString("tenant")
			tenantID, err := dcconfig.GetTenantID(tenantFlag)
			if err != nil {
				return err
			}
			return runProjectDelete(cmd.Context(), tenantID, args[0], skipConfirm)
		},
	}
	cmd.Flags().BoolVarP(&skipConfirm, "yes", "y", false, "Skip confirmation prompt")
	return cmd
}

func runProjectDelete(ctx context.Context, tenantID, projectID string, skipConfirm bool) error {
	if !confirmDelete(fmt.Sprintf("Delete project %s in tenant %s? This cannot be undone. [y/N] ", projectID, tenantID), skipConfirm) {
		fmt.Println("Cancelled.")
		return nil
	}

	creds, err := dcconfig.LoadCredentials()
	if err != nil {
		return err
	}
	apiClient := client.New(creds.AccessToken)

	resp, err := apiClient.Typed.DeleteProjectWithResponse(ctx, tenantID, projectID)
	if err != nil {
		return fmt.Errorf("DELETE /v1/tenants/%s/projects/%s: %w", tenantID, projectID, err)
	}
	if resp.StatusCode() >= http.StatusMultipleChoices {
		return apiErrorf(resp.StatusCode(), resp.Body)
	}
	fmt.Printf("Project %s deleted from tenant %s.\n", projectID, tenantID)
	return nil
}

// ── helpers ───────────────────────────────────────────────────────────────────

func printProject(p *dcapi.Project) {
	row(14, "ID", p.Id)
	row(14, "Name", p.Name)
	row(14, "Tenant", p.TenantId)
	if p.Description != nil && *p.Description != "" {
		row(14, "Description", *p.Description)
	}
	row(14, "vCPU quota", fmt.Sprintf("%d", p.CpuCores))
	row(14, "Memory (GiB)", fmt.Sprintf("%d", p.MemoryGb))
	row(14, "Storage (GiB)", fmt.Sprintf("%d", p.StorageGb))
	row(14, "Max VNets", fmt.Sprintf("%d", p.MaxVnets))
	row(14, "Max Clusters", fmt.Sprintf("%d", p.MaxClusters))
	row(14, "Max Volumes", fmt.Sprintf("%d", p.MaxVolumes))
	row(14, "Max Public IPs", fmt.Sprintf("%d", p.MaxPublicIps))
	row(14, "Created", p.CreatedAt.Format("2006-01-02T15:04:05Z07:00"))
	row(14, "Updated", p.UpdatedAt.Format("2006-01-02T15:04:05Z07:00"))
	row(14, "Created by", p.CreatedBy)
}

func row(labelWidth int, label, value string) {
	if value == "" {
		return
	}
	fmt.Printf("  %-*s %s\n", labelWidth, label+":", value)
}

func truncate(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	if maxLen <= 3 {
		return s[:maxLen]
	}
	return s[:maxLen-3] + "..."
}

func confirmDelete(prompt string, force bool) bool {
	if force {
		return true
	}
	fmt.Print(prompt)
	reader := bufio.NewReader(os.Stdin)
	line, _ := reader.ReadString('\n')
	return strings.EqualFold(strings.TrimSpace(line), "y")
}
