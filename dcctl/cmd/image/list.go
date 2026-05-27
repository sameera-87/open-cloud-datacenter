package image

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"

	"github.com/spf13/cobra"
	"github.com/wso2/dcctl/cmd/internal/cliutil"
	"github.com/wso2/dcctl/internal/client"
	dcconfig "github.com/wso2/dcctl/internal/config"
)

func newListCmd() *cobra.Command {
	var outputJSON bool

	cmd := &cobra.Command{
		Use:   "list",
		Short: "List available VM images",
		Long: `List all VM images available in the datacenter.

The ID column is what you pass to --image when creating a VM or cluster.

Example:
  dcctl image list
  dcctl vm create --name web-01 --image default/image-abc123 ...`,
		RunE: func(cmd *cobra.Command, args []string) error {
			tenantFlag, _ := cmd.Root().PersistentFlags().GetString("tenant")
			tenantID, err := dcconfig.GetTenantID(tenantFlag)
			if err != nil {
				return err
			}
			return runListImages(cmd.Context(), tenantID, outputJSON)
		},
	}
	cmd.Flags().BoolVar(&outputJSON, "json", false, "Output as JSON")
	return cmd
}

func runListImages(ctx context.Context, tenantID string, outputJSON bool) error {
	creds, err := dcconfig.LoadCredentials()
	if err != nil {
		return err
	}
	apiClient := client.New(creds.AccessToken)

	resp, err := apiClient.Typed.ListImagesWithResponse(ctx, tenantID)
	if err != nil {
		return fmt.Errorf("GET /v1/tenants/%s/images: %w", tenantID, err)
	}
	if resp.StatusCode() != http.StatusOK || resp.JSON200 == nil {
		return cliutil.APIErrorf(resp.StatusCode(), resp.Body)
	}
	images := *resp.JSON200

	if outputJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(images)
	}

	if len(images) == 0 {
		fmt.Println("No images found.")
		return nil
	}

	fmt.Printf("  %-40s  %s\n", "ID", "DISPLAY NAME")
	fmt.Printf("  %-40s  %s\n", "──────────────────────────────────────────", "────────────────────────")
	for _, im := range images {
		fmt.Printf("  %-40s  %s\n", im.Id, im.DisplayName)
	}
	return nil
}
