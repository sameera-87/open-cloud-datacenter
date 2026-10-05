// Unit tests for vnet_peering.go (data source).
//
// dcapi_vnet_peering lists GET .../vnets/{vnet_id}/peerings (bare array) and filters by
// name. Match sets Id "tenant_id/project_id/vnet_id/peering_id". Peerings are directional,
// so this only inspects peerings originating from vnet_id. No match is a hard error.
package datasources

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/hashicorp/terraform-plugin-sdk/v2/helper/schema"
	"terraform-provider-dcapi/internal/client"
)

func newTestVNetPeeringDSData(t *testing.T, raw map[string]interface{}) *schema.ResourceData {
	t.Helper()
	return schema.TestResourceDataRaw(t, DataSourceVNetPeering().Schema, raw)
}

// TestDataSourceVNetPeering_Schema pins the Required lookups and Computed outputs,
// including the one bool field (allow_forwarded_traffic).
func TestDataSourceVNetPeering_Schema(t *testing.T) {
	s := DataSourceVNetPeering().Schema

	for _, key := range []string{"tenant_id", "project_id", "vnet_id", "name"} {
		if f := s[key]; f == nil || !f.Required {
			t.Errorf("%s: want a Required lookup field, got %+v", key, f)
		}
	}
	for _, key := range []string{"peering_id", "peer_vnet_id", "allow_forwarded_traffic",
		"status", "provider_type", "message", "warning", "created_at", "updated_at"} {
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

// TestDataSourceVNetPeeringRead_Success verifies the name filter, composite Id, and that
// the bool field is copied through correctly.
func TestDataSourceVNetPeeringRead_Success(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/tenants/acme/projects/infra/vnets/vn-1/peerings" {
			t.Errorf("path = %s, want .../vnets/vn-1/peerings", r.URL.Path)
		}
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`[
			{"id":"pr-other","name":"other"},
			{"id":"pr-2","name":"to-shared","peer_vnet_id":"vn-99","allow_forwarded_traffic":true,
			 "status":"ACTIVE","provider_type":"kubeovn","message":"ok","warning":"asymmetric",
			 "created_at":"2026-01-01T00:00:00Z","updated_at":"2026-01-02T00:00:00Z"}
		]`))
	}))
	defer server.Close()

	c, _ := client.NewClient(server.URL, "test-token")
	d := newTestVNetPeeringDSData(t, map[string]interface{}{
		"tenant_id": "acme", "project_id": "infra", "vnet_id": "vn-1", "name": "to-shared"})

	diags := dataSourceVNetPeeringRead(context.Background(), d, c)
	if diags.HasError() {
		t.Fatalf("dataSourceVNetPeeringRead returned unexpected error diagnostics: %v", diags)
	}
	if got, want := d.Id(), "acme/infra/vn-1/pr-2"; got != want {
		t.Errorf("Id() = %q, want %q", got, want)
	}
	if got, want := d.Get("peer_vnet_id").(string), "vn-99"; got != want {
		t.Errorf("peer_vnet_id = %q, want %q", got, want)
	}
	if got := d.Get("allow_forwarded_traffic").(bool); !got {
		t.Errorf("allow_forwarded_traffic = %v, want true", got)
	}
	if got, want := d.Get("warning").(string), "asymmetric"; got != want {
		t.Errorf("warning = %q, want %q", got, want)
	}
}

// TestDataSourceVNetPeeringRead_NotFound verifies an unmatched name is a hard error.
func TestDataSourceVNetPeeringRead_NotFound(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`[{"id":"pr-other","name":"other"}]`))
	}))
	defer server.Close()

	c, _ := client.NewClient(server.URL, "test-token")
	d := newTestVNetPeeringDSData(t, map[string]interface{}{
		"tenant_id": "acme", "project_id": "infra", "vnet_id": "vn-1", "name": "ghost"})

	diags := dataSourceVNetPeeringRead(context.Background(), d, c)
	if !diags.HasError() {
		t.Fatal("diags.HasError() = false, want true for a peering name not in the list")
	}
}

// TestDataSourceVNetPeeringRead_APIError verifies a failed list request surfaces as an error.
func TestDataSourceVNetPeeringRead_APIError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte(`{"error":"boom"}`))
	}))
	defer server.Close()

	c, _ := client.NewClient(server.URL, "test-token")
	d := newTestVNetPeeringDSData(t, map[string]interface{}{
		"tenant_id": "acme", "project_id": "infra", "vnet_id": "vn-1", "name": "to-shared"})

	diags := dataSourceVNetPeeringRead(context.Background(), d, c)
	if !diags.HasError() {
		t.Fatal("diags.HasError() = false, want true for a 500 response")
	}
}
