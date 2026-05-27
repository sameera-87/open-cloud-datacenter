// Package keyvault provides the `dcctl keyvault` command group.
package keyvault

import "github.com/spf13/cobra"

// NewKeyVaultCmd returns the `dcctl keyvault` parent command.
func NewKeyVaultCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "keyvault",
		Short: "Manage Key Vaults",
	}
	cmd.AddCommand(newCreateCmd())
	cmd.AddCommand(newGetCmd())
	cmd.AddCommand(newListCmd())
	cmd.AddCommand(newDeleteCmd())
	cmd.AddCommand(newCredentialsCmd())

	// `dcctl keyvault endpoint <verb>` — per-VPC Private Endpoint into a vault.
	endpointCmd := &cobra.Command{
		Use:   "endpoint",
		Short: "Manage Key Vault private endpoints",
	}
	endpointCmd.AddCommand(newEndpointCreateCmd())
	endpointCmd.AddCommand(newEndpointGetCmd())
	endpointCmd.AddCommand(newEndpointListCmd())
	endpointCmd.AddCommand(newEndpointDeleteCmd())
	cmd.AddCommand(endpointCmd)

	// `dcctl keyvault secret <verb>` — human secret CRUD via dc-api proxy.
	// The user never handles bao or AppRole creds; dc-api authenticates
	// to OpenBao with its own backend token.
	secretCmd := &cobra.Command{
		Use:   "secret",
		Short: "Manage secrets inside a Key Vault",
	}
	secretCmd.AddCommand(newSecretPutCmd())
	secretCmd.AddCommand(newSecretGetCmd())
	secretCmd.AddCommand(newSecretListCmd())
	secretCmd.AddCommand(newSecretDeleteCmd())
	secretCmd.AddCommand(newSecretRestoreCmd())
	cmd.AddCommand(secretCmd)

	return cmd
}
