// Package subnet provides the `dcctl subnet` command group.
package subnet

import "github.com/spf13/cobra"

// NewSubnetCmd returns the `dcctl subnet` parent command.
func NewSubnetCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "subnet",
		Short: "Manage subnets within VNets",
	}
	cmd.AddCommand(newCreateCmd())
	cmd.AddCommand(newGetCmd())
	cmd.AddCommand(newListCmd())
	cmd.AddCommand(newDeleteCmd())
	return cmd
}
