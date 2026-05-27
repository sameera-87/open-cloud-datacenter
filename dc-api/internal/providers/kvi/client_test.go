package kvi_test

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"

	"github.com/wso2/dc-api/internal/providers"
	"github.com/wso2/dc-api/internal/providers/kvi"
)

// newFakeDyn builds a dynamic-client fake registered for the KVI GVRs +
// core Secret. List shapes are "<Kind>List" by convention.
func newFakeDyn() *dynamicfake.FakeDynamicClient {
	scheme := runtime.NewScheme()
	listKinds := map[schema.GroupVersionResource]string{
		{Group: "keyvault.opencloud.wso2.com", Version: "v1alpha1", Resource: "keyvaultbackends"}:  "KeyVaultBackendList",
		{Group: "keyvault.opencloud.wso2.com", Version: "v1alpha1", Resource: "keyvaultinstances"}: "KeyVaultInstanceList",
		{Group: "", Version: "v1", Resource: "secrets"}:                                            "SecretList",
	}
	return dynamicfake.NewSimpleDynamicClientWithCustomListKinds(scheme, listKinds)
}

func TestEnsureKeyVaultBackend_CreatesCorrectShape(t *testing.T) {
	dyn := newFakeDyn()
	c := kvi.NewClient(dyn, nil)

	tuuid := uuid.MustParse("11111111-2222-3333-4444-555555555555")
	if err := c.EnsureKeyVaultBackend(context.Background(), "acme", tuuid, providers.KeyVaultBackendSpec{
		CPU: "2", MemoryGB: 4, StorageGB: 5,
	}); err != nil {
		t.Fatalf("EnsureKeyVaultBackend: %v", err)
	}

	gvr := schema.GroupVersionResource{Group: "keyvault.opencloud.wso2.com", Version: "v1alpha1", Resource: "keyvaultbackends"}
	got, err := dyn.Resource(gvr).Namespace("dc-tenant-acme").Get(context.Background(), "kvb-acme", metav1Get())
	if err != nil {
		t.Fatalf("Get Backend: %v", err)
	}

	// kind + namespace + name
	if got.GetKind() != "KeyVaultBackend" {
		t.Errorf("Kind: got %q want KeyVaultBackend", got.GetKind())
	}
	if got.GetNamespace() != "dc-tenant-acme" {
		t.Errorf("Namespace: got %q want dc-tenant-acme", got.GetNamespace())
	}
	if got.GetName() != "kvb-acme" {
		t.Errorf("Name: got %q want kvb-acme", got.GetName())
	}

	// dc-api labels
	want := map[string]string{
		"dc-api.wso2.com/tenant":        "acme",
		"dc-api.wso2.com/tenant-uuid":   tuuid.String(),
		"dc-api.wso2.com/resource-kind": "keyvault-backend",
		"dc-api.wso2.com/resource-name": "acme",
	}
	for k, v := range want {
		if got.GetLabels()[k] != v {
			t.Errorf("label %s: got %q want %q", k, got.GetLabels()[k], v)
		}
	}

	// spec fields
	cpu, _, _ := unstructured.NestedString(got.Object, "spec", "cpu")
	if cpu != "2" {
		t.Errorf("spec.cpu: got %q want 2", cpu)
	}
	mem, _, _ := unstructured.NestedInt64(got.Object, "spec", "memoryGB")
	if mem != 4 {
		t.Errorf("spec.memoryGB: got %d want 4", mem)
	}
	stor, _, _ := unstructured.NestedInt64(got.Object, "spec", "storageGB")
	if stor != 5 {
		t.Errorf("spec.storageGB: got %d want 5", stor)
	}

	// Second Ensure is a no-op (idempotency).
	if err := c.EnsureKeyVaultBackend(context.Background(), "acme", tuuid, providers.KeyVaultBackendSpec{}); err != nil {
		t.Fatalf("second EnsureKeyVaultBackend: %v", err)
	}
}

func TestCreateKeyVaultInstance_ShapeAndLabels(t *testing.T) {
	dyn := newFakeDyn()
	c := kvi.NewClient(dyn, nil)

	req := providers.KeyVaultInstanceCreateRequest{
		Name:           "kv-abc12345",
		Namespace:      "dc-acme-prod",
		Labels:         map[string]string{"dc-api.wso2.com/tenant": "acme", "dc-api.wso2.com/resource-uuid": "abc12345-..."},
		BackendName:    "kvb-acme",
		BackendNS:      "dc-tenant-acme",
		SoftDeleteDays: 14,
	}
	if err := c.CreateKeyVaultInstance(context.Background(), req); err != nil {
		t.Fatalf("CreateKeyVaultInstance: %v", err)
	}

	gvr := schema.GroupVersionResource{Group: "keyvault.opencloud.wso2.com", Version: "v1alpha1", Resource: "keyvaultinstances"}
	got, err := dyn.Resource(gvr).Namespace("dc-acme-prod").Get(context.Background(), "kv-abc12345", metav1Get())
	if err != nil {
		t.Fatalf("Get KVI: %v", err)
	}
	if got.GetKind() != "KeyVaultInstance" {
		t.Errorf("Kind: got %q want KeyVaultInstance", got.GetKind())
	}
	if got.GetLabels()["dc-api.wso2.com/resource-uuid"] != "abc12345-..." {
		t.Errorf("label propagation broken: %v", got.GetLabels())
	}
	bn, _, _ := unstructured.NestedString(got.Object, "spec", "backendRef", "name")
	if bn != "kvb-acme" {
		t.Errorf("spec.backendRef.name: got %q want kvb-acme", bn)
	}
	bns, _, _ := unstructured.NestedString(got.Object, "spec", "backendRef", "namespace")
	if bns != "dc-tenant-acme" {
		t.Errorf("spec.backendRef.namespace: got %q want dc-tenant-acme", bns)
	}
	sdd, _, _ := unstructured.NestedInt64(got.Object, "spec", "softDeleteDays")
	if sdd != 14 {
		t.Errorf("spec.softDeleteDays: got %d want 14", sdd)
	}
}

func TestGetKeyVaultInstance_ReadsStatus(t *testing.T) {
	dyn := newFakeDyn()
	c := kvi.NewClient(dyn, nil)

	// Seed a CR with a populated status block — what the operator would write.
	gvr := schema.GroupVersionResource{Group: "keyvault.opencloud.wso2.com", Version: "v1alpha1", Resource: "keyvaultinstances"}
	cr := &unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": "keyvault.opencloud.wso2.com/v1alpha1",
		"kind":       "KeyVaultInstance",
		"metadata":   map[string]interface{}{"name": "kv-x", "namespace": "dc-acme-prod"},
		"spec":       map[string]interface{}{"backendRef": map[string]interface{}{"name": "kvb-acme", "namespace": "dc-tenant-acme"}},
		"status": map[string]interface{}{
			"phase":     "Ready",
			"message":   "AppRole provisioned",
			"mountPath": "tenants/uuid1/uuid2",
			"endpoint": map[string]interface{}{
				"address":   "kvb-acme-active.dc-tenant-acme.svc.cluster.local",
				"port":      float64(8200),
				"secretRef": map[string]interface{}{"name": "keyvault-abc-creds"},
			},
		},
	}}
	if _, err := dyn.Resource(gvr).Namespace("dc-acme-prod").Create(context.Background(), cr, metav1Create()); err != nil {
		t.Fatalf("seed CR: %v", err)
	}

	got, err := c.GetKeyVaultInstance(context.Background(), "dc-acme-prod", "kv-x")
	if err != nil {
		t.Fatalf("GetKeyVaultInstance: %v", err)
	}
	if got.Phase != "Ready" {
		t.Errorf("Phase: got %q want Ready", got.Phase)
	}
	if got.MountPath != "tenants/uuid1/uuid2" {
		t.Errorf("MountPath: got %q", got.MountPath)
	}
	if got.EndpointAddress != "kvb-acme-active.dc-tenant-acme.svc.cluster.local" {
		t.Errorf("EndpointAddress: got %q", got.EndpointAddress)
	}
	if got.EndpointPort != 8200 {
		t.Errorf("EndpointPort: got %d", got.EndpointPort)
	}
	if got.SecretRefName != "keyvault-abc-creds" {
		t.Errorf("SecretRefName: got %q", got.SecretRefName)
	}

	// Non-existent CR returns (nil, nil).
	missing, err := c.GetKeyVaultInstance(context.Background(), "dc-acme-prod", "kv-nope")
	if err != nil {
		t.Fatalf("missing CR err: %v", err)
	}
	if missing != nil {
		t.Errorf("missing CR: got %+v want nil", missing)
	}
}
