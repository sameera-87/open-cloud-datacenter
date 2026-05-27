// Package kvi — openbao_proxy.go
//
// Minimal OpenBao KV-v2 client that proxies requests through the Kubernetes
// API server's pod-proxy mechanism. dc-api never speaks directly to the
// OpenBao pods over the network; every request goes via:
//
//	k8s apiserver → pod proxy → OpenBao pod :<port>/v1/...
//
// This avoids punching firewall holes between the dc-api pod and the
// tenant's OpenBao StatefulSet.
//
// Security note: the root token is NEVER logged, never included in error
// messages returned to callers, and never stored beyond the lifetime of a
// single request invocation.
package kvi

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"k8s.io/client-go/rest"
)

const (
	// maxValueSize is the maximum allowed plaintext value size (1 MiB).
	// This mirrors OpenBao's default per-secret cap.
	maxValueSize = 1 << 20 // 1 MiB

	// openbaoPort is the default OpenBao API port.
	openbaoPort = 8200
)

// OpenBaoProxy wraps the Kubernetes REST client to proxy requests to an
// OpenBao pod running inside the cluster.
type OpenBaoProxy struct {
	restCfg *rest.Config
}

// NewOpenBaoProxy creates a proxy backed by the given Kubernetes REST config.
func NewOpenBaoProxy(restCfg *rest.Config) *OpenBaoProxy {
	return &OpenBaoProxy{restCfg: restCfg}
}

// ── Secret data types ─────────────────────────────────────────────────────────

// SecretData is the payload returned by a successful KV-v2 read.
type SecretData struct {
	// Value is the plaintext secret value stored under the "value" key in
	// the KV-v2 data map. We store secrets as {"value": "<plaintext>",
	// "metadata": <map>} so a single write carries both.
	Value    string            `json:"value"`
	Metadata map[string]string `json:"metadata,omitempty"`
}

// SecretVersion holds KV-v2 version metadata returned by a read or metadata
// call.
type SecretVersion struct {
	// Version is the KV-v2 version number (monotonically increasing).
	Version int `json:"version"`
	// CreatedTime is the RFC-3339 timestamp when this version was written.
	CreatedTime string `json:"created_time"`
	// DeletionTime is non-empty (RFC-3339) if this version has been
	// soft-deleted.
	DeletionTime string `json:"deletion_time"`
	// Destroyed is true if this version has been hard-destroyed.
	Destroyed bool `json:"destroyed"`
}

// SecretMetadata is the full metadata block for a key (all versions).
type SecretMetadata struct {
	CurrentVersion int                      `json:"current_version"`
	OldestVersion  int                      `json:"oldest_version"`
	CreatedTime    string                   `json:"created_time"`
	UpdatedTime    string                   `json:"updated_time"`
	Versions       map[string]SecretVersion `json:"versions"`
}

// ReadResult is the combined output of a KV-v2 data read.
type ReadResult struct {
	Data    SecretData    `json:"data"`
	Version SecretVersion `json:"version"`
}

// ── OpenBao API error types ───────────────────────────────────────────────────

// ErrOpenBaoNotFound is returned when OpenBao responds with 404.
type ErrOpenBaoNotFound struct{ Key string }

func (e ErrOpenBaoNotFound) Error() string {
	return fmt.Sprintf("secret %q not found in vault", e.Key)
}

// ErrOpenBaoUnavailable is returned when OpenBao responds with 429 (standby)
// or 503 (sealed / upgrading). Callers should surface this as dc-api 503.
type ErrOpenBaoUnavailable struct {
	StatusCode int
	Message    string
}

func (e ErrOpenBaoUnavailable) Error() string {
	return fmt.Sprintf("openbao unavailable (HTTP %d): %s", e.StatusCode, e.Message)
}

// ── Proxy call helper ─────────────────────────────────────────────────────────

// call issues an HTTP request through the Kubernetes pod-proxy for the named
// pod in the given namespace. The request is forwarded to :<openbaoPort>/v1/<path>.
//
// token is the OpenBao root token — it MUST NOT appear in log output or error
// messages returned to the user; the caller is responsible for sanitising any
// error it re-wraps.
func (p *OpenBaoProxy) call(
	ctx context.Context,
	method, namespace, podName, vaultPath string,
	token string,
	reqBody interface{},
) ([]byte, int, error) {
	// Build the k8s pod-proxy URL:
	//   /api/v1/namespaces/<ns>/pods/<pod>:<port>/proxy/v1/<vaultPath>
	proxyPath := fmt.Sprintf(
		"/api/v1/namespaces/%s/pods/%s:%d/proxy/v1/%s",
		namespace, podName, openbaoPort, vaultPath,
	)

	var bodyReader io.Reader
	if reqBody != nil {
		b, err := json.Marshal(reqBody)
		if err != nil {
			return nil, 0, fmt.Errorf("marshal request body: %w", err)
		}
		bodyReader = bytes.NewReader(b)
	}

	// Derive the API server URL from the REST config.
	apiServerURL := strings.TrimRight(p.restCfg.Host, "/")
	fullURL := apiServerURL + proxyPath

	req, err := http.NewRequestWithContext(ctx, method, fullURL, bodyReader)
	if err != nil {
		return nil, 0, fmt.Errorf("build request: %w", err)
	}
	if reqBody != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	// OpenBao root token auth — never log or surface this value.
	req.Header.Set("X-Vault-Token", token)

	// Build an HTTP client from the REST config (handles TLS + bearer auth to
	// the k8s apiserver). We call rest.HTTPClientFor which respects any
	// cluster-internal TLS and bearer token.
	httpClient, err := rest.HTTPClientFor(p.restCfg)
	if err != nil {
		return nil, 0, fmt.Errorf("build k8s http client: %w", err)
	}

	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, 0, fmt.Errorf("pod-proxy request to OpenBao: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, resp.StatusCode, fmt.Errorf("read response body: %w", err)
	}

	// Surface backend unavailability.
	if resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode == http.StatusServiceUnavailable {
		msg := extractErrors(body)
		return nil, resp.StatusCode, ErrOpenBaoUnavailable{StatusCode: resp.StatusCode, Message: msg}
	}

	return body, resp.StatusCode, nil
}

// ── KV-v2 operations ─────────────────────────────────────────────────────────

// WriteSecret writes a KV-v2 secret at <mount>/data/<key>. The value and
// optional metadata are stored as {"data": {"value": ..., "metadata": ...}}.
//
// Returns the version number of the just-written secret, and whether the key
// was new (isNew=true means 201, isNew=false means 200).
func (p *OpenBaoProxy) WriteSecret(
	ctx context.Context,
	namespace, podName, mount, key, token, value string,
	meta map[string]string,
) (version int, isNew bool, err error) {
	// Pre-check: does metadata exist? Determines 201 vs 200.
	_, metaErr := p.GetMetadata(ctx, namespace, podName, mount, key, token)
	isNew = metaErr != nil // Not found → first write

	dataMap := map[string]interface{}{"value": value}
	if len(meta) > 0 {
		dataMap["metadata"] = meta
	}
	body := map[string]interface{}{"data": dataMap}

	path := mount + "/data/" + key
	respBody, statusCode, callErr := p.call(ctx, http.MethodPost, namespace, podName, path, token, body)
	if callErr != nil {
		return 0, false, fmt.Errorf("write secret: %w", callErr)
	}
	if statusCode != http.StatusOK && statusCode != http.StatusNoContent {
		return 0, false, fmt.Errorf("write secret: unexpected status %d: %s", statusCode, sanitiseBody(respBody))
	}

	// Parse version from response.
	var out struct {
		Data struct {
			Version int `json:"version"`
		} `json:"data"`
	}
	_ = json.Unmarshal(respBody, &out)
	return out.Data.Version, isNew, nil
}

// ReadSecret reads the KV-v2 secret at <mount>/data/<key>. If version > 0 the
// specific version is fetched; otherwise the latest version is returned.
//
// Returns ErrOpenBaoNotFound if the key has never been written.
func (p *OpenBaoProxy) ReadSecret(
	ctx context.Context,
	namespace, podName, mount, key, token string,
	version int,
) (*ReadResult, error) {
	path := mount + "/data/" + key
	if version > 0 {
		path = fmt.Sprintf("%s?version=%d", path, version)
	}

	respBody, statusCode, err := p.call(ctx, http.MethodGet, namespace, podName, path, token, nil)
	if err != nil {
		return nil, fmt.Errorf("read secret: %w", err)
	}
	if statusCode == http.StatusNotFound {
		return nil, ErrOpenBaoNotFound{Key: key}
	}
	if statusCode != http.StatusOK {
		return nil, fmt.Errorf("read secret: unexpected status %d: %s", statusCode, sanitiseBody(respBody))
	}

	// KV-v2 response shape:
	// {"data": {"data": {...}, "metadata": {"version":N, "created_time":"...", "deletion_time":"...", "destroyed":bool}}}
	var raw struct {
		Data struct {
			Data     map[string]interface{} `json:"data"`
			Metadata struct {
				Version      int    `json:"version"`
				CreatedTime  string `json:"created_time"`
				DeletionTime string `json:"deletion_time"`
				Destroyed    bool   `json:"destroyed"`
			} `json:"metadata"`
		} `json:"data"`
	}
	if err := json.Unmarshal(respBody, &raw); err != nil {
		return nil, fmt.Errorf("read secret: decode response: %w", err)
	}

	// Extract value and optional inline metadata from the data map.
	value, _ := raw.Data.Data["value"].(string)
	var inlineMeta map[string]string
	if m, ok := raw.Data.Data["metadata"]; ok {
		switch mv := m.(type) {
		case map[string]interface{}:
			inlineMeta = make(map[string]string, len(mv))
			for k, v := range mv {
				inlineMeta[k], _ = v.(string)
			}
		case map[string]string:
			inlineMeta = mv
		}
	}

	return &ReadResult{
		Data: SecretData{Value: value, Metadata: inlineMeta},
		Version: SecretVersion{
			Version:      raw.Data.Metadata.Version,
			CreatedTime:  raw.Data.Metadata.CreatedTime,
			DeletionTime: raw.Data.Metadata.DeletionTime,
			Destroyed:    raw.Data.Metadata.Destroyed,
		},
	}, nil
}

// ListKeys returns the sorted list of keys at <mount>/metadata?list=true.
//
// OpenBao KV-v2 supports both LIST /metadata/ and GET /metadata?list=true.
// The Kubernetes pod-proxy only passes standard HTTP methods (not LIST), so
// we use the GET ?list=true form which produces identical output.
//
// Returns an empty slice (not an error) when the mount has no keys yet.
func (p *OpenBaoProxy) ListKeys(
	ctx context.Context,
	namespace, podName, mount, token string,
) ([]string, error) {
	// Use GET ?list=true — the Kubernetes pod-proxy does not forward the
	// non-standard LIST method (responds 405 MethodNotAllowed).
	path := mount + "/metadata?list=true"

	respBody, statusCode, err := p.call(ctx, http.MethodGet, namespace, podName, path, token, nil)
	if err != nil {
		return nil, fmt.Errorf("list keys: %w", err)
	}
	if statusCode == http.StatusNotFound {
		// Mount exists but no keys yet — return empty list.
		return nil, nil
	}
	if statusCode != http.StatusOK {
		return nil, fmt.Errorf("list keys: unexpected status %d: %s", statusCode, sanitiseBody(respBody))
	}

	var out struct {
		Data struct {
			Keys []string `json:"keys"`
		} `json:"data"`
	}
	if err := json.Unmarshal(respBody, &out); err != nil {
		return nil, fmt.Errorf("list keys: decode response: %w", err)
	}
	return out.Data.Keys, nil
}

// GetMetadata returns the metadata for a single key. Returns ErrOpenBaoNotFound
// if the key has never been written.
func (p *OpenBaoProxy) GetMetadata(
	ctx context.Context,
	namespace, podName, mount, key, token string,
) (*SecretMetadata, error) {
	path := mount + "/metadata/" + key

	respBody, statusCode, err := p.call(ctx, http.MethodGet, namespace, podName, path, token, nil)
	if err != nil {
		return nil, fmt.Errorf("get metadata: %w", err)
	}
	if statusCode == http.StatusNotFound {
		return nil, ErrOpenBaoNotFound{Key: key}
	}
	if statusCode != http.StatusOK {
		return nil, fmt.Errorf("get metadata: unexpected status %d: %s", statusCode, sanitiseBody(respBody))
	}

	var raw struct {
		Data struct {
			CurrentVersion int    `json:"current_version"`
			OldestVersion  int    `json:"oldest_version"`
			CreatedTime    string `json:"created_time"`
			UpdatedTime    string `json:"updated_time"`
			Versions       map[string]struct {
				CreatedTime  string `json:"created_time"`
				DeletionTime string `json:"deletion_time"`
				Destroyed    bool   `json:"destroyed"`
				Version      int    `json:"version"`
			} `json:"versions"`
		} `json:"data"`
	}
	if err := json.Unmarshal(respBody, &raw); err != nil {
		return nil, fmt.Errorf("get metadata: decode response: %w", err)
	}

	versions := make(map[string]SecretVersion, len(raw.Data.Versions))
	for vk, vv := range raw.Data.Versions {
		versions[vk] = SecretVersion{
			Version:      vv.Version,
			CreatedTime:  vv.CreatedTime,
			DeletionTime: vv.DeletionTime,
			Destroyed:    vv.Destroyed,
		}
	}

	return &SecretMetadata{
		CurrentVersion: raw.Data.CurrentVersion,
		OldestVersion:  raw.Data.OldestVersion,
		CreatedTime:    raw.Data.CreatedTime,
		UpdatedTime:    raw.Data.UpdatedTime,
		Versions:       versions,
	}, nil
}

// DeleteSecret soft-deletes the latest version of a key.
//
// KV-v2 has two delete paths:
//   - DELETE /metadata/<key>  → permanent hard-delete of all versions + history
//   - DELETE /data/<key>      → soft-delete of the CURRENT version only
//
// We use /data/<key> so the key's version history is preserved and earlier
// versions remain readable. This matches the API contract (the handler returns
// 410 Gone on a soft-deleted latest version, and ?version=N still returns
// older versions).
//
// Returns ErrOpenBaoNotFound if the key has never been written.
func (p *OpenBaoProxy) DeleteSecret(
	ctx context.Context,
	namespace, podName, mount, key, token string,
) error {
	// Use DELETE /data/<key> — soft-deletes the current version, preserving
	// version history. NOT /metadata/<key> which is a hard/permanent delete.
	path := mount + "/data/" + key

	respBody, statusCode, err := p.call(ctx, http.MethodDelete, namespace, podName, path, token, nil)
	if err != nil {
		return fmt.Errorf("delete secret: %w", err)
	}
	if statusCode == http.StatusNotFound {
		return ErrOpenBaoNotFound{Key: key}
	}
	if statusCode != http.StatusOK && statusCode != http.StatusNoContent {
		return fmt.Errorf("delete secret: unexpected status %d: %s", statusCode, sanitiseBody(respBody))
	}
	return nil
}

// UndeleteSecretVersion reverses a soft-delete on a specific version of a
// secret in OpenBao's KV-v2 mount. Mapped to POST /<mount>/undelete/<key>
// with {"versions":[N]}.
//
// Returns ErrOpenBaoNotFound if the key has never been written.
func (p *OpenBaoProxy) UndeleteSecretVersion(
	ctx context.Context,
	namespace, podName, mount, key, token string,
	version int,
) error {
	path := mount + "/undelete/" + key
	body := map[string]any{"versions": []int{version}}

	respBody, statusCode, err := p.call(ctx, http.MethodPost, namespace, podName, path, token, body)
	if err != nil {
		return fmt.Errorf("undelete secret: %w", err)
	}
	if statusCode == http.StatusNotFound {
		return ErrOpenBaoNotFound{Key: key}
	}
	if statusCode != http.StatusOK && statusCode != http.StatusNoContent {
		return fmt.Errorf("undelete secret: unexpected status %d: %s", statusCode, sanitiseBody(respBody))
	}
	return nil
}

// ── AppRole secret_id rotation ────────────────────────────────────────────────

// ListSecretIDAccessors returns the accessors of every active secret_id
// bound to the named role. LIST against /v1/auth/approle/role/<role>/secret-id.
// Returns an empty slice when the role has no active secret_ids (or 404 on
// list ⇒ none).
func (p *OpenBaoProxy) ListSecretIDAccessors(
	ctx context.Context,
	namespace, podName, role, token string,
) ([]string, error) {
	path := "auth/approle/role/" + role + "/secret-id?list=true"

	respBody, statusCode, err := p.call(ctx, http.MethodGet, namespace, podName, path, token, nil)
	if err != nil {
		return nil, fmt.Errorf("list secret-id accessors: %w", err)
	}
	if statusCode == http.StatusNotFound {
		return nil, nil
	}
	if statusCode != http.StatusOK {
		return nil, fmt.Errorf("list secret-id accessors: unexpected status %d: %s",
			statusCode, sanitiseBody(respBody))
	}
	var resp struct {
		Data struct {
			Keys []string `json:"keys"`
		} `json:"data"`
	}
	if err := json.Unmarshal(respBody, &resp); err != nil {
		return nil, fmt.Errorf("decode list response: %w", err)
	}
	return resp.Data.Keys, nil
}

// DestroySecretIDAccessor invalidates a specific secret_id by its accessor.
// POST to /v1/auth/approle/role/<role>/secret-id-accessor/destroy with
// {"secret_id_accessor": "..."}. Idempotent: a 404 is treated as success
// (already gone).
func (p *OpenBaoProxy) DestroySecretIDAccessor(
	ctx context.Context,
	namespace, podName, role, accessor, token string,
) error {
	path := "auth/approle/role/" + role + "/secret-id-accessor/destroy"
	body := map[string]string{"secret_id_accessor": accessor}

	respBody, statusCode, err := p.call(ctx, http.MethodPost, namespace, podName, path, token, body)
	if err != nil {
		return fmt.Errorf("destroy secret-id-accessor: %w", err)
	}
	if statusCode == http.StatusNotFound || statusCode == http.StatusNoContent || statusCode == http.StatusOK {
		return nil
	}
	return fmt.Errorf("destroy secret-id-accessor: unexpected status %d: %s",
		statusCode, sanitiseBody(respBody))
}

// GenerateSecretID mints a new secret_id for the named role. Returns the
// secret_id value (one-time-shown) and its accessor. POST to
// /v1/auth/approle/role/<role>/secret-id with empty body uses role-default
// TTLs.
func (p *OpenBaoProxy) GenerateSecretID(
	ctx context.Context,
	namespace, podName, role, token string,
) (secretID, accessor string, err error) {
	path := "auth/approle/role/" + role + "/secret-id"

	respBody, statusCode, err := p.call(ctx, http.MethodPost, namespace, podName, path, token, map[string]any{})
	if err != nil {
		return "", "", fmt.Errorf("generate secret-id: %w", err)
	}
	if statusCode != http.StatusOK {
		return "", "", fmt.Errorf("generate secret-id: unexpected status %d: %s",
			statusCode, sanitiseBody(respBody))
	}
	var resp struct {
		Data struct {
			SecretID         string `json:"secret_id"`
			SecretIDAccessor string `json:"secret_id_accessor"`
		} `json:"data"`
	}
	if err := json.Unmarshal(respBody, &resp); err != nil {
		return "", "", fmt.Errorf("decode generate response: %w", err)
	}
	if resp.Data.SecretID == "" {
		return "", "", fmt.Errorf("generate secret-id: empty secret_id in response")
	}
	return resp.Data.SecretID, resp.Data.SecretIDAccessor, nil
}

// ── Helper ────────────────────────────────────────────────────────────────────

// extractErrors pulls the "errors" array from an OpenBao error response body
// (standard format: {"errors": ["msg1", ...]}) and joins them. Falls back to
// truncated raw body if parsing fails.
func extractErrors(body []byte) string {
	var e struct {
		Errors []string `json:"errors"`
	}
	if json.Unmarshal(body, &e) == nil && len(e.Errors) > 0 {
		return strings.Join(e.Errors, "; ")
	}
	// Truncate large raw bodies — never include a token value in error strings.
	const maxLen = 256
	s := string(body)
	if len(s) > maxLen {
		s = s[:maxLen] + "…"
	}
	return s
}

// sanitiseBody returns a safe-to-include snippet of a response body for use in
// internal error messages. It is intentionally short to avoid leaking secret
// material from unusual error responses.
func sanitiseBody(body []byte) string {
	const maxLen = 200
	s := string(body)
	if len(s) > maxLen {
		s = s[:maxLen] + "…"
	}
	return s
}
