// Unit tests for route_table_association.go.
//
// Associations are unusual: the DC-API has no dedicated GET endpoint for an
// association, so Read fetches the PARENT route table (GET .../route-tables/{rt_id})
// and scans its embedded associations list for the stored association ID. These tests
// therefore exercise that "read-through-the-parent" path, plus the five-part composite
// state ID "tenant_id/project_id/vnet_id/route_table_id/association_id".
//
// See project_test.go for the shared httptest + schema.TestResourceDataRaw patterns this
// file reuses.
package resources

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/hashicorp/terraform-plugin-sdk/v2/helper/schema"
	"terraform-provider-dcapi/internal/client"
)

// newTestRouteTableAssociationData builds a *schema.ResourceData for
// ResourceRouteTableAssociation() from a raw config map.
func newTestRouteTableAssociationData(t *testing.T, raw map[string]interface{}) *schema.ResourceData {
	t.Helper()
	return schema.TestResourceDataRaw(t, ResourceRouteTableAssociation().Schema, raw)
}

// TestResourceRouteTableAssociation_Schema verifies the schema invariants the rest of
// the logic depends on: all five path inputs are Required + immutable, and the three
// API-populated fields are Computed-only.
func TestResourceRouteTableAssociation_Schema(t *testing.T) {
	s := ResourceRouteTableAssociation().Schema

	immutableRequired := []string{"tenant_id", "project_id", "vnet_id", "route_table_id", "subnet_id"}
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

	computedOnly := []string{"association_id", "created_at", "warning"}
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

// TestResourceRouteTableAssociationCreate_Success verifies the POST path, method, the
// five-part composite ID, and that the API-returned computed fields land in state.
func TestResourceRouteTableAssociationCreate_Success(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("method = %s, want POST", r.Method)
		}
		if r.URL.Path != "/v1/tenants/acme/projects/infra/vnets/vnet-1/route-tables/rt-1/associations" {
			t.Errorf("path = %s, want .../route-tables/rt-1/associations", r.URL.Path)
		}
		w.WriteHeader(http.StatusCreated)
		w.Write([]byte(`{
			"id": "assoc-1",
			"route_table_id": "rt-1",
			"subnet_id": "subnet-1",
			"created_at": "2026-01-01T00:00:00Z",
			"warning": "routing not yet enforced"
		}`))
	}))
	defer server.Close()

	c, err := client.NewClient(server.URL, "test-token")
	if err != nil {
		t.Fatalf("client.NewClient: %v", err)
	}

	d := newTestRouteTableAssociationData(t, map[string]interface{}{
		"tenant_id":      "acme",
		"project_id":     "infra",
		"vnet_id":        "vnet-1",
		"route_table_id": "rt-1",
		"subnet_id":      "subnet-1",
	})

	diags := resourceRouteTableAssociationCreate(context.Background(), d, c)
	if diags.HasError() {
		t.Fatalf("create returned unexpected error diagnostics: %v", diags)
	}

	if got, want := d.Id(), "acme/infra/vnet-1/rt-1/assoc-1"; got != want {
		t.Errorf("Id() = %q, want %q", got, want)
	}
	if got, want := d.Get("association_id").(string), "assoc-1"; got != want {
		t.Errorf("association_id = %q, want %q", got, want)
	}
	if got, want := d.Get("warning").(string), "routing not yet enforced"; got != want {
		t.Errorf("warning = %q, want %q", got, want)
	}
}

// TestResourceRouteTableAssociationCreate_APIError verifies an API failure surfaces as an
// error diagnostic and never sets an ID.
func TestResourceRouteTableAssociationCreate_APIError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		w.Write([]byte(`{"error":"subnet already associated"}`))
	}))
	defer server.Close()

	c, _ := client.NewClient(server.URL, "test-token")
	d := newTestRouteTableAssociationData(t, map[string]interface{}{
		"tenant_id":      "acme",
		"project_id":     "infra",
		"vnet_id":        "vnet-1",
		"route_table_id": "rt-1",
		"subnet_id":      "subnet-1",
	})

	diags := resourceRouteTableAssociationCreate(context.Background(), d, c)
	if !diags.HasError() {
		t.Fatal("diags.HasError() = false, want true for a failed create")
	}
	if d.Id() != "" {
		t.Errorf("Id() = %q, want empty string when create fails", d.Id())
	}
}

// TestResourceRouteTableAssociationRead_Success verifies Read scans the parent route
// table's associations list and refreshes state when the association is present.
func TestResourceRouteTableAssociationRead_Success(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/tenants/acme/projects/infra/vnets/vnet-1/route-tables/rt-1" {
			t.Errorf("path = %s, want .../route-tables/rt-1", r.URL.Path)
		}
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{
			"id": "rt-1",
			"vnet_id": "vnet-1",
			"tenant_id": "acme",
			"name": "rt",
			"associations": [
				{"id": "assoc-1", "route_table_id": "rt-1", "subnet_id": "subnet-1", "created_at": "2026-01-01T00:00:00Z"}
			]
		}`))
	}))
	defer server.Close()

	c, _ := client.NewClient(server.URL, "test-token")
	d := newTestRouteTableAssociationData(t, map[string]interface{}{})
	d.SetId("acme/infra/vnet-1/rt-1/assoc-1")

	diags := resourceRouteTableAssociationRead(context.Background(), d, c)
	if diags.HasError() {
		t.Fatalf("read returned unexpected error diagnostics: %v", diags)
	}
	if d.Id() != "acme/infra/vnet-1/rt-1/assoc-1" {
		t.Errorf("Id() = %q, want unchanged", d.Id())
	}
	if got, want := d.Get("subnet_id").(string), "subnet-1"; got != want {
		t.Errorf("subnet_id = %q, want %q", got, want)
	}
	if got, want := d.Get("association_id").(string), "assoc-1"; got != want {
		t.Errorf("association_id = %q, want %q", got, want)
	}
}

// TestResourceRouteTableAssociationRead_RouteTableNotFound verifies that a 404 on the
// parent route table (GetRouteTable returns (nil,nil)) clears the ID without error —
// the association is implicitly gone with its parent.
func TestResourceRouteTableAssociationRead_RouteTableNotFound(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		w.Write([]byte(`{"error":"route table not found"}`))
	}))
	defer server.Close()

	c, _ := client.NewClient(server.URL, "test-token")
	d := newTestRouteTableAssociationData(t, map[string]interface{}{})
	d.SetId("acme/infra/vnet-1/rt-1/assoc-1")

	diags := resourceRouteTableAssociationRead(context.Background(), d, c)
	if diags.HasError() {
		t.Fatalf("read returned unexpected error diagnostics on 404: %v", diags)
	}
	if d.Id() != "" {
		t.Errorf("Id() = %q, want empty string when parent route table is gone", d.Id())
	}
}

// TestResourceRouteTableAssociationRead_AssociationGone verifies that when the route table
// still exists but no longer lists our association ID, Read clears the ID without error.
func TestResourceRouteTableAssociationRead_AssociationGone(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{
			"id": "rt-1",
			"vnet_id": "vnet-1",
			"tenant_id": "acme",
			"name": "rt",
			"associations": [
				{"id": "some-other-assoc", "route_table_id": "rt-1", "subnet_id": "subnet-9", "created_at": "2026-01-01T00:00:00Z"}
			]
		}`))
	}))
	defer server.Close()

	c, _ := client.NewClient(server.URL, "test-token")
	d := newTestRouteTableAssociationData(t, map[string]interface{}{})
	d.SetId("acme/infra/vnet-1/rt-1/assoc-1")

	diags := resourceRouteTableAssociationRead(context.Background(), d, c)
	if diags.HasError() {
		t.Fatalf("read returned unexpected error diagnostics: %v", diags)
	}
	if d.Id() != "" {
		t.Errorf("Id() = %q, want empty string when association is no longer listed", d.Id())
	}
}

// TestResourceRouteTableAssociationRead_InvalidID verifies a state ID without exactly five
// "/"-separated parts produces an error diagnostic rather than panicking.
func TestResourceRouteTableAssociationRead_InvalidID(t *testing.T) {
	c, _ := client.NewClient("http://unused.invalid", "test-token")
	d := newTestRouteTableAssociationData(t, map[string]interface{}{})
	d.SetId("acme/infra/vnet-1") // only three parts

	diags := resourceRouteTableAssociationRead(context.Background(), d, c)
	if !diags.HasError() {
		t.Fatal("diags.HasError() = false, want true for a malformed state ID")
	}
}

// TestResourceRouteTableAssociationDelete_Success verifies a successful DELETE returns no
// error. (The function itself does not clear the ID on success — the SDK framework does
// that after DeleteContext returns without error; see delete-not-found below for the one
// path that explicitly clears it.)
func TestResourceRouteTableAssociationDelete_Success(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodDelete {
			t.Errorf("method = %s, want DELETE", r.Method)
		}
		if r.URL.Path != "/v1/tenants/acme/projects/infra/vnets/vnet-1/route-tables/rt-1/associations/assoc-1" {
			t.Errorf("path = %s, want .../associations/assoc-1", r.URL.Path)
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()

	c, _ := client.NewClient(server.URL, "test-token")
	d := newTestRouteTableAssociationData(t, map[string]interface{}{})
	d.SetId("acme/infra/vnet-1/rt-1/assoc-1")

	diags := resourceRouteTableAssociationDelete(context.Background(), d, c)
	if diags.HasError() {
		t.Fatalf("delete returned unexpected error diagnostics: %v", diags)
	}
}

// TestResourceRouteTableAssociationDelete_NotFound verifies that a 404 (already deleted
// upstream) is treated as success AND explicitly clears the ID.
func TestResourceRouteTableAssociationDelete_NotFound(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		w.Write([]byte(`{"error":"association not found"}`))
	}))
	defer server.Close()

	c, _ := client.NewClient(server.URL, "test-token")
	d := newTestRouteTableAssociationData(t, map[string]interface{}{})
	d.SetId("acme/infra/vnet-1/rt-1/assoc-1")

	diags := resourceRouteTableAssociationDelete(context.Background(), d, c)
	if diags.HasError() {
		t.Fatalf("delete returned unexpected error diagnostics on 404: %v", diags)
	}
	if d.Id() != "" {
		t.Errorf("Id() = %q, want empty string after a 404 delete", d.Id())
	}
}

// TestResourceRouteTableAssociationDelete_Error verifies a non-404 failure surfaces as an
// error and does NOT clear the ID.
func TestResourceRouteTableAssociationDelete_Error(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte(`{"error":"boom"}`))
	}))
	defer server.Close()

	c, _ := client.NewClient(server.URL, "test-token")
	d := newTestRouteTableAssociationData(t, map[string]interface{}{})
	d.SetId("acme/infra/vnet-1/rt-1/assoc-1")

	diags := resourceRouteTableAssociationDelete(context.Background(), d, c)
	if !diags.HasError() {
		t.Fatal("diags.HasError() = false, want true for a 500 delete")
	}
	if d.Id() == "" {
		t.Error("Id() = \"\", want it to remain set since delete failed")
	}
}
