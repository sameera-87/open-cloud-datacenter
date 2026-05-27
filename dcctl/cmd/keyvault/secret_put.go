// Package keyvault — Key Vault secret put command.
//
// `dcctl keyvault secret put <vault-id> <key>` writes a secret value into
// the vault's KV-v2 mount. Value can come from --value, --from-file, or
// stdin. dc-api proxies to OpenBao using its own backend token; the user
// never handles bao or AppRole creds.
package keyvault

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"

	"github.com/google/uuid"
	"github.com/spf13/cobra"
	"github.com/wso2/dcctl/cmd/internal/cliutil"
	"github.com/wso2/dcctl/internal/client"
	dcapi "github.com/wso2/dcctl/internal/client/generated"
	dcconfig "github.com/wso2/dcctl/internal/config"
)

func newSecretPutCmd() *cobra.Command {
	var value string
	var fromFile string
	var metadataPairs []string
	var outputJSON bool

	cmd := &cobra.Command{
		Use:   "put <vault-id> <key>",
		Short: "Write a secret value into a Key Vault",
		Long: `Write a secret value into a Key Vault. The value can be provided in three ways:

  --value <v>          Inline value (visible in shell history; OK for ad-hoc use)
  --from-file <path>   Read value from a file; use - for stdin
  (no flag)            Read from stdin

dc-api proxies the write to OpenBao using its own backend token. The user
never handles bao or the workload AppRole credentials.

Key naming: lowercase alphanumerics + . - _ , 1-256 chars (Azure KV-style).

Examples:
  dcctl keyvault secret put <vault> db-password --value hunter2
  dcctl keyvault secret put <vault> tls-cert --from-file ./server.crt
  cat secret.txt | dcctl keyvault secret put <vault> raw-blob
  dcctl keyvault secret put <vault> api-key --value sk-... \
      --metadata team=billing --metadata env=prod`,
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			tenantFlag, _ := cmd.Root().PersistentFlags().GetString("tenant")
			tenantID, err := dcconfig.GetTenantID(tenantFlag)
			if err != nil {
				return err
			}
			projectFlag, _ := cmd.Root().PersistentFlags().GetString("project")
			projectID, err := dcconfig.GetProjectID(projectFlag, tenantID)
			if err != nil {
				return err
			}
			valStr, err := resolveSecretValue(value, fromFile)
			if err != nil {
				return err
			}
			meta, err := parseMetadataFlags(metadataPairs)
			if err != nil {
				return err
			}
			return runPutKeyVaultSecret(cmd.Context(), tenantID, projectID, args[0], args[1], valStr, meta, outputJSON)
		},
	}
	cmd.Flags().StringVar(&value, "value", "", "Inline secret value")
	cmd.Flags().StringVar(&fromFile, "from-file", "", "Read value from file (use - for stdin)")
	cmd.Flags().StringSliceVar(&metadataPairs, "metadata", nil, "key=value metadata (repeatable)")
	cmd.Flags().BoolVar(&outputJSON, "json", false, "Output the written secret summary as JSON")
	return cmd
}

func resolveSecretValue(inline, fromFile string) (string, error) {
	switch {
	case inline != "" && fromFile != "":
		return "", fmt.Errorf("--value and --from-file are mutually exclusive")
	case inline != "":
		return inline, nil
	case fromFile == "-":
		b, err := io.ReadAll(os.Stdin)
		if err != nil {
			return "", fmt.Errorf("read stdin: %w", err)
		}
		return string(b), nil
	case fromFile != "":
		b, err := os.ReadFile(fromFile)
		if err != nil {
			return "", fmt.Errorf("read %s: %w", fromFile, err)
		}
		return string(b), nil
	default:
		// no flag, no inline → expect stdin (matches Unix tools)
		stat, _ := os.Stdin.Stat()
		if (stat.Mode() & os.ModeCharDevice) != 0 {
			return "", fmt.Errorf("no value provided: use --value, --from-file, or pipe via stdin")
		}
		b, err := io.ReadAll(os.Stdin)
		if err != nil {
			return "", fmt.Errorf("read stdin: %w", err)
		}
		return string(b), nil
	}
}

func parseMetadataFlags(pairs []string) (*map[string]string, error) {
	if len(pairs) == 0 {
		return nil, nil
	}
	out := make(map[string]string, len(pairs))
	for _, p := range pairs {
		// Split on first '=' only; values may legitimately contain '='.
		var k, v string
		for i, c := range p {
			if c == '=' {
				k, v = p[:i], p[i+1:]
				break
			}
		}
		if k == "" {
			return nil, fmt.Errorf("invalid --metadata %q: expected key=value", p)
		}
		out[k] = v
	}
	return &out, nil
}

func runPutKeyVaultSecret(ctx context.Context, tenantID, projectID, vaultID, key, value string, metadata *map[string]string, outputJSON bool) error {
	creds, err := dcconfig.LoadCredentials()
	if err != nil {
		return err
	}
	vID, err := uuid.Parse(vaultID)
	if err != nil {
		return fmt.Errorf("invalid Key Vault id %q: %w", vaultID, err)
	}

	apiClient := client.New(creds.AccessToken)
	body := dcapi.PutKeyVaultSecretJSONRequestBody{
		Value:    value,
		Metadata: metadata,
	}
	resp, err := apiClient.Typed.PutKeyVaultSecretWithResponse(ctx, tenantID, projectID, vID, key, body)
	if err != nil {
		return fmt.Errorf("PUT /v1/.../keyvaults/%s/secrets/%s: %w", vaultID, key, err)
	}
	// 201 Created on first write, 200 OK on a new version.
	switch resp.StatusCode() {
	case http.StatusCreated, http.StatusOK:
		// fallthrough — handled below
	default:
		return cliutil.APIErrorf(resp.StatusCode(), resp.Body)
	}

	// Both 200 and 201 carry a KeyVaultSecret payload.
	var out *dcapi.KeyVaultSecret
	if resp.JSON200 != nil {
		out = resp.JSON200
	} else if resp.JSON201 != nil {
		out = resp.JSON201
	}
	if out == nil {
		return fmt.Errorf("unexpected empty response body (HTTP %d)", resp.StatusCode())
	}

	verb := "Updated"
	if resp.StatusCode() == http.StatusCreated {
		verb = "Created"
	}

	if outputJSON {
		// Hide the value from JSON output too — same posture as text.
		out.Value = ""
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(out)
	}

	fmt.Printf("%s secret %q (version %d)\n", verb, key, out.Version)
	return nil
}
