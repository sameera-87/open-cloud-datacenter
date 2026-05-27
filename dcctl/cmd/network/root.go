// Package network provides the `dcctl network` command group — a read-only
// view of the legacy VM bridge networks (the pre-VPC networking model).
//
// New deployments should prefer `dcctl vnet` + `dcctl subnet` (KubeOVN VPCs).
// This subcommand exists so operators can still enumerate the legacy bridges
// that some long-lived VMs are still attached to.
package network

import "github.com/spf13/cobra"

// NewNetworkCmd returns the `dcctl network` parent command.
func NewNetworkCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "network",
		Short: "Inspect legacy VM bridge networks",
	}
	cmd.AddCommand(newListCmd())
	return cmd
}
