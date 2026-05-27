// Package cluster — kubeconfig command.
//
// `dcctl cluster kubeconfig <cluster-id>` downloads the kubeconfig for a cluster
// that is in ACTIVE status. Writes to stdout by default so it can be piped:
//
//	dcctl cluster kubeconfig <id> > ~/.kube/my-cluster.yaml
//	dcctl cluster kubeconfig <id> --file ~/.kube/my-cluster.yaml
package cluster

import (
	"context"
	"fmt"
	"net/http"
	"os"

	"github.com/google/uuid"
	"github.com/spf13/cobra"
	"github.com/wso2/dcctl/cmd/internal/cliutil"
	"github.com/wso2/dcctl/internal/client"
	dcconfig "github.com/wso2/dcctl/internal/config"
)

func newKubeconfigCmd() *cobra.Command {
	var filePath string

	cmd := &cobra.Command{
		Use:   "kubeconfig <cluster-id>",
		Short: "Download kubeconfig for a cluster",
		Long: `Download the kubeconfig YAML for an ACTIVE cluster.

The cluster must be in ACTIVE status. If it is still PENDING, poll with:
  dcctl cluster get <id>

Examples:
  # Print to stdout (pipe into kubectl)
  dcctl cluster kubeconfig <id> | kubectl --kubeconfig /dev/stdin get nodes

  # Save to file
  dcctl cluster kubeconfig <id> --file ~/.kube/prod-k8s-01.yaml`,
		Args: cobra.ExactArgs(1),
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
			return runKubeconfig(cmd.Context(), tenantID, projectID, args[0], filePath)
		},
	}
	cmd.Flags().StringVarP(&filePath, "file", "f", "", "Write kubeconfig to this file (default: stdout)")
	return cmd
}

func runKubeconfig(ctx context.Context, tenantID, projectID, clusterID, filePath string) error {
	creds, err := dcconfig.LoadCredentials()
	if err != nil {
		return err
	}
	parsedID, err := uuid.Parse(clusterID)
	if err != nil {
		return fmt.Errorf("invalid cluster id %q: %w", clusterID, err)
	}

	apiClient := client.New(creds.AccessToken)
	resp, err := apiClient.Typed.GetClusterKubeconfigWithResponse(ctx, tenantID, projectID, parsedID)
	if err != nil {
		return fmt.Errorf("GET /v1/tenants/%s/clusters/%s/kubeconfig: %w", tenantID, clusterID, err)
	}
	if resp.StatusCode() != http.StatusOK {
		return cliutil.APIErrorf(resp.StatusCode(), resp.Body)
	}

	if filePath == "" {
		_, _ = os.Stdout.Write(resp.Body)
		return nil
	}

	if err := os.WriteFile(filePath, resp.Body, 0600); err != nil {
		return fmt.Errorf("write kubeconfig to %s: %w", filePath, err)
	}
	fmt.Printf("Kubeconfig written to %s\n", filePath)
	fmt.Printf("  kubectl --kubeconfig %s get nodes\n", filePath)
	return nil
}
