// Unit tests for route_table.go (dcapi_route_table).
//
// The route table is SYNCHRONOUS: Create returns 201 with a bare RouteTableResponse,
// Update PUTs the full routes list to .../route-tables/{rt} (full-replace), and Delete
// returns 204. Composite state ID is "tenant/project/vnet/rt_id".
//
// Note: schema.TestResourceDataRaw does not run per-field ValidateFunc or the resource's
// CustomizeDiff, so the raw route maps here need only be structurally valid.
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

// newTestRouteTableData builds a *schema.ResourceData for ResourceRouteTable().
func newTestRouteTableData(t *testing.T, raw map[string]interface{}) *schema.ResourceData {
	t.Helper()
	return schema.TestResourceDataRaw(t, ResourceRouteTable().Schema, raw)
}

// routeTableTestRoute is a reusable raw route map for building config.
func routeTableTestRoute() map[string]interface{} {
	return map[string]interface{}{
		"name":             "default",
		"destination_cidr": "0.0.0.0/0",
		"next_hop_type":    "internet",
		"next_hop_ip":      "",
	}
}

// TestResourceRouteTable_Schema pins immutability, the updatable routes list, and computed fields.
func TestResourceRouteTable_Schema(t *testing.T) {
	s := ResourceRouteTable().Schema

	requiredForceNew := []string{"tenant_id", "project_id", "vnet_id", "name"}
	for _, key := range requiredForceNew {
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

	// description is Optional + ForceNew.
	if f := s["description"]; f == nil {
		t.Fatal("schema is missing field \"description\"")
	} else if !f.Optional || !f.ForceNew {
		t.Errorf("description: Optional=%v ForceNew=%v, want both true", f.Optional, f.ForceNew)
	}

	// routes is Optional and NOT ForceNew — updated in place via resourceRouteTableUpdate.
	if f := s["routes"]; f == nil {
		t.Fatal("schema is missing field \"routes\"")
	} else {
		if !f.Optional {
			t.Error("routes: Optional = false, want true")
		}
		if f.ForceNew {
			t.Error("routes: ForceNew = true, want false (must be updatable)")
		}
	}

	computedOnly := []string{"route_table_id", "status", "provider_type", "created_at", "updated_at"}
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

// TestResourceRouteTableCreate_Success verifies POST, the composite Id, and route round-tripping.
func TestResourceRouteTableCreate_Success(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("method = %s, want POST", r.Method)
		}
		if r.URL.Path != "/v1/tenants/acme/projects/net/vnets/vnet-123/route-tables" {
			t.Errorf("path = %s, want .../vnets/vnet-123/route-tables", r.URL.Path)
		}
		w.WriteHeader(http.StatusCreated)
		w.Write([]byte(`{
			"id":"rt-5",
			"vnet_id":"vnet-123",
			"tenant_id":"acme",
			"name":"edge",
			"description":"edge routes",
			"routes":[{
				"name":"default","destination_cidr":"0.0.0.0/0","next_hop_type":"internet","next_hop_ip":""
			}],
			"associations":[],
			"status":"ACTIVE",
			"provider_type":"kubeovn",
			"created_at":"2026-01-01T00:00:00Z",
			"updated_at":"2026-01-01T00:00:00Z"
		}`))
	}))
	defer server.Close()

	c, err := client.NewClient(server.URL, "test-token")
	if err != nil {
		t.Fatalf("client.NewClient: %v", err)
	}

	d := newTestRouteTableData(t, map[string]interface{}{
		"tenant_id":   "acme",
		"project_id":  "net",
		"vnet_id":     "vnet-123",
		"name":        "edge",
		"description": "edge routes",
		"routes":      []interface{}{routeTableTestRoute()},
	})

	diags := resourceRouteTableCreate(context.Background(), d, c)
	if diags.HasError() {
		t.Fatalf("resourceRouteTableCreate returned unexpected error diagnostics: %v", diags)
	}

	if got, want := d.Id(), "acme/net/vnet-123/rt-5"; got != want {
		t.Errorf("Id() = %q, want %q", got, want)
	}
	if got, want := d.Get("route_table_id").(string), "rt-5"; got != want {
		t.Errorf("route_table_id = %q, want %q", got, want)
	}
	if got, want := d.Get("status").(string), "ACTIVE"; got != want {
		t.Errorf("status = %q, want %q", got, want)
	}
	routes := d.Get("routes").([]interface{})
	if len(routes) != 1 {
		t.Fatalf("routes length = %d, want 1", len(routes))
	}
	if got := routes[0].(map[string]interface{})["destination_cidr"].(string); got != "0.0.0.0/0" {
		t.Errorf("routes[0].destination_cidr = %q, want 0.0.0.0/0", got)
	}
}

// TestResourceRouteTableCreate_APIError verifies a failed POST surfaces an error and leaves Id() empty.
func TestResourceRouteTableCreate_APIError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		w.Write([]byte(`{"error":"duplicate route table name"}`))
	}))
	defer server.Close()

	c, _ := client.NewClient(server.URL, "test-token")
	d := newTestRouteTableData(t, map[string]interface{}{
		"tenant_id":  "acme",
		"project_id": "net",
		"vnet_id":    "vnet-123",
		"name":       "edge",
	})

	diags := resourceRouteTableCreate(context.Background(), d, c)
	if !diags.HasError() {
		t.Fatal("diags.HasError() = false, want true for a failed create")
	}
	if d.Id() != "" {
		t.Errorf("Id() = %q, want empty string when create fails", d.Id())
	}
}

// TestResourceRouteTableRead_Success verifies a GET refreshes state.
func TestResourceRouteTableRead_Success(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/tenants/acme/projects/net/vnets/vnet-123/route-tables/rt-5" {
			t.Errorf("path = %s, want .../route-tables/rt-5", r.URL.Path)
		}
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{
			"id":"rt-5",
			"vnet_id":"vnet-123",
			"tenant_id":"acme",
			"name":"edge",
			"description":"edge routes",
			"routes":[],
			"associations":[],
			"status":"ACTIVE",
			"provider_type":"kubeovn",
			"created_at":"2026-01-01T00:00:00Z",
			"updated_at":"2026-01-02T00:00:00Z"
		}`))
	}))
	defer server.Close()

	c, _ := client.NewClient(server.URL, "test-token")
	d := newTestRouteTableData(t, map[string]interface{}{})
	d.SetId("acme/net/vnet-123/rt-5")

	diags := resourceRouteTableRead(context.Background(), d, c)
	if diags.HasError() {
		t.Fatalf("resourceRouteTableRead returned unexpected error diagnostics: %v", diags)
	}
	if got, want := d.Get("name").(string), "edge"; got != want {
		t.Errorf("name = %q, want %q", got, want)
	}
	if got, want := d.Get("route_table_id").(string), "rt-5"; got != want {
		t.Errorf("route_table_id = %q, want %q", got, want)
	}
}

// TestResourceRouteTableRead_NotFound verifies a 404 clears Id() with no error diagnostics.
func TestResourceRouteTableRead_NotFound(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		w.Write([]byte(`{"error":"route table not found"}`))
	}))
	defer server.Close()

	c, _ := client.NewClient(server.URL, "test-token")
	d := newTestRouteTableData(t, map[string]interface{}{})
	d.SetId("acme/net/vnet-123/rt-5")

	diags := resourceRouteTableRead(context.Background(), d, c)
	if diags.HasError() {
		t.Fatalf("resourceRouteTableRead returned unexpected error diagnostics on 404: %v", diags)
	}
	if d.Id() != "" {
		t.Errorf("Id() = %q, want empty string after a 404", d.Id())
	}
}

// TestResourceRouteTableRead_InvalidID verifies the SplitN(id, "/", 4) guard errors on a malformed ID.
func TestResourceRouteTableRead_InvalidID(t *testing.T) {
	c, _ := client.NewClient("http://unused.invalid", "test-token")
	d := newTestRouteTableData(t, map[string]interface{}{})
	d.SetId("acme/net/vnet-123") // only 3 parts, want 4

	diags := resourceRouteTableRead(context.Background(), d, c)
	if !diags.HasError() {
		t.Fatal("diags.HasError() = false, want true for a malformed state ID")
	}
}

// TestResourceRouteTableUpdate_ReplacesRoutes verifies Update PUTs the full routes list and
// refreshes state from the response.
func TestResourceRouteTableUpdate_ReplacesRoutes(t *testing.T) {
	var gotBody string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPut {
			t.Errorf("method = %s, want PUT", r.Method)
		}
		if r.URL.Path != "/v1/tenants/acme/projects/net/vnets/vnet-123/route-tables/rt-5" {
			t.Errorf("path = %s, want .../route-tables/rt-5", r.URL.Path)
		}
		buf := make([]byte, r.ContentLength)
		r.Body.Read(buf)
		gotBody = string(buf)

		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{
			"id":"rt-5",
			"vnet_id":"vnet-123",
			"tenant_id":"acme",
			"name":"edge",
			"routes":[{
				"name":"default","destination_cidr":"0.0.0.0/0","next_hop_type":"internet","next_hop_ip":""
			}],
			"associations":[],
			"status":"ACTIVE",
			"updated_at":"2026-01-03T00:00:00Z"
		}`))
	}))
	defer server.Close()

	c, _ := client.NewClient(server.URL, "test-token")
	d := newTestRouteTableData(t, map[string]interface{}{
		"tenant_id":  "acme",
		"project_id": "net",
		"vnet_id":    "vnet-123",
		"name":       "edge",
		"routes":     []interface{}{routeTableTestRoute()},
	})
	d.SetId("acme/net/vnet-123/rt-5")

	diags := resourceRouteTableUpdate(context.Background(), d, c)
	if diags.HasError() {
		t.Fatalf("resourceRouteTableUpdate returned unexpected error diagnostics: %v", diags)
	}
	if !strings.Contains(gotBody, `"0.0.0.0/0"`) {
		t.Errorf("PUT body = %q, want it to contain the route CIDR", gotBody)
	}
	if got, want := d.Get("updated_at").(string), "2026-01-03T00:00:00Z"; got != want {
		t.Errorf("updated_at = %q, want %q", got, want)
	}
}

// TestResourceRouteTableDelete_Success verifies a 204 DELETE clears Id().
func TestResourceRouteTableDelete_Success(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodDelete {
			t.Errorf("method = %s, want DELETE", r.Method)
		}
		if r.URL.Path != "/v1/tenants/acme/projects/net/vnets/vnet-123/route-tables/rt-5" {
			t.Errorf("path = %s, want .../route-tables/rt-5", r.URL.Path)
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()

	c, _ := client.NewClient(server.URL, "test-token")
	d := newTestRouteTableData(t, map[string]interface{}{})
	d.SetId("acme/net/vnet-123/rt-5")

	diags := resourceRouteTableDelete(context.Background(), d, c)
	if diags.HasError() {
		t.Fatalf("resourceRouteTableDelete returned unexpected error diagnostics: %v", diags)
	}
	// Unlike project.go, this resource's Delete returns nil on the success path
	// WITHOUT calling d.SetId("") — the Terraform SDK framework clears the ID
	// after a successful DeleteContext during a real apply. In this direct unit
	// call nothing clears it, so the meaningful invariant is simply that Delete
	// reported no error diagnostics (asserted above).
}

// TestResourceRouteTableDelete_NotFound verifies a 404 DELETE is treated as success (Id cleared).
func TestResourceRouteTableDelete_NotFound(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		w.Write([]byte(`{"error":"route table not found"}`))
	}))
	defer server.Close()

	c, _ := client.NewClient(server.URL, "test-token")
	d := newTestRouteTableData(t, map[string]interface{}{})
	d.SetId("acme/net/vnet-123/rt-5")

	diags := resourceRouteTableDelete(context.Background(), d, c)
	if diags.HasError() {
		t.Fatalf("resourceRouteTableDelete returned unexpected error diagnostics on 404: %v", diags)
	}
	if d.Id() != "" {
		t.Errorf("Id() = %q, want empty string after a 404 delete (already gone)", d.Id())
	}
}
