// Unit tests for private_endpoint.go.
//
// Private endpoints nest under a KeyVault and are fully SYNCHRONOUS (201 create, 204 delete)
// — no polling. The composite state ID has four parts: "tenant_id/project_id/kv_id/endpoint_id"
// (note: the VNet/Subnet live in the request body, NOT the URL path or the ID).
//
// See project_test.go for the shared httptest + schema.TestResourceDataRaw patterns.
package resources

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/hashicorp/terraform-plugin-sdk/v2/helper/schema"
	"terraform-provider-dcapi/internal/client"
)

// newTestPrivateEndpointData builds a *schema.ResourceData for ResourcePrivateEndpoint().
func newTestPrivateEndpointData(t *testing.T, raw map[string]interface{}) *schema.ResourceData {
	t.Helper()
	return schema.TestResourceDataRaw(t, ResourcePrivateEndpoint().Schema, raw)
}

// privateEndpointJSON is the object returned by Create (201) and Read (200).
const privateEndpointJSON = `{
	"id": "ep-1",
	"tenant_id": "acme",
	"target_type": "key_vault",
	"target_id": "kv-1",
	"vnet_id": "vnet-1",
	"subnet_id": "subnet-1",
	"name": "kv-endpoint",
	"ip_address": "10.0.0.5",
	"hostname": "kv-endpoint.internal",
	"status": "ACTIVE",
	"message": "ok",
	"created_at": "2026-01-01T00:00:00Z",
	"updated_at": "2026-01-02T00:00:00Z"
}`

func TestResourcePrivateEndpoint_Schema(t *testing.T) {
	s := ResourcePrivateEndpoint().Schema

	immutableRequired := []string{"tenant_id", "project_id", "kv_id", "name", "vnet_id", "subnet_id"}
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

	computedOnly := []string{"endpoint_id", "target_type", "target_id", "ip_address", "hostname", "status", "message", "created_at", "updated_at"}
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

// TestResourcePrivateEndpointCreate_Success verifies the POST method/path, the four-part
// composite ID, and copied computed fields.
func TestResourcePrivateEndpointCreate_Success(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("method = %s, want POST", r.Method)
		}
		if r.URL.Path != "/v1/tenants/acme/projects/infra/keyvaults/kv-1/private-endpoints" {
			t.Errorf("path = %s, want .../keyvaults/kv-1/private-endpoints", r.URL.Path)
		}
		w.WriteHeader(http.StatusCreated)
		w.Write([]byte(privateEndpointJSON))
	}))
	defer server.Close()

	c, err := client.NewClient(server.URL, "test-token")
	if err != nil {
		t.Fatalf("client.NewClient: %v", err)
	}

	d := newTestPrivateEndpointData(t, map[string]interface{}{
		"tenant_id":  "acme",
		"project_id": "infra",
		"kv_id":      "kv-1",
		"name":       "kv-endpoint",
		"vnet_id":    "vnet-1",
		"subnet_id":  "subnet-1",
	})

	diags := resourcePrivateEndpointCreate(context.Background(), d, c)
	if diags.HasError() {
		t.Fatalf("create returned unexpected error diagnostics: %v", diags)
	}

	if got, want := d.Id(), "acme/infra/kv-1/ep-1"; got != want {
		t.Errorf("Id() = %q, want %q", got, want)
	}
	if got, want := d.Get("endpoint_id").(string), "ep-1"; got != want {
		t.Errorf("endpoint_id = %q, want %q", got, want)
	}
	if got, want := d.Get("ip_address").(string), "10.0.0.5"; got != want {
		t.Errorf("ip_address = %q, want %q", got, want)
	}
	if got, want := d.Get("target_type").(string), "key_vault"; got != want {
		t.Errorf("target_type = %q, want %q", got, want)
	}
}

// TestResourcePrivateEndpointCreate_APIError verifies a failed POST surfaces an error and never
// sets an ID (covers the 501 Not Implemented case too — it is just a non-2xx error).
func TestResourcePrivateEndpointCreate_APIError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotImplemented)
		w.Write([]byte(`{"error":"endpoint provisioner not enabled"}`))
	}))
	defer server.Close()

	c, _ := client.NewClient(server.URL, "test-token")
	d := newTestPrivateEndpointData(t, map[string]interface{}{
		"tenant_id":  "acme",
		"project_id": "infra",
		"kv_id":      "kv-1",
		"name":       "kv-endpoint",
		"vnet_id":    "vnet-1",
		"subnet_id":  "subnet-1",
	})

	diags := resourcePrivateEndpointCreate(context.Background(), d, c)
	if !diags.HasError() {
		t.Fatal("diags.HasError() = false, want true for a failed create")
	}
	if d.Id() != "" {
		t.Errorf("Id() = %q, want empty string when create fails", d.Id())
	}
}

// TestResourcePrivateEndpointRead_Success verifies a GET refreshes state fields.
func TestResourcePrivateEndpointRead_Success(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/tenants/acme/projects/infra/keyvaults/kv-1/private-endpoints/ep-1" {
			t.Errorf("path = %s, want .../private-endpoints/ep-1", r.URL.Path)
		}
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(privateEndpointJSON))
	}))
	defer server.Close()

	c, _ := client.NewClient(server.URL, "test-token")
	d := newTestPrivateEndpointData(t, map[string]interface{}{})
	d.SetId("acme/infra/kv-1/ep-1")

	diags := resourcePrivateEndpointRead(context.Background(), d, c)
	if diags.HasError() {
		t.Fatalf("read returned unexpected error diagnostics: %v", diags)
	}
	if got, want := d.Get("name").(string), "kv-endpoint"; got != want {
		t.Errorf("name = %q, want %q", got, want)
	}
	if got, want := d.Get("hostname").(string), "kv-endpoint.internal"; got != want {
		t.Errorf("hostname = %q, want %q", got, want)
	}
	if got, want := d.Get("subnet_id").(string), "subnet-1"; got != want {
		t.Errorf("subnet_id = %q, want %q", got, want)
	}
}

// TestResourcePrivateEndpointRead_NotFound verifies a 404 clears the ID without error.
func TestResourcePrivateEndpointRead_NotFound(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		w.Write([]byte(`{"error":"endpoint not found"}`))
	}))
	defer server.Close()

	c, _ := client.NewClient(server.URL, "test-token")
	d := newTestPrivateEndpointData(t, map[string]interface{}{})
	d.SetId("acme/infra/kv-1/ep-1")

	diags := resourcePrivateEndpointRead(context.Background(), d, c)
	if diags.HasError() {
		t.Fatalf("read returned unexpected error diagnostics on 404: %v", diags)
	}
	if d.Id() != "" {
		t.Errorf("Id() = %q, want empty string after a 404", d.Id())
	}
}

// TestResourcePrivateEndpointRead_InvalidID verifies a state ID without exactly four parts is an
// error.
func TestResourcePrivateEndpointRead_InvalidID(t *testing.T) {
	c, _ := client.NewClient("http://unused.invalid", "test-token")
	d := newTestPrivateEndpointData(t, map[string]interface{}{})
	d.SetId("acme/infra") // only two parts

	diags := resourcePrivateEndpointRead(context.Background(), d, c)
	if !diags.HasError() {
		t.Fatal("diags.HasError() = false, want true for a malformed state ID")
	}
}

// TestResourcePrivateEndpointDelete_Success verifies a successful DELETE returns no error.
func TestResourcePrivateEndpointDelete_Success(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodDelete {
			t.Errorf("method = %s, want DELETE", r.Method)
		}
		if r.URL.Path != "/v1/tenants/acme/projects/infra/keyvaults/kv-1/private-endpoints/ep-1" {
			t.Errorf("path = %s, want .../private-endpoints/ep-1", r.URL.Path)
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()

	c, _ := client.NewClient(server.URL, "test-token")
	d := newTestPrivateEndpointData(t, map[string]interface{}{})
	d.SetId("acme/infra/kv-1/ep-1")

	diags := resourcePrivateEndpointDelete(context.Background(), d, c)
	if diags.HasError() {
		t.Fatalf("delete returned unexpected error diagnostics: %v", diags)
	}
}

// TestResourcePrivateEndpointDelete_NotFound verifies a 404 is treated as success and clears the
// ID.
func TestResourcePrivateEndpointDelete_NotFound(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		w.Write([]byte(`{"error":"endpoint not found"}`))
	}))
	defer server.Close()

	c, _ := client.NewClient(server.URL, "test-token")
	d := newTestPrivateEndpointData(t, map[string]interface{}{})
	d.SetId("acme/infra/kv-1/ep-1")

	diags := resourcePrivateEndpointDelete(context.Background(), d, c)
	if diags.HasError() {
		t.Fatalf("delete returned unexpected error diagnostics on 404: %v", diags)
	}
	if d.Id() != "" {
		t.Errorf("Id() = %q, want empty string after a 404 delete", d.Id())
	}
}

// TestResourcePrivateEndpointDelete_Error verifies a non-404 failure surfaces as an error and
// keeps the ID.
func TestResourcePrivateEndpointDelete_Error(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte(`{"error":"boom"}`))
	}))
	defer server.Close()

	c, _ := client.NewClient(server.URL, "test-token")
	d := newTestPrivateEndpointData(t, map[string]interface{}{})
	d.SetId("acme/infra/kv-1/ep-1")

	diags := resourcePrivateEndpointDelete(context.Background(), d, c)
	if !diags.HasError() {
		t.Fatal("diags.HasError() = false, want true for a 500 delete")
	}
	if d.Id() == "" {
		t.Error("Id() = \"\", want it to remain set since delete failed")
	}
}
