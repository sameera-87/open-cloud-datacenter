// Package keyvault — Key Vault credentials subcommand group.
//
// `dcctl keyvault credentials <verb>`:
//   get     — fetch the AppRole credentials (shown ONCE)
//   rotate  — atomically destroy + remint the AppRole secret_id (shown ONCE)
//
// Both verbs share the shown-once contract: dc-api stamps
// credentials_consumed_at = NOW on success, so a re-fetch via
// `credentials get` returns 410 Gone. Subsequent rotates always work.
package keyvault

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"

	"github.com/google/uuid"
	"github.com/spf13/cobra"
	"github.com/wso2/dcctl/cmd/internal/cliutil"
	"github.com/wso2/dcctl/internal/client"
	dcconfig "github.com/wso2/dcctl/internal/config"
)

// newCredentialsCmd returns the `dcctl keyvault credentials` parent.
func newCredentialsCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "credentials",
		Short: "Retrieve or rotate a Key Vault's AppRole credentials",
	}
	cmd.AddCommand(newCredentialsGetCmd())
	cmd.AddCommand(newCredentialsRotateCmd())
	return cmd
}

func newCredentialsGetCmd() *cobra.Command {
	var outputJSON bool
	cmd := &cobra.Command{
		Use:   "get <id>",
		Short: "Retrieve a Key Vault's AppRole credentials (shown once)",
		Long: `Fetch the AppRole credentials (role_id, secret_id, mount_path,
backend_address, backend_port) that the operator provisioned for the named
Key Vault. The response is shown ONCE — dc-api will not return them again.
Subsequent calls return 410 Gone (use 'dcctl keyvault credentials rotate'
to mint fresh credentials).

Pre-conditions:
  - The vault must be ACTIVE (operator finished init + unseal + mount +
    AppRole). Otherwise: 409 Conflict.
  - The dc-api deployment must have the KVI operator integration wired.
    Otherwise: 501 Not Implemented.

Examples:
  dcctl keyvault credentials get 6f1c4d2a-0000-0000-0000-000000000071
  dcctl keyvault credentials get 6f1c4d2a-0000-0000-0000-000000000071 --json`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			tenantID, projectID, err := resolveTenantProject(cmd)
			if err != nil {
				return err
			}
			return runGetKeyVaultCredentials(cmd.Context(), tenantID, projectID, args[0], outputJSON)
		},
	}
	cmd.Flags().BoolVar(&outputJSON, "json", false, "Output as JSON")
	return cmd
}

func newCredentialsRotateCmd() *cobra.Command {
	var outputJSON bool
	var yes bool
	cmd := &cobra.Command{
		Use:   "rotate <id>",
		Short: "Atomically rotate a Key Vault's AppRole credentials (shown once)",
		Long: `Destroys every active secret_id on the AppRole and mints a fresh one.
Old credentials stop working IMMEDIATELY — any workload still holding the
previous secret_id will fail its next AppRole login.

The new secret_id is returned in the response and is shown ONCE; same
shown-once contract as 'credentials get'.

Pre-conditions:
  - The vault must be ACTIVE.
  - The dc-api deployment must have the KVI operator integration wired.

Examples:
  dcctl keyvault credentials rotate 6f1c4d2a-...
  dcctl keyvault credentials rotate 6f1c4d2a-... --yes        # skip confirmation
  dcctl keyvault credentials rotate 6f1c4d2a-... --json       # script-friendly`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			tenantID, projectID, err := resolveTenantProject(cmd)
			if err != nil {
				return err
			}
			if !confirm(fmt.Sprintf(
				"Rotate credentials for vault %s? Old secret_id stops working immediately. [y/N]: ",
				args[0],
			), yes) {
				fmt.Println("Aborted.")
				return nil
			}
			return runRotateKeyVaultCredentials(cmd.Context(), tenantID, projectID, args[0], outputJSON)
		},
	}
	cmd.Flags().BoolVar(&outputJSON, "json", false, "Output as JSON")
	cmd.Flags().BoolVar(&yes, "yes", false, "Skip confirmation prompt")
	return cmd
}

func resolveTenantProject(cmd *cobra.Command) (string, string, error) {
	tenantFlag, _ := cmd.Root().PersistentFlags().GetString("tenant")
	tenantID, err := dcconfig.GetTenantID(tenantFlag)
	if err != nil {
		return "", "", err
	}
	projectFlag, _ := cmd.Root().PersistentFlags().GetString("project")
	projectID, err := dcconfig.GetProjectID(projectFlag, tenantID)
	if err != nil {
		return "", "", err
	}
	return tenantID, projectID, nil
}

func runGetKeyVaultCredentials(ctx context.Context, tenantID, projectID, id string, outputJSON bool) error {
	parsedID, err := uuid.Parse(id)
	if err != nil {
		return fmt.Errorf("invalid Key Vault id %q: %w", id, err)
	}
	creds, err := dcconfig.LoadCredentials()
	if err != nil {
		return err
	}
	apiClient := client.New(creds.AccessToken)
	resp, err := apiClient.Typed.GetKeyVaultCredentialsWithResponse(ctx, tenantID, projectID, parsedID)
	if err != nil {
		return fmt.Errorf("GET /v1/tenants/%s/keyvaults/%s/credentials: %w", tenantID, id, err)
	}
	if resp.StatusCode() != http.StatusOK || resp.JSON200 == nil {
		return cliutil.APIErrorf(resp.StatusCode(), resp.Body)
	}
	return renderCredentialsFields(resp.JSON200, outputJSON)
}

func runRotateKeyVaultCredentials(ctx context.Context, tenantID, projectID, id string, outputJSON bool) error {
	parsedID, err := uuid.Parse(id)
	if err != nil {
		return fmt.Errorf("invalid Key Vault id %q: %w", id, err)
	}
	creds, err := dcconfig.LoadCredentials()
	if err != nil {
		return err
	}
	apiClient := client.New(creds.AccessToken)
	resp, err := apiClient.Typed.RotateKeyVaultCredentialsWithResponse(ctx, tenantID, projectID, parsedID)
	if err != nil {
		return fmt.Errorf("POST /v1/tenants/%s/keyvaults/%s/credentials/rotate: %w", tenantID, id, err)
	}
	if resp.StatusCode() != http.StatusOK || resp.JSON200 == nil {
		return cliutil.APIErrorf(resp.StatusCode(), resp.Body)
	}
	return renderCredentialsFields(resp.JSON200, outputJSON)
}

// renderCredentialsFields prints the shown-once block in either text or JSON.
// Used by both get + rotate; both return the same KeyVaultCredentials shape.
// Marshal-then-unmarshal to a local struct keeps this file independent of
// the generated package's identifier churn between regens.
func renderCredentialsFields(out interface{}, outputJSON bool) error {
	type creds struct {
		RoleID         string `json:"role_id"`
		SecretID       string `json:"secret_id"`
		MountPath      string `json:"mount_path"`
		BackendAddress string `json:"backend_address"`
		BackendPort    string `json:"backend_port"`
	}
	raw, err := json.Marshal(out)
	if err != nil {
		return fmt.Errorf("marshal credentials: %w", err)
	}
	var c creds
	if err := json.Unmarshal(raw, &c); err != nil {
		return fmt.Errorf("decode credentials: %w", err)
	}
	if outputJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(c)
	}
	if isTerminal(os.Stdout) {
		fmt.Fprintln(os.Stderr, "WARNING: These credentials are shown ONCE and cannot be retrieved again. Store them securely.")
	}
	fmt.Println("Key Vault credentials (shown ONCE):")
	fmt.Println()
	fmt.Printf("  role_id:          %s\n", c.RoleID)
	fmt.Printf("  secret_id:        %s\n", c.SecretID)
	fmt.Printf("  mount_path:       %s\n", c.MountPath)
	fmt.Printf("  backend_address:  %s\n", c.BackendAddress)
	fmt.Printf("  backend_port:     %s\n", c.BackendPort)
	fmt.Println()
	return nil
}

func isTerminal(f *os.File) bool {
	fi, err := f.Stat()
	if err != nil {
		return false
	}
	return fi.Mode()&os.ModeCharDevice != 0
}
