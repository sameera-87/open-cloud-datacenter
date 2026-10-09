// Unit tests for subnet.go (data source).
//
// dcapi_subnet lists GET .../vnets/{vnet_id}/subnets (bare array) and filters by name.
// Match sets a four-part Id "tenant_id/project_id/vnet_id/subnet_uuid". No match is a
// hard error.
package datasources

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/hashicorp/terraform-plugin-sdk/v2/helper/schema"
	"terraform-provider-dcapi/internal/client"
)

func newTestSubnetDSData(t *testing.T, raw map[string]interface{}) *schema.ResourceData {
	t.Helper()
	return schema.TestResourceDataRaw(t, DataSourceSubnet().Schema, raw)
}

// TestDataSourceSubnet_Schema pins the four Required lookups and the Computed outputs.
func TestDataSourceSubnet_Schema(t *testing.T) {
	s := DataSourceSubnet().Schema

	for _, key := range []string{"tenant_id", "project_id", "vnet_id", "name"} {
		if f := s[key]; f == nil || !f.Required {
			t.Errorf("%s: want a Required lookup field, got %+v", key, f)
		}
	}
	for _, key := range []string{"subnet_uuid", "cidr", "gateway", "description",
		"status", "provider_type", "message", "created_at", "updated_at"} {
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

// TestDataSourceSubnetRead_Success verifies the name filter and composite Id/state copy.
func TestDataSourceSubnetRead_Success(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/tenants/acme/projects/infra/vnets/vn-1/subnets" {
			t.Errorf("path = %s, want .../vnets/vn-1/subnets", r.URL.Path)
		}
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`[
			{"id":"sn-other","name":"other"},
			{"id":"sn-9","name":"web","cidr":"10.1.1.0/24","gateway":"10.1.1.1",
			 "description":"web tier","status":"ACTIVE","provider_type":"kubeovn",
			 "message":"ok","created_at":"2026-01-01T00:00:00Z","updated_at":"2026-01-02T00:00:00Z"}
		]`))
	}))
	defer server.Close()

	c, _ := client.NewClient(server.URL, "test-token")
	d := newTestSubnetDSData(t, map[string]interface{}{
		"tenant_id": "acme", "project_id": "infra", "vnet_id": "vn-1", "name": "web"})

	diags := dataSourceSubnetRead(context.Background(), d, c)
	if diags.HasError() {
		t.Fatalf("dataSourceSubnetRead returned unexpected error diagnostics: %v", diags)
	}
	if got, want := d.Id(), "acme/infra/vn-1/sn-9"; got != want {
		t.Errorf("Id() = %q, want %q", got, want)
	}
	if got, want := d.Get("subnet_uuid").(string), "sn-9"; got != want {
		t.Errorf("subnet_uuid = %q, want %q", got, want)
	}
	if got, want := d.Get("cidr").(string), "10.1.1.0/24"; got != want {
		t.Errorf("cidr = %q, want %q", got, want)
	}
}

// TestDataSourceSubnetRead_NotFound verifies an unmatched name is a hard error.
func TestDataSourceSubnetRead_NotFound(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`[{"id":"sn-other","name":"other"}]`))
	}))
	defer server.Close()

	c, _ := client.NewClient(server.URL, "test-token")
	d := newTestSubnetDSData(t, map[string]interface{}{
		"tenant_id": "acme", "project_id": "infra", "vnet_id": "vn-1", "name": "ghost"})

	diags := dataSourceSubnetRead(context.Background(), d, c)
	if !diags.HasError() {
		t.Fatal("diags.HasError() = false, want true for a subnet name not in the list")
	}
}

// TestDataSourceSubnetRead_APIError verifies a failed list request surfaces as an error.
func TestDataSourceSubnetRead_APIError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte(`{"error":"boom"}`))
	}))
	defer server.Close()

	c, _ := client.NewClient(server.URL, "test-token")
	d := newTestSubnetDSData(t, map[string]interface{}{
		"tenant_id": "acme", "project_id": "infra", "vnet_id": "vn-1", "name": "web"})

	diags := dataSourceSubnetRead(context.Background(), d, c)
	if !diags.HasError() {
		t.Fatal("diags.HasError() = false, want true for a 500 response")
	}
}
