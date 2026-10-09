// Unit tests for key_vault.go (data source).
//
// dcapi_key_vault lists GET .../keyvaults (bare array) and filters by name. Match sets a
// three-part Id "tenant_id/project_id/kv_uuid". Credentials are deliberately never
// exposed by this data source. No match is a hard error.
package datasources

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/hashicorp/terraform-plugin-sdk/v2/helper/schema"
	"terraform-provider-dcapi/internal/client"
)

func newTestKeyVaultDSData(t *testing.T, raw map[string]interface{}) *schema.ResourceData {
	t.Helper()
	return schema.TestResourceDataRaw(t, DataSourceKeyVault().Schema, raw)
}

// TestDataSourceKeyVault_Schema pins the Required lookups and Computed outputs, and
// confirms credential fields are absent (never exposed by a read-only data source).
func TestDataSourceKeyVault_Schema(t *testing.T) {
	s := DataSourceKeyVault().Schema

	for _, key := range []string{"tenant_id", "project_id", "name"} {
		if f := s[key]; f == nil || !f.Required {
			t.Errorf("%s: want a Required lookup field, got %+v", key, f)
		}
	}
	for _, key := range []string{"kv_uuid", "soft_delete_days", "status", "message",
		"mount_path", "endpoint_address", "endpoint_port", "created_at", "updated_at"} {
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
	for _, key := range []string{"role_id", "secret_id"} {
		if s[key] != nil {
			t.Errorf("schema exposes %q, want it absent (credentials must not be surfaced)", key)
		}
	}
}

// TestDataSourceKeyVaultRead_Success verifies the name filter, composite Id, and that
// both string and int (endpoint_port) computed fields are copied.
func TestDataSourceKeyVaultRead_Success(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/tenants/acme/projects/infra/keyvaults" {
			t.Errorf("path = %s, want .../keyvaults", r.URL.Path)
		}
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`[
			{"id":"kv-other","name":"other"},
			{"id":"kv-7","name":"secrets","soft_delete_days":30,"status":"ACTIVE","message":"ok",
			 "mount_path":"kv/acme","endpoint_address":"openbao.svc","endpoint_port":8200,
			 "created_at":"2026-01-01T00:00:00Z","updated_at":"2026-01-02T00:00:00Z"}
		]`))
	}))
	defer server.Close()

	c, _ := client.NewClient(server.URL, "test-token")
	d := newTestKeyVaultDSData(t, map[string]interface{}{
		"tenant_id": "acme", "project_id": "infra", "name": "secrets"})

	diags := dataSourceKeyVaultRead(context.Background(), d, c)
	if diags.HasError() {
		t.Fatalf("dataSourceKeyVaultRead returned unexpected error diagnostics: %v", diags)
	}
	if got, want := d.Id(), "acme/infra/kv-7"; got != want {
		t.Errorf("Id() = %q, want %q", got, want)
	}
	if got, want := d.Get("kv_uuid").(string), "kv-7"; got != want {
		t.Errorf("kv_uuid = %q, want %q", got, want)
	}
	if got, want := d.Get("endpoint_port").(int), 8200; got != want {
		t.Errorf("endpoint_port = %d, want %d", got, want)
	}
}

// TestDataSourceKeyVaultRead_NotFound verifies an unmatched name is a hard error.
func TestDataSourceKeyVaultRead_NotFound(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`[{"id":"kv-other","name":"other"}]`))
	}))
	defer server.Close()

	c, _ := client.NewClient(server.URL, "test-token")
	d := newTestKeyVaultDSData(t, map[string]interface{}{
		"tenant_id": "acme", "project_id": "infra", "name": "ghost"})

	diags := dataSourceKeyVaultRead(context.Background(), d, c)
	if !diags.HasError() {
		t.Fatal("diags.HasError() = false, want true for a key vault name not in the list")
	}
}

// TestDataSourceKeyVaultRead_APIError verifies a failed list request surfaces as an error.
func TestDataSourceKeyVaultRead_APIError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte(`{"error":"boom"}`))
	}))
	defer server.Close()

	c, _ := client.NewClient(server.URL, "test-token")
	d := newTestKeyVaultDSData(t, map[string]interface{}{
		"tenant_id": "acme", "project_id": "infra", "name": "secrets"})

	diags := dataSourceKeyVaultRead(context.Background(), d, c)
	if !diags.HasError() {
		t.Fatal("diags.HasError() = false, want true for a 500 response")
	}
}
