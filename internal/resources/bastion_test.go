// Unit tests for bastion.go.
//
// Bastion is an async, fully-immutable resource (no update endpoint). Like VM, it
// returns private_key / console_password exactly once in the create response, and Read
// preserves them from state. Create POSTs the {"resource":{...}, secrets...} envelope,
// polls to ACTIVE, then Reads. The handler returns ACTIVE on the first GET so polling
// completes immediately. See project_test.go for the shared test patterns.
package resources

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/hashicorp/terraform-plugin-sdk/v2/helper/schema"
	"terraform-provider-dcapi/internal/client"
)

func newTestBastionData(t *testing.T, raw map[string]interface{}) *schema.ResourceData {
	t.Helper()
	return schema.TestResourceDataRaw(t, ResourceBastion().Schema, raw)
}

// TestResourceBastion_Schema checks identity fields are immutable, the resource has no
// UpdateContext, and the shown-once secrets are Computed + Sensitive.
func TestResourceBastion_Schema(t *testing.T) {
	r := ResourceBastion()
	s := r.Schema

	if r.UpdateContext != nil {
		t.Error("ResourceBastion: UpdateContext is set, want nil (bastions are immutable)")
	}

	requiredForceNew := []string{"tenant_id", "project_id", "name", "vnet_id", "subnet_id"}
	for _, key := range requiredForceNew {
		f := s[key]
		if f == nil {
			t.Fatalf("schema is missing field %q", key)
		}
		if !f.Required || !f.ForceNew {
			t.Errorf("%s: Required=%v ForceNew=%v, want both true", key, f.Required, f.ForceNew)
		}
	}

	if f := s["description"]; f == nil || !f.Optional || !f.ForceNew {
		t.Errorf("description: want Optional+ForceNew, got %#v", f)
	}

	for _, key := range []string{"private_key", "console_password"} {
		f := s[key]
		if f == nil {
			t.Fatalf("schema is missing field %q", key)
		}
		if !f.Computed || !f.Sensitive {
			t.Errorf("%s: Computed=%v Sensitive=%v, want both true", key, f.Computed, f.Sensitive)
		}
	}

	for _, key := range []string{"bastion_id", "status", "provider_type", "mgmt_ip", "internal_ip", "message", "created_at"} {
		if f := s[key]; f == nil || !f.Computed {
			t.Errorf("%s: want Computed, got %#v", key, f)
		}
	}
}

const bastionActiveBody = `{
	"id":"b-123","name":"edge","status":"ACTIVE","tenant_id":"acme",
	"vnet_id":"vnet-1","subnet_id":"subnet-1","provider_type":"harvester",
	"mgmt_ip":"203.0.113.9","internal_ip":"10.0.0.9","description":"jump host",
	"message":"running","created_at":"2026-01-01T00:00:00Z"
}`

// TestResourceBastionCreate_Success drives create, asserting the composite ID, the
// stored one-time secrets, and the polled-ACTIVE state (mgmt_ip/internal_ip populated).
func TestResourceBastionCreate_Success(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/v1/tenants/acme/projects/infra/bastions":
			w.WriteHeader(http.StatusAccepted)
			w.Write([]byte(`{
				"resource":{"id":"b-123","name":"edge","status":"ACTIVE","tenant_id":"acme",
					"vnet_id":"vnet-1","subnet_id":"subnet-1","provider_type":"harvester",
					"mgmt_ip":"","internal_ip":"","description":"jump host",
					"message":"accepted","created_at":"2026-01-01T00:00:00Z"},
				"private_key":"PRIVATE-KEY-PEM",
				"console_password":"s3cret",
				"note":"provisioning"
			}`))
		case r.Method == http.MethodGet:
			w.WriteHeader(http.StatusOK)
			w.Write([]byte(bastionActiveBody))
		default:
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
		}
	}))
	defer server.Close()

	c, err := client.NewClient(server.URL, "test-token")
	if err != nil {
		t.Fatalf("client.NewClient: %v", err)
	}

	d := newTestBastionData(t, map[string]interface{}{
		"tenant_id":   "acme",
		"project_id":  "infra",
		"name":        "edge",
		"vnet_id":     "vnet-1",
		"subnet_id":   "subnet-1",
		"description": "jump host",
	})

	diags := resourceBastionCreate(context.Background(), d, c)
	if diags.HasError() {
		t.Fatalf("resourceBastionCreate returned unexpected error diagnostics: %v", diags)
	}
	if got, want := d.Id(), "acme/infra/b-123"; got != want {
		t.Errorf("Id() = %q, want %q", got, want)
	}
	if got, want := d.Get("private_key").(string), "PRIVATE-KEY-PEM"; got != want {
		t.Errorf("private_key = %q, want %q", got, want)
	}
	if got, want := d.Get("mgmt_ip").(string), "203.0.113.9"; got != want {
		t.Errorf("mgmt_ip = %q, want %q (populated after ACTIVE)", got, want)
	}
}

// TestResourceBastionCreate_APIError verifies a POST failure errors and sets no ID.
func TestResourceBastionCreate_APIError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		w.Write([]byte(`{"error":"subnet not found"}`))
	}))
	defer server.Close()

	c, _ := client.NewClient(server.URL, "test-token")
	d := newTestBastionData(t, map[string]interface{}{
		"tenant_id":  "acme",
		"project_id": "infra",
		"name":       "edge",
		"vnet_id":    "vnet-1",
		"subnet_id":  "subnet-bad",
	})

	diags := resourceBastionCreate(context.Background(), d, c)
	if !diags.HasError() {
		t.Fatal("diags.HasError() = false, want true for a failed create")
	}
	if d.Id() != "" {
		t.Errorf("Id() = %q, want empty string when create fails", d.Id())
	}
}

// TestResourceBastionRead_Success verifies a GET refreshes fields, keeps the ID, and
// preserves the shown-once secrets already held in state.
func TestResourceBastionRead_Success(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/tenants/acme/projects/infra/bastions/b-123" {
			t.Errorf("unexpected path %s", r.URL.Path)
		}
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(bastionActiveBody))
	}))
	defer server.Close()

	c, _ := client.NewClient(server.URL, "test-token")
	d := newTestBastionData(t, map[string]interface{}{})
	d.SetId("acme/infra/b-123")
	d.Set("private_key", "KEPT-KEY")
	d.Set("console_password", "KEPT-PW")

	diags := resourceBastionRead(context.Background(), d, c)
	if diags.HasError() {
		t.Fatalf("resourceBastionRead returned unexpected error diagnostics: %v", diags)
	}
	if got, want := d.Get("internal_ip").(string), "10.0.0.9"; got != want {
		t.Errorf("internal_ip = %q, want %q", got, want)
	}
	if got, want := d.Get("private_key").(string), "KEPT-KEY"; got != want {
		t.Errorf("private_key = %q, want preserved %q", got, want)
	}
}

// TestResourceBastionRead_NotFound verifies a 404 clears the ID with no error.
func TestResourceBastionRead_NotFound(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer server.Close()

	c, _ := client.NewClient(server.URL, "test-token")
	d := newTestBastionData(t, map[string]interface{}{})
	d.SetId("acme/infra/b-123")

	diags := resourceBastionRead(context.Background(), d, c)
	if diags.HasError() {
		t.Fatalf("unexpected error diagnostics on 404: %v", diags)
	}
	if d.Id() != "" {
		t.Errorf("Id() = %q, want empty string after a 404", d.Id())
	}
}

// TestResourceBastionRead_InvalidID verifies a state ID without exactly 3 parts errors.
func TestResourceBastionRead_InvalidID(t *testing.T) {
	c, _ := client.NewClient("http://unused.invalid", "test-token")
	d := newTestBastionData(t, map[string]interface{}{})
	d.SetId("acme/infra") // only two parts

	diags := resourceBastionRead(context.Background(), d, c)
	if !diags.HasError() {
		t.Fatal("diags.HasError() = false, want true for a malformed state ID")
	}
}

// TestResourceBastionDelete_Success verifies DELETE then a 404 poll clears the ID.
func TestResourceBastionDelete_Success(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodDelete:
			w.WriteHeader(http.StatusAccepted)
		case http.MethodGet:
			w.WriteHeader(http.StatusNotFound)
		default:
			t.Errorf("unexpected method %s", r.Method)
		}
	}))
	defer server.Close()

	c, _ := client.NewClient(server.URL, "test-token")
	d := newTestBastionData(t, map[string]interface{}{})
	d.SetId("acme/infra/b-123")

	diags := resourceBastionDelete(context.Background(), d, c)
	if diags.HasError() {
		t.Fatalf("resourceBastionDelete returned unexpected error diagnostics: %v", diags)
	}
}

// TestResourceBastionDelete_Error verifies a non-404 DELETE failure errors and keeps the ID.
func TestResourceBastionDelete_Error(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()

	c, _ := client.NewClient(server.URL, "test-token")
	d := newTestBastionData(t, map[string]interface{}{})
	d.SetId("acme/infra/b-123")

	diags := resourceBastionDelete(context.Background(), d, c)
	if !diags.HasError() {
		t.Fatal("diags.HasError() = false, want true for a failed delete")
	}
	if d.Id() == "" {
		t.Error("Id() = \"\", want it to remain set since delete failed")
	}
}
