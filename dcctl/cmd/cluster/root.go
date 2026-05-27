// Package cluster provides the `dcctl cluster` command group.
package cluster

import "github.com/spf13/cobra"

// NewClusterCmd returns the `dcctl cluster` parent command.
func NewClusterCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "cluster",
		Short: "Manage RKE2 clusters",
	}
	cmd.AddCommand(newCreateCmd())
	cmd.AddCommand(newGetCmd())
	cmd.AddCommand(newListCmd())
	cmd.AddCommand(newDeleteCmd())
	cmd.AddCommand(newKubeconfigCmd())
	cmd.AddCommand(newNodePoolCmd())
	return cmd
}
