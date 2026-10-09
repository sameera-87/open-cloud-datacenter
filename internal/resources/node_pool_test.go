// Unit tests for node_pool.go.
//
// Node pool is an async resource WITH an update endpoint (node_count/taints/labels are
// mutable). Create POSTs a bare NodePoolResponse (no "resource" wrapper), polls to
// "ready", then Reads; Update PATCHes, polls, then Reads. The httptest handlers return
// status "ready" on the first GET so waitForNodePoolReady completes on its first poll.
//
// See project_test.go for the canonical TestResourceDataRaw / httptest explanation.
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

func newTestNodePoolData(t *testing.T, raw map[string]interface{}) *schema.ResourceData {
	t.Helper()
	return schema.TestResourceDataRaw(t, ResourceNodePool().Schema, raw)
}

// TestResourceNodePool_Schema checks identity fields are immutable, node_count is
// updatable (not ForceNew), and the resource exposes an UpdateContext.
func TestResourceNodePool_Schema(t *testing.T) {
	r := ResourceNodePool()
	s := r.Schema

	if r.UpdateContext == nil {
		t.Error("ResourceNodePool: UpdateContext is nil, want set (node pools are scalable)")
	}

	requiredForceNew := []string{"tenant_id", "project_id", "cluster_id", "name", "size"}
	for _, key := range requiredForceNew {
		f := s[key]
		if f == nil {
			t.Fatalf("schema is missing field %q", key)
		}
		if !f.Required || !f.ForceNew {
			t.Errorf("%s: Required=%v ForceNew=%v, want both true", key, f.Required, f.ForceNew)
		}
	}

	// node_count must be updatable in place — ForceNew would force a recreate on scale.
	if f := s["node_count"]; f == nil || !f.Required || f.ForceNew {
		t.Errorf("node_count: want Required+not-ForceNew, got %#v", f)
	}
	// taints/labels are updatable too (full-replace on PATCH).
	for _, key := range []string{"taints", "labels"} {
		if f := s[key]; f == nil || !f.Optional || f.ForceNew {
			t.Errorf("%s: want Optional+not-ForceNew, got %#v", key, f)
		}
	}

	for _, key := range []string{"node_pool_id", "role", "status", "message", "created_at"} {
		if f := s[key]; f == nil || !f.Computed {
			t.Errorf("%s: want Computed, got %#v", key, f)
		}
	}
}

const nodePoolReadyBody = `{
	"id":"np-123","name":"workers","role":"worker","size":"medium","count":3,
	"disk_gb":40,"image_name":"","taints":[],"labels":{"team":"infra"},
	"status":"ready","message":"ready","created_at":"2026-01-01T00:00:00Z"
}`

// TestResourceNodePoolCreate_Success drives create: POST returns the bare pool body,
// the first GET reports "ready" so polling completes, then Read syncs state. The
// composite ID must be tenant/project/cluster/pool_name.
func TestResourceNodePoolCreate_Success(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/v1/tenants/acme/projects/infra/clusters/c-1/node-pools":
			w.WriteHeader(http.StatusAccepted)
			w.Write([]byte(nodePoolReadyBody))
		case r.Method == http.MethodGet && r.URL.Path == "/v1/tenants/acme/projects/infra/clusters/c-1/node-pools/workers":
			w.WriteHeader(http.StatusOK)
			w.Write([]byte(nodePoolReadyBody))
		default:
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
		}
	}))
	defer server.Close()

	c, err := client.NewClient(server.URL, "test-token")
	if err != nil {
		t.Fatalf("client.NewClient: %v", err)
	}

	d := newTestNodePoolData(t, map[string]interface{}{
		"tenant_id":  "acme",
		"project_id": "infra",
		"cluster_id": "c-1",
		"name":       "workers",
		"size":       "medium",
		"node_count": 3,
	})

	diags := resourceNodePoolCreate(context.Background(), d, c)
	if diags.HasError() {
		t.Fatalf("resourceNodePoolCreate returned unexpected error diagnostics: %v", diags)
	}
	if got, want := d.Id(), "acme/infra/c-1/workers"; got != want {
		t.Errorf("Id() = %q, want %q", got, want)
	}
	if got, want := d.Get("node_pool_id").(string), "np-123"; got != want {
		t.Errorf("node_pool_id = %q, want %q", got, want)
	}
	if got, want := d.Get("status").(string), "ready"; got != want {
		t.Errorf("status = %q, want %q", got, want)
	}
}

// TestResourceNodePoolCreate_APIError verifies a POST failure errors and sets no ID.
func TestResourceNodePoolCreate_APIError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		w.Write([]byte(`{"error":"pool name reserved"}`))
	}))
	defer server.Close()

	c, _ := client.NewClient(server.URL, "test-token")
	d := newTestNodePoolData(t, map[string]interface{}{
		"tenant_id":  "acme",
		"project_id": "infra",
		"cluster_id": "c-1",
		"name":       "system",
		"size":       "medium",
		"node_count": 3,
	})

	diags := resourceNodePoolCreate(context.Background(), d, c)
	if !diags.HasError() {
		t.Fatal("diags.HasError() = false, want true for a failed create")
	}
	if d.Id() != "" {
		t.Errorf("Id() = %q, want empty string when create fails", d.Id())
	}
}

// TestResourceNodePoolRead_Success verifies a GET refreshes fields, including the
// flattened labels map, and keeps the ID.
func TestResourceNodePoolRead_Success(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/tenants/acme/projects/infra/clusters/c-1/node-pools/workers" {
			t.Errorf("unexpected path %s", r.URL.Path)
		}
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(nodePoolReadyBody))
	}))
	defer server.Close()

	c, _ := client.NewClient(server.URL, "test-token")
	d := newTestNodePoolData(t, map[string]interface{}{})
	d.SetId("acme/infra/c-1/workers")

	diags := resourceNodePoolRead(context.Background(), d, c)
	if diags.HasError() {
		t.Fatalf("resourceNodePoolRead returned unexpected error diagnostics: %v", diags)
	}
	if got, want := d.Get("node_count").(int), 3; got != want {
		t.Errorf("node_count = %d, want %d", got, want)
	}
	labels := d.Get("labels").(map[string]interface{})
	if labels["team"] != "infra" {
		t.Errorf("labels[team] = %v, want infra", labels["team"])
	}
}

// TestResourceNodePoolRead_NotFound verifies a 404 clears the ID with no error.
func TestResourceNodePoolRead_NotFound(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer server.Close()

	c, _ := client.NewClient(server.URL, "test-token")
	d := newTestNodePoolData(t, map[string]interface{}{})
	d.SetId("acme/infra/c-1/workers")

	diags := resourceNodePoolRead(context.Background(), d, c)
	if diags.HasError() {
		t.Fatalf("unexpected error diagnostics on 404: %v", diags)
	}
	if d.Id() != "" {
		t.Errorf("Id() = %q, want empty string after a 404", d.Id())
	}
}

// TestResourceNodePoolRead_InvalidID verifies a state ID without exactly 4 parts errors.
func TestResourceNodePoolRead_InvalidID(t *testing.T) {
	c, _ := client.NewClient("http://unused.invalid", "test-token")
	d := newTestNodePoolData(t, map[string]interface{}{})
	d.SetId("acme/infra/c-1") // only three parts

	diags := resourceNodePoolRead(context.Background(), d, c)
	if !diags.HasError() {
		t.Fatal("diags.HasError() = false, want true for a malformed state ID")
	}
}

// TestResourceNodePoolUpdate_Success drives the scale path: PATCH succeeds, the first
// GET reports "ready" so polling completes, then Read syncs. We also assert the PATCH
// body carried the new count.
func TestResourceNodePoolUpdate_Success(t *testing.T) {
	var gotBody string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodPatch:
			buf := make([]byte, r.ContentLength)
			r.Body.Read(buf)
			gotBody = string(buf)
			w.WriteHeader(http.StatusOK)
			w.Write([]byte(nodePoolReadyBody))
		case http.MethodGet:
			w.WriteHeader(http.StatusOK)
			w.Write([]byte(nodePoolReadyBody))
		default:
			t.Errorf("unexpected method %s", r.Method)
		}
	}))
	defer server.Close()

	c, _ := client.NewClient(server.URL, "test-token")
	d := newTestNodePoolData(t, map[string]interface{}{
		"tenant_id":  "acme",
		"project_id": "infra",
		"cluster_id": "c-1",
		"name":       "workers",
		"size":       "medium",
		"node_count": 5,
	})
	d.SetId("acme/infra/c-1/workers")

	diags := resourceNodePoolUpdate(context.Background(), d, c)
	if diags.HasError() {
		t.Fatalf("resourceNodePoolUpdate returned unexpected error diagnostics: %v", diags)
	}
	if !strings.Contains(gotBody, `"count":5`) {
		t.Errorf("PATCH body = %q, want it to contain count:5", gotBody)
	}
}

// TestResourceNodePoolDelete_Success verifies DELETE then a 404 poll clears the ID.
func TestResourceNodePoolDelete_Success(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodDelete:
			w.WriteHeader(http.StatusAccepted)
		case http.MethodGet:
			w.WriteHeader(http.StatusNotFound)
		default:
			t.Errorf("unexpected method %s", r.Method)
		}
	}))
	defer server.Close()

	c, _ := client.NewClient(server.URL, "test-token")
	d := newTestNodePoolData(t, map[string]interface{}{})
	d.SetId("acme/infra/c-1/workers")

	diags := resourceNodePoolDelete(context.Background(), d, c)
	if diags.HasError() {
		t.Fatalf("resourceNodePoolDelete returned unexpected error diagnostics: %v", diags)
	}
}

// TestResourceNodePoolDelete_Error verifies a non-404 DELETE failure errors and keeps the ID.
func TestResourceNodePoolDelete_Error(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()

	c, _ := client.NewClient(server.URL, "test-token")
	d := newTestNodePoolData(t, map[string]interface{}{})
	d.SetId("acme/infra/c-1/workers")

	diags := resourceNodePoolDelete(context.Background(), d, c)
	if !diags.HasError() {
		t.Fatal("diags.HasError() = false, want true for a failed delete")
	}
	if d.Id() == "" {
		t.Error("Id() = \"\", want it to remain set since delete failed")
	}
}
