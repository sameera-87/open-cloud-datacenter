// Unit tests for cluster.go.
//
// Cluster is one of the "large, async" resources: create POSTs, polls the cluster
// to ACTIVE (waitForClusterActive), fetches the kubeconfig, then calls Read. Rather
// than exercise the polling loop over many iterations, the httptest handler below
// returns the terminal status ("ACTIVE") on the very first GET, so the StateChangeConf
// reaches its Target on the first refresh and returns immediately.
//
// See project_test.go for the canonical explanation of schema.TestResourceDataRaw and
// the httptest.NewServer + client.NewClient pattern reused here.
package resources

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-sdk/v2/helper/schema"
	"terraform-provider-dcapi/internal/client"
)

// newTestClusterData builds a *schema.ResourceData for ResourceCluster() from a raw
// config map. Nested blocks (system_pool, worker_pools) follow the TypeList shape:
// a []interface{} of map[string]interface{}.
func newTestClusterData(t *testing.T, raw map[string]interface{}) *schema.ResourceData {
	t.Helper()
	return schema.TestResourceDataRaw(t, ResourceCluster().Schema, raw)
}

// TestResourceCluster_Schema asserts the schema invariants the rest of cluster.go
// relies on. Every cluster field is immutable (no update endpoint), so the required
// identity fields and the system_pool block must all be ForceNew.
func TestResourceCluster_Schema(t *testing.T) {
	r := ResourceCluster()
	s := r.Schema

	// Cluster has no in-place update: the resource must expose no UpdateContext.
	if r.UpdateContext != nil {
		t.Error("ResourceCluster: UpdateContext is set, want nil (clusters are immutable)")
	}

	requiredForceNew := []string{"tenant_id", "project_id", "name", "k8s_version", "image_name", "system_pool"}
	for _, key := range requiredForceNew {
		f := s[key]
		if f == nil {
			t.Fatalf("schema is missing field %q", key)
		}
		if !f.Required {
			t.Errorf("%s: Required = false, want true", key)
		}
		if !f.ForceNew {
			t.Errorf("%s: ForceNew = false, want true (clusters are immutable)", key)
		}
	}

	if f := s["worker_pools"]; f == nil || !f.Optional || !f.ForceNew {
		t.Errorf("worker_pools: want Optional+ForceNew, got %#v", f)
	}

	computed := []string{"cluster_id", "status", "provider_type", "worker_pool_count", "total_node_count", "message", "created_at", "kubeconfig"}
	for _, key := range computed {
		f := s[key]
		if f == nil {
			t.Fatalf("schema is missing field %q", key)
		}
		if !f.Computed {
			t.Errorf("%s: Computed = false, want true", key)
		}
	}

	if !s["kubeconfig"].Sensitive {
		t.Error("kubeconfig: Sensitive = false, want true")
	}
}

// clusterActiveBody is a GET /clusters/{id} response in the terminal ACTIVE state, so
// waitForClusterActive finishes on its first poll.
const clusterActiveBody = `{
	"id": "c-123",
	"name": "demo",
	"status": "ACTIVE",
	"tenant_id": "acme",
	"provider_type": "rancher",
	"system_pool": {"id":"sp-1","role":"system","size":"medium","count":3,"disk_gb":40,"status":"ready"},
	"worker_pool_count": 1,
	"total_node_count": 4,
	"message": "ready",
	"created_at": "2026-01-01T00:00:00Z"
}`

// TestResourceClusterCreate_Success drives the full create path: POST returns the
// {"resource":{...}} envelope, the first GET reports ACTIVE so polling completes, and
// the kubeconfig endpoint returns YAML. The composite ID must be tenant/project/cluster_id.
func TestResourceClusterCreate_Success(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/v1/tenants/acme/projects/infra/clusters":
			w.WriteHeader(http.StatusAccepted)
			w.Write([]byte(`{"resource":{
				"id":"c-123","name":"demo","status":"ACTIVE","tenant_id":"acme",
				"provider_type":"rancher","worker_pool_count":1,"total_node_count":4,
				"message":"accepted","created_at":"2026-01-01T00:00:00Z"
			},"note":"provisioning"}`))
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/kubeconfig"):
			w.WriteHeader(http.StatusOK)
			w.Write([]byte("apiVersion: v1\nkind: Config\n"))
		case r.Method == http.MethodGet:
			w.WriteHeader(http.StatusOK)
			w.Write([]byte(clusterActiveBody))
		default:
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
		}
	}))
	defer server.Close()

	c, err := client.NewClient(server.URL, "test-token")
	if err != nil {
		t.Fatalf("client.NewClient: %v", err)
	}

	d := newTestClusterData(t, map[string]interface{}{
		"tenant_id":   "acme",
		"project_id":  "infra",
		"name":        "demo",
		"k8s_version": "v1.33.10+rke2r3",
		"image_name":  "rancher-infra/ubuntu-22-04",
		"system_pool": []interface{}{
			map[string]interface{}{"size": "medium", "count": 3, "disk_gb": 40},
		},
	})

	diags := resourceClusterCreate(context.Background(), d, c)
	if diags.HasError() {
		t.Fatalf("resourceClusterCreate returned unexpected error diagnostics: %v", diags)
	}
	if got, want := d.Id(), "acme/infra/c-123"; got != want {
		t.Errorf("Id() = %q, want %q", got, want)
	}
	if got, want := d.Get("cluster_id").(string), "c-123"; got != want {
		t.Errorf("cluster_id = %q, want %q", got, want)
	}
	if got, want := d.Get("status").(string), "ACTIVE"; got != want {
		t.Errorf("status = %q, want %q", got, want)
	}
	if got := d.Get("kubeconfig").(string); !strings.Contains(got, "kind: Config") {
		t.Errorf("kubeconfig = %q, want it to contain the fetched YAML", got)
	}
}

// TestResourceClusterCreate_APIError verifies a POST failure surfaces as an error and
// never sets an ID.
func TestResourceClusterCreate_APIError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		w.Write([]byte(`{"error":"invalid k8s_version"}`))
	}))
	defer server.Close()

	c, _ := client.NewClient(server.URL, "test-token")
	d := newTestClusterData(t, map[string]interface{}{
		"tenant_id":   "acme",
		"project_id":  "infra",
		"name":        "demo",
		"k8s_version": "bogus",
		"image_name":  "rancher-infra/ubuntu-22-04",
		"system_pool": []interface{}{
			map[string]interface{}{"size": "medium", "count": 3},
		},
	})

	diags := resourceClusterCreate(context.Background(), d, c)
	if !diags.HasError() {
		t.Fatal("diags.HasError() = false, want true for a failed create")
	}
	if d.Id() != "" {
		t.Errorf("Id() = %q, want empty string when create fails", d.Id())
	}
}

// TestResourceClusterCreate_NetworkMutualExclusion verifies the pure-logic validation
// that runs before any HTTP call: network_name and vnet_id/subnet_id are exclusive.
func TestResourceClusterCreate_NetworkMutualExclusion(t *testing.T) {
	c, _ := client.NewClient("http://unused.invalid", "test-token")
	d := newTestClusterData(t, map[string]interface{}{
		"tenant_id":    "acme",
		"project_id":   "infra",
		"name":         "demo",
		"k8s_version":  "v1.33.10+rke2r3",
		"image_name":   "rancher-infra/ubuntu-22-04",
		"network_name": "iaas/vm-network-001",
		"vnet_id":      "vnet-1",
		"system_pool": []interface{}{
			map[string]interface{}{"size": "medium", "count": 3},
		},
	})

	diags := resourceClusterCreate(context.Background(), d, c)
	if !diags.HasError() {
		t.Fatal("diags.HasError() = false, want true when network_name and vnet_id are both set")
	}
}

// TestResourceClusterRead_Success verifies a GET refreshes fields and keeps the ID.
func TestResourceClusterRead_Success(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/kubeconfig") {
			w.WriteHeader(http.StatusOK)
			w.Write([]byte("apiVersion: v1\n"))
			return
		}
		if r.URL.Path != "/v1/tenants/acme/projects/infra/clusters/c-123" {
			t.Errorf("path = %s, want /v1/tenants/acme/projects/infra/clusters/c-123", r.URL.Path)
		}
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(clusterActiveBody))
	}))
	defer server.Close()

	c, _ := client.NewClient(server.URL, "test-token")
	d := newTestClusterData(t, map[string]interface{}{})
	d.SetId("acme/infra/c-123")

	diags := resourceClusterRead(context.Background(), d, c)
	if diags.HasError() {
		t.Fatalf("resourceClusterRead returned unexpected error diagnostics: %v", diags)
	}
	if d.Id() != "acme/infra/c-123" {
		t.Errorf("Id() = %q, want unchanged", d.Id())
	}
	if got, want := d.Get("total_node_count").(int), 4; got != want {
		t.Errorf("total_node_count = %d, want %d", got, want)
	}
	sp := d.Get("system_pool").([]interface{})
	if len(sp) != 1 {
		t.Fatalf("system_pool length = %d, want 1", len(sp))
	}
	if got := sp[0].(map[string]interface{})["count"].(int); got != 3 {
		t.Errorf("system_pool.count = %d, want 3", got)
	}
}

// TestResourceClusterRead_NotFound verifies a 404 clears the ID with no error.
func TestResourceClusterRead_NotFound(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		w.Write([]byte(`{"error":"not found"}`))
	}))
	defer server.Close()

	c, _ := client.NewClient(server.URL, "test-token")
	d := newTestClusterData(t, map[string]interface{}{})
	d.SetId("acme/infra/c-123")

	diags := resourceClusterRead(context.Background(), d, c)
	if diags.HasError() {
		t.Fatalf("resourceClusterRead returned unexpected error diagnostics on 404: %v", diags)
	}
	if d.Id() != "" {
		t.Errorf("Id() = %q, want empty string after a 404", d.Id())
	}
}

// TestResourceClusterRead_InvalidID verifies a malformed state ID (not 3 parts) errors.
func TestResourceClusterRead_InvalidID(t *testing.T) {
	c, _ := client.NewClient("http://unused.invalid", "test-token")
	d := newTestClusterData(t, map[string]interface{}{})
	d.SetId("acme/infra") // only two parts

	diags := resourceClusterRead(context.Background(), d, c)
	if !diags.HasError() {
		t.Fatal("diags.HasError() = false, want true for a malformed state ID")
	}
}

// TestResourceClusterDelete_Success verifies DELETE then a 404 on the poll clears the ID.
func TestResourceClusterDelete_Success(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodDelete:
			w.WriteHeader(http.StatusAccepted)
		case http.MethodGet:
			// waitForClusterDeleted polls until 404 == DELETED.
			w.WriteHeader(http.StatusNotFound)
		default:
			t.Errorf("unexpected method %s", r.Method)
		}
	}))
	defer server.Close()

	c, _ := client.NewClient(server.URL, "test-token")
	d := newTestClusterData(t, map[string]interface{}{})
	d.SetId("acme/infra/c-123")

	diags := resourceClusterDelete(context.Background(), d, c)
	if diags.HasError() {
		t.Fatalf("resourceClusterDelete returned unexpected error diagnostics: %v", diags)
	}
}

// TestResourceClusterDelete_Error verifies a non-404 DELETE failure surfaces an error
// and leaves the ID in place.
func TestResourceClusterDelete_Error(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte(`{"error":"backend failure"}`))
	}))
	defer server.Close()

	c, _ := client.NewClient(server.URL, "test-token")
	d := newTestClusterData(t, map[string]interface{}{})
	d.SetId("acme/infra/c-123")

	diags := resourceClusterDelete(context.Background(), d, c)
	if !diags.HasError() {
		t.Fatal("diags.HasError() = false, want true for a failed delete")
	}
	if d.Id() == "" {
		t.Error("Id() = \"\", want it to remain set since delete failed")
	}
}
