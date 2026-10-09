// Unit tests for key_vault_secret.go.
//
// See project_test.go for the patterns reused here (schema.TestResourceDataRaw +
// httptest.NewServer + a real *client.DCAPIClient). KeyVaultSecret has two quirks
// worth noting:
//
//   - Create and Update both go through the SAME upsert PUT (there is no POST and no
//     partial-update endpoint), so the "create" method asserted below is PUT, not POST.
//   - The composite state ID packs FOUR path components: "tenant_id/project_id/key_vault_id/key".
package resources

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/hashicorp/terraform-plugin-sdk/v2/helper/schema"
	"terraform-provider-dcapi/internal/client"
)

// newTestKeyVaultSecretData builds a *schema.ResourceData for ResourceKeyVaultSecret().
func newTestKeyVaultSecretData(t *testing.T, raw map[string]interface{}) *schema.ResourceData {
	t.Helper()
	return schema.TestResourceDataRaw(t, ResourceKeyVaultSecret().Schema, raw)
}

// TestResourceKeyVaultSecret_Schema locks down the schema flags the rest of the file
// relies on: the four path components are Required+ForceNew, value is Required and
// updatable (NOT ForceNew), metadata is Optional, and version/created_at are API-only.
func TestResourceKeyVaultSecret_Schema(t *testing.T) {
	s := ResourceKeyVaultSecret().Schema

	immutableRequired := []string{"tenant_id", "project_id", "key_vault_id", "key"}
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

	// value is a Required secret that is updatable in place via the upsert PUT.
	if f := s["value"]; f == nil {
		t.Fatal("schema is missing field \"value\"")
	} else {
		if !f.Required {
			t.Error("value: Required = false, want true")
		}
		if f.ForceNew {
			t.Error("value: ForceNew = true, want false (updatable via PUT)")
		}
		if !f.Sensitive {
			t.Error("value: Sensitive = false, want true")
		}
	}

	if f := s["metadata"]; f == nil {
		t.Fatal("schema is missing field \"metadata\"")
	} else if !f.Optional {
		t.Error("metadata: Optional = false, want true")
	}

	computedOnly := []string{"version", "created_at"}
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

// TestResourceKeyVaultSecretCreate_Success verifies the upsert PUT populates the
// four-part composite ID and copies version/created_at into state.
func TestResourceKeyVaultSecretCreate_Success(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPut {
			t.Errorf("method = %s, want PUT", r.Method)
		}
		if r.URL.Path != "/v1/tenants/acme/projects/infra/keyvaults/kv-1/secrets/db-password" {
			t.Errorf("unexpected path = %s", r.URL.Path)
		}
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{
			"key": "db-password",
			"value": "s3cr3t",
			"version": 1,
			"metadata": {"env": "prod"},
			"created_at": "2026-01-01T00:00:00Z",
			"deleted_at": null
		}`))
	}))
	defer server.Close()

	c, err := client.NewClient(server.URL, "test-token")
	if err != nil {
		t.Fatalf("client.NewClient: %v", err)
	}

	d := newTestKeyVaultSecretData(t, map[string]interface{}{
		"tenant_id":    "acme",
		"project_id":   "infra",
		"key_vault_id": "kv-1",
		"key":          "db-password",
		"value":        "s3cr3t",
	})

	diags := resourceKeyVaultSecretCreate(context.Background(), d, c)
	if diags.HasError() {
		t.Fatalf("resourceKeyVaultSecretCreate returned unexpected error diagnostics: %v", diags)
	}

	if got, want := d.Id(), "acme/infra/kv-1/db-password"; got != want {
		t.Errorf("Id() = %q, want %q", got, want)
	}
	if got, want := d.Get("version").(int), 1; got != want {
		t.Errorf("version = %d, want %d", got, want)
	}
	if got, want := d.Get("created_at").(string), "2026-01-01T00:00:00Z"; got != want {
		t.Errorf("created_at = %q, want %q", got, want)
	}
}

// TestResourceKeyVaultSecretCreate_APIError verifies a non-2xx leaves Id() empty.
func TestResourceKeyVaultSecretCreate_APIError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		w.Write([]byte(`{"error":"keyvault not ACTIVE"}`))
	}))
	defer server.Close()

	c, _ := client.NewClient(server.URL, "test-token")
	d := newTestKeyVaultSecretData(t, map[string]interface{}{
		"tenant_id":    "acme",
		"project_id":   "infra",
		"key_vault_id": "kv-1",
		"key":          "db-password",
		"value":        "s3cr3t",
	})

	diags := resourceKeyVaultSecretCreate(context.Background(), d, c)
	if !diags.HasError() {
		t.Fatal("diags.HasError() = false, want true for a failed create")
	}
	if d.Id() != "" {
		t.Errorf("Id() = %q, want empty string when create fails", d.Id())
	}
}

// TestResourceKeyVaultSecretRead_Success verifies a GET refreshes value/metadata/version.
func TestResourceKeyVaultSecretRead_Success(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/tenants/acme/projects/infra/keyvaults/kv-1/secrets/db-password" {
			t.Errorf("unexpected path = %s", r.URL.Path)
		}
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{
			"key": "db-password",
			"value": "s3cr3t",
			"version": 3,
			"metadata": {"env": "prod"},
			"created_at": "2026-01-01T00:00:00Z",
			"deleted_at": null
		}`))
	}))
	defer server.Close()

	c, _ := client.NewClient(server.URL, "test-token")
	d := newTestKeyVaultSecretData(t, map[string]interface{}{})
	d.SetId("acme/infra/kv-1/db-password")

	diags := resourceKeyVaultSecretRead(context.Background(), d, c)
	if diags.HasError() {
		t.Fatalf("resourceKeyVaultSecretRead returned unexpected error diagnostics: %v", diags)
	}
	if got, want := d.Get("value").(string), "s3cr3t"; got != want {
		t.Errorf("value = %q, want %q", got, want)
	}
	if got, want := d.Get("version").(int), 3; got != want {
		t.Errorf("version = %d, want %d", got, want)
	}
	if got, want := d.Get("key").(string), "db-password"; got != want {
		t.Errorf("key = %q, want %q", got, want)
	}
}

// TestResourceKeyVaultSecretRead_NotFound verifies a 404 (or 410) clears the ID without error.
func TestResourceKeyVaultSecretRead_NotFound(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		w.Write([]byte(`{"error":"secret not found"}`))
	}))
	defer server.Close()

	c, _ := client.NewClient(server.URL, "test-token")
	d := newTestKeyVaultSecretData(t, map[string]interface{}{})
	d.SetId("acme/infra/kv-1/db-password")

	diags := resourceKeyVaultSecretRead(context.Background(), d, c)
	if diags.HasError() {
		t.Fatalf("resourceKeyVaultSecretRead returned unexpected error diagnostics on 404: %v", diags)
	}
	if d.Id() != "" {
		t.Errorf("Id() = %q, want empty string after a 404", d.Id())
	}
}

// TestResourceKeyVaultSecretRead_InvalidID verifies a state ID without exactly four
// slash-separated parts errors instead of panicking on the SplitN result.
func TestResourceKeyVaultSecretRead_InvalidID(t *testing.T) {
	c, _ := client.NewClient("http://unused.invalid", "test-token")
	d := newTestKeyVaultSecretData(t, map[string]interface{}{})
	d.SetId("too/few/parts")

	diags := resourceKeyVaultSecretRead(context.Background(), d, c)
	if !diags.HasError() {
		t.Fatal("diags.HasError() = false, want true for a malformed state ID")
	}
}

// TestResourceKeyVaultSecretUpdate_Success verifies Update re-sends the upsert PUT and
// refreshes the bumped version into state.
func TestResourceKeyVaultSecretUpdate_Success(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPut {
			t.Errorf("method = %s, want PUT", r.Method)
		}
		if r.URL.Path != "/v1/tenants/acme/projects/infra/keyvaults/kv-1/secrets/db-password" {
			t.Errorf("unexpected path = %s", r.URL.Path)
		}
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{
			"key": "db-password",
			"value": "newer",
			"version": 4,
			"metadata": {},
			"created_at": "2026-01-02T00:00:00Z",
			"deleted_at": null
		}`))
	}))
	defer server.Close()

	c, _ := client.NewClient(server.URL, "test-token")
	d := newTestKeyVaultSecretData(t, map[string]interface{}{
		"tenant_id":    "acme",
		"project_id":   "infra",
		"key_vault_id": "kv-1",
		"key":          "db-password",
		"value":        "newer",
	})
	d.SetId("acme/infra/kv-1/db-password")

	diags := resourceKeyVaultSecretUpdate(context.Background(), d, c)
	if diags.HasError() {
		t.Fatalf("resourceKeyVaultSecretUpdate returned unexpected error diagnostics: %v", diags)
	}
	if got, want := d.Get("version").(int), 4; got != want {
		t.Errorf("version = %d, want %d", got, want)
	}
}

// TestResourceKeyVaultSecretDelete_Success verifies a successful DELETE clears the ID.
func TestResourceKeyVaultSecretDelete_Success(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodDelete {
			t.Errorf("method = %s, want DELETE", r.Method)
		}
		if r.URL.Path != "/v1/tenants/acme/projects/infra/keyvaults/kv-1/secrets/db-password" {
			t.Errorf("unexpected path = %s", r.URL.Path)
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()

	c, _ := client.NewClient(server.URL, "test-token")
	d := newTestKeyVaultSecretData(t, map[string]interface{}{})
	d.SetId("acme/infra/kv-1/db-password")

	diags := resourceKeyVaultSecretDelete(context.Background(), d, c)
	if diags.HasError() {
		t.Fatalf("resourceKeyVaultSecretDelete returned unexpected error diagnostics: %v", diags)
	}
	if d.Id() != "" {
		t.Errorf("Id() = %q, want empty string after a successful delete", d.Id())
	}
}

// TestResourceKeyVaultSecretDelete_NotFoundIsSuccess verifies a 404 on delete is treated
// as success (the secret is already gone) and clears the ID.
func TestResourceKeyVaultSecretDelete_NotFoundIsSuccess(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		w.Write([]byte(`{"error":"secret not found"}`))
	}))
	defer server.Close()

	c, _ := client.NewClient(server.URL, "test-token")
	d := newTestKeyVaultSecretData(t, map[string]interface{}{})
	d.SetId("acme/infra/kv-1/db-password")

	diags := resourceKeyVaultSecretDelete(context.Background(), d, c)
	if diags.HasError() {
		t.Fatalf("resourceKeyVaultSecretDelete returned unexpected error diagnostics on 404: %v", diags)
	}
	if d.Id() != "" {
		t.Errorf("Id() = %q, want empty string after a 404 delete", d.Id())
	}
}

// TestResourceKeyVaultSecretDelete_Error verifies a non-404/410 failure surfaces as an
// error and keeps the ID so Terraform keeps tracking the resource.
func TestResourceKeyVaultSecretDelete_Error(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte(`{"error":"backend unavailable"}`))
	}))
	defer server.Close()

	c, _ := client.NewClient(server.URL, "test-token")
	d := newTestKeyVaultSecretData(t, map[string]interface{}{})
	d.SetId("acme/infra/kv-1/db-password")

	diags := resourceKeyVaultSecretDelete(context.Background(), d, c)
	if !diags.HasError() {
		t.Fatal("diags.HasError() = false, want true for a 500 on delete")
	}
	if d.Id() == "" {
		t.Error("Id() = \"\", want it to remain set since delete failed")
	}
}
