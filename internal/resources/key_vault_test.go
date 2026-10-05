// Unit tests for key_vault.go.
//
// KeyVault is async-on-create (poll to ACTIVE) but synchronous-on-delete (204, no poll).
// Three traits make it distinct:
//  1. Create returns a BARE body (no "resource" wrapper).
//  2. Credentials (role_id/secret_id) come from a SEPARATE GET .../credentials endpoint,
//     fetched exactly once right after the vault is ACTIVE; Read preserves them from state.
//  3. It has an UpdateContext, but the ONLY in-place change is credentials_rotation, which
//     triggers POST .../credentials/rotate; every other field is ForceNew.
//
// Handlers return status "ACTIVE" on the first GET so waitForKeyVaultActive completes on
// its first poll. See project_test.go for the shared test patterns.
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

func newTestKeyVaultData(t *testing.T, raw map[string]interface{}) *schema.ResourceData {
	t.Helper()
	return schema.TestResourceDataRaw(t, ResourceKeyVault().Schema, raw)
}

// TestResourceKeyVault_Schema checks identity fields are immutable, credentials_rotation
// is the only updatable field, the resource exposes an UpdateContext, and secret_id is
// Computed + Sensitive.
func TestResourceKeyVault_Schema(t *testing.T) {
	r := ResourceKeyVault()
	s := r.Schema

	if r.UpdateContext == nil {
		t.Error("ResourceKeyVault: UpdateContext is nil, want set (credentials rotation)")
	}

	requiredForceNew := []string{"tenant_id", "project_id", "name"}
	for _, key := range requiredForceNew {
		f := s[key]
		if f == nil {
			t.Fatalf("schema is missing field %q", key)
		}
		if !f.Required || !f.ForceNew {
			t.Errorf("%s: Required=%v ForceNew=%v, want both true", key, f.Required, f.ForceNew)
		}
	}

	if f := s["soft_delete_days"]; f == nil || !f.Optional || !f.ForceNew {
		t.Errorf("soft_delete_days: want Optional+ForceNew, got %#v", f)
	}
	// credentials_rotation drives the only in-place update path — must NOT be ForceNew.
	if f := s["credentials_rotation"]; f == nil || !f.Optional || f.ForceNew {
		t.Errorf("credentials_rotation: want Optional+not-ForceNew, got %#v", f)
	}

	if f := s["secret_id"]; f == nil || !f.Computed || !f.Sensitive {
		t.Errorf("secret_id: want Computed+Sensitive, got %#v", f)
	}
	if f := s["role_id"]; f == nil || !f.Computed {
		t.Errorf("role_id: want Computed, got %#v", f)
	}
}

const keyVaultActiveBody = `{
	"id":"kv-123","tenant_id":"acme","name":"prod-secrets","soft_delete_days":30,
	"status":"ACTIVE","message":"ready","mount_path":"tenant-acme/prod-secrets",
	"endpoint_address":"openbao.svc","endpoint_port":8200,
	"created_at":"2026-01-01T00:00:00Z","updated_at":"2026-01-01T00:00:00Z"
}`

// TestResourceKeyVaultCreate_Success drives the full create path: POST returns the bare
// ACTIVE body, the credentials endpoint returns role_id/secret_id, and the GET poll +
// Read see ACTIVE. The composite ID must be tenant/project/keyvault_id.
func TestResourceKeyVaultCreate_Success(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/v1/tenants/acme/projects/infra/keyvaults":
			w.WriteHeader(http.StatusCreated)
			w.Write([]byte(keyVaultActiveBody))
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/credentials"):
			w.WriteHeader(http.StatusOK)
			w.Write([]byte(`{"role_id":"role-1","secret_id":"secret-1","mount_path":"tenant-acme/prod-secrets","backend_address":"openbao.svc","backend_port":"8200"}`))
		case r.Method == http.MethodGet:
			w.WriteHeader(http.StatusOK)
			w.Write([]byte(keyVaultActiveBody))
		default:
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
		}
	}))
	defer server.Close()

	c, err := client.NewClient(server.URL, "test-token")
	if err != nil {
		t.Fatalf("client.NewClient: %v", err)
	}

	d := newTestKeyVaultData(t, map[string]interface{}{
		"tenant_id":  "acme",
		"project_id": "infra",
		"name":       "prod-secrets",
	})

	diags := resourceKeyVaultCreate(context.Background(), d, c)
	if diags.HasError() {
		t.Fatalf("resourceKeyVaultCreate returned unexpected error diagnostics: %v", diags)
	}
	if got, want := d.Id(), "acme/infra/kv-123"; got != want {
		t.Errorf("Id() = %q, want %q", got, want)
	}
	if got, want := d.Get("role_id").(string), "role-1"; got != want {
		t.Errorf("role_id = %q, want %q", got, want)
	}
	if got, want := d.Get("secret_id").(string), "secret-1"; got != want {
		t.Errorf("secret_id = %q, want %q", got, want)
	}
	if got, want := d.Get("mount_path").(string), "tenant-acme/prod-secrets"; got != want {
		t.Errorf("mount_path = %q, want %q", got, want)
	}
}

// TestResourceKeyVaultCreate_APIError verifies a POST failure errors and sets no ID.
func TestResourceKeyVaultCreate_APIError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		w.Write([]byte(`{"error":"name already in use"}`))
	}))
	defer server.Close()

	c, _ := client.NewClient(server.URL, "test-token")
	d := newTestKeyVaultData(t, map[string]interface{}{
		"tenant_id":  "acme",
		"project_id": "infra",
		"name":       "prod-secrets",
	})

	diags := resourceKeyVaultCreate(context.Background(), d, c)
	if !diags.HasError() {
		t.Fatal("diags.HasError() = false, want true for a failed create")
	}
	if d.Id() != "" {
		t.Errorf("Id() = %q, want empty string when create fails", d.Id())
	}
}

// TestResourceKeyVaultRead_Success verifies a GET refreshes fields, keeps the ID, and
// preserves the one-time credentials already held in state (the GET body has none).
func TestResourceKeyVaultRead_Success(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/tenants/acme/projects/infra/keyvaults/kv-123" {
			t.Errorf("unexpected path %s", r.URL.Path)
		}
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(keyVaultActiveBody))
	}))
	defer server.Close()

	c, _ := client.NewClient(server.URL, "test-token")
	d := newTestKeyVaultData(t, map[string]interface{}{})
	d.SetId("acme/infra/kv-123")
	d.Set("role_id", "role-kept")
	d.Set("secret_id", "secret-kept")

	diags := resourceKeyVaultRead(context.Background(), d, c)
	if diags.HasError() {
		t.Fatalf("resourceKeyVaultRead returned unexpected error diagnostics: %v", diags)
	}
	if got, want := d.Get("status").(string), "ACTIVE"; got != want {
		t.Errorf("status = %q, want %q", got, want)
	}
	if got, want := d.Get("endpoint_port").(int), 8200; got != want {
		t.Errorf("endpoint_port = %d, want %d", got, want)
	}
	if got, want := d.Get("secret_id").(string), "secret-kept"; got != want {
		t.Errorf("secret_id = %q, want preserved %q", got, want)
	}
}

// TestResourceKeyVaultRead_NotFound verifies a 404 clears the ID with no error.
func TestResourceKeyVaultRead_NotFound(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer server.Close()

	c, _ := client.NewClient(server.URL, "test-token")
	d := newTestKeyVaultData(t, map[string]interface{}{})
	d.SetId("acme/infra/kv-123")

	diags := resourceKeyVaultRead(context.Background(), d, c)
	if diags.HasError() {
		t.Fatalf("unexpected error diagnostics on 404: %v", diags)
	}
	if d.Id() != "" {
		t.Errorf("Id() = %q, want empty string after a 404", d.Id())
	}
}

// TestResourceKeyVaultRead_InvalidID verifies a state ID without exactly 3 parts errors.
func TestResourceKeyVaultRead_InvalidID(t *testing.T) {
	c, _ := client.NewClient("http://unused.invalid", "test-token")
	d := newTestKeyVaultData(t, map[string]interface{}{})
	d.SetId("acme/infra") // only two parts

	diags := resourceKeyVaultRead(context.Background(), d, c)
	if !diags.HasError() {
		t.Fatal("diags.HasError() = false, want true for a malformed state ID")
	}
}

// TestResourceKeyVaultUpdate_RotatesCredentials verifies that changing
// credentials_rotation triggers POST .../credentials/rotate and stores the new secret_id.
func TestResourceKeyVaultUpdate_RotatesCredentials(t *testing.T) {
	var rotated bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/credentials/rotate") {
			rotated = true
			w.WriteHeader(http.StatusOK)
			w.Write([]byte(`{"role_id":"role-1","secret_id":"secret-2","mount_path":"tenant-acme/prod-secrets","backend_address":"openbao.svc","backend_port":"8200"}`))
			return
		}
		t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
	}))
	defer server.Close()

	c, _ := client.NewClient(server.URL, "test-token")
	// credentials_rotation present in raw config => HasChange("credentials_rotation") is true.
	d := newTestKeyVaultData(t, map[string]interface{}{
		"tenant_id":            "acme",
		"project_id":           "infra",
		"name":                 "prod-secrets",
		"credentials_rotation": "2026-10-01",
	})
	d.SetId("acme/infra/kv-123")

	diags := resourceKeyVaultUpdate(context.Background(), d, c)
	if diags.HasError() {
		t.Fatalf("resourceKeyVaultUpdate returned unexpected error diagnostics: %v", diags)
	}
	if !rotated {
		t.Error("rotate endpoint was not called")
	}
	if got, want := d.Get("secret_id").(string), "secret-2"; got != want {
		t.Errorf("secret_id = %q, want rotated value %q", got, want)
	}
}

// TestResourceKeyVaultDelete_Success verifies the synchronous DELETE (204, no poll)
// clears the ID.
func TestResourceKeyVaultDelete_Success(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodDelete {
			t.Errorf("method = %s, want DELETE (delete is synchronous, no GET poll)", r.Method)
		}
		if r.URL.Path != "/v1/tenants/acme/projects/infra/keyvaults/kv-123" {
			t.Errorf("unexpected path %s", r.URL.Path)
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()

	c, _ := client.NewClient(server.URL, "test-token")
	d := newTestKeyVaultData(t, map[string]interface{}{})
	d.SetId("acme/infra/kv-123")

	diags := resourceKeyVaultDelete(context.Background(), d, c)
	if diags.HasError() {
		t.Fatalf("resourceKeyVaultDelete returned unexpected error diagnostics: %v", diags)
	}
	if d.Id() != "" {
		t.Errorf("Id() = %q, want empty string after a successful delete", d.Id())
	}
}

// TestResourceKeyVaultDelete_Error verifies a non-404 DELETE failure errors and keeps the ID.
func TestResourceKeyVaultDelete_Error(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()

	c, _ := client.NewClient(server.URL, "test-token")
	d := newTestKeyVaultData(t, map[string]interface{}{})
	d.SetId("acme/infra/kv-123")

	diags := resourceKeyVaultDelete(context.Background(), d, c)
	if !diags.HasError() {
		t.Fatal("diags.HasError() = false, want true for a failed delete")
	}
	if d.Id() == "" {
		t.Error("Id() = \"\", want it to remain set since delete failed")
	}
}
