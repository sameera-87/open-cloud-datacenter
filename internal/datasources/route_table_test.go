// Unit tests for route_table.go (data source).
//
// dcapi_route_table lists GET .../vnets/{vnet_id}/route-tables (bare array) and filters by
// name. Match sets Id "tenant_id/project_id/vnet_id/route_table_id" and flattens the
// nested routes array into Terraform list blocks. No match is a hard error.
package datasources

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/hashicorp/terraform-plugin-sdk/v2/helper/schema"
	"terraform-provider-dcapi/internal/client"
)

func newTestRouteTableDSData(t *testing.T, raw map[string]interface{}) *schema.ResourceData {
	t.Helper()
	return schema.TestResourceDataRaw(t, DataSourceRouteTable().Schema, raw)
}

// TestDataSourceRouteTable_Schema pins the Required lookups and Computed outputs.
func TestDataSourceRouteTable_Schema(t *testing.T) {
	s := DataSourceRouteTable().Schema

	for _, key := range []string{"tenant_id", "project_id", "vnet_id", "name"} {
		if f := s[key]; f == nil || !f.Required {
			t.Errorf("%s: want a Required lookup field, got %+v", key, f)
		}
	}
	for _, key := range []string{"route_table_id", "description", "routes",
		"status", "provider_type", "created_at", "updated_at"} {
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

// TestDataSourceRouteTableRead_Success verifies the name filter, composite Id, and that
// the nested route entries are flattened into state.
func TestDataSourceRouteTableRead_Success(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/tenants/acme/projects/infra/vnets/vn-1/route-tables" {
			t.Errorf("path = %s, want .../vnets/vn-1/route-tables", r.URL.Path)
		}
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`[
			{"id":"rt-other","name":"other"},
			{"id":"rt-3","name":"egress","description":"default egress","status":"ACTIVE",
			 "provider_type":"kubeovn","created_at":"2026-01-01T00:00:00Z","updated_at":"2026-01-02T00:00:00Z",
			 "routes":[{"name":"default","destination_cidr":"0.0.0.0/0","next_hop_type":"internet","next_hop_ip":""}]}
		]`))
	}))
	defer server.Close()

	c, _ := client.NewClient(server.URL, "test-token")
	d := newTestRouteTableDSData(t, map[string]interface{}{
		"tenant_id": "acme", "project_id": "infra", "vnet_id": "vn-1", "name": "egress"})

	diags := dataSourceRouteTableRead(context.Background(), d, c)
	if diags.HasError() {
		t.Fatalf("dataSourceRouteTableRead returned unexpected error diagnostics: %v", diags)
	}
	if got, want := d.Id(), "acme/infra/vn-1/rt-3"; got != want {
		t.Errorf("Id() = %q, want %q", got, want)
	}
	if got, want := d.Get("route_table_id").(string), "rt-3"; got != want {
		t.Errorf("route_table_id = %q, want %q", got, want)
	}
	routes := d.Get("routes").([]interface{})
	if len(routes) != 1 {
		t.Fatalf("len(routes) = %d, want 1", len(routes))
	}
	r0 := routes[0].(map[string]interface{})
	if r0["destination_cidr"].(string) != "0.0.0.0/0" || r0["next_hop_type"].(string) != "internet" {
		t.Errorf("route[0] = %v, want destination_cidr=0.0.0.0/0 next_hop_type=internet", r0)
	}
}

// TestDataSourceRouteTableRead_NotFound verifies an unmatched name is a hard error.
func TestDataSourceRouteTableRead_NotFound(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`[{"id":"rt-other","name":"other"}]`))
	}))
	defer server.Close()

	c, _ := client.NewClient(server.URL, "test-token")
	d := newTestRouteTableDSData(t, map[string]interface{}{
		"tenant_id": "acme", "project_id": "infra", "vnet_id": "vn-1", "name": "ghost"})

	diags := dataSourceRouteTableRead(context.Background(), d, c)
	if !diags.HasError() {
		t.Fatal("diags.HasError() = false, want true for a route table name not in the list")
	}
}

// TestDataSourceRouteTableRead_APIError verifies a failed list request surfaces as an error.
func TestDataSourceRouteTableRead_APIError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte(`{"error":"boom"}`))
	}))
	defer server.Close()

	c, _ := client.NewClient(server.URL, "test-token")
	d := newTestRouteTableDSData(t, map[string]interface{}{
		"tenant_id": "acme", "project_id": "infra", "vnet_id": "vn-1", "name": "egress"})

	diags := dataSourceRouteTableRead(context.Background(), d, c)
	if !diags.HasError() {
		t.Fatal("diags.HasError() = false, want true for a 500 response")
	}
}
