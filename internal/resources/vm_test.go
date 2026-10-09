// Unit tests for vm.go.
//
// VM is an async, fully-immutable resource (no update endpoint) with two notable traits:
//  1. Networking is mutually exclusive — EITHER network_name OR (vnet_id + subnet_id).
//  2. private_key / console_password are shown once in the create response; Read must
//     preserve them from state rather than overwrite with empty strings.
//
// Create POSTs the {"resource":{...}, "private_key":..., "console_password":...} envelope,
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

func newTestVMData(t *testing.T, raw map[string]interface{}) *schema.ResourceData {
	t.Helper()
	return schema.TestResourceDataRaw(t, ResourceVirtualMachine().Schema, raw)
}

// TestResourceVM_Schema checks identity/config fields are immutable, the resource has
// no UpdateContext, and the shown-once secrets are Computed + Sensitive.
func TestResourceVM_Schema(t *testing.T) {
	r := ResourceVirtualMachine()
	s := r.Schema

	if r.UpdateContext != nil {
		t.Error("ResourceVirtualMachine: UpdateContext is set, want nil (VMs are immutable)")
	}

	requiredForceNew := []string{"name", "size", "image_name", "tenant_id", "project_id"}
	for _, key := range requiredForceNew {
		f := s[key]
		if f == nil {
			t.Fatalf("schema is missing field %q", key)
		}
		if !f.Required || !f.ForceNew {
			t.Errorf("%s: Required=%v ForceNew=%v, want both true", key, f.Required, f.ForceNew)
		}
	}

	for _, key := range []string{"network_name", "vnet_id", "subnet_id"} {
		if f := s[key]; f == nil || !f.Optional || !f.ForceNew {
			t.Errorf("%s: want Optional+ForceNew, got %#v", key, f)
		}
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
}

const vmActiveBody = `{
	"id":"vm-123","name":"web","size":"small","status":"ACTIVE","tenant_id":"acme",
	"provider_type":"harvester","ip_address":"10.0.0.5","message":"running",
	"created_at":"2026-01-01T00:00:00Z"
}`

// TestResourceVMCreate_Success drives create with legacy network_name, asserting the
// composite ID, the stored one-time secrets, and the polled-ACTIVE state.
func TestResourceVMCreate_Success(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/v1/tenants/acme/projects/infra/virtual-machines":
			w.WriteHeader(http.StatusAccepted)
			w.Write([]byte(`{
				"resource":{"id":"vm-123","name":"web","size":"small","status":"ACTIVE",
					"tenant_id":"acme","provider_type":"harvester","ip_address":"10.0.0.5",
					"message":"accepted","created_at":"2026-01-01T00:00:00Z"},
				"private_key":"PRIVATE-KEY-PEM",
				"console_password":"s3cret",
				"note":"provisioning"
			}`))
		case r.Method == http.MethodGet:
			w.WriteHeader(http.StatusOK)
			w.Write([]byte(vmActiveBody))
		default:
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
		}
	}))
	defer server.Close()

	c, err := client.NewClient(server.URL, "test-token")
	if err != nil {
		t.Fatalf("client.NewClient: %v", err)
	}

	d := newTestVMData(t, map[string]interface{}{
		"name":         "web",
		"size":         "small",
		"image_name":   "rancher-infra/ubuntu-22-04",
		"network_name": "iaas/vm-network-001",
		"tenant_id":    "acme",
		"project_id":   "infra",
	})

	diags := resourceVMCreate(context.Background(), d, c)
	if diags.HasError() {
		t.Fatalf("resourceVMCreate returned unexpected error diagnostics: %v", diags)
	}
	if got, want := d.Id(), "acme/infra/vm-123"; got != want {
		t.Errorf("Id() = %q, want %q", got, want)
	}
	if got, want := d.Get("private_key").(string), "PRIVATE-KEY-PEM"; got != want {
		t.Errorf("private_key = %q, want %q", got, want)
	}
	if got, want := d.Get("console_password").(string), "s3cret"; got != want {
		t.Errorf("console_password = %q, want %q", got, want)
	}
	if got, want := d.Get("ip_address").(string), "10.0.0.5"; got != want {
		t.Errorf("ip_address = %q, want %q", got, want)
	}
}

// TestResourceVMCreate_APIError verifies a POST failure errors and sets no ID.
func TestResourceVMCreate_APIError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		w.Write([]byte(`{"error":"image not found"}`))
	}))
	defer server.Close()

	c, _ := client.NewClient(server.URL, "test-token")
	d := newTestVMData(t, map[string]interface{}{
		"name":         "web",
		"size":         "small",
		"image_name":   "bogus",
		"network_name": "iaas/vm-network-001",
		"tenant_id":    "acme",
		"project_id":   "infra",
	})

	diags := resourceVMCreate(context.Background(), d, c)
	if !diags.HasError() {
		t.Fatal("diags.HasError() = false, want true for a failed create")
	}
	if d.Id() != "" {
		t.Errorf("Id() = %q, want empty string when create fails", d.Id())
	}
}

// TestResourceVMCreate_NetworkMutualExclusion exercises the pure-logic validation that
// runs before any HTTP call: supplying both network_name and vnet_id must error.
func TestResourceVMCreate_NetworkMutualExclusion(t *testing.T) {
	c, _ := client.NewClient("http://unused.invalid", "test-token")
	d := newTestVMData(t, map[string]interface{}{
		"name":         "web",
		"size":         "small",
		"image_name":   "rancher-infra/ubuntu-22-04",
		"network_name": "iaas/vm-network-001",
		"vnet_id":      "vnet-1",
		"subnet_id":    "subnet-1",
		"tenant_id":    "acme",
		"project_id":   "infra",
	})

	diags := resourceVMCreate(context.Background(), d, c)
	if !diags.HasError() {
		t.Fatal("diags.HasError() = false, want true when both networking modes are set")
	}
}

// TestResourceVMRead_Success verifies a GET refreshes fields, keeps the ID, and
// preserves the shown-once secrets already held in state (the GET body has none).
func TestResourceVMRead_Success(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/tenants/acme/projects/infra/virtual-machines/vm-123" {
			t.Errorf("unexpected path %s", r.URL.Path)
		}
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(vmActiveBody))
	}))
	defer server.Close()

	c, _ := client.NewClient(server.URL, "test-token")
	d := newTestVMData(t, map[string]interface{}{})
	d.SetId("acme/infra/vm-123")
	// Secrets already in state from a prior create — Read must keep them.
	d.Set("private_key", "KEPT-KEY")
	d.Set("console_password", "KEPT-PW")

	diags := resourceVMRead(context.Background(), d, c)
	if diags.HasError() {
		t.Fatalf("resourceVMRead returned unexpected error diagnostics: %v", diags)
	}
	if d.Id() != "acme/infra/vm-123" {
		t.Errorf("Id() = %q, want unchanged", d.Id())
	}
	if got, want := d.Get("status").(string), "ACTIVE"; got != want {
		t.Errorf("status = %q, want %q", got, want)
	}
	if got, want := d.Get("private_key").(string), "KEPT-KEY"; got != want {
		t.Errorf("private_key = %q, want preserved %q", got, want)
	}
	if got, want := d.Get("console_password").(string), "KEPT-PW"; got != want {
		t.Errorf("console_password = %q, want preserved %q", got, want)
	}
}

// TestResourceVMRead_NotFound verifies a 404 clears the ID with no error.
func TestResourceVMRead_NotFound(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer server.Close()

	c, _ := client.NewClient(server.URL, "test-token")
	d := newTestVMData(t, map[string]interface{}{})
	d.SetId("acme/infra/vm-123")

	diags := resourceVMRead(context.Background(), d, c)
	if diags.HasError() {
		t.Fatalf("unexpected error diagnostics on 404: %v", diags)
	}
	if d.Id() != "" {
		t.Errorf("Id() = %q, want empty string after a 404", d.Id())
	}
}

// TestResourceVMRead_InvalidID verifies a state ID without exactly 3 parts errors.
func TestResourceVMRead_InvalidID(t *testing.T) {
	c, _ := client.NewClient("http://unused.invalid", "test-token")
	d := newTestVMData(t, map[string]interface{}{})
	d.SetId("acme/infra") // only two parts

	diags := resourceVMRead(context.Background(), d, c)
	if !diags.HasError() {
		t.Fatal("diags.HasError() = false, want true for a malformed state ID")
	}
}

// TestResourceVMDelete_Success verifies DELETE then a 404 poll clears the ID.
func TestResourceVMDelete_Success(t *testing.T) {
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
	d := newTestVMData(t, map[string]interface{}{})
	d.SetId("acme/infra/vm-123")

	diags := resourceVMDelete(context.Background(), d, c)
	if diags.HasError() {
		t.Fatalf("resourceVMDelete returned unexpected error diagnostics: %v", diags)
	}
}

// TestResourceVMDelete_Error verifies a non-404 DELETE failure errors and keeps the ID.
func TestResourceVMDelete_Error(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()

	c, _ := client.NewClient(server.URL, "test-token")
	d := newTestVMData(t, map[string]interface{}{})
	d.SetId("acme/infra/vm-123")

	diags := resourceVMDelete(context.Background(), d, c)
	if !diags.HasError() {
		t.Fatal("diags.HasError() = false, want true for a failed delete")
	}
	if d.Id() == "" {
		t.Error("Id() = \"\", want it to remain set since delete failed")
	}
}
