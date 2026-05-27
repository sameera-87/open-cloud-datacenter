// Package peering provides the `dcctl peering` command group.
package peering

import "github.com/spf13/cobra"

// NewPeeringCmd returns the `dcctl peering` parent command.
func NewPeeringCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "peering",
		Short: "Manage VNet peerings",
	}
	cmd.AddCommand(newCreateCmd())
	cmd.AddCommand(newGetCmd())
	cmd.AddCommand(newListCmd())
	cmd.AddCommand(newDeleteCmd())
	return cmd
}
