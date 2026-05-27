// Package openbao is a small HTTP client for the OpenBao REST API.
//
// All requests target a single pod (not a Service) because init/unseal/raft
// operations are intentionally per-pod and Service load-balancing would
// route them to whichever pod the kube-proxy picked. The transport routes
// through the kube API server's pod proxy
// (/api/v1/namespaces/<ns>/pods/<pod>:<port>/proxy/...), so the controller
// can talk to pods from outside the cluster (local dev) without any
// port-forwarding, while in-cluster it works identically.
package openbao

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/serializer"
	"k8s.io/client-go/rest"
)

// Client talks to one OpenBao pod via the kube API pod-proxy.
type Client struct {
	rest      rest.Interface
	namespace string
	pod       string
	port      int

	token string // X-Vault-Token; empty when unauthenticated (init/unseal/health work without)
}

// NewClient builds a Client that proxies through the kube API server to
// reach <pod>:<port> in <namespace>.
func NewClient(cfg *rest.Config, namespace, pod string, port int) (*Client, error) {
	// REST client scoped to "" (core) group so AbsPath stays simple.
	// NegotiatedSerializer is required for any rest.RESTClient.
	cfgCopy := rest.CopyConfig(cfg)
	cfgCopy.APIPath = "/api"
	cfgCopy.GroupVersion = &metav1.SchemeGroupVersion
	cfgCopy.NegotiatedSerializer = serializer.NewCodecFactory(runtime.NewScheme()).WithoutConversion()
	rc, err := rest.RESTClientFor(cfgCopy)
	if err != nil {
		return nil, fmt.Errorf("build rest client: %w", err)
	}
	return &Client{rest: rc, namespace: namespace, pod: pod, port: port}, nil
}

// SetToken sets the X-Vault-Token sent on subsequent requests.
// Use the root_token returned by Init for privileged operations.
func (c *Client) SetToken(token string) { c.token = token }

// do performs an HTTP request via the kube API pod-proxy. If out is non-nil
// and the response has a JSON body, it is decoded into out. Returns the
// HTTP status code and a typed error for non-2xx responses.
func (c *Client) do(ctx context.Context, method, path string, body, out any) (int, error) {
	var rdr io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return 0, fmt.Errorf("marshal request body: %w", err)
		}
		rdr = bytes.NewReader(b)
	}
	absPath := fmt.Sprintf("/api/v1/namespaces/%s/pods/%s:%d/proxy%s",
		c.namespace, c.pod, c.port, path)

	req := c.rest.Verb(method).AbsPath(absPath)
	if rdr != nil {
		req = req.Body(rdr)
	}
	if c.token != "" {
		req = req.SetHeader("X-Vault-Token", c.token)
	}
	req = req.SetHeader("Content-Type", "application/json")

	raw, err := req.DoRaw(ctx)
	status := http.StatusOK
	if err != nil {
		var se *apierrors.StatusError
		if errors.As(err, &se) {
			status = int(se.ErrStatus.Code)
		}
		return status, fmt.Errorf("%s %s: %w (body=%s)", method, path, err, string(raw))
	}
	if out != nil && len(raw) > 0 {
		if err := json.Unmarshal(raw, out); err != nil {
			return status, fmt.Errorf("decode %s %s response: %w (body=%s)", method, path, err, string(raw))
		}
	}
	return status, nil
}

// ── OpenBao API types ───────────────────────────────────────────────────────

// SealStatus mirrors the JSON shape of GET /v1/sys/seal-status. Only fields
// the controller actually reads are declared.
type SealStatus struct {
	Type        string `json:"type"`
	Initialized bool   `json:"initialized"`
	Sealed      bool   `json:"sealed"`
	Threshold   int    `json:"t"`
	Shares      int    `json:"n"`
	Progress    int    `json:"progress"`
	Version     string `json:"version"`
	ClusterName string `json:"cluster_name,omitempty"`
	ClusterID   string `json:"cluster_id,omitempty"`
}

// Health mirrors GET /v1/sys/health. We only consume the boolean fields.
type Health struct {
	Initialized bool `json:"initialized"`
	Sealed      bool `json:"sealed"`
	Standby     bool `json:"standby"`
}

// InitResponse mirrors POST /v1/sys/init's response.
//
// NB: the REST API uses `keys_base64` here. The `bao` CLI's JSON output
// uses `unseal_keys_b64` — DON'T confuse the two; the spike's
// /tmp/openbao-init.json file is bao CLI output and looks different.
type InitResponse struct {
	Keys       []string `json:"keys"`         // hex-encoded shares
	KeysBase64 []string `json:"keys_base64"`  // same shares, base64
	RootToken  string   `json:"root_token"`
}

// Leader mirrors GET /v1/sys/leader's response.
type Leader struct {
	HAEnabled            bool   `json:"ha_enabled"`
	IsSelf               bool   `json:"is_self"`
	LeaderAddress        string `json:"leader_address"`
	LeaderClusterAddress string `json:"leader_cluster_address"`
}

// ── API operations ──────────────────────────────────────────────────────────

// Health returns the pod's high-level health.
// Works even when sealed/uninitialized.
func (c *Client) Health(ctx context.Context) (*Health, error) {
	var h Health
	// 200 = unsealed+active, 429 = unsealed+standby, 472 = DR secondary,
	// 473 = perf standby, 501 = not initialized, 503 = sealed.
	// All status codes return a JSON body, so we want to read the body even
	// on "errors". Use ?standbyok=true to coerce 429 to 200 — but for our
	// purposes we still need to handle the seal-related codes ourselves.
	if _, err := c.do(ctx, http.MethodGet, "/v1/sys/health?standbyok=true&sealedcode=200&uninitcode=200", nil, &h); err != nil {
		return nil, err
	}
	return &h, nil
}

// SealStatus returns the pod's seal state in detail.
func (c *Client) SealStatus(ctx context.Context) (*SealStatus, error) {
	var s SealStatus
	if _, err := c.do(ctx, http.MethodGet, "/v1/sys/seal-status", nil, &s); err != nil {
		return nil, err
	}
	return &s, nil
}

// Init runs POST /v1/sys/init on this pod (which must be the very first
// pod of an uninitialised Raft cluster). Returns the unseal keys + root
// token; the caller is responsible for persisting them durably before
// dropping them on the floor.
func (c *Client) Init(ctx context.Context, shares, threshold int) (*InitResponse, error) {
	body := map[string]int{
		"secret_shares":    shares,
		"secret_threshold": threshold,
	}
	var r InitResponse
	if _, err := c.do(ctx, http.MethodPost, "/v1/sys/init", body, &r); err != nil {
		return nil, err
	}
	return &r, nil
}

// Unseal submits one key share. Call repeatedly with distinct keys until
// the returned SealStatus reports Sealed=false. Idempotent at the unseal
// progress level — sending the same key twice resets progress.
func (c *Client) Unseal(ctx context.Context, key string) (*SealStatus, error) {
	body := map[string]string{"key": key}
	var s SealStatus
	if _, err := c.do(ctx, http.MethodPut, "/v1/sys/unseal", body, &s); err != nil {
		return nil, err
	}
	return &s, nil
}

// RaftJoin tells this pod to join the Raft cluster led by leaderAPIAddr.
// Idempotent: if already joined, returns without error.
func (c *Client) RaftJoin(ctx context.Context, leaderAPIAddr string) error {
	body := map[string]any{
		"leader_api_addr":  leaderAPIAddr,
		"retry":            true,
		"auto_join_scheme": "http",
	}
	_, err := c.do(ctx, http.MethodPost, "/v1/sys/storage/raft/join", body, nil)
	return err
}

// Leader returns the active-leader info reported by this pod.
// Empty LeaderAddress means election in progress.
func (c *Client) Leader(ctx context.Context) (*Leader, error) {
	var l Leader
	if _, err := c.do(ctx, http.MethodGet, "/v1/sys/leader", nil, &l); err != nil {
		return nil, err
	}
	return &l, nil
}
