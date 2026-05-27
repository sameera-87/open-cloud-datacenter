// Package client provides the DC-API HTTP client used by all CLI commands.
//
// The runtime surface is a thin wrapper around the oapi-codegen-generated
// dcapi.ClientWithResponses. All command code in dcctl/cmd/ calls
// apiClient.Typed.<Operation>WithResponse(...) and gets typed structs back
// from the spec. There is no longer a legacy untyped Get/Post/Delete surface;
// the generated client owns the wire format.
package client

import (
	"context"
	"crypto/tls"
	"net/http"
	"time"

	"github.com/spf13/viper"
	dcapi "github.com/wso2/dcctl/internal/client/generated"
)

// Client carries the typed DC-API client plus the small bit of
// dcctl-specific state needed to build it (base URL, access token,
// TLS preference). The typed client is exposed directly as Typed.
//
// token and httpClient are stored so that hand-written methods in cluster.go
// (and future non-generated helpers) can make raw HTTP calls with the same
// auth header and TLS settings as the generated client, without re-reading
// credentials from disk on every call.
type Client struct {
	Typed      *dcapi.ClientWithResponses
	token      string
	httpClient *http.Client
}

// New creates a Client with the given access token.
// The base URL is read from Viper (config file or DCCTL_DCAPI_URL env var).
// If `insecure_skip_verify` is true in config (or DCCTL_INSECURE_SKIP_VERIFY
// env var), TLS certificate validation is disabled — useful in dev when the
// cluster is serving a self-signed ingress-nginx default cert.
func New(accessToken string) *Client {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	if viper.GetBool("insecure_skip_verify") {
		transport.TLSClientConfig = &tls.Config{InsecureSkipVerify: true} //nolint:gosec // dev/self-signed support
	}
	httpClient := &http.Client{
		Timeout:   30 * time.Second,
		Transport: transport,
	}

	typed, err := dcapi.NewClientWithResponses(
		viper.GetString("dcapi_url"),
		dcapi.WithHTTPClient(httpClient),
		dcapi.WithRequestEditorFn(func(_ context.Context, req *http.Request) error {
			req.Header.Set("Authorization", "Bearer "+accessToken)
			req.Header.Set("Accept", "application/json")
			return nil
		}),
	)
	if err != nil {
		// Construction only fails on a malformed base URL — which Viper would
		// have caught upstream. Returning a nil-typed Client makes the first
		// call NPE clearly rather than silently issuing requests without auth.
		return &Client{}
	}
	return &Client{Typed: typed, token: accessToken, httpClient: httpClient}
}
