// Unit tests for dns_record.go.
//
// DNS records are fully SYNCHRONOUS (no polling): Create is an upsert POST (201), Update is
// a full-replace PUT (200), Delete is a DELETE (204). name+type form the immutable upsert
// identity (ForceNew); values+ttl are updatable in place. The composite state ID has five
// parts: "tenant_id/project_id/vnet_id/zone_id/record_id".
//
// See project_test.go for the shared httptest + schema.TestResourceDataRaw patterns.
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

// newTestDNSRecordData builds a *schema.ResourceData for ResourceDnsRecord().
func newTestDNSRecordData(t *testing.T, raw map[string]interface{}) *schema.ResourceData {
	t.Helper()
	return schema.TestResourceDataRaw(t, ResourceDnsRecord().Schema, raw)
}

func TestResourceDNSRecord_Schema(t *testing.T) {
	s := ResourceDnsRecord().Schema

	// Path params plus the name/type upsert identity are Required + immutable.
	immutableRequired := []string{"tenant_id", "project_id", "vnet_id", "zone_id", "name", "type"}
	for _, key := range immutableRequired {
		f := s[key]
		if f == nil {
			t.Fatalf("schema is missing field %q", key)
		}
		if !f.Required {
			t.Errorf("%s: Required = false, want true", key)
		}
		if !f.ForceNew {
			t.Errorf("%s: ForceNew = false, want true (must be immutable)", key)
		}
	}

	// values is Required but updatable in place (must NOT be ForceNew).
	if f := s["values"]; f == nil {
		t.Fatal("schema is missing field values")
	} else {
		if !f.Required {
			t.Error("values: Required = false, want true")
		}
		if f.ForceNew {
			t.Error("values: ForceNew = true, want false (must be updatable via PUT)")
		}
	}

	// ttl is Optional (default 300) and updatable in place.
	if f := s["ttl"]; f == nil {
		t.Fatal("schema is missing field ttl")
	} else {
		if !f.Optional {
			t.Error("ttl: Optional = false, want true")
		}
		if f.ForceNew {
			t.Error("ttl: ForceNew = true, want false (must be updatable via PUT)")
		}
	}

	computedOnly := []string{"record_id", "created_at"}
	for _, key := range computedOnly {
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

// TestResourceDNSRecordCreate_Success verifies the upsert POST method/path, the five-part
// composite ID, and that computed fields land in state.
func TestResourceDNSRecordCreate_Success(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("method = %s, want POST", r.Method)
		}
		if r.URL.Path != "/v1/tenants/acme/projects/infra/vnets/vnet-1/dns-zones/zone-1/records" {
			t.Errorf("path = %s, want .../dns-zones/zone-1/records", r.URL.Path)
		}
		w.WriteHeader(http.StatusCreated)
		w.Write([]byte(`{
			"id": "rec-1",
			"zone_id": "zone-1",
			"tenant_id": "acme",
			"type": "A",
			"name": "www",
			"values": ["10.0.0.1"],
			"ttl": 300,
			"created_at": "2026-01-01T00:00:00Z"
		}`))
	}))
	defer server.Close()

	c, err := client.NewClient(server.URL, "test-token")
	if err != nil {
		t.Fatalf("client.NewClient: %v", err)
	}

	d := newTestDNSRecordData(t, map[string]interface{}{
		"tenant_id":  "acme",
		"project_id": "infra",
		"vnet_id":    "vnet-1",
		"zone_id":    "zone-1",
		"name":       "www",
		"type":       "A",
		"values":     []interface{}{"10.0.0.1"},
		"ttl":        300,
	})

	diags := resourceDnsRecordCreate(context.Background(), d, c)
	if diags.HasError() {
		t.Fatalf("create returned unexpected error diagnostics: %v", diags)
	}

	if got, want := d.Id(), "acme/infra/vnet-1/zone-1/rec-1"; got != want {
		t.Errorf("Id() = %q, want %q", got, want)
	}
	if got, want := d.Get("record_id").(string), "rec-1"; got != want {
		t.Errorf("record_id = %q, want %q", got, want)
	}
	if got, want := d.Get("ttl").(int), 300; got != want {
		t.Errorf("ttl = %d, want %d", got, want)
	}
}

// TestResourceDNSRecordCreate_APIError verifies a failed POST surfaces an error and never sets
// an ID.
func TestResourceDNSRecordCreate_APIError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		w.Write([]byte(`{"error":"invalid record type"}`))
	}))
	defer server.Close()

	c, _ := client.NewClient(server.URL, "test-token")
	d := newTestDNSRecordData(t, map[string]interface{}{
		"tenant_id":  "acme",
		"project_id": "infra",
		"vnet_id":    "vnet-1",
		"zone_id":    "zone-1",
		"name":       "www",
		"type":       "A",
		"values":     []interface{}{"10.0.0.1"},
	})

	diags := resourceDnsRecordCreate(context.Background(), d, c)
	if !diags.HasError() {
		t.Fatal("diags.HasError() = false, want true for a failed create")
	}
	if d.Id() != "" {
		t.Errorf("Id() = %q, want empty string when create fails", d.Id())
	}
}

// TestResourceDNSRecordRead_Success verifies a GET refreshes state fields including the values
// list.
func TestResourceDNSRecordRead_Success(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/tenants/acme/projects/infra/vnets/vnet-1/dns-zones/zone-1/records/rec-1" {
			t.Errorf("path = %s, want .../records/rec-1", r.URL.Path)
		}
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{
			"id": "rec-1",
			"zone_id": "zone-1",
			"tenant_id": "acme",
			"type": "A",
			"name": "www",
			"values": ["10.0.0.1", "10.0.0.2"],
			"ttl": 600,
			"created_at": "2026-01-01T00:00:00Z"
		}`))
	}))
	defer server.Close()

	c, _ := client.NewClient(server.URL, "test-token")
	d := newTestDNSRecordData(t, map[string]interface{}{})
	d.SetId("acme/infra/vnet-1/zone-1/rec-1")

	diags := resourceDnsRecordRead(context.Background(), d, c)
	if diags.HasError() {
		t.Fatalf("read returned unexpected error diagnostics: %v", diags)
	}
	if got, want := d.Get("name").(string), "www"; got != want {
		t.Errorf("name = %q, want %q", got, want)
	}
	if got, want := d.Get("ttl").(int), 600; got != want {
		t.Errorf("ttl = %d, want %d", got, want)
	}
	vals := d.Get("values").([]interface{})
	if len(vals) != 2 || vals[0].(string) != "10.0.0.1" || vals[1].(string) != "10.0.0.2" {
		t.Errorf("values = %v, want [10.0.0.1 10.0.0.2]", vals)
	}
}

// TestResourceDNSRecordRead_NotFound verifies a 404 clears the ID without error.
func TestResourceDNSRecordRead_NotFound(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		w.Write([]byte(`{"error":"record not found"}`))
	}))
	defer server.Close()

	c, _ := client.NewClient(server.URL, "test-token")
	d := newTestDNSRecordData(t, map[string]interface{}{})
	d.SetId("acme/infra/vnet-1/zone-1/rec-1")

	diags := resourceDnsRecordRead(context.Background(), d, c)
	if diags.HasError() {
		t.Fatalf("read returned unexpected error diagnostics on 404: %v", diags)
	}
	if d.Id() != "" {
		t.Errorf("Id() = %q, want empty string after a 404", d.Id())
	}
}

// TestResourceDNSRecordRead_InvalidID verifies a state ID without exactly five parts is an
// error.
func TestResourceDNSRecordRead_InvalidID(t *testing.T) {
	c, _ := client.NewClient("http://unused.invalid", "test-token")
	d := newTestDNSRecordData(t, map[string]interface{}{})
	d.SetId("acme/infra/vnet-1/zone-1") // only four parts

	diags := resourceDnsRecordRead(context.Background(), d, c)
	if !diags.HasError() {
		t.Fatal("diags.HasError() = false, want true for a malformed state ID")
	}
}

// TestResourceDNSRecordUpdate_Success verifies the PUT method/path and that the full
// (replaced) values list plus ttl are sent and refreshed into state.
func TestResourceDNSRecordUpdate_Success(t *testing.T) {
	var gotBody string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPut {
			t.Errorf("method = %s, want PUT", r.Method)
		}
		if r.URL.Path != "/v1/tenants/acme/projects/infra/vnets/vnet-1/dns-zones/zone-1/records/rec-1" {
			t.Errorf("path = %s, want .../records/rec-1", r.URL.Path)
		}
		buf := make([]byte, r.ContentLength)
		r.Body.Read(buf)
		gotBody = string(buf)

		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{
			"id": "rec-1",
			"zone_id": "zone-1",
			"type": "A",
			"name": "www",
			"values": ["10.0.0.9"],
			"ttl": 120,
			"created_at": "2026-01-01T00:00:00Z"
		}`))
	}))
	defer server.Close()

	c, _ := client.NewClient(server.URL, "test-token")
	d := newTestDNSRecordData(t, map[string]interface{}{
		"tenant_id":  "acme",
		"project_id": "infra",
		"vnet_id":    "vnet-1",
		"zone_id":    "zone-1",
		"name":       "www",
		"type":       "A",
		"values":     []interface{}{"10.0.0.9"},
		"ttl":        120,
	})
	d.SetId("acme/infra/vnet-1/zone-1/rec-1")

	diags := resourceDnsRecordUpdate(context.Background(), d, c)
	if diags.HasError() {
		t.Fatalf("update returned unexpected error diagnostics: %v", diags)
	}
	if !strings.Contains(gotBody, "10.0.0.9") {
		t.Errorf("PUT body = %q, want it to contain the new value 10.0.0.9", gotBody)
	}
	if got, want := d.Get("ttl").(int), 120; got != want {
		t.Errorf("ttl = %d, want %d", got, want)
	}
	vals := d.Get("values").([]interface{})
	if len(vals) != 1 || vals[0].(string) != "10.0.0.9" {
		t.Errorf("values = %v, want [10.0.0.9]", vals)
	}
}

// TestResourceDNSRecordDelete_Success verifies a successful DELETE returns no error.
func TestResourceDNSRecordDelete_Success(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodDelete {
			t.Errorf("method = %s, want DELETE", r.Method)
		}
		if r.URL.Path != "/v1/tenants/acme/projects/infra/vnets/vnet-1/dns-zones/zone-1/records/rec-1" {
			t.Errorf("path = %s, want .../records/rec-1", r.URL.Path)
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()

	c, _ := client.NewClient(server.URL, "test-token")
	d := newTestDNSRecordData(t, map[string]interface{}{})
	d.SetId("acme/infra/vnet-1/zone-1/rec-1")

	diags := resourceDnsRecordDelete(context.Background(), d, c)
	if diags.HasError() {
		t.Fatalf("delete returned unexpected error diagnostics: %v", diags)
	}
}

// TestResourceDNSRecordDelete_NotFound verifies a 404 is treated as success and clears the ID.
func TestResourceDNSRecordDelete_NotFound(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		w.Write([]byte(`{"error":"record not found"}`))
	}))
	defer server.Close()

	c, _ := client.NewClient(server.URL, "test-token")
	d := newTestDNSRecordData(t, map[string]interface{}{})
	d.SetId("acme/infra/vnet-1/zone-1/rec-1")

	diags := resourceDnsRecordDelete(context.Background(), d, c)
	if diags.HasError() {
		t.Fatalf("delete returned unexpected error diagnostics on 404: %v", diags)
	}
	if d.Id() != "" {
		t.Errorf("Id() = %q, want empty string after a 404 delete", d.Id())
	}
}

// TestResourceDNSRecordDelete_Error verifies a non-404 failure surfaces as an error and keeps
// the ID.
func TestResourceDNSRecordDelete_Error(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte(`{"error":"boom"}`))
	}))
	defer server.Close()

	c, _ := client.NewClient(server.URL, "test-token")
	d := newTestDNSRecordData(t, map[string]interface{}{})
	d.SetId("acme/infra/vnet-1/zone-1/rec-1")

	diags := resourceDnsRecordDelete(context.Background(), d, c)
	if !diags.HasError() {
		t.Fatal("diags.HasError() = false, want true for a 500 delete")
	}
	if d.Id() == "" {
		t.Error("Id() = \"\", want it to remain set since delete failed")
	}
}
