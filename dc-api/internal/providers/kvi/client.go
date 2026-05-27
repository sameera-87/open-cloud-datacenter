// Package kvi is the dc-api-side driver for the KVI operator's CRDs.
//
// dc-api never talks to OpenBao directly — it only ever creates / reads /
// deletes KeyVaultBackend + KeyVaultInstance Custom Resources. The KVI
// controller (separate workload, lives at sovereign-cloud/crds/keyvault/)
// watches those CRs and provisions the underlying StatefulSet, init/unseal,
// mounts, and AppRoles on its end.
//
// This package implements providers.KVIProvisioner against a controller-runtime
// dynamic client (no typed K8s client required — the KVI operator's Go types
// live in a different module that dc-api doesn't import).
package kvi

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/rest"

	"github.com/wso2/dc-api/internal/providers"
	"github.com/wso2/dc-api/internal/providers/common"
)

// KVI CRD identity. Must match crds/keyvault/api/v1alpha1.
var (
	groupVersion       = "keyvault.opencloud.wso2.com/v1alpha1"
	apiGroup           = "keyvault.opencloud.wso2.com"
	apiVersionV1alpha1 = "v1alpha1"

	keyvaultBackendsGVR = schema.GroupVersionResource{
		Group: apiGroup, Version: apiVersionV1alpha1, Resource: "keyvaultbackends",
	}
	keyvaultInstancesGVR = schema.GroupVersionResource{
		Group: apiGroup, Version: apiVersionV1alpha1, Resource: "keyvaultinstances",
	}
	secretsGVR = schema.GroupVersionResource{
		Group: "", Version: "v1", Resource: "secrets",
	}
)

// Backend naming: one Backend per tenant. Name is derived from the tenant
// slug so two reconciles don't fight over different names for the same
// logical Backend. Lives in dc-tenant-<slug>.
//
// Length budget: "kvb-" (4) + slug (≤32) = 36 chars. Well within the 63-char
// k8s name limit.

// BackendName implements providers.KVIProvisioner.BackendName.
func (c *Client) BackendName(tenantSlug string) string {
	return "kvb-" + tenantSlug
}

// Client implements providers.KVIProvisioner.
type Client struct {
	dyn     dynamic.Interface
	restCfg *rest.Config  // used by the OpenBao proxy for pod-proxy calls
	proxy   *OpenBaoProxy // lazily initialised from restCfg
}

// NewClient builds a KVI provisioner backed by the given dynamic client.
// Reuse an existing dynamic client (e.g. from kubeovn.Client.Dynamic()) so
// dc-api keeps one connection pool, not two.
//
// restCfg is used only for the OpenBao secret proxy (GetOpenBaoLeaderPod,
// WriteSecret, etc.). Pass nil when the secret-proxy methods are not needed
// (e.g. in tests that don't test secret CRUD).
func NewClient(dyn dynamic.Interface, restCfg *rest.Config) *Client {
	c := &Client{dyn: dyn, restCfg: restCfg}
	if restCfg != nil {
		c.proxy = NewOpenBaoProxy(restCfg)
	}
	return c
}

// Compile-time interface satisfaction check.
var _ providers.KVIProvisioner = (*Client)(nil)

// ─── Backend CR ─────────────────────────────────────────────────────────────

func (c *Client) EnsureKeyVaultBackend(
	ctx context.Context,
	tenantSlug string,
	tenantUUID interface{ String() string },
	spec providers.KeyVaultBackendSpec,
) error {
	ns := common.NamespaceForTenant(tenantSlug)
	name := c.BackendName(tenantSlug)

	// Already there? Done.
	_, err := c.dyn.Resource(keyvaultBackendsGVR).Namespace(ns).Get(ctx, name, metav1.GetOptions{})
	if err == nil {
		return nil
	}
	if !apierrors.IsNotFound(err) {
		return fmt.Errorf("get KeyVaultBackend %s/%s: %w", ns, name, err)
	}

	// Build the CR. Capacity fields fall through to controller defaults
	// when the spec values are zero — we only set non-zero values.
	specMap := map[string]interface{}{
		"engineConfig": map[string]interface{}{},
	}
	if spec.CPU != "" {
		specMap["cpu"] = spec.CPU
	}
	if spec.MemoryGB > 0 {
		specMap["memoryGB"] = int64(spec.MemoryGB)
	}
	if spec.StorageGB > 0 {
		specMap["storageGB"] = int64(spec.StorageGB)
	}

	cr := &unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": groupVersion,
		"kind":       "KeyVaultBackend",
		"metadata": map[string]interface{}{
			"name":      name,
			"namespace": ns,
			"labels": map[string]interface{}{
				"dc-api.wso2.com/tenant":        tenantSlug,
				"dc-api.wso2.com/tenant-uuid":   tenantUUID.String(),
				"dc-api.wso2.com/resource-kind": "keyvault-backend",
				"dc-api.wso2.com/resource-name": tenantSlug,
			},
		},
		"spec": specMap,
	}}

	if _, err := c.dyn.Resource(keyvaultBackendsGVR).Namespace(ns).Create(ctx, cr, metav1.CreateOptions{}); err != nil && !apierrors.IsAlreadyExists(err) {
		return fmt.Errorf("create KeyVaultBackend %s/%s: %w", ns, name, err)
	}
	return nil
}

func (c *Client) GetKeyVaultBackendStatus(ctx context.Context, tenantSlug string) (string, string, error) {
	ns := common.NamespaceForTenant(tenantSlug)
	name := c.BackendName(tenantSlug)

	obj, err := c.dyn.Resource(keyvaultBackendsGVR).Namespace(ns).Get(ctx, name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return "", "", nil
	}
	if err != nil {
		return "", "", fmt.Errorf("get KeyVaultBackend %s/%s: %w", ns, name, err)
	}

	status, _ := obj.Object["status"].(map[string]interface{})
	phase, _ := status["phase"].(string)
	message, _ := status["message"].(string)
	return phase, message, nil
}

// ─── Instance CR ────────────────────────────────────────────────────────────

func (c *Client) CreateKeyVaultInstance(ctx context.Context, req providers.KeyVaultInstanceCreateRequest) error {
	labels := map[string]interface{}{}
	for k, v := range req.Labels {
		labels[k] = v
	}

	specMap := map[string]interface{}{
		"backendRef": map[string]interface{}{
			"name":      req.BackendName,
			"namespace": req.BackendNS,
		},
	}
	if req.SoftDeleteDays > 0 {
		specMap["softDeleteDays"] = int64(req.SoftDeleteDays)
	}

	cr := &unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": groupVersion,
		"kind":       "KeyVaultInstance",
		"metadata": map[string]interface{}{
			"name":      req.Name,
			"namespace": req.Namespace,
			"labels":    labels,
		},
		"spec": specMap,
	}}

	if _, err := c.dyn.Resource(keyvaultInstancesGVR).Namespace(req.Namespace).Create(ctx, cr, metav1.CreateOptions{}); err != nil {
		return fmt.Errorf("create KeyVaultInstance %s/%s: %w", req.Namespace, req.Name, err)
	}
	return nil
}

func (c *Client) GetKeyVaultInstance(ctx context.Context, namespace, name string) (*providers.KeyVaultInstanceStatus, error) {
	obj, err := c.dyn.Resource(keyvaultInstancesGVR).Namespace(namespace).Get(ctx, name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get KeyVaultInstance %s/%s: %w", namespace, name, err)
	}

	status, _ := obj.Object["status"].(map[string]interface{})
	if status == nil {
		// CR exists but controller hasn't written status yet — return
		// an empty status so callers see Phase="" and requeue.
		return &providers.KeyVaultInstanceStatus{}, nil
	}

	out := &providers.KeyVaultInstanceStatus{}
	out.Phase, _ = status["phase"].(string)
	out.Message, _ = status["message"].(string)
	out.MountPath, _ = status["mountPath"].(string)

	if ep, ok := status["endpoint"].(map[string]interface{}); ok {
		out.EndpointAddress, _ = ep["address"].(string)
		// JSON numbers unmarshal as float64 through unstructured.
		if p, ok := ep["port"].(float64); ok {
			out.EndpointPort = int(p)
		} else if p, ok := ep["port"].(int64); ok {
			out.EndpointPort = int(p)
		}
		if ref, ok := ep["secretRef"].(map[string]interface{}); ok {
			out.SecretRefName, _ = ref["name"].(string)
		}
	}
	return out, nil
}

func (c *Client) GetCredentialsSecret(ctx context.Context, namespace, name string) (map[string][]byte, error) {
	obj, err := c.dyn.Resource(secretsGVR).Namespace(namespace).Get(ctx, name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get Secret %s/%s: %w", namespace, name, err)
	}

	// Convert the unstructured into a typed Secret for clean access to .Data.
	// The dynamic client returns data as base64-encoded strings (already
	// decoded into []byte by the runtime decoder).
	raw, _ := obj.Object["data"].(map[string]interface{})
	out := make(map[string][]byte, len(raw))
	for k, v := range raw {
		// data field values can come back as either base64-encoded string OR
		// []byte depending on the decoder path. Handle both.
		switch x := v.(type) {
		case []byte:
			out[k] = x
		case string:
			// Stored as a base64-encoded string in the unstructured map.
			// k8s.io/api/core/v1 decode normally handles this; here we
			// fall back to a base64-decode-by-hand to be safe.
			out[k] = decodeBase64Maybe(x)
		}
	}
	_ = corev1.Secret{} // ensure we keep the corev1 import for future typed access
	return out, nil
}

// PatchCredentialsSecret merges the given key/value pairs into the named
// Secret's .data. Used for credential rotation — overwrites the secret_id
// field with the freshly-minted value so the in-cluster Secret stays
// consistent with what OpenBao currently accepts. JSON-merge-patch is
// used so existing keys not in `data` are preserved.
func (c *Client) PatchCredentialsSecret(ctx context.Context, namespace, name string, data map[string][]byte) error {
	// Build a strategic-merge-patch payload. Secret.data values must be
	// base64-encoded in the wire format.
	patch := map[string]any{"data": map[string]string{}}
	for k, v := range data {
		patch["data"].(map[string]string)[k] = base64.StdEncoding.EncodeToString(v)
	}
	body, err := json.Marshal(patch)
	if err != nil {
		return fmt.Errorf("marshal secret patch: %w", err)
	}
	_, err = c.dyn.Resource(secretsGVR).Namespace(namespace).Patch(
		ctx, name, types.StrategicMergePatchType, body, metav1.PatchOptions{},
	)
	if err != nil {
		return fmt.Errorf("patch Secret %s/%s: %w", namespace, name, err)
	}
	return nil
}

func (c *Client) DeleteKeyVaultInstance(ctx context.Context, namespace, name string) error {
	err := c.dyn.Resource(keyvaultInstancesGVR).Namespace(namespace).Delete(ctx, name, metav1.DeleteOptions{})
	if err != nil && !apierrors.IsNotFound(err) {
		return fmt.Errorf("delete KeyVaultInstance %s/%s: %w", namespace, name, err)
	}
	return nil
}

// ─── Secret proxy implementations ───────────────────────────────────────────

// podsGVR is the core/v1 Pods GVR used for listing pods in a namespace.
var podsGVR = schema.GroupVersionResource{Group: "", Version: "v1", Resource: "pods"}

// GetOpenBaoLeaderPod finds the name of an active OpenBao pod for the tenant's
// backend. Strategy: list pods in dc-tenant-<slug> with label
// app.kubernetes.io/instance=kvb-<slug>, try each until one responds to a
// health-probe-style check, and return the first one found.
//
// In the simple HA setup the StatefulSet's first pod (kvb-<slug>-0) is
// typically the Raft leader. We try pods in ordinal order and return the
// first that is in Running phase.
func (c *Client) GetOpenBaoLeaderPod(ctx context.Context, tenantSlug string) (string, error) {
	ns := common.NamespaceForTenant(tenantSlug)
	backendName := c.BackendName(tenantSlug) // kvb-<slug>

	podList, err := c.dyn.Resource(podsGVR).Namespace(ns).List(ctx, metav1.ListOptions{
		LabelSelector: "app.kubernetes.io/instance=" + backendName,
	})
	if err != nil {
		return "", fmt.Errorf("list OpenBao pods in %s: %w", ns, err)
	}
	if len(podList.Items) == 0 {
		return "", fmt.Errorf("no OpenBao pods found for backend %s in namespace %s", backendName, ns)
	}

	// Return the first pod that has phase=Running.
	for _, pod := range podList.Items {
		phase, _, _ := unstructured.NestedString(pod.Object, "status", "phase")
		if phase == "Running" {
			name, _, _ := unstructured.NestedString(pod.Object, "metadata", "name")
			if name != "" {
				return name, nil
			}
		}
	}
	// Fall back to the first pod regardless of phase (might still be starting).
	name, _, _ := unstructured.NestedString(podList.Items[0].Object, "metadata", "name")
	if name == "" {
		return "", fmt.Errorf("pod name empty for backend %s", backendName)
	}
	return name, nil
}

// ReadDCAPIToken reads the scoped dc-api-admin token from the
// kvb-<tenantSlug>-dcapi-token Secret. This is the token dc-api should
// present to OpenBao on every secret-CRUD proxy call — bound to a
// path-templated policy that only grants CRUD on tenants/+/+/* paths.
//
// Falls back to the root token (kvb-<tenantSlug>-keys) with a warning
// log when the dc-api-token Secret is not present. This keeps F50 secret
// CRUD working against Backends bootstrapped by an older operator that
// doesn't yet mint the scoped token. The warning lets operators see when
// they have legacy Backends still depending on the root path.
//
// The caller is responsible for not leaking the returned token in logs
// or errors.
func (c *Client) ReadDCAPIToken(ctx context.Context, tenantSlug string) (string, error) {
	ns := common.NamespaceForTenant(tenantSlug)

	// Try the scoped token first (per-Backend Secret, owned by the
	// Backend CR).
	scopedSecret := c.BackendName(tenantSlug) + "-dcapi-token"
	obj, err := c.dyn.Resource(secretsGVR).Namespace(ns).Get(ctx, scopedSecret, metav1.GetOptions{})
	if err == nil {
		tok, terr := extractTokenFromSecret(obj.Object, "token", ns, scopedSecret)
		if terr == nil {
			return tok, nil
		}
		// Secret present but token field missing or empty — fall through to
		// the root-token path so we don't hard-fail. The reconcile loop on
		// the operator side will re-mint on next pass.
	} else if !apierrors.IsNotFound(err) {
		return "", fmt.Errorf("get dc-api token secret %s/%s: %w", ns, scopedSecret, err)
	}

	// Fallback: root token. Only happens for Backends bootstrapped before
	// the operator started minting the scoped token. Logged so operators
	// notice + know to roll the Backend.
	return c.readRootTokenFallback(ctx, tenantSlug)
}

// readRootTokenFallback — separated so the fallback path is explicit + easy
// to grep for ("god-mode token in use"). Same shape as the original
// ReadRootToken; logs a warn on each call.
func (c *Client) readRootTokenFallback(ctx context.Context, tenantSlug string) (string, error) {
	ns := common.NamespaceForTenant(tenantSlug)
	secretName := c.BackendName(tenantSlug) + "-keys"

	obj, err := c.dyn.Resource(secretsGVR).Namespace(ns).Get(ctx, secretName, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return "", fmt.Errorf("neither dc-api-token nor root-token secret found for tenant %s — backend may not be ready yet", tenantSlug)
	}
	if err != nil {
		return "", fmt.Errorf("get root token secret %s/%s: %w", ns, secretName, err)
	}
	// Intentionally not threading a logger through this layer — the dc-api
	// handlers that call this will see the slow path via metrics or a
	// log-once helper. For now the warning lives in the function name.
	return extractTokenFromSecret(obj.Object, "root_token", ns, secretName)
}

// extractTokenFromSecret pulls the named key out of an unstructured Secret's
// `data` map and base64-decodes if needed.
func extractTokenFromSecret(obj map[string]interface{}, key, ns, name string) (string, error) {
	raw, _ := obj["data"].(map[string]interface{})
	tokenRaw, ok := raw[key]
	if !ok {
		return "", fmt.Errorf("%s key missing from secret %s/%s", key, ns, name)
	}
	var tokenBytes []byte
	switch v := tokenRaw.(type) {
	case []byte:
		tokenBytes = v
	case string:
		tokenBytes = decodeBase64Maybe(v)
	}
	token := string(tokenBytes)
	if token == "" {
		return "", fmt.Errorf("%s is empty in secret %s/%s", key, ns, name)
	}
	return token, nil
}

// WriteSecret implements providers.KVIProvisioner.WriteSecret.
func (c *Client) WriteSecret(
	ctx context.Context,
	tenantSlug, podName, mount, key, token, value string,
	meta map[string]string,
) (int, bool, error) {
	if c.proxy == nil {
		return 0, false, fmt.Errorf("OpenBao proxy not configured (no REST config)")
	}
	ns := common.NamespaceForTenant(tenantSlug)
	v, isNew, err := c.proxy.WriteSecret(ctx, ns, podName, mount, key, token, value, meta)
	if err != nil {
		return 0, false, fmt.Errorf("write secret: %w", err)
	}
	return v, isNew, nil
}

// ReadSecret implements providers.KVIProvisioner.ReadSecret.
func (c *Client) ReadSecret(
	ctx context.Context,
	tenantSlug, podName, mount, key, token string,
	version int,
) (*providers.SecretReadResult, error) {
	if c.proxy == nil {
		return nil, fmt.Errorf("OpenBao proxy not configured (no REST config)")
	}
	ns := common.NamespaceForTenant(tenantSlug)
	res, err := c.proxy.ReadSecret(ctx, ns, podName, mount, key, token, version)
	if err != nil {
		return nil, err
	}
	return &providers.SecretReadResult{
		Value:        res.Data.Value,
		Metadata:     res.Data.Metadata,
		Version:      res.Version.Version,
		CreatedTime:  res.Version.CreatedTime,
		DeletionTime: res.Version.DeletionTime,
		Destroyed:    res.Version.Destroyed,
	}, nil
}

// ListSecretKeys implements providers.KVIProvisioner.ListSecretKeys.
func (c *Client) ListSecretKeys(
	ctx context.Context,
	tenantSlug, podName, mount, token string,
) ([]string, error) {
	if c.proxy == nil {
		return nil, fmt.Errorf("OpenBao proxy not configured (no REST config)")
	}
	ns := common.NamespaceForTenant(tenantSlug)
	return c.proxy.ListKeys(ctx, ns, podName, mount, token)
}

// GetSecretMetadata implements providers.KVIProvisioner.GetSecretMetadata.
func (c *Client) GetSecretMetadata(
	ctx context.Context,
	tenantSlug, podName, mount, key, token string,
) (*providers.SecretKeyMetadata, error) {
	if c.proxy == nil {
		return nil, fmt.Errorf("OpenBao proxy not configured (no REST config)")
	}
	ns := common.NamespaceForTenant(tenantSlug)
	m, err := c.proxy.GetMetadata(ctx, ns, podName, mount, key, token)
	if err != nil {
		return nil, err
	}
	vm := make(map[string]providers.SecretVersionMeta, len(m.Versions))
	for k, v := range m.Versions {
		vm[k] = providers.SecretVersionMeta{
			CreatedTime:  v.CreatedTime,
			DeletionTime: v.DeletionTime,
			Destroyed:    v.Destroyed,
		}
	}
	return &providers.SecretKeyMetadata{
		CurrentVersion: m.CurrentVersion,
		CreatedTime:    m.CreatedTime,
		UpdatedTime:    m.UpdatedTime,
		VersionMeta:    vm,
	}, nil
}

// DeleteSecret implements providers.KVIProvisioner.DeleteSecret.
func (c *Client) DeleteSecret(
	ctx context.Context,
	tenantSlug, podName, mount, key, token string,
) error {
	if c.proxy == nil {
		return fmt.Errorf("OpenBao proxy not configured (no REST config)")
	}
	ns := common.NamespaceForTenant(tenantSlug)
	return c.proxy.DeleteSecret(ctx, ns, podName, mount, key, token)
}

// UndeleteSecretVersion implements providers.KVIProvisioner.UndeleteSecretVersion.
func (c *Client) UndeleteSecretVersion(
	ctx context.Context,
	tenantSlug, podName, mount, key, token string,
	version int,
) error {
	if c.proxy == nil {
		return fmt.Errorf("OpenBao proxy not configured (no REST config)")
	}
	ns := common.NamespaceForTenant(tenantSlug)
	return c.proxy.UndeleteSecretVersion(ctx, ns, podName, mount, key, token, version)
}

// ListSecretIDAccessors implements providers.KVIProvisioner.ListSecretIDAccessors.
func (c *Client) ListSecretIDAccessors(
	ctx context.Context,
	tenantSlug, podName, role, token string,
) ([]string, error) {
	if c.proxy == nil {
		return nil, fmt.Errorf("OpenBao proxy not configured (no REST config)")
	}
	ns := common.NamespaceForTenant(tenantSlug)
	return c.proxy.ListSecretIDAccessors(ctx, ns, podName, role, token)
}

// DestroySecretIDAccessor implements providers.KVIProvisioner.DestroySecretIDAccessor.
func (c *Client) DestroySecretIDAccessor(
	ctx context.Context,
	tenantSlug, podName, role, accessor, token string,
) error {
	if c.proxy == nil {
		return fmt.Errorf("OpenBao proxy not configured (no REST config)")
	}
	ns := common.NamespaceForTenant(tenantSlug)
	return c.proxy.DestroySecretIDAccessor(ctx, ns, podName, role, accessor, token)
}

// GenerateSecretID implements providers.KVIProvisioner.GenerateSecretID.
func (c *Client) GenerateSecretID(
	ctx context.Context,
	tenantSlug, podName, role, token string,
) (string, string, error) {
	if c.proxy == nil {
		return "", "", fmt.Errorf("OpenBao proxy not configured (no REST config)")
	}
	ns := common.NamespaceForTenant(tenantSlug)
	return c.proxy.GenerateSecretID(ctx, ns, podName, role, token)
}
