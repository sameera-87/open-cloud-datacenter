// Package rancher — steve.go
//
// SteveClient is a thin HTTP client for the Rancher Steve API (/v1/<kind>
// endpoints). Steve is the Rancher-native REST API that the Rancher UI uses
// for all resource operations on provisioning.cattle.io and related API groups.
//
// Why Steve instead of the v3 Norman API?
//
//	The Norman v3 client (client.go) handles legacy RKE1 objects and some
//	management-plane operations (generateKubeconfig, cloud credentials). For
//	provisioning.cattle.io.clusters and rke-machine-config.cattle.io.harvesterconfigs,
//	Steve is the correct endpoint: it handles the full server-side transaction
//	(secret creation, v3 management cluster mirror, etc.) that the Norman path
//	leaves as a gap. The Rancher UI uses Steve for all of these; so should we.
//
// URL scheme:
//
//	list/create:  <baseURL>/v1/<kind>/<namespace>
//	get/update/delete: <baseURL>/v1/<kind>/<namespace>/<name>
//
// Kind format: <group>.<resource> e.g.:
//
//	"provisioning.cattle.io.clusters"
//	"rke-machine-config.cattle.io.harvesterconfigs"
//	"secrets" (core Secret — Steve uses bare plural, NOT "core.v1.secrets")
//
// Auth: Bearer token (same token as the Norman v3 client).
//
// Note on CSRF: Rancher's Steve API does NOT require the X-Api-Action-Links or
// X-Api-Csrf headers for programmatic clients that authenticate via Bearer
// token. The headers are only required when the request originates from a
// browser session (cookie-based auth). Bearer-token clients are exempt.
package rancher

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
)

// SteveResource is the generic response envelope returned by all Steve endpoints.
// Specific fields (spec, status, metadata) are preserved as raw JSON so callers
// can decode them into purpose-specific structs without a second round-trip.
type SteveResource struct {
	// Standard Kubernetes metadata fields exposed by Steve.
	ID        string                 `json:"id"`        // "<namespace>/<name>" form
	Type      string                 `json:"type"`      // e.g. "provisioning.cattle.io.cluster"
	APIVersion string                `json:"apiVersion"`
	Kind      string                 `json:"kind"`
	Metadata  map[string]interface{} `json:"metadata"`
	Spec      json.RawMessage        `json:"spec,omitempty"`
	Status    json.RawMessage        `json:"status,omitempty"`
	Data      json.RawMessage        `json:"data,omitempty"` // Secrets expose data here
}

// SteveCollection wraps a list response from Steve (/v1/<kind>/<ns>).
type SteveCollection struct {
	Type  string          `json:"type"`  // "collection"
	Count int             `json:"count"`
	Data  []SteveResource `json:"data"`
}

// SteveClient talks to the Rancher Steve API (/v1/*).
// It shares the same base URL and Bearer token as the Norman v3 client in
// client.go but targets different URL prefixes.
//
// Instantiate via NewSteveClient. The zero value is not usable.
type SteveClient struct {
	baseURL    string
	token      string
	httpClient *http.Client
}

// NewSteveClient creates a SteveClient.
// baseURL: Rancher base URL, e.g. "https://rancher-lk-dev.wso2.com" (no trailing slash).
// token: Rancher API token, e.g. "token-xxxxx:yyyyyyy".
// httpClient: caller-provided client; pass an InsecureTLSClient for dev environments.
// The http.Client's transport and timeout are fully controlled by the caller.
func NewSteveClient(baseURL, token string, httpClient *http.Client) *SteveClient {
	return &SteveClient{
		baseURL:    baseURL,
		token:      token,
		httpClient: httpClient,
	}
}

// Create POSTs a new resource to Steve.
//
// kind: group.resource kind string, e.g. "provisioning.cattle.io.clusters".
// ns: namespace for the resource, e.g. "fleet-default".
// body: the full JSON body to POST (must be a valid map or struct that encodes
// to the Steve expected format — include "type", "metadata", and "spec" keys).
//
// Returns the created resource or an error wrapping the HTTP status and body.
func (s *SteveClient) Create(ctx context.Context, kind, ns string, body interface{}) (*SteveResource, error) {
	path := fmt.Sprintf("/v1/%s/%s", kind, ns)
	raw, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("steve create %s: marshal body: %w", kind, err)
	}
	resp, err := s.do(ctx, http.MethodPost, path, raw)
	if err != nil {
		return nil, fmt.Errorf("steve create %s in %s: %w", kind, ns, err)
	}
	defer resp.Body.Close()

	b, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("steve create %s: read response body: %w", kind, err)
	}
	if resp.StatusCode != http.StatusCreated && resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("steve create %s in %s: HTTP %d: %s", kind, ns, resp.StatusCode, truncate(b, 512))
	}
	var res SteveResource
	if err := json.Unmarshal(b, &res); err != nil {
		return nil, fmt.Errorf("steve create %s: decode response: %w", kind, err)
	}
	return &res, nil
}

// Get fetches a named resource from Steve.
//
// kind: group.resource kind string.
// ns:   namespace, e.g. "fleet-default".
// name: resource name within the namespace.
//
// Returns ErrSteveNotFound if the resource does not exist (HTTP 404).
func (s *SteveClient) Get(ctx context.Context, kind, ns, name string) (*SteveResource, error) {
	path := fmt.Sprintf("/v1/%s/%s/%s", kind, ns, name)
	resp, err := s.do(ctx, http.MethodGet, path, nil)
	if err != nil {
		return nil, fmt.Errorf("steve get %s/%s/%s: %w", kind, ns, name, err)
	}
	defer resp.Body.Close()

	b, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("steve get %s: read response body: %w", kind, err)
	}
	if resp.StatusCode == http.StatusNotFound {
		return nil, &SteveNotFoundError{Kind: kind, NS: ns, Name: name}
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("steve get %s/%s/%s: HTTP %d: %s", kind, ns, name, resp.StatusCode, truncate(b, 512))
	}
	var res SteveResource
	if err := json.Unmarshal(b, &res); err != nil {
		return nil, fmt.Errorf("steve get %s/%s/%s: decode response: %w", kind, ns, name, err)
	}
	return &res, nil
}

// Update PUTs a full replacement body for an existing resource.
//
// Steve requires the body to include metadata.resourceVersion (optimistic
// concurrency). The caller should GET the resource first, modify fields, and
// pass the modified body. The response is the updated resource.
func (s *SteveClient) Update(ctx context.Context, kind, ns, name string, body interface{}) (*SteveResource, error) {
	path := fmt.Sprintf("/v1/%s/%s/%s", kind, ns, name)
	raw, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("steve update %s/%s/%s: marshal body: %w", kind, ns, name, err)
	}
	resp, err := s.do(ctx, http.MethodPut, path, raw)
	if err != nil {
		return nil, fmt.Errorf("steve update %s/%s/%s: %w", kind, ns, name, err)
	}
	defer resp.Body.Close()

	b, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("steve update %s: read response body: %w", kind, err)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("steve update %s/%s/%s: HTTP %d: %s", kind, ns, name, resp.StatusCode, truncate(b, 512))
	}
	var res SteveResource
	if err := json.Unmarshal(b, &res); err != nil {
		return nil, fmt.Errorf("steve update %s/%s/%s: decode response: %w", kind, ns, name, err)
	}
	return &res, nil
}

// Delete removes a resource from Steve. Returns nil if successful.
// Tolerates HTTP 404 (resource already gone) as success.
func (s *SteveClient) Delete(ctx context.Context, kind, ns, name string) error {
	path := fmt.Sprintf("/v1/%s/%s/%s", kind, ns, name)
	resp, err := s.do(ctx, http.MethodDelete, path, nil)
	if err != nil {
		return fmt.Errorf("steve delete %s/%s/%s: %w", kind, ns, name, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound {
		// Already gone — idempotent success.
		return nil
	}
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusNoContent {
		b, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("steve delete %s/%s/%s: HTTP %d: %s", kind, ns, name, resp.StatusCode, truncate(b, 512))
	}
	return nil
}

// List returns all resources of a given kind in a namespace.
func (s *SteveClient) List(ctx context.Context, kind, ns string) ([]SteveResource, error) {
	return s.ListByLabel(ctx, kind, ns, "")
}

// ListByLabel returns resources of a given kind in a namespace, optionally
// filtered by a Kubernetes label selector (e.g. "cluster.x-k8s.io/cluster-name=foo").
// Empty labelSelector behaves identically to List.
func (s *SteveClient) ListByLabel(ctx context.Context, kind, ns, labelSelector string) ([]SteveResource, error) {
	path := fmt.Sprintf("/v1/%s/%s", kind, ns)
	if labelSelector != "" {
		path += "?labelSelector=" + url.QueryEscape(labelSelector)
	}
	resp, err := s.do(ctx, http.MethodGet, path, nil)
	if err != nil {
		return nil, fmt.Errorf("steve list %s in %s: %w", kind, ns, err)
	}
	defer resp.Body.Close()

	b, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("steve list %s: read response body: %w", kind, err)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("steve list %s in %s: HTTP %d: %s", kind, ns, resp.StatusCode, truncate(b, 512))
	}
	var col SteveCollection
	if err := json.Unmarshal(b, &col); err != nil {
		return nil, fmt.Errorf("steve list %s: decode collection: %w", kind, err)
	}
	return col.Data, nil
}

// Patch applies a JSON merge patch to an existing resource. Used for
// finalizer removal: `{"metadata":{"finalizers":[]}}`. Content-Type is
// `application/merge-patch+json` per the JSON Merge Patch RFC and Kubernetes
// API expectations. Tolerates HTTP 404 as idempotent success.
func (s *SteveClient) Patch(ctx context.Context, kind, ns, name string, patch []byte) error {
	path := fmt.Sprintf("/v1/%s/%s/%s", kind, ns, name)
	req, err := http.NewRequestWithContext(ctx, http.MethodPatch, s.baseURL+path, bytes.NewReader(patch))
	if err != nil {
		return fmt.Errorf("steve patch %s/%s/%s: build request: %w", kind, ns, name, err)
	}
	req.Header.Set("Authorization", "Bearer "+s.token)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Content-Type", "application/merge-patch+json")
	resp, err := s.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("steve patch %s/%s/%s: %w", kind, ns, name, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return nil
	}
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("steve patch %s/%s/%s: HTTP %d: %s", kind, ns, name, resp.StatusCode, truncate(b, 512))
	}
	return nil
}

// ── HTTP helper ───────────────────────────────────────────────────────────────

// do executes a single HTTP request, injecting Authorization and Content-Type
// headers. body may be nil for GET/DELETE requests.
func (s *SteveClient) do(ctx context.Context, method, path string, body []byte) (*http.Response, error) {
	var bodyReader io.Reader
	if body != nil {
		bodyReader = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, s.baseURL+path, bodyReader)
	if err != nil {
		return nil, fmt.Errorf("build request %s %s: %w", method, path, err)
	}
	req.Header.Set("Authorization", "Bearer "+s.token)
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	return s.httpClient.Do(req)
}

// ── Error types ───────────────────────────────────────────────────────────────

// SteveNotFoundError is returned by Get when the resource does not exist (HTTP 404).
// Callers can use errors.As(err, &SteveNotFoundError{}) to distinguish "not found"
// from other errors.
type SteveNotFoundError struct {
	Kind string
	NS   string
	Name string
}

func (e *SteveNotFoundError) Error() string {
	return fmt.Sprintf("steve: %s %s/%s not found", e.Kind, e.NS, e.Name)
}

// IsSteveNotFound returns true if err is a *SteveNotFoundError (HTTP 404 from Steve).
func IsSteveNotFound(err error) bool {
	var e *SteveNotFoundError
	return errors.As(err, &e)
}

// ── Helpers ───────────────────────────────────────────────────────────────────

// truncate returns at most n bytes of b, appending "…" if truncated.
// Used to avoid flooding logs with giant error bodies.
func truncate(b []byte, n int) string {
	if len(b) <= n {
		return string(b)
	}
	return string(b[:n]) + "…"
}
