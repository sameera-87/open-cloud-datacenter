// Unit tests for tenant.go.
//
// See project_test.go for the shared patterns. Tenants have a few quirks the tests
// below pin down:
//
//   - The state ID is just the tenant slug (d.SetId(tenant.ID)), not a composite — so
//     there is no SplitN parse and therefore no "invalid ID" test.
//   - There is no GET-by-id endpoint: Read calls GetTenantByID, which GETs the bare
//     /v1/tenants list and scans it, so the Read mocks return a JSON ARRAY.
//   - cpu_cores_cap/memory_gb_cap/storage_gb_cap are Optional+Computed and updatable via
//     PATCH /v1/admin/tenants/{id}; Update only sends the caps that changed.
//   - Delete has no API endpoint — it only clears state, so it never fails.
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

// newTestTenantData builds a *schema.ResourceData for ResourceTenant().
func newTestTenantData(t *testing.T, raw map[string]interface{}) *schema.ResourceData {
	t.Helper()
	return schema.TestResourceDataRaw(t, ResourceTenant().Schema, raw)
}

// TestResourceTenant_Schema locks down the immutability/updatability invariants: tenant_id
// is Required+ForceNew, name/description are Optional+ForceNew, the three caps are
// Optional+Computed and NOT ForceNew, and the rest are API-only Computed fields.
func TestResourceTenant_Schema(t *testing.T) {
	s := ResourceTenant().Schema

	if f := s["tenant_id"]; f == nil {
		t.Fatal("schema is missing field \"tenant_id\"")
	} else {
		if !f.Required {
			t.Error("tenant_id: Required = false, want true")
		}
		if !f.ForceNew {
			t.Error("tenant_id: ForceNew = false, want true (must be immutable)")
		}
	}

	optionalImmutable := []string{"name", "description"}
	for _, key := range optionalImmutable {
		f := s[key]
		if f == nil {
			t.Fatalf("schema is missing field %q", key)
		}
		if !f.Optional {
			t.Errorf("%s: Optional = false, want true", key)
		}
		if !f.ForceNew {
			t.Errorf("%s: ForceNew = false, want true (must be immutable)", key)
		}
	}

	updatableCaps := []string{"cpu_cores_cap", "memory_gb_cap", "storage_gb_cap"}
	for _, key := range updatableCaps {
		f := s[key]
		if f == nil {
			t.Fatalf("schema is missing field %q", key)
		}
		if f.ForceNew {
			t.Errorf("%s: ForceNew = true, want false (must be updatable via PATCH)", key)
		}
		if !f.Optional || !f.Computed {
			t.Errorf("%s: Optional=%v Computed=%v, want both true (API supplies a default)", key, f.Optional, f.Computed)
		}
	}

	computedOnly := []string{"tenant_uuid", "asgardeo_group", "created_at", "created_by"}
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

// TestResourceTenantCreate_Success verifies the POST sets the state ID to the slug and
// stores the API-resolved cap defaults plus the computed fields.
func TestResourceTenantCreate_Success(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("method = %s, want POST", r.Method)
		}
		if r.URL.Path != "/v1/admin/tenants" {
			t.Errorf("path = %s, want /v1/admin/tenants", r.URL.Path)
		}
		w.WriteHeader(http.StatusCreated)
		w.Write([]byte(`{
			"id": "acme",
			"tenant_uuid": "11111111-1111-1111-1111-111111111111",
			"name": "Acme Corp",
			"description": "Acme tenant",
			"asgardeo_group": "dc-tenant-acme",
			"cpu_cores_cap": 80,
			"memory_gb_cap": 256,
			"storage_gb_cap": 2000,
			"created_at": "2026-01-01T00:00:00Z",
			"created_by": "user@example.com"
		}`))
	}))
	defer server.Close()

	c, err := client.NewClient(server.URL, "test-token")
	if err != nil {
		t.Fatalf("client.NewClient: %v", err)
	}

	d := newTestTenantData(t, map[string]interface{}{
		"tenant_id": "acme",
		"name":      "Acme Corp",
	})

	diags := resourceTenantCreate(context.Background(), d, c)
	if diags.HasError() {
		t.Fatalf("resourceTenantCreate returned unexpected error diagnostics: %v", diags)
	}

	if got, want := d.Id(), "acme"; got != want {
		t.Errorf("Id() = %q, want %q", got, want)
	}
	if got, want := d.Get("tenant_uuid").(string), "11111111-1111-1111-1111-111111111111"; got != want {
		t.Errorf("tenant_uuid = %q, want %q", got, want)
	}
	if got, want := d.Get("asgardeo_group").(string), "dc-tenant-acme"; got != want {
		t.Errorf("asgardeo_group = %q, want %q", got, want)
	}
	if got, want := d.Get("cpu_cores_cap").(int), 80; got != want {
		t.Errorf("cpu_cores_cap = %d, want %d (API-resolved default)", got, want)
	}
}

// TestResourceTenantCreate_APIError verifies a non-2xx leaves Id() empty.
func TestResourceTenantCreate_APIError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		w.Write([]byte(`{"error":"tenant id already exists"}`))
	}))
	defer server.Close()

	c, _ := client.NewClient(server.URL, "test-token")
	d := newTestTenantData(t, map[string]interface{}{"tenant_id": "acme"})

	diags := resourceTenantCreate(context.Background(), d, c)
	if !diags.HasError() {
		t.Fatal("diags.HasError() = false, want true for a failed create")
	}
	if d.Id() != "" {
		t.Errorf("Id() = %q, want empty string when create fails", d.Id())
	}
}

// TestResourceTenantRead_Success verifies Read scans the /v1/tenants list and refreshes
// state from the matching entry.
func TestResourceTenantRead_Success(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/tenants" {
			t.Errorf("path = %s, want /v1/tenants", r.URL.Path)
		}
		w.WriteHeader(http.StatusOK)
		// A JSON array — GetTenantByID scans this for a matching id.
		w.Write([]byte(`[
			{"id": "other", "name": "Other"},
			{
				"id": "acme",
				"tenant_uuid": "11111111-1111-1111-1111-111111111111",
				"name": "Acme Corp",
				"description": "Acme tenant",
				"asgardeo_group": "dc-tenant-acme",
				"cpu_cores_cap": 120,
				"memory_gb_cap": 512,
				"storage_gb_cap": 4000,
				"created_at": "2026-01-01T00:00:00Z",
				"created_by": "user@example.com"
			}
		]`))
	}))
	defer server.Close()

	c, _ := client.NewClient(server.URL, "test-token")
	d := newTestTenantData(t, map[string]interface{}{})
	d.SetId("acme")

	diags := resourceTenantRead(context.Background(), d, c)
	if diags.HasError() {
		t.Fatalf("resourceTenantRead returned unexpected error diagnostics: %v", diags)
	}
	if d.Id() != "acme" {
		t.Errorf("Id() = %q, want unchanged %q", d.Id(), "acme")
	}
	if got, want := d.Get("name").(string), "Acme Corp"; got != want {
		t.Errorf("name = %q, want %q", got, want)
	}
	if got, want := d.Get("cpu_cores_cap").(int), 120; got != want {
		t.Errorf("cpu_cores_cap = %d, want %d", got, want)
	}
}

// TestResourceTenantRead_NotFound verifies that a tenant missing from the list clears the
// ID without an error (external drift).
func TestResourceTenantRead_NotFound(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`[{"id": "other", "name": "Other"}]`))
	}))
	defer server.Close()

	c, _ := client.NewClient(server.URL, "test-token")
	d := newTestTenantData(t, map[string]interface{}{})
	d.SetId("acme")

	diags := resourceTenantRead(context.Background(), d, c)
	if diags.HasError() {
		t.Fatalf("resourceTenantRead returned unexpected error diagnostics: %v", diags)
	}
	if d.Id() != "" {
		t.Errorf("Id() = %q, want empty string when tenant is absent from the list", d.Id())
	}
}

// TestResourceTenantUpdate_OnlySendsChangedCaps verifies Update PATCHes only the caps that
// changed — cpu_cores_cap is present in the raw config (HasChange true) while the others
// are omitted (HasChange false) and must not appear in the PATCH body.
func TestResourceTenantUpdate_OnlySendsChangedCaps(t *testing.T) {
	var gotBody string

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPatch {
			t.Errorf("method = %s, want PATCH", r.Method)
		}
		if r.URL.Path != "/v1/admin/tenants/acme" {
			t.Errorf("path = %s, want /v1/admin/tenants/acme", r.URL.Path)
		}
		buf := make([]byte, r.ContentLength)
		r.Body.Read(buf)
		gotBody = string(buf)

		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{
			"id": "acme",
			"cpu_cores_cap": 200,
			"memory_gb_cap": 256,
			"storage_gb_cap": 2000
		}`))
	}))
	defer server.Close()

	c, _ := client.NewClient(server.URL, "test-token")
	d := newTestTenantData(t, map[string]interface{}{
		"tenant_id":     "acme",
		"cpu_cores_cap": 200,
	})
	d.SetId("acme")

	if !d.HasChange("cpu_cores_cap") {
		t.Fatal("test setup invalid: expected HasChange(cpu_cores_cap) == true")
	}
	if d.HasChange("memory_gb_cap") || d.HasChange("storage_gb_cap") {
		t.Fatal("test setup invalid: expected memory_gb_cap/storage_gb_cap to have no change")
	}

	diags := resourceTenantUpdate(context.Background(), d, c)
	if diags.HasError() {
		t.Fatalf("resourceTenantUpdate returned unexpected error diagnostics: %v", diags)
	}

	if !strings.Contains(gotBody, `"cpu_cores_cap":200`) {
		t.Errorf("PATCH body = %q, want it to contain cpu_cores_cap:200", gotBody)
	}
	if strings.Contains(gotBody, "memory_gb_cap") || strings.Contains(gotBody, "storage_gb_cap") {
		t.Errorf("PATCH body = %q, want memory/storage caps omitted since they didn't change", gotBody)
	}

	if got, want := d.Get("cpu_cores_cap").(int), 200; got != want {
		t.Errorf("cpu_cores_cap = %d, want %d", got, want)
	}
}

// TestResourceTenantUpdate_APIError verifies a PATCH failure surfaces as an error.
func TestResourceTenantUpdate_APIError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		w.Write([]byte(`{"error":"cap below current allocation"}`))
	}))
	defer server.Close()

	c, _ := client.NewClient(server.URL, "test-token")
	d := newTestTenantData(t, map[string]interface{}{
		"tenant_id":     "acme",
		"cpu_cores_cap": 1,
	})
	d.SetId("acme")

	diags := resourceTenantUpdate(context.Background(), d, c)
	if !diags.HasError() {
		t.Fatal("diags.HasError() = false, want true for a failed update")
	}
}

// TestResourceTenantDelete_Success verifies Delete clears the ID. There is no DC-API
// delete endpoint for tenants, so this is a pure state operation and never touches HTTP.
func TestResourceTenantDelete_Success(t *testing.T) {
	c, _ := client.NewClient("http://unused.invalid", "test-token")
	d := newTestTenantData(t, map[string]interface{}{})
	d.SetId("acme")

	diags := resourceTenantDelete(context.Background(), d, c)
	if diags.HasError() {
		t.Fatalf("resourceTenantDelete returned unexpected error diagnostics: %v", diags)
	}
	if d.Id() != "" {
		t.Errorf("Id() = %q, want empty string after delete", d.Id())
	}
}
