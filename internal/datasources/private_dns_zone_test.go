// Unit tests for private_dns_zone.go (data source).
//
// dcapi_private_dns_zone lists GET .../vnets/{vnet_id}/dns-zones (bare array) and filters
// by name. Match sets Id "tenant_id/project_id/vnet_id/zone_id". No match is a hard error.
package datasources

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/hashicorp/terraform-plugin-sdk/v2/helper/schema"
	"terraform-provider-dcapi/internal/client"
)

func newTestPrivateDNSZoneDSData(t *testing.T, raw map[string]interface{}) *schema.ResourceData {
	t.Helper()
	return schema.TestResourceDataRaw(t, DataSourcePrivateDNSZone().Schema, raw)
}

// TestDataSourcePrivateDNSZone_Schema pins the Required lookups and Computed outputs.
func TestDataSourcePrivateDNSZone_Schema(t *testing.T) {
	s := DataSourcePrivateDNSZone().Schema

	for _, key := range []string{"tenant_id", "project_id", "vnet_id", "name"} {
		if f := s[key]; f == nil || !f.Required {
			t.Errorf("%s: want a Required lookup field, got %+v", key, f)
		}
	}
	for _, key := range []string{"zone_id", "description", "status", "provider_type",
		"message", "created_at", "updated_at"} {
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

// TestDataSourcePrivateDNSZoneRead_Success verifies the name filter, composite Id, and
// state copy.
func TestDataSourcePrivateDNSZoneRead_Success(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/tenants/acme/projects/infra/vnets/vn-1/dns-zones" {
			t.Errorf("path = %s, want .../vnets/vn-1/dns-zones", r.URL.Path)
		}
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`[
			{"id":"z-other","name":"other.internal"},
			{"id":"z-8","name":"internal.wso2.com","description":"internal zone","status":"ACTIVE",
			 "provider_type":"kubeovn","message":"ok","created_at":"2026-01-01T00:00:00Z",
			 "updated_at":"2026-01-02T00:00:00Z"}
		]`))
	}))
	defer server.Close()

	c, _ := client.NewClient(server.URL, "test-token")
	d := newTestPrivateDNSZoneDSData(t, map[string]interface{}{
		"tenant_id": "acme", "project_id": "infra", "vnet_id": "vn-1", "name": "internal.wso2.com"})

	diags := dataSourcePrivateDnsZoneRead(context.Background(), d, c)
	if diags.HasError() {
		t.Fatalf("dataSourcePrivateDnsZoneRead returned unexpected error diagnostics: %v", diags)
	}
	if got, want := d.Id(), "acme/infra/vn-1/z-8"; got != want {
		t.Errorf("Id() = %q, want %q", got, want)
	}
	if got, want := d.Get("zone_id").(string), "z-8"; got != want {
		t.Errorf("zone_id = %q, want %q", got, want)
	}
	if got, want := d.Get("status").(string), "ACTIVE"; got != want {
		t.Errorf("status = %q, want %q", got, want)
	}
}

// TestDataSourcePrivateDNSZoneRead_NotFound verifies an unmatched name is a hard error.
func TestDataSourcePrivateDNSZoneRead_NotFound(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`[{"id":"z-other","name":"other.internal"}]`))
	}))
	defer server.Close()

	c, _ := client.NewClient(server.URL, "test-token")
	d := newTestPrivateDNSZoneDSData(t, map[string]interface{}{
		"tenant_id": "acme", "project_id": "infra", "vnet_id": "vn-1", "name": "ghost"})

	diags := dataSourcePrivateDnsZoneRead(context.Background(), d, c)
	if !diags.HasError() {
		t.Fatal("diags.HasError() = false, want true for a DNS zone name not in the list")
	}
}

// TestDataSourcePrivateDNSZoneRead_APIError verifies a failed list request surfaces as an error.
func TestDataSourcePrivateDNSZoneRead_APIError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte(`{"error":"boom"}`))
	}))
	defer server.Close()

	c, _ := client.NewClient(server.URL, "test-token")
	d := newTestPrivateDNSZoneDSData(t, map[string]interface{}{
		"tenant_id": "acme", "project_id": "infra", "vnet_id": "vn-1", "name": "internal.wso2.com"})

	diags := dataSourcePrivateDnsZoneRead(context.Background(), d, c)
	if !diags.HasError() {
		t.Fatal("diags.HasError() = false, want true for a 500 response")
	}
}
