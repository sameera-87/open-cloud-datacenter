// Unit tests for tenant.go (data source).
//
// dcapi_tenant has no GET-by-id endpoint upstream, so GetTenantByID lists GET /v1/tenants
// (a bare JSON array) and scans client-side for the matching slug. The data source's Id
// is the bare tenant slug. A lookup that matches nothing is a hard error.
package datasources

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/hashicorp/terraform-plugin-sdk/v2/helper/schema"
	"terraform-provider-dcapi/internal/client"
)

func newTestTenantDSData(t *testing.T, raw map[string]interface{}) *schema.ResourceData {
	t.Helper()
	return schema.TestResourceDataRaw(t, DataSourceTenant().Schema, raw)
}

// TestDataSourceTenant_Schema pins "id" as the sole Required lookup argument and the
// rest as Computed state.
func TestDataSourceTenant_Schema(t *testing.T) {
	s := DataSourceTenant().Schema

	if f := s["id"]; f == nil || !f.Required {
		t.Errorf("id: want a Required lookup field, got %+v", f)
	}

	computed := []string{"name", "description", "cpu_cores_cap", "memory_gb_cap",
		"storage_gb_cap", "tenant_uuid", "asgardeo_group", "created_at", "created_by", "roles"}
	for _, key := range computed {
		f := s[key]
		if f == nil {
			t.Fatalf("schema is missing field %q", key)
		}
		if !f.Computed {
			t.Errorf("%s: Computed = false, want true", key)
		}
		if f.Required || f.Optional {
			t.Errorf("%s: Required=%v Optional=%v, want both false", key, f.Required, f.Optional)
		}
	}
}

// TestDataSourceTenantRead_Success verifies the list-and-scan finds the matching tenant,
// sets Id to its slug, and copies scalar + list (roles) fields into state.
func TestDataSourceTenantRead_Success(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/tenants" {
			t.Errorf("path = %s, want /v1/tenants", r.URL.Path)
		}
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`[
			{"id":"other","name":"Other"},
			{"id":"acme","name":"Acme","description":"desc","cpu_cores_cap":100,"memory_gb_cap":256,
			 "storage_gb_cap":1000,"tenant_uuid":"22222222-2222-2222-2222-222222222222",
			 "asgardeo_group":"dc-tenant-acme","created_at":"2026-01-01T00:00:00Z",
			 "created_by":"admin@example.com","roles":["owner","member"]}
		]`))
	}))
	defer server.Close()

	c, _ := client.NewClient(server.URL, "test-token")
	d := newTestTenantDSData(t, map[string]interface{}{"id": "acme"})

	diags := dataSourceTenantRead(context.Background(), d, c)
	if diags.HasError() {
		t.Fatalf("dataSourceTenantRead returned unexpected error diagnostics: %v", diags)
	}
	if got, want := d.Id(), "acme"; got != want {
		t.Errorf("Id() = %q, want %q", got, want)
	}
	if got, want := d.Get("asgardeo_group").(string), "dc-tenant-acme"; got != want {
		t.Errorf("asgardeo_group = %q, want %q", got, want)
	}
	if got, want := d.Get("cpu_cores_cap").(int), 100; got != want {
		t.Errorf("cpu_cores_cap = %d, want %d", got, want)
	}
	roles := d.Get("roles").([]interface{})
	if len(roles) != 2 || roles[0].(string) != "owner" {
		t.Errorf("roles = %v, want [owner member]", roles)
	}
}

// TestDataSourceTenantRead_NotFound verifies a slug that isn't in the list is a hard
// error diagnostic.
func TestDataSourceTenantRead_NotFound(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`[{"id":"other","name":"Other"}]`))
	}))
	defer server.Close()

	c, _ := client.NewClient(server.URL, "test-token")
	d := newTestTenantDSData(t, map[string]interface{}{"id": "ghost"})

	diags := dataSourceTenantRead(context.Background(), d, c)
	if !diags.HasError() {
		t.Fatal("diags.HasError() = false, want true for a tenant slug not present in the list")
	}
}

// TestDataSourceTenantRead_APIError verifies a failed list request surfaces as an error.
func TestDataSourceTenantRead_APIError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte(`{"error":"boom"}`))
	}))
	defer server.Close()

	c, _ := client.NewClient(server.URL, "test-token")
	d := newTestTenantDSData(t, map[string]interface{}{"id": "acme"})

	diags := dataSourceTenantRead(context.Background(), d, c)
	if !diags.HasError() {
		t.Fatal("diags.HasError() = false, want true for a 500 response")
	}
}
