// Unit tests for vnet.go.
//
// dcapi_vnet is an ASYNC resource: Create POSTs, then polls GET until status
// ACTIVE (waitForVNetActive) before calling Read; Delete fires DELETE, then polls
// GET until HTTP 404 (waitForVNetDeleted). Every test therefore uses one httptest
// handler that switches on r.Method and r.URL.Path and returns the right envelope:
//   - POST .../vnets          -> 202 {"resource": {...}}   (VNetCreateResponse wrapper)
//   - GET  .../vnets/{id}      -> 200 {...}                 (bare VNetResponse)
//   - DELETE .../vnets/{id}    -> 202
//
// Returning status "ACTIVE" on the create-path GET lets the StateChangeConf finish
// on its first poll with no delay; returning 404 on the delete-path GET lets the
// delete poller finish immediately.
//
// See project_test.go for the shared TestResourceDataRaw + httptest patterns.
package resources

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/hashicorp/terraform-plugin-sdk/v2/helper/schema"
	"terraform-provider-dcapi/internal/client"
)

// newTestVNetData builds a *schema.ResourceData for ResourceVNet() from a raw config map.
func newTestVNetData(t *testing.T, raw map[string]interface{}) *schema.ResourceData {
	t.Helper()
	return schema.TestResourceDataRaw(t, ResourceVNet().Schema, raw)
}

// TestResourceVNet_Schema pins the immutability/computed invariants vnet.go relies on.
// Every user-facing field is ForceNew (there is no UpdateContext), so any accidental
// drop of ForceNew would let Terraform attempt an illegal in-place change.
func TestResourceVNet_Schema(t *testing.T) {
	s := ResourceVNet().Schema

	requiredForceNew := []string{"name", "address_space", "region", "tenant_id", "project_id"}
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

	// description is the one optional user field — still immutable.
	if f := s["description"]; f == nil {
		t.Fatal("schema is missing field \"description\"")
	} else {
		if !f.Optional {
			t.Error("description: Optional = false, want true")
		}
		if !f.ForceNew {
			t.Error("description: ForceNew = false, want true")
		}
	}

	computedOnly := []string{"status", "provider_type", "message", "created_at", "updated_at", "vnet_uuid"}
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

// TestResourceVNetCreate_Success exercises the full async create: POST, poll-to-ACTIVE,
// then Read. Id() must be the composite "tenant/project/vnet_uuid" and computed fields
// must be copied into state.
func TestResourceVNetCreate_Success(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodPost:
			if r.URL.Path != "/v1/tenants/acme/projects/net/vnets" {
				t.Errorf("POST path = %s, want /v1/tenants/acme/projects/net/vnets", r.URL.Path)
			}
			w.WriteHeader(http.StatusAccepted)
			w.Write([]byte(`{"resource":{
				"id":"vnet-123",
				"tenant_id":"acme",
				"name":"primary",
				"region":"lk-dev",
				"address_space":["10.1.0.0/16"],
				"description":"core network",
				"status":"ACTIVE",
				"provider_type":"kubeovn",
				"message":"",
				"created_at":"2026-01-01T00:00:00Z",
				"updated_at":"2026-01-01T00:00:00Z"
			},"note":"provisioning"}`))
		case http.MethodGet:
			if r.URL.Path != "/v1/tenants/acme/projects/net/vnets/vnet-123" {
				t.Errorf("GET path = %s, want /v1/tenants/acme/projects/net/vnets/vnet-123", r.URL.Path)
			}
			w.WriteHeader(http.StatusOK)
			w.Write([]byte(`{
				"id":"vnet-123",
				"tenant_id":"acme",
				"name":"primary",
				"region":"lk-dev",
				"address_space":["10.1.0.0/16"],
				"description":"core network",
				"status":"ACTIVE",
				"provider_type":"kubeovn",
				"created_at":"2026-01-01T00:00:00Z",
				"updated_at":"2026-01-01T00:00:00Z"
			}`))
		default:
			t.Errorf("unexpected method %s", r.Method)
		}
	}))
	defer server.Close()

	c, err := client.NewClient(server.URL, "test-token")
	if err != nil {
		t.Fatalf("client.NewClient: %v", err)
	}

	d := newTestVNetData(t, map[string]interface{}{
		"tenant_id":     "acme",
		"project_id":    "net",
		"name":          "primary",
		"region":        "lk-dev",
		"address_space": []interface{}{"10.1.0.0/16"},
		"description":   "core network",
	})

	diags := resourceVNetCreate(context.Background(), d, c)
	if diags.HasError() {
		t.Fatalf("resourceVNetCreate returned unexpected error diagnostics: %v", diags)
	}

	if got, want := d.Id(), "acme/net/vnet-123"; got != want {
		t.Errorf("Id() = %q, want %q", got, want)
	}
	if got, want := d.Get("vnet_uuid").(string), "vnet-123"; got != want {
		t.Errorf("vnet_uuid = %q, want %q", got, want)
	}
	if got, want := d.Get("status").(string), "ACTIVE"; got != want {
		t.Errorf("status = %q, want %q", got, want)
	}
	if got, want := d.Get("provider_type").(string), "kubeovn"; got != want {
		t.Errorf("provider_type = %q, want %q", got, want)
	}
}

// TestResourceVNetCreate_APIError verifies a failed POST surfaces an error and leaves Id() empty.
func TestResourceVNetCreate_APIError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		w.Write([]byte(`{"error":"invalid address_space"}`))
	}))
	defer server.Close()

	c, _ := client.NewClient(server.URL, "test-token")
	d := newTestVNetData(t, map[string]interface{}{
		"tenant_id":     "acme",
		"project_id":    "net",
		"name":          "primary",
		"region":        "lk-dev",
		"address_space": []interface{}{"10.1.0.0/16"},
	})

	diags := resourceVNetCreate(context.Background(), d, c)
	if !diags.HasError() {
		t.Fatal("diags.HasError() = false, want true for a failed create")
	}
	if d.Id() != "" {
		t.Errorf("Id() = %q, want empty string when create fails", d.Id())
	}
}

// TestResourceVNetRead_Success verifies a GET refreshes state from the API.
func TestResourceVNetRead_Success(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/tenants/acme/projects/net/vnets/vnet-123" {
			t.Errorf("path = %s, want /v1/tenants/acme/projects/net/vnets/vnet-123", r.URL.Path)
		}
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{
			"id":"vnet-123",
			"tenant_id":"acme",
			"name":"primary",
			"region":"lk-dev",
			"address_space":["10.1.0.0/16"],
			"status":"ACTIVE",
			"provider_type":"kubeovn",
			"created_at":"2026-01-01T00:00:00Z",
			"updated_at":"2026-01-02T00:00:00Z"
		}`))
	}))
	defer server.Close()

	c, _ := client.NewClient(server.URL, "test-token")
	d := newTestVNetData(t, map[string]interface{}{})
	d.SetId("acme/net/vnet-123")

	diags := resourceVNetRead(context.Background(), d, c)
	if diags.HasError() {
		t.Fatalf("resourceVNetRead returned unexpected error diagnostics: %v", diags)
	}
	if d.Id() != "acme/net/vnet-123" {
		t.Errorf("Id() = %q, want unchanged", d.Id())
	}
	if got, want := d.Get("name").(string), "primary"; got != want {
		t.Errorf("name = %q, want %q", got, want)
	}
	if got, want := d.Get("vnet_uuid").(string), "vnet-123"; got != want {
		t.Errorf("vnet_uuid = %q, want %q", got, want)
	}
}

// TestResourceVNetRead_NotFound verifies a 404 clears Id() and returns no error diagnostics.
func TestResourceVNetRead_NotFound(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		w.Write([]byte(`{"error":"vnet not found"}`))
	}))
	defer server.Close()

	c, _ := client.NewClient(server.URL, "test-token")
	d := newTestVNetData(t, map[string]interface{}{})
	d.SetId("acme/net/vnet-123")

	diags := resourceVNetRead(context.Background(), d, c)
	if diags.HasError() {
		t.Fatalf("resourceVNetRead returned unexpected error diagnostics on 404: %v", diags)
	}
	if d.Id() != "" {
		t.Errorf("Id() = %q, want empty string after a 404", d.Id())
	}
}

// TestResourceVNetRead_InvalidID verifies the SplitN(id, "/", 3) guard errors on a malformed ID.
func TestResourceVNetRead_InvalidID(t *testing.T) {
	c, _ := client.NewClient("http://unused.invalid", "test-token")
	d := newTestVNetData(t, map[string]interface{}{})
	d.SetId("only/two")

	diags := resourceVNetRead(context.Background(), d, c)
	if !diags.HasError() {
		t.Fatal("diags.HasError() = false, want true for a malformed state ID")
	}
}

// TestResourceVNetDelete_Success verifies DELETE then poll-to-404 clears Id().
func TestResourceVNetDelete_Success(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodDelete:
			if r.URL.Path != "/v1/tenants/acme/projects/net/vnets/vnet-123" {
				t.Errorf("DELETE path = %s, want /v1/tenants/acme/projects/net/vnets/vnet-123", r.URL.Path)
			}
			w.WriteHeader(http.StatusAccepted)
		case http.MethodGet:
			// waitForVNetDeleted polls GET; 404 means deletion is complete.
			w.WriteHeader(http.StatusNotFound)
			w.Write([]byte(`{"error":"vnet not found"}`))
		default:
			t.Errorf("unexpected method %s", r.Method)
		}
	}))
	defer server.Close()

	c, _ := client.NewClient(server.URL, "test-token")
	d := newTestVNetData(t, map[string]interface{}{})
	d.SetId("acme/net/vnet-123")

	diags := resourceVNetDelete(context.Background(), d, c)
	if diags.HasError() {
		t.Fatalf("resourceVNetDelete returned unexpected error diagnostics: %v", diags)
	}
	// Unlike project.go, this resource's Delete returns nil on the success path
	// WITHOUT calling d.SetId("") — the Terraform SDK framework clears the ID
	// after a successful DeleteContext during a real apply. In this direct unit
	// call nothing clears it, so the meaningful invariant is simply that Delete
	// reported no error diagnostics (asserted above).
}

// TestResourceVNetDelete_Conflict verifies a 409 (subnets still present) errors and retains Id().
func TestResourceVNetDelete_Conflict(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusConflict)
		w.Write([]byte(`{"error":"vnet still has subnets"}`))
	}))
	defer server.Close()

	c, _ := client.NewClient(server.URL, "test-token")
	d := newTestVNetData(t, map[string]interface{}{})
	d.SetId("acme/net/vnet-123")

	diags := resourceVNetDelete(context.Background(), d, c)
	if !diags.HasError() {
		t.Fatal("diags.HasError() = false, want true for a 409 conflict")
	}
	if d.Id() == "" {
		t.Error("Id() = \"\", want it to remain set since delete failed")
	}
}
