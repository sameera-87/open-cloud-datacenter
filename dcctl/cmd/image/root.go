// Package image provides the `dcctl image` command group.
package image

import "github.com/spf13/cobra"

// NewImageCmd returns the `dcctl image` parent command.
func NewImageCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "image",
		Short: "Manage VM images",
	}
	cmd.AddCommand(newCreateCmd())
	cmd.AddCommand(newListCmd())
	return cmd
}
