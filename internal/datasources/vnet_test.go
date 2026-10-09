// Unit tests for vnet.go (data source).
//
// dcapi_vnet has no get-by-name endpoint, so ListVNets fetches GET .../vnets (a bare
// array) and the data source filters by name client-side. On a match it sets a composite
// Id of "tenant_id/project_id/vnet_uuid" and copies the VNet fields — including the
// address_space list — into state. A name that matches nothing is a hard error.
package datasources

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/hashicorp/terraform-plugin-sdk/v2/helper/schema"
	"terraform-provider-dcapi/internal/client"
)

func newTestVNetDSData(t *testing.T, raw map[string]interface{}) *schema.ResourceData {
	t.Helper()
	return schema.TestResourceDataRaw(t, DataSourceVNet().Schema, raw)
}

// TestDataSourceVNet_Schema pins tenant_id/project_id/name as Required lookups and the
// rest as Computed.
func TestDataSourceVNet_Schema(t *testing.T) {
	s := DataSourceVNet().Schema

	for _, key := range []string{"tenant_id", "project_id", "name"} {
		if f := s[key]; f == nil || !f.Required {
			t.Errorf("%s: want a Required lookup field, got %+v", key, f)
		}
	}
	for _, key := range []string{"vnet_uuid", "address_space", "region", "description",
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

// TestDataSourceVNetRead_Success verifies the name filter picks the right VNet, the
// composite Id is built, and scalar + list fields land in state.
func TestDataSourceVNetRead_Success(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/tenants/acme/projects/infra/vnets" {
			t.Errorf("path = %s, want /v1/tenants/acme/projects/infra/vnets", r.URL.Path)
		}
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`[
			{"id":"vn-other","name":"other"},
			{"id":"vn-123","name":"core","region":"lk","address_space":["10.1.0.0/16"],
			 "description":"core net","status":"ACTIVE","provider_type":"kubeovn",
			 "message":"ok","created_at":"2026-01-01T00:00:00Z","updated_at":"2026-01-02T00:00:00Z"}
		]`))
	}))
	defer server.Close()

	c, _ := client.NewClient(server.URL, "test-token")
	d := newTestVNetDSData(t, map[string]interface{}{"tenant_id": "acme", "project_id": "infra", "name": "core"})

	diags := dataSourceVNetRead(context.Background(), d, c)
	if diags.HasError() {
		t.Fatalf("dataSourceVNetRead returned unexpected error diagnostics: %v", diags)
	}
	if got, want := d.Id(), "acme/infra/vn-123"; got != want {
		t.Errorf("Id() = %q, want %q", got, want)
	}
	if got, want := d.Get("vnet_uuid").(string), "vn-123"; got != want {
		t.Errorf("vnet_uuid = %q, want %q", got, want)
	}
	if got, want := d.Get("region").(string), "lk"; got != want {
		t.Errorf("region = %q, want %q", got, want)
	}
	as := d.Get("address_space").([]interface{})
	if len(as) != 1 || as[0].(string) != "10.1.0.0/16" {
		t.Errorf("address_space = %v, want [10.1.0.0/16]", as)
	}
}

// TestDataSourceVNetRead_NotFound verifies an unmatched name is a hard error.
func TestDataSourceVNetRead_NotFound(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`[{"id":"vn-other","name":"other"}]`))
	}))
	defer server.Close()

	c, _ := client.NewClient(server.URL, "test-token")
	d := newTestVNetDSData(t, map[string]interface{}{"tenant_id": "acme", "project_id": "infra", "name": "ghost"})

	diags := dataSourceVNetRead(context.Background(), d, c)
	if !diags.HasError() {
		t.Fatal("diags.HasError() = false, want true for a VNet name not in the list")
	}
}

// TestDataSourceVNetRead_APIError verifies a failed list request surfaces as an error.
func TestDataSourceVNetRead_APIError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte(`{"error":"boom"}`))
	}))
	defer server.Close()

	c, _ := client.NewClient(server.URL, "test-token")
	d := newTestVNetDSData(t, map[string]interface{}{"tenant_id": "acme", "project_id": "infra", "name": "core"})

	diags := dataSourceVNetRead(context.Background(), d, c)
	if !diags.HasError() {
		t.Fatal("diags.HasError() = false, want true for a 500 response")
	}
}
