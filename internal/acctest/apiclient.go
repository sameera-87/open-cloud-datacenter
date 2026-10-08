package acctest

import (
	"context"
	"fmt"
	"os"
	"time"

	"terraform-provider-dcapi/internal/client"
)

// NewAPIClient returns a DC-API client built from DCAPI_ENDPOINT and DCAPI_TOKEN.
//
// Checks, Disappears steps and sweepers use it to talk to DC-API *without* Terraform,
// so they don't depend on the provider code they are testing.
func NewAPIClient() (*client.DCAPIClient, error) {
	endpoint, token := os.Getenv("DCAPI_ENDPOINT"), os.Getenv("DCAPI_TOKEN")
	if endpoint == "" || token == "" {
		return nil, fmt.Errorf("DCAPI_ENDPOINT and DCAPI_TOKEN must be set")
	}
	return client.NewClient(endpoint, token)
}

// mustAPIClient is NewAPIClient for check functions, which can only return an error.
func mustAPIClient() (*client.DCAPIClient, error) {
	c, err := NewAPIClient()
	if err != nil {
		return nil, fmt.Errorf("building DC-API client: %w", err)
	}
	return c, nil
}

// WaitGone polls exists every 10 seconds until it returns false or timeout passes.
// Used after out-of-band deletes of async resources, which return 202 before the
// object is really gone.
func WaitGone(ctx context.Context, timeout time.Duration, exists func(ctx context.Context) (bool, error)) error {
	deadline := time.Now().Add(timeout)
	for {
		found, err := exists(ctx)
		if err != nil {
			return err
		}
		if !found {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("object still exists after %s", timeout)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(10 * time.Second):
		}
	}
}
