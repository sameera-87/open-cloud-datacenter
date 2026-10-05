// Unit tests for project.go (data source).
//
// Data sources are read-only: the only lifecycle func is dataSourceProjectRead. Each
// test here follows the same shape as the resource tests — a httptest.Server stands in
// for DC-API, client.NewClient points a real *client.DCAPIClient at it, and we call the
// Read func directly and assert on the resulting *schema.ResourceData.
//
// dcapi_project resolves directly via GetProjectByID (GET .../projects/{id}), so there
// is no List+filter — the composite Id is "tenant_id/project_id".
package datasources

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/hashicorp/terraform-plugin-sdk/v2/helper/schema"
	"terraform-provider-dcapi/internal/client"
)

func newTestProjectDSData(t *testing.T, raw map[string]interface{}) *schema.ResourceData {
	t.Helper()
	return schema.TestResourceDataRaw(t, DataSourceProject().Schema, raw)
}

// TestDataSourceProject_Schema pins the lookup/exposed split: tenant_id and project_id
// are the Required lookup keys a user supplies, and everything else is Computed state
// the API fills in. If a Computed attribute ever gained Required/Optional, Terraform
// would start demanding it in config — this guards that.
func TestDataSourceProject_Schema(t *testing.T) {
	s := DataSourceProject().Schema

	for _, key := range []string{"tenant_id", "project_id"} {
		f := s[key]
		if f == nil {
			t.Fatalf("schema is missing lookup field %q", key)
		}
		if !f.Required {
			t.Errorf("%s: Required = false, want true (lookup argument)", key)
		}
	}

	computed := []string{"name", "description", "cpu_cores", "memory_gb", "storage_gb",
		"max_vnets", "max_clusters", "max_volumes", "max_public_ips",
		"project_uuid", "tenant_uuid", "created_at", "updated_at", "created_by"}
	for _, key := range computed {
		f := s[key]
		if f == nil {
			t.Fatalf("schema is missing field %q", key)
		}
		if !f.Computed {
			t.Errorf("%s: Computed = false, want true", key)
		}
		if f.Required || f.Optional {
			t.Errorf("%s: Required=%v Optional=%v, want both false (API-only field)", key, f.Required, f.Optional)
		}
	}
}

// TestDataSourceProjectRead_Success verifies a found project sets the composite Id and
// copies the API fields into state.
func TestDataSourceProjectRead_Success(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/tenants/acme/projects/infra" {
			t.Errorf("path = %s, want /v1/tenants/acme/projects/infra", r.URL.Path)
		}
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{
			"id": "infra",
			"tenant_id": "acme",
			"name": "Infra",
			"description": "core infra",
			"cpu_cores": 8,
			"memory_gb": 32,
			"storage_gb": 200,
			"max_vnets": 10,
			"project_uuid": "11111111-1111-1111-1111-111111111111",
			"tenant_uuid": "22222222-2222-2222-2222-222222222222",
			"created_at": "2026-01-01T00:00:00Z",
			"created_by": "user@example.com"
		}`))
	}))
	defer server.Close()

	c, _ := client.NewClient(server.URL, "test-token")
	d := newTestProjectDSData(t, map[string]interface{}{"tenant_id": "acme", "project_id": "infra"})

	diags := dataSourceProjectRead(context.Background(), d, c)
	if diags.HasError() {
		t.Fatalf("dataSourceProjectRead returned unexpected error diagnostics: %v", diags)
	}
	if got, want := d.Id(), "acme/infra"; got != want {
		t.Errorf("Id() = %q, want %q", got, want)
	}
	if got, want := d.Get("name").(string), "Infra"; got != want {
		t.Errorf("name = %q, want %q", got, want)
	}
	if got, want := d.Get("cpu_cores").(int), 8; got != want {
		t.Errorf("cpu_cores = %d, want %d", got, want)
	}
	if got, want := d.Get("project_uuid").(string), "11111111-1111-1111-1111-111111111111"; got != want {
		t.Errorf("project_uuid = %q, want %q", got, want)
	}
}

// TestDataSourceProjectRead_NotFound verifies that a 404 (which GetProjectByID turns
// into a nil project) is surfaced as an error diagnostic — unlike a managed resource's
// Read, a data source lookup that finds nothing is a hard error, not silent drift.
func TestDataSourceProjectRead_NotFound(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		w.Write([]byte(`{"error":"project not found"}`))
	}))
	defer server.Close()

	c, _ := client.NewClient(server.URL, "test-token")
	d := newTestProjectDSData(t, map[string]interface{}{"tenant_id": "acme", "project_id": "ghost"})

	diags := dataSourceProjectRead(context.Background(), d, c)
	if !diags.HasError() {
		t.Fatal("diags.HasError() = false, want true for a project that doesn't exist")
	}
}

// TestDataSourceProjectRead_APIError verifies a non-404 API failure is also surfaced
// as an error diagnostic.
func TestDataSourceProjectRead_APIError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte(`{"error":"boom"}`))
	}))
	defer server.Close()

	c, _ := client.NewClient(server.URL, "test-token")
	d := newTestProjectDSData(t, map[string]interface{}{"tenant_id": "acme", "project_id": "infra"})

	diags := dataSourceProjectRead(context.Background(), d, c)
	if !diags.HasError() {
		t.Fatal("diags.HasError() = false, want true for a 500 response")
	}
}
