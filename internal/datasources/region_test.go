// Unit tests for region.go (data source).
//
// Regions are platform-wide: ListRegions hits GET /v1/regions, which — uniquely in this
// API — returns an {"items":[...]} envelope rather than a bare array. The data source
// filters by name client-side, sets Id to the region slug, and flattens each zone's
// nested (possibly nil) agent status into two plain string fields.
package datasources

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/hashicorp/terraform-plugin-sdk/v2/helper/schema"
	"terraform-provider-dcapi/internal/client"
)

func newTestRegionDSData(t *testing.T, raw map[string]interface{}) *schema.ResourceData {
	t.Helper()
	return schema.TestResourceDataRaw(t, DataSourceRegion().Schema, raw)
}

// TestDataSourceRegion_Schema pins "name" as the Required lookup and the rest as Computed.
func TestDataSourceRegion_Schema(t *testing.T) {
	s := DataSourceRegion().Schema

	if f := s["name"]; f == nil || !f.Required {
		t.Errorf("name: want a Required lookup field, got %+v", f)
	}
	for _, key := range []string{"display_name", "description", "status", "zones"} {
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

// TestDataSourceRegionRead_Success verifies the items-envelope is parsed, the name match
// sets Id, and flattenZones turns a nested agent (and a nil agent) into the flat
// agent_version/agent_last_seen fields.
func TestDataSourceRegionRead_Success(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/regions" {
			t.Errorf("path = %s, want /v1/regions", r.URL.Path)
		}
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"items":[
			{"name":"us","display_name":"US"},
			{"name":"lk","display_name":"Sri Lanka","description":"LK region","status":"up",
			 "zones":[
				{"name":"lk-a","status":"up","agent":{"version":"1.2.3","last_seen":"2026-01-01T00:00:00Z"}},
				{"name":"lk-b","status":"unknown","agent":null}
			 ]}
		]}`))
	}))
	defer server.Close()

	c, _ := client.NewClient(server.URL, "test-token")
	d := newTestRegionDSData(t, map[string]interface{}{"name": "lk"})

	diags := dataSourceRegionRead(context.Background(), d, c)
	if diags.HasError() {
		t.Fatalf("dataSourceRegionRead returned unexpected error diagnostics: %v", diags)
	}
	if got, want := d.Id(), "lk"; got != want {
		t.Errorf("Id() = %q, want %q", got, want)
	}
	if got, want := d.Get("display_name").(string), "Sri Lanka"; got != want {
		t.Errorf("display_name = %q, want %q", got, want)
	}

	zones := d.Get("zones").([]interface{})
	if len(zones) != 2 {
		t.Fatalf("len(zones) = %d, want 2", len(zones))
	}
	z0 := zones[0].(map[string]interface{})
	if z0["agent_version"].(string) != "1.2.3" {
		t.Errorf("zone[0] agent_version = %q, want 1.2.3", z0["agent_version"])
	}
	z1 := zones[1].(map[string]interface{})
	if z1["agent_version"].(string) != "" {
		t.Errorf("zone[1] agent_version = %q, want empty (nil agent)", z1["agent_version"])
	}
}

// TestDataSourceRegionRead_NotFound verifies a name that matches no region is a hard error.
func TestDataSourceRegionRead_NotFound(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"items":[{"name":"us","display_name":"US"}]}`))
	}))
	defer server.Close()

	c, _ := client.NewClient(server.URL, "test-token")
	d := newTestRegionDSData(t, map[string]interface{}{"name": "ghost"})

	diags := dataSourceRegionRead(context.Background(), d, c)
	if !diags.HasError() {
		t.Fatal("diags.HasError() = false, want true for a region name not present in the list")
	}
}

// TestDataSourceRegionRead_APIError verifies a failed list request surfaces as an error.
func TestDataSourceRegionRead_APIError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte(`{"error":"boom"}`))
	}))
	defer server.Close()

	c, _ := client.NewClient(server.URL, "test-token")
	d := newTestRegionDSData(t, map[string]interface{}{"name": "lk"})

	diags := dataSourceRegionRead(context.Background(), d, c)
	if !diags.HasError() {
		t.Fatal("diags.HasError() = false, want true for a 500 response")
	}
}
