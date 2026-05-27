package render_test

import (
	"testing"

	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	keyvaultv1alpha1 "github.com/wso2/keyvault-operator/api/v1alpha1"
	"github.com/wso2/keyvault-operator/internal/controller/render"
)

// testCR returns a Backend CR shaped like what dc-api would create:
// a per-tenant namespace, the seven dc-api.wso2.com/* labels stamped
// at creation time, and a sensible default capacity.
func testCR() *keyvaultv1alpha1.KeyVaultBackend {
	return &keyvaultv1alpha1.KeyVaultBackend{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "kvb-acme",
			Namespace: "dc-tenant-acme",
			Labels: map[string]string{
				"dc-api.wso2.com/tenant":        "acme",
				"dc-api.wso2.com/tenant-uuid":   "abcd1234-0000-0000-0000-000000000001",
				"dc-api.wso2.com/resource-uuid": "abcd1234-0000-0000-0000-000000000002",
				"dc-api.wso2.com/resource-kind": "keyvault-backend",
				"dc-api.wso2.com/resource-name": "acme",
				// Unrelated label that should NOT propagate.
				"my.private.label": "should-stay-here",
			},
		},
		Spec: keyvaultv1alpha1.KeyVaultBackendSpec{
			CPU:       resource.MustParse("3"),
			MemoryGB:  6,
			StorageGB: 4,
			EngineConfig: keyvaultv1alpha1.BackendEngineConfig{
				HAReplicas:   3,
				StorageClass: "longhorn",
			},
		},
	}
}

func TestStatefulSet_BasicShape(t *testing.T) {
	cr := testCR()
	sts := render.StatefulSet(cr)

	if sts.Name != "kvb-acme" {
		t.Errorf("Name: got %q want kvb-acme", sts.Name)
	}
	if sts.Namespace != "dc-tenant-acme" {
		t.Errorf("Namespace: got %q want dc-tenant-acme", sts.Namespace)
	}
	if *sts.Spec.Replicas != 3 {
		t.Errorf("Replicas: got %d want 3", *sts.Spec.Replicas)
	}
	if sts.Spec.ServiceName != "kvb-acme-internal" {
		t.Errorf("ServiceName: got %q want kvb-acme-internal", sts.Spec.ServiceName)
	}

	// Per-pod resources = total / replicas = 3 CPU / 3 = 1 CPU; 6 GiB / 3 = 2 GiB.
	c := sts.Spec.Template.Spec.Containers[0]
	if got := c.Resources.Requests.Cpu().String(); got != "1" {
		t.Errorf("per-pod CPU: got %q want 1", got)
	}
	if got := c.Resources.Requests.Memory().String(); got != "2Gi" {
		t.Errorf("per-pod memory: got %q want 2Gi", got)
	}
	if got := c.Resources.Limits.Cpu().String(); got != "1" {
		t.Errorf("per-pod CPU limit: got %q want 1", got)
	}

	// PVC size = spec.storageGB = 4Gi.
	pvc := sts.Spec.VolumeClaimTemplates[0]
	if got := pvc.Spec.Resources.Requests.Storage().String(); got != "4Gi" {
		t.Errorf("PVC size: got %q want 4Gi", got)
	}
	if *pvc.Spec.StorageClassName != "longhorn" {
		t.Errorf("StorageClass: got %q want longhorn", *pvc.Spec.StorageClassName)
	}

	// Pod labels include dc-api propagation + helm-style identifiers.
	pl := sts.Spec.Template.ObjectMeta.Labels
	if pl["dc-api.wso2.com/tenant"] != "acme" {
		t.Errorf("dc-api/tenant label not propagated to pod template; got %v", pl)
	}
	if pl["my.private.label"] != "" {
		t.Errorf("non-dc-api label leaked to pod template: %v", pl)
	}
	if pl["app.kubernetes.io/name"] != "openbao" {
		t.Errorf("missing app.kubernetes.io/name=openbao; got %v", pl)
	}
}

func TestServices_SelectorsAndIPs(t *testing.T) {
	cr := testCR()

	internal := render.InternalService(cr)
	if internal.Spec.ClusterIP != "None" {
		t.Errorf("InternalService ClusterIP: got %q want None (headless)", internal.Spec.ClusterIP)
	}
	if !internal.Spec.PublishNotReadyAddresses {
		t.Error("InternalService must set publishNotReadyAddresses=true so unsealed pods are discoverable")
	}

	active := render.ActiveService(cr)
	if active.Spec.Selector["openbao-active"] != "true" {
		t.Errorf("ActiveService selector missing openbao-active=true; got %v", active.Spec.Selector)
	}
	if active.Spec.ClusterIP == "None" {
		t.Error("ActiveService must be a regular ClusterIP, not headless")
	}

	standby := render.StandbyService(cr)
	if standby.Spec.Selector["openbao-standby"] != "true" {
		t.Errorf("StandbyService selector missing openbao-standby=true; got %v", standby.Spec.Selector)
	}
}

func TestConfigMap_HCL(t *testing.T) {
	cr := testCR()
	cm := render.ConfigMap(cr)

	hcl := cm.Data["extraconfig-from-values.hcl"]
	wants := []string{
		`ui = true`,
		`listener "tcp"`,
		`address = "[::]:8200"`,
		`cluster_address = "[::]:8201"`,
		`storage "raft"`,
		`path = "/openbao/data"`,
		`service_registration "kubernetes"`,
	}
	for _, w := range wants {
		if !contains(hcl, w) {
			t.Errorf("ConfigMap HCL missing %q; got:\n%s", w, hcl)
		}
	}
}

func TestLabels_OnlyDCAPILabelsPropagate(t *testing.T) {
	cr := testCR()
	all := render.AllLabels(cr)
	if all["dc-api.wso2.com/tenant"] != "acme" {
		t.Errorf("dc-api tenant label not propagated to common labels: %v", all)
	}
	if _, present := all["my.private.label"]; present {
		t.Errorf("non-dc-api label leaked into common labels: %v", all)
	}
	// app.kubernetes.io/* must be present.
	if all["app.kubernetes.io/name"] != "openbao" {
		t.Errorf("workload identifier missing: %v", all)
	}
}

func TestPodPatchRole_ScopedToNamespaceAndPods(t *testing.T) {
	cr := testCR()
	role := render.PodPatchRole(cr)

	if role.Namespace != "dc-tenant-acme" {
		t.Errorf("Role.Namespace: got %q want dc-tenant-acme (must be namespace-scoped)", role.Namespace)
	}
	if len(role.Rules) != 1 {
		t.Fatalf("Role.Rules: got %d want 1", len(role.Rules))
	}
	rule := role.Rules[0]
	if len(rule.Resources) != 1 || rule.Resources[0] != "pods" {
		t.Errorf("Role.Rules[0].Resources: got %v want [pods] (no other resources allowed)", rule.Resources)
	}
	for _, v := range rule.Verbs {
		if v != "get" && v != "patch" && v != "update" {
			t.Errorf("Role.Rules[0].Verbs has unexpected %q (only get/patch/update permitted)", v)
		}
	}

	rb := render.PodPatchRoleBinding(cr)
	if len(rb.Subjects) != 1 || rb.Subjects[0].Name != render.ServiceAccountName(cr) {
		t.Errorf("RoleBinding subjects: got %+v want a single subject for the Backend SA", rb.Subjects)
	}
	if rb.RoleRef.Name != role.Name {
		t.Errorf("RoleBinding.RoleRef.Name: got %q want %q (must reference our Role)", rb.RoleRef.Name, role.Name)
	}
}

func TestStatefulSet_FloorsTinySpecs(t *testing.T) {
	cr := testCR()
	cr.Spec.CPU = resource.MustParse("100m") // total
	cr.Spec.MemoryGB = 0                     // total
	sts := render.StatefulSet(cr)
	c := sts.Spec.Template.Spec.Containers[0]
	// 100m / 3 replicas = 33m, floored to 100m per pod
	if got := c.Resources.Requests.Cpu().MilliValue(); got != 100 {
		t.Errorf("CPU floor: got %dm want 100m per pod", got)
	}
	// 0 GiB / 3 → max(0, 1) = 1 GiB per pod
	if got := c.Resources.Requests.Memory().String(); got != "1Gi" {
		t.Errorf("memory floor: got %q want 1Gi", got)
	}
}

func contains(haystack, needle string) bool {
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] == needle {
			return true
		}
	}
	return false
}
