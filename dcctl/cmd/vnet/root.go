// Package vnet provides the `dcctl vnet` command group.
package vnet

import "github.com/spf13/cobra"

// NewVNetCmd returns the `dcctl vnet` parent command.
func NewVNetCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "vnet",
		Short: "Manage virtual networks (VNets)",
	}
	cmd.AddCommand(newCreateCmd())
	cmd.AddCommand(newGetCmd())
	cmd.AddCommand(newListCmd())
	cmd.AddCommand(newDeleteCmd())
	return cmd
}
