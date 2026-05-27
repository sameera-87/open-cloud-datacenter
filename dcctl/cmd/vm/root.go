// Package vm provides the `dcctl vm` command group.
package vm

import "github.com/spf13/cobra"

// NewVMCmd returns the `dcctl vm` parent command.
func NewVMCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "vm",
		Short: "Manage virtual machines",
	}
	cmd.AddCommand(newCreateCmd())
	cmd.AddCommand(newGetCmd())
	cmd.AddCommand(newListCmd())
	cmd.AddCommand(newDeleteCmd())
	return cmd
}
