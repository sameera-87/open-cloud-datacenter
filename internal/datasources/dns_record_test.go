// Unit tests for dns_record.go (data source).
//
// dcapi_dns_record lists GET .../dns-zones/{zone_id}/records (bare array) and filters on
// (name, type) together — a zone can hold same-named records of different types, so name
// alone is not a unique key. Match sets Id "tenant_id/project_id/vnet_id/zone_id/record_id".
// No match is a hard error.
package datasources

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/hashicorp/terraform-plugin-sdk/v2/helper/schema"
	"terraform-provider-dcapi/internal/client"
)

func newTestDNSRecordDSData(t *testing.T, raw map[string]interface{}) *schema.ResourceData {
	t.Helper()
	return schema.TestResourceDataRaw(t, DataSourceDNSRecord().Schema, raw)
}

// TestDataSourceDNSRecord_Schema pins the Required lookups (note type is part of the
// lookup identity, not just name) and the Computed outputs.
func TestDataSourceDNSRecord_Schema(t *testing.T) {
	s := DataSourceDNSRecord().Schema

	for _, key := range []string{"tenant_id", "project_id", "vnet_id", "zone_id", "name", "type"} {
		if f := s[key]; f == nil || !f.Required {
			t.Errorf("%s: want a Required lookup field, got %+v", key, f)
		}
	}
	for _, key := range []string{"record_id", "values", "ttl", "created_at"} {
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

// TestDataSourceDNSRecordRead_Success verifies the (name,type) filter: two records share
// the name "api", so only the one whose type also matches must be selected.
func TestDataSourceDNSRecordRead_Success(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/tenants/acme/projects/infra/vnets/vn-1/dns-zones/z-8/records" {
			t.Errorf("path = %s, want .../dns-zones/z-8/records", r.URL.Path)
		}
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`[
			{"id":"rec-cname","name":"api","type":"CNAME","values":["lb.internal"],"ttl":60,"created_at":"2026-01-01T00:00:00Z"},
			{"id":"rec-a","name":"api","type":"A","values":["10.1.1.5","10.1.1.6"],"ttl":300,"created_at":"2026-01-02T00:00:00Z"}
		]`))
	}))
	defer server.Close()

	c, _ := client.NewClient(server.URL, "test-token")
	d := newTestDNSRecordDSData(t, map[string]interface{}{
		"tenant_id": "acme", "project_id": "infra", "vnet_id": "vn-1", "zone_id": "z-8",
		"name": "api", "type": "A"})

	diags := dataSourceDnsRecordRead(context.Background(), d, c)
	if diags.HasError() {
		t.Fatalf("dataSourceDnsRecordRead returned unexpected error diagnostics: %v", diags)
	}
	if got, want := d.Id(), "acme/infra/vn-1/z-8/rec-a"; got != want {
		t.Errorf("Id() = %q, want %q (must pick the A record, not the CNAME)", got, want)
	}
	if got, want := d.Get("record_id").(string), "rec-a"; got != want {
		t.Errorf("record_id = %q, want %q", got, want)
	}
	if got, want := d.Get("ttl").(int), 300; got != want {
		t.Errorf("ttl = %d, want %d", got, want)
	}
	values := d.Get("values").([]interface{})
	if len(values) != 2 || values[0].(string) != "10.1.1.5" {
		t.Errorf("values = %v, want [10.1.1.5 10.1.1.6]", values)
	}
}

// TestDataSourceDNSRecordRead_NotFound verifies that a name present but with a different
// type (no exact name+type match) is a hard error.
func TestDataSourceDNSRecordRead_NotFound(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`[{"id":"rec-cname","name":"api","type":"CNAME","values":["lb.internal"],"ttl":60}]`))
	}))
	defer server.Close()

	c, _ := client.NewClient(server.URL, "test-token")
	d := newTestDNSRecordDSData(t, map[string]interface{}{
		"tenant_id": "acme", "project_id": "infra", "vnet_id": "vn-1", "zone_id": "z-8",
		"name": "api", "type": "A"})

	diags := dataSourceDnsRecordRead(context.Background(), d, c)
	if !diags.HasError() {
		t.Fatal("diags.HasError() = false, want true when the name matches but the type does not")
	}
}

// TestDataSourceDNSRecordRead_APIError verifies a failed list request surfaces as an error.
func TestDataSourceDNSRecordRead_APIError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte(`{"error":"boom"}`))
	}))
	defer server.Close()

	c, _ := client.NewClient(server.URL, "test-token")
	d := newTestDNSRecordDSData(t, map[string]interface{}{
		"tenant_id": "acme", "project_id": "infra", "vnet_id": "vn-1", "zone_id": "z-8",
		"name": "api", "type": "A"})

	diags := dataSourceDnsRecordRead(context.Background(), d, c)
	if !diags.HasError() {
		t.Fatal("diags.HasError() = false, want true for a 500 response")
	}
}
