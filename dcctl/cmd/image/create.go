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
	dcapi "github.com/wso2/dcctl/internal/client/generated"
	dcconfig "github.com/wso2/dcctl/internal/config"
)

func newCreateCmd() *cobra.Command {
	var (
		name       string
		url        string
		outputJSON bool
	)

	cmd := &cobra.Command{
		Use:   "create",
		Short: "Upload a VM image from a URL",
		Long: `Register a VM image in the datacenter by providing a download URL.

DC-API instructs Harvester to download the image. Use 'dcctl image list' to
check when the image is ready.

The ID returned is what you pass to --image when creating a VM.

Examples:
  dcctl image create \
    --name ubuntu-22.04 \
    --url https://cloud-images.ubuntu.com/jammy/current/jammy-server-cloudimg-amd64.img

  dcctl image create \
    --name debian-12 \
    --url https://cloud.debian.org/images/cloud/bookworm/latest/debian-12-genericcloud-amd64.qcow2`,
		RunE: func(cmd *cobra.Command, args []string) error {
			tenantFlag, _ := cmd.Root().PersistentFlags().GetString("tenant")
			tenantID, err := dcconfig.GetTenantID(tenantFlag)
			if err != nil {
				return err
			}
			return runCreateImage(cmd.Context(), tenantID, name, url, outputJSON)
		},
	}

	cmd.Flags().StringVarP(&name, "name", "n", "", "Image display name (required)")
	cmd.Flags().StringVarP(&url, "url", "u", "", "Download URL for the image (required)")
	cmd.Flags().BoolVar(&outputJSON, "json", false, "Output as JSON")

	cmd.MarkFlagRequired("name") //nolint:errcheck
	cmd.MarkFlagRequired("url")  //nolint:errcheck

	return cmd
}

func runCreateImage(ctx context.Context, tenantID, name, url string, outputJSON bool) error {
	creds, err := dcconfig.LoadCredentials()
	if err != nil {
		return err
	}
	apiClient := client.New(creds.AccessToken)

	fmt.Printf("Registering image %q from %s ...\n", name, url)

	resp, err := apiClient.Typed.CreateImageWithResponse(ctx, tenantID, dcapi.CreateImageRequest{
		DisplayName: name,
		Url:         url,
	})
	if err != nil {
		return fmt.Errorf("POST /v1/tenants/%s/images: %w", tenantID, err)
	}
	if resp.StatusCode() != http.StatusAccepted || resp.JSON202 == nil {
		return cliutil.APIErrorf(resp.StatusCode(), resp.Body)
	}
	img := resp.JSON202

	if outputJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(img)
	}

	fmt.Printf("\nImage registered!\n")
	fmt.Printf("  ID:           %s\n", img.Id)
	fmt.Printf("  Display Name: %s\n", img.DisplayName)
	fmt.Printf("\nHarvester is downloading the image. Check progress:\n")
	fmt.Printf("  dcctl image list\n")
	fmt.Printf("\nOnce ready, create a VM with:\n")
	fmt.Printf("  dcctl vm create --image %s ...\n", img.Id)
	return nil
}
