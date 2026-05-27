//go:build integration

package integration

// TestSteveClient — live integration test for the Rancher Steve API client.
//
// This test requires:
//
//	DCAPI_RANCHER_URL=https://rancher-lk-dev.wso2.com
//	DCAPI_RANCHER_TOKEN=token-xxxxx:yyyyyyy
//	DCAPI_RANCHER_INSECURE=true  (for self-signed cert)
//
// It exercises only the LIST operation (read-only) against the live Rancher,
// confirming that:
//  1. Bearer-token auth is accepted.
//  2. The Steve /v1/provisioning.cattle.io.clusters/fleet-default endpoint responds 200.
//  3. The existing dcapi-controlplane-rke2 cluster is present in the result.
//
// No cluster is created or deleted — this test is safe to run against production.
//
// Run:
//
//	KUBECONFIG=$HOME/.kube/config KUBE_CONTEXT=harvester-dev \
//	DCAPI_RANCHER_URL=https://rancher-lk-dev.wso2.com \
//	DCAPI_RANCHER_TOKEN=$DCAPI_RANCHER_TOKEN \
//	DCAPI_RANCHER_INSECURE=true \
//	TESTCONTAINERS_RYUK_DISABLED=true DOCKER_HOST=unix:///${HOME}/.rd/docker.sock \
//	go test -count=1 -tags integration -timeout 10m -run 'TestSteveClient' ./test/integration/...

import (
	"context"
	"crypto/tls"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/wso2/dc-api/internal/providers/rancher"
)

// rancherEnvConfigured returns true when the minimum env vars for live Rancher
// tests are present. Tests that require live Rancher skip when this is false.
func rancherEnvConfigured() bool {
	return os.Getenv("DCAPI_RANCHER_URL") != "" && os.Getenv("DCAPI_RANCHER_TOKEN") != ""
}

// newLiveSteveClient returns a SteveClient configured from environment variables.
// Skips the test (via t.Skip) if required env vars are not set.
func newLiveSteveClient(t *testing.T) *rancher.SteveClient {
	t.Helper()
	if !rancherEnvConfigured() {
		t.Skip("DCAPI_RANCHER_URL and DCAPI_RANCHER_TOKEN not set — skipping live Steve test")
	}
	baseURL := strings.TrimRight(os.Getenv("DCAPI_RANCHER_URL"), "/")
	token := os.Getenv("DCAPI_RANCHER_TOKEN")
	insecure := strings.ToLower(os.Getenv("DCAPI_RANCHER_INSECURE")) == "true"

	transport := http.DefaultTransport
	if insecure {
		//nolint:gosec — intentionally for dev/self-signed cert environments only
		transport = &http.Transport{
			TLSClientConfig: &tls.Config{InsecureSkipVerify: true}, //nolint:gosec
		}
	}
	httpClient := &http.Client{
		Transport: transport,
		Timeout:   30 * time.Second,
	}
	return rancher.NewSteveClient(baseURL, token, httpClient)
}

// TestSteveClient_ListClusters_LiveRancher calls Steve LIST against the live
// Rancher and asserts the existing dcapi-controlplane-rke2 cluster is returned.
// This is read-only and safe against any environment.
func TestSteveClient_ListClusters_LiveRancher(t *testing.T) {
	client := newLiveSteveClient(t)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	items, err := client.List(ctx, "provisioning.cattle.io.clusters", "fleet-default")
	require.NoError(t, err, "Steve LIST provisioning.cattle.io.clusters in fleet-default failed")
	require.NotEmpty(t, items, "expected at least one cluster in fleet-default, got none")

	// Confirm the known control-plane cluster is present.
	var found bool
	for _, item := range items {
		if item.ID == "fleet-default/dcapi-controlplane-rke2" {
			found = true
			break
		}
	}
	require.True(t, found,
		"expected fleet-default/dcapi-controlplane-rke2 in Steve cluster list, got IDs: %v",
		clusterIDs(items))

	t.Logf("Steve LIST: %d clusters found in fleet-default", len(items))
	for _, item := range items {
		t.Logf("  - %s (type: %s)", item.ID, item.Type)
	}
}

// TestSteveClient_GetCluster_LiveRancher fetches the existing dcapi-controlplane-rke2
// cluster by name from Steve and validates that the status is parseable.
func TestSteveClient_GetCluster_LiveRancher(t *testing.T) {
	client := newLiveSteveClient(t)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	res, err := client.Get(ctx, "provisioning.cattle.io.clusters", "fleet-default", "dcapi-controlplane-rke2")
	require.NoError(t, err, "Steve GET provisioning.cattle.io.clusters/fleet-default/dcapi-controlplane-rke2 failed")
	require.NotNil(t, res)
	require.Equal(t, "fleet-default/dcapi-controlplane-rke2", res.ID)

	// Verify status is non-nil and decodable.
	require.NotNil(t, res.Status, "expected status field in Steve GET response")
	t.Logf("dcapi-controlplane-rke2 status: %s", string(res.Status))
}

// TestSteveClient_GetNonexistentCluster returns a SteveNotFoundError.
func TestSteveClient_GetNonexistentCluster_LiveRancher(t *testing.T) {
	client := newLiveSteveClient(t)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	_, err := client.Get(ctx, "provisioning.cattle.io.clusters", "fleet-default", "this-cluster-does-not-exist-f32test")
	require.Error(t, err)
	require.True(t, rancher.IsSteveNotFound(err),
		"expected SteveNotFoundError for nonexistent cluster, got: %v", err)
}

// ── Helpers ───────────────────────────────────────────────────────────────────

// clusterIDs extracts IDs from a SteveResource slice for error messages.
func clusterIDs(items []rancher.SteveResource) []string {
	ids := make([]string, len(items))
	for i, item := range items {
		ids[i] = item.ID
	}
	return ids
}
