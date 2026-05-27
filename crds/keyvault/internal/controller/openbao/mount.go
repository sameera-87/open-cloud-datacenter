package openbao

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
)

// ── Secrets-engine (mount) operations ───────────────────────────────────────

// MountConfig is the body of POST /v1/sys/mounts/<path>.
type MountConfig struct {
	Type        string            `json:"type"`
	Description string            `json:"description,omitempty"`
	Options     map[string]string `json:"options,omitempty"`
}

// MountExists returns true when a secrets engine is already mounted at
// `path`. Uses GET /v1/sys/mounts/<path>; OpenBao responds 200 when
// present and 400 when not.
func (c *Client) MountExists(ctx context.Context, path string) (bool, error) {
	status, err := c.do(ctx, http.MethodGet, "/v1/sys/mounts/"+path, nil, nil)
	if err == nil {
		return true, nil
	}
	// 400 = no such mount; treat as "doesn't exist" rather than a hard error.
	if isHTTPStatus(err, http.StatusBadRequest) || status == http.StatusBadRequest {
		return false, nil
	}
	if isHTTPStatus(err, http.StatusNotFound) || status == http.StatusNotFound {
		return false, nil
	}
	return false, err
}

// EnableKVv2Mount enables a kv-v2 secrets engine at `path` if not already
// enabled. Idempotent — safe to call repeatedly.
func (c *Client) EnableKVv2Mount(ctx context.Context, path string) error {
	exists, err := c.MountExists(ctx, path)
	if err != nil {
		return fmt.Errorf("check mount: %w", err)
	}
	if exists {
		return nil
	}
	body := MountConfig{
		Type:    "kv",
		Options: map[string]string{"version": "2"},
	}
	if _, err := c.do(ctx, http.MethodPost, "/v1/sys/mounts/"+path, body, nil); err != nil {
		return fmt.Errorf("enable kv-v2 mount: %w", err)
	}
	return nil
}

// ConfigureKVv2 sets the kv-v2 metadata config for the mount. The most
// useful option is `delete_version_after` — how long a deleted version
// stays recoverable before being permanently removed.
//
// goDuration must be a Go duration string ("720h" for 30 days).
//
// Retries on the "Upgrading from non-versioned to versioned data" response,
// which OpenBao returns the first time /<mount>/config is hit on a freshly
// enabled kv-v2 mount before the backend has lazy-initialized its v2
// metadata. It's transient (usually <1s) but surfaces as a hard error
// because the HTTP status is non-2xx.
func (c *Client) ConfigureKVv2(ctx context.Context, mountPath, deleteVersionAfter string) error {
	body := map[string]any{
		"delete_version_after": deleteVersionAfter,
	}
	const maxAttempts = 6
	var lastErr error
	for attempt := 1; attempt <= maxAttempts; attempt++ {
		_, err := c.do(ctx, http.MethodPost, "/v1/"+mountPath+"/config", body, nil)
		if err == nil {
			return nil
		}
		lastErr = err
		if !strings.Contains(err.Error(), "Upgrading from non-versioned to versioned") {
			return err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(time.Duration(attempt) * 500 * time.Millisecond):
		}
	}
	return fmt.Errorf("configure mount: still upgrading after %d attempts: %w", maxAttempts, lastErr)
}

// ── Policy operations ───────────────────────────────────────────────────────

// WritePolicy creates or replaces an ACL policy.
// policyHCL is the policy body (path "...." { capabilities = [...] } blocks).
func (c *Client) WritePolicy(ctx context.Context, name, policyHCL string) error {
	body := map[string]string{"policy": policyHCL}
	_, err := c.do(ctx, http.MethodPut, "/v1/sys/policies/acl/"+name, body, nil)
	return err
}

// ── Auth method operations ──────────────────────────────────────────────────

// AuthExists returns true when an auth method is enabled at `path`.
// Uses GET /v1/sys/auth and looks for the path in the response map.
func (c *Client) AuthExists(ctx context.Context, path string) (bool, error) {
	var resp map[string]any
	if _, err := c.do(ctx, http.MethodGet, "/v1/sys/auth", nil, &resp); err != nil {
		return false, err
	}
	// Auth methods are keyed by "<path>/" in the response.
	if _, ok := resp[path+"/"]; ok {
		return true, nil
	}
	// Newer OpenBao versions wrap the map in a "data" key.
	if data, ok := resp["data"].(map[string]any); ok {
		_, present := data[path+"/"]
		return present, nil
	}
	return false, nil
}

// EnableApproleAuth enables the approle auth method at /v1/auth/approle.
// Idempotent.
func (c *Client) EnableApproleAuth(ctx context.Context) error {
	exists, err := c.AuthExists(ctx, "approle")
	if err != nil {
		return fmt.Errorf("check approle auth: %w", err)
	}
	if exists {
		return nil
	}
	body := map[string]string{"type": "approle"}
	if _, err := c.do(ctx, http.MethodPost, "/v1/sys/auth/approle", body, nil); err != nil {
		return fmt.Errorf("enable approle auth: %w", err)
	}
	return nil
}

// ── AppRole operations ──────────────────────────────────────────────────────

// AppRoleParams describes an AppRole role. Only the fields the controller
// sets today are exposed — others default in OpenBao.
type AppRoleParams struct {
	TokenPolicies []string `json:"token_policies"`
	TokenTTL      string   `json:"token_ttl,omitempty"`     // e.g. "1h"
	TokenMaxTTL   string   `json:"token_max_ttl,omitempty"` // e.g. "4h"
}

// WriteAppRoleRole creates or replaces an AppRole. Idempotent at the
// role level — re-creating doesn't rotate the role_id.
func (c *Client) WriteAppRoleRole(ctx context.Context, name string, params AppRoleParams) error {
	_, err := c.do(ctx, http.MethodPost, "/v1/auth/approle/role/"+name, params, nil)
	return err
}

// ReadRoleID returns the stable role_id for an existing AppRole.
// Calling repeatedly returns the same value.
func (c *Client) ReadRoleID(ctx context.Context, name string) (string, error) {
	var resp struct {
		Data struct {
			RoleID string `json:"role_id"`
		} `json:"data"`
	}
	if _, err := c.do(ctx, http.MethodGet, "/v1/auth/approle/role/"+name+"/role-id", nil, &resp); err != nil {
		return "", err
	}
	return resp.Data.RoleID, nil
}

// GenerateSecretID mints a fresh secret_id for an AppRole. Calling twice
// returns two different secret_ids; the controller MUST only do this once
// per KeyVaultInstance and persist the result in the credentials Secret.
func (c *Client) GenerateSecretID(ctx context.Context, name string) (string, error) {
	var resp struct {
		Data struct {
			SecretID string `json:"secret_id"`
		} `json:"data"`
	}
	if _, err := c.do(ctx, http.MethodPost, "/v1/auth/approle/role/"+name+"/secret-id", map[string]any{}, &resp); err != nil {
		return "", err
	}
	return resp.Data.SecretID, nil
}

// ── Delete operations (all idempotent — not-found is treated as success) ────

// DisableMount unmounts a secrets engine at `path`. **Destructive** — kv-v2
// metadata + every secret version stored under the mount is lost. Use
// inside finalizer flow only.
func (c *Client) DisableMount(ctx context.Context, path string) error {
	_, err := c.do(ctx, http.MethodDelete, "/v1/sys/mounts/"+path, nil, nil)
	if err == nil || isHTTPStatus(err, http.StatusNotFound) {
		return nil
	}
	return err
}

// DeletePolicy removes an ACL policy by name. Idempotent — 204/404 both OK.
func (c *Client) DeletePolicy(ctx context.Context, name string) error {
	_, err := c.do(ctx, http.MethodDelete, "/v1/sys/policies/acl/"+name, nil, nil)
	if err == nil || isHTTPStatus(err, http.StatusNotFound) {
		return nil
	}
	return err
}

// DeleteAppRoleRole removes an AppRole role + invalidates every outstanding
// secret_id for that role. Idempotent.
func (c *Client) DeleteAppRoleRole(ctx context.Context, name string) error {
	_, err := c.do(ctx, http.MethodDelete, "/v1/auth/approle/role/"+name, nil, nil)
	if err == nil || isHTTPStatus(err, http.StatusNotFound) {
		return nil
	}
	return err
}

// ── helpers ─────────────────────────────────────────────────────────────────

// isHTTPStatus checks whether err originated from an apiserver call that
// returned the given status code. apierrors.StatusError carries .Code.
func isHTTPStatus(err error, code int) bool {
	var se *apierrors.StatusError
	if errors.As(err, &se) {
		return int(se.ErrStatus.Code) == code
	}
	return false
}
