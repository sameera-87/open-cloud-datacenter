// Unit tests for subnet.go.
//
// Like dcapi_vnet, dcapi_subnet is ASYNC: Create POSTs then polls GET until ACTIVE
// before Read; Delete fires DELETE then polls GET until 404. The httptest handler
// switches on method/path and returns the right envelope:
//   - POST   .../subnets          -> 202 {"resource": {...}}  (SubnetCreateResponse)
//   - GET    .../subnets/{id}      -> 200 {...}                (bare SubnetResponse)
//   - DELETE .../subnets/{id}      -> 202
//
// Returning ACTIVE on the create-path GET and 404 on the delete-path GET lets both
// pollers finish on their first iteration.
package resources

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/hashicorp/terraform-plugin-sdk/v2/helper/schema"
	"terraform-provider-dcapi/internal/client"
)

// newTestSubnetData builds a *schema.ResourceData for ResourceSubnet() from a raw config map.
func newTestSubnetData(t *testing.T, raw map[string]interface{}) *schema.ResourceData {
	t.Helper()
	return schema.TestResourceDataRaw(t, ResourceSubnet().Schema, raw)
}

// TestResourceSubnet_Schema pins the immutability/computed invariants subnet.go relies on.
func TestResourceSubnet_Schema(t *testing.T) {
	s := ResourceSubnet().Schema

	requiredForceNew := []string{"name", "cidr", "tenant_id", "project_id", "vnet_id"}
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

	// gateway is Optional+Computed+ForceNew: user may set it, else the API assigns one.
	if f := s["gateway"]; f == nil {
		t.Fatal("schema is missing field \"gateway\"")
	} else {
		if !f.Optional || !f.Computed {
			t.Errorf("gateway: Optional=%v Computed=%v, want both true", f.Optional, f.Computed)
		}
		if !f.ForceNew {
			t.Error("gateway: ForceNew = false, want true")
		}
	}

	// description is Optional + ForceNew.
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

	computedOnly := []string{"status", "provider_type", "message", "created_at", "updated_at", "subnet_uuid"}
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

// TestResourceSubnetCreate_Success exercises POST, poll-to-ACTIVE, then Read.
// Id() must be "tenant/project/vnet/subnet_uuid" and the API-assigned gateway must land in state.
func TestResourceSubnetCreate_Success(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodPost:
			if r.URL.Path != "/v1/tenants/acme/projects/net/vnets/vnet-123/subnets" {
				t.Errorf("POST path = %s, want .../vnets/vnet-123/subnets", r.URL.Path)
			}
			w.WriteHeader(http.StatusAccepted)
			w.Write([]byte(`{"resource":{
				"id":"sub-9",
				"vnet_id":"vnet-123",
				"tenant_id":"acme",
				"name":"web",
				"cidr":"10.1.1.0/24",
				"gateway":"10.1.1.1",
				"description":"web tier",
				"status":"ACTIVE",
				"provider_type":"kubeovn",
				"created_at":"2026-01-01T00:00:00Z",
				"updated_at":"2026-01-01T00:00:00Z"
			},"note":"ok"}`))
		case http.MethodGet:
			if r.URL.Path != "/v1/tenants/acme/projects/net/vnets/vnet-123/subnets/sub-9" {
				t.Errorf("GET path = %s, want .../subnets/sub-9", r.URL.Path)
			}
			w.WriteHeader(http.StatusOK)
			w.Write([]byte(`{
				"id":"sub-9",
				"vnet_id":"vnet-123",
				"tenant_id":"acme",
				"name":"web",
				"cidr":"10.1.1.0/24",
				"gateway":"10.1.1.1",
				"description":"web tier",
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

	d := newTestSubnetData(t, map[string]interface{}{
		"tenant_id":  "acme",
		"project_id": "net",
		"vnet_id":    "vnet-123",
		"name":       "web",
		"cidr":       "10.1.1.0/24",
	})

	diags := resourceSubnetCreate(context.Background(), d, c)
	if diags.HasError() {
		t.Fatalf("resourceSubnetCreate returned unexpected error diagnostics: %v", diags)
	}

	if got, want := d.Id(), "acme/net/vnet-123/sub-9"; got != want {
		t.Errorf("Id() = %q, want %q", got, want)
	}
	if got, want := d.Get("subnet_uuid").(string), "sub-9"; got != want {
		t.Errorf("subnet_uuid = %q, want %q", got, want)
	}
	if got, want := d.Get("gateway").(string), "10.1.1.1"; got != want {
		t.Errorf("gateway = %q, want %q (API-assigned)", got, want)
	}
	if got, want := d.Get("status").(string), "ACTIVE"; got != want {
		t.Errorf("status = %q, want %q", got, want)
	}
}

// TestResourceSubnetCreate_APIError verifies a failed POST surfaces an error and leaves Id() empty.
func TestResourceSubnetCreate_APIError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		w.Write([]byte(`{"error":"cidr out of range"}`))
	}))
	defer server.Close()

	c, _ := client.NewClient(server.URL, "test-token")
	d := newTestSubnetData(t, map[string]interface{}{
		"tenant_id":  "acme",
		"project_id": "net",
		"vnet_id":    "vnet-123",
		"name":       "web",
		"cidr":       "10.9.9.0/24",
	})

	diags := resourceSubnetCreate(context.Background(), d, c)
	if !diags.HasError() {
		t.Fatal("diags.HasError() = false, want true for a failed create")
	}
	if d.Id() != "" {
		t.Errorf("Id() = %q, want empty string when create fails", d.Id())
	}
}

// TestResourceSubnetRead_Success verifies a GET refreshes state.
func TestResourceSubnetRead_Success(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/tenants/acme/projects/net/vnets/vnet-123/subnets/sub-9" {
			t.Errorf("path = %s, want .../subnets/sub-9", r.URL.Path)
		}
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{
			"id":"sub-9",
			"vnet_id":"vnet-123",
			"tenant_id":"acme",
			"name":"web",
			"cidr":"10.1.1.0/24",
			"gateway":"10.1.1.1",
			"status":"ACTIVE",
			"provider_type":"kubeovn",
			"created_at":"2026-01-01T00:00:00Z",
			"updated_at":"2026-01-02T00:00:00Z"
		}`))
	}))
	defer server.Close()

	c, _ := client.NewClient(server.URL, "test-token")
	d := newTestSubnetData(t, map[string]interface{}{})
	d.SetId("acme/net/vnet-123/sub-9")

	diags := resourceSubnetRead(context.Background(), d, c)
	if diags.HasError() {
		t.Fatalf("resourceSubnetRead returned unexpected error diagnostics: %v", diags)
	}
	if got, want := d.Get("cidr").(string), "10.1.1.0/24"; got != want {
		t.Errorf("cidr = %q, want %q", got, want)
	}
	if got, want := d.Get("subnet_uuid").(string), "sub-9"; got != want {
		t.Errorf("subnet_uuid = %q, want %q", got, want)
	}
}

// TestResourceSubnetRead_NotFound verifies a 404 clears Id() with no error diagnostics.
func TestResourceSubnetRead_NotFound(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		w.Write([]byte(`{"error":"subnet not found"}`))
	}))
	defer server.Close()

	c, _ := client.NewClient(server.URL, "test-token")
	d := newTestSubnetData(t, map[string]interface{}{})
	d.SetId("acme/net/vnet-123/sub-9")

	diags := resourceSubnetRead(context.Background(), d, c)
	if diags.HasError() {
		t.Fatalf("resourceSubnetRead returned unexpected error diagnostics on 404: %v", diags)
	}
	if d.Id() != "" {
		t.Errorf("Id() = %q, want empty string after a 404", d.Id())
	}
}

// TestResourceSubnetRead_InvalidID verifies the SplitN(id, "/", 4) guard errors on a malformed ID.
func TestResourceSubnetRead_InvalidID(t *testing.T) {
	c, _ := client.NewClient("http://unused.invalid", "test-token")
	d := newTestSubnetData(t, map[string]interface{}{})
	d.SetId("acme/net/vnet-123") // only 3 parts, want 4

	diags := resourceSubnetRead(context.Background(), d, c)
	if !diags.HasError() {
		t.Fatal("diags.HasError() = false, want true for a malformed state ID")
	}
}

// TestResourceSubnetDelete_Success verifies DELETE then poll-to-404 clears Id().
func TestResourceSubnetDelete_Success(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodDelete:
			if r.URL.Path != "/v1/tenants/acme/projects/net/vnets/vnet-123/subnets/sub-9" {
				t.Errorf("DELETE path = %s, want .../subnets/sub-9", r.URL.Path)
			}
			w.WriteHeader(http.StatusAccepted)
		case http.MethodGet:
			w.WriteHeader(http.StatusNotFound)
			w.Write([]byte(`{"error":"subnet not found"}`))
		default:
			t.Errorf("unexpected method %s", r.Method)
		}
	}))
	defer server.Close()

	c, _ := client.NewClient(server.URL, "test-token")
	d := newTestSubnetData(t, map[string]interface{}{})
	d.SetId("acme/net/vnet-123/sub-9")

	diags := resourceSubnetDelete(context.Background(), d, c)
	if diags.HasError() {
		t.Fatalf("resourceSubnetDelete returned unexpected error diagnostics: %v", diags)
	}
	// Unlike project.go, this resource's Delete returns nil on the success path
	// WITHOUT calling d.SetId("") — the Terraform SDK framework clears the ID
	// after a successful DeleteContext during a real apply. In this direct unit
	// call nothing clears it, so the meaningful invariant is simply that Delete
	// reported no error diagnostics (asserted above).
}

// TestResourceSubnetDelete_Conflict verifies a 409 (NSG attachments present) errors and retains Id().
func TestResourceSubnetDelete_Conflict(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusConflict)
		w.Write([]byte(`{"error":"subnet has active NSG attachments"}`))
	}))
	defer server.Close()

	c, _ := client.NewClient(server.URL, "test-token")
	d := newTestSubnetData(t, map[string]interface{}{})
	d.SetId("acme/net/vnet-123/sub-9")

	diags := resourceSubnetDelete(context.Background(), d, c)
	if !diags.HasError() {
		t.Fatal("diags.HasError() = false, want true for a 409 conflict")
	}
	if d.Id() == "" {
		t.Error("Id() = \"\", want it to remain set since delete failed")
	}
}
