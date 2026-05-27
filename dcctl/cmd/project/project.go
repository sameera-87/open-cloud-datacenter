// Package project provides the `dcctl project` sub-command group.
//
// Projects are the primary isolation boundary inside a tenant. Every
// per-resource API endpoint (VMs, clusters, VNets, …) is scoped to a
// project, so the CLI must always know which project to operate against.
//
// Command tree:
//
//	dcctl project set <id>     — pin a project as the active default for the current tenant
//	dcctl project current      — print the currently active project
//	dcctl project create <id>  — POST /v1/tenants/{tid}/projects
//	dcctl project list         — GET  /v1/tenants/{tid}/projects
//	dcctl project get <id>     — GET  /v1/tenants/{tid}/projects/{pid}
//	dcctl project delete <id>  — DELETE /v1/tenants/{tid}/projects/{pid}
package project

import (
	"github.com/spf13/cobra"
)

// NewProjectCmd returns the `dcctl project` parent command.
func NewProjectCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "project",
		Short: "Manage projects within a tenant",
		Long: `Manage projects — the resource-isolation boundary inside a tenant.

Every resource (VM, cluster, VNet, …) belongs to exactly one project.
Set the active project once with 'dcctl project set' so you don't have
to pass --project on every command.

Project selection (persisted to ~/.dcctl/context.yaml):
  dcctl project set <id>     — pin a project as the active default
  dcctl project current      — print the currently active project

Project CRUD:
  dcctl project create <id>  — create a new project (quota flags optional)
  dcctl project list         — list all projects in the active tenant
  dcctl project get <id>     — get a project by ID
  dcctl project delete <id>  — delete a project (must be empty)`,
		SilenceUsage: true,
	}

	for _, c := range newContextCmds() {
		cmd.AddCommand(c)
	}
	for _, c := range newCRUDCmds() {
		cmd.AddCommand(c)
	}

	return cmd
}
