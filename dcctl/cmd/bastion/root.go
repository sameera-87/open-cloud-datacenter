// Package bastion provides the `dcctl bastion` command group.
package bastion

import "github.com/spf13/cobra"

// NewBastionCmd returns the `dcctl bastion` parent command.
func NewBastionCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "bastion",
		Short: "Manage bastion hosts",
	}
	cmd.AddCommand(newCreateCmd())
	cmd.AddCommand(newGetCmd())
	cmd.AddCommand(newListCmd())
	cmd.AddCommand(newDeleteCmd())
	return cmd
}
