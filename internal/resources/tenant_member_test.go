// Unit tests for tenant_member.go.
//
// See project_test.go for the shared patterns. Tenant members have no UpdateContext
// (every field is ForceNew) and no GET-by-id endpoint, so:
//
//   - The composite state ID is "tenant_id/principal_id", and principal_id echoes the
//     user_sub (which may contain a "|", e.g. "auth0|abc123").
//   - Read resolves the membership by LISTING /v1/tenants/{id}/members (a JSON array) and
//     scanning for a matching principal_id, mirroring GetTenantByID's list-and-scan.
package resources

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/hashicorp/terraform-plugin-sdk/v2/helper/schema"
	"terraform-provider-dcapi/internal/client"
)

// newTestTenantMemberData builds a *schema.ResourceData for ResourceTenantMember().
func newTestTenantMemberData(t *testing.T, raw map[string]interface{}) *schema.ResourceData {
	t.Helper()
	return schema.TestResourceDataRaw(t, ResourceTenantMember().Schema, raw)
}

// TestResourceTenantMember_Schema verifies all user-supplied fields are immutable
// (ForceNew — no update endpoint) and the API-set fields are Computed-only.
func TestResourceTenantMember_Schema(t *testing.T) {
	s := ResourceTenantMember().Schema

	requiredImmutable := []string{"tenant_id", "user_sub", "role"}
	for _, key := range requiredImmutable {
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

	if f := s["display_alias"]; f == nil {
		t.Fatal("schema is missing field \"display_alias\"")
	} else {
		if !f.Optional {
			t.Error("display_alias: Optional = false, want true")
		}
		if !f.ForceNew {
			t.Error("display_alias: ForceNew = false, want true (must be immutable)")
		}
	}

	computedOnly := []string{"member_id", "principal_type", "scope_type", "scope_id", "granted_at", "granted_by"}
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

// TestResourceTenantMemberCreate_Success verifies the POST sets the composite ID to
// "tenant_id/principal_id" and copies the role_assignment fields into state.
func TestResourceTenantMemberCreate_Success(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("method = %s, want POST", r.Method)
		}
		if r.URL.Path != "/v1/tenants/acme/members" {
			t.Errorf("path = %s, want /v1/tenants/acme/members", r.URL.Path)
		}
		w.WriteHeader(http.StatusCreated)
		w.Write([]byte(`{
			"id": "ra-uuid-1",
			"principal_type": "user",
			"principal_id": "auth0|abc123",
			"scope_type": "tenant",
			"scope_id": "acme",
			"role": "member",
			"granted_at": "2026-01-01T00:00:00Z",
			"granted_by": "owner@example.com",
			"display_alias": "Alice"
		}`))
	}))
	defer server.Close()

	c, err := client.NewClient(server.URL, "test-token")
	if err != nil {
		t.Fatalf("client.NewClient: %v", err)
	}

	d := newTestTenantMemberData(t, map[string]interface{}{
		"tenant_id":     "acme",
		"user_sub":      "auth0|abc123",
		"role":          "member",
		"display_alias": "Alice",
	})

	diags := resourceTenantMemberCreate(context.Background(), d, c)
	if diags.HasError() {
		t.Fatalf("resourceTenantMemberCreate returned unexpected error diagnostics: %v", diags)
	}

	if got, want := d.Id(), "acme/auth0|abc123"; got != want {
		t.Errorf("Id() = %q, want %q", got, want)
	}
	if got, want := d.Get("member_id").(string), "ra-uuid-1"; got != want {
		t.Errorf("member_id = %q, want %q", got, want)
	}
	if got, want := d.Get("principal_type").(string), "user"; got != want {
		t.Errorf("principal_type = %q, want %q", got, want)
	}
	if got, want := d.Get("granted_by").(string), "owner@example.com"; got != want {
		t.Errorf("granted_by = %q, want %q", got, want)
	}
}

// TestResourceTenantMemberCreate_APIError verifies a non-2xx leaves Id() empty.
func TestResourceTenantMemberCreate_APIError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusConflict)
		w.Write([]byte(`{"error":"member already exists"}`))
	}))
	defer server.Close()

	c, _ := client.NewClient(server.URL, "test-token")
	d := newTestTenantMemberData(t, map[string]interface{}{
		"tenant_id": "acme",
		"user_sub":  "auth0|abc123",
		"role":      "member",
	})

	diags := resourceTenantMemberCreate(context.Background(), d, c)
	if !diags.HasError() {
		t.Fatal("diags.HasError() = false, want true for a failed create")
	}
	if d.Id() != "" {
		t.Errorf("Id() = %q, want empty string when create fails", d.Id())
	}
}

// TestResourceTenantMemberRead_Success verifies Read scans the members list and refreshes
// state from the entry whose principal_id matches the one encoded in the state ID.
func TestResourceTenantMemberRead_Success(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/tenants/acme/members" {
			t.Errorf("path = %s, want /v1/tenants/acme/members", r.URL.Path)
		}
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`[
			{"id": "ra-0", "principal_id": "auth0|other", "role": "viewer"},
			{
				"id": "ra-uuid-1",
				"principal_type": "user",
				"principal_id": "auth0|abc123",
				"scope_type": "tenant",
				"scope_id": "acme",
				"role": "member",
				"granted_at": "2026-01-01T00:00:00Z",
				"granted_by": "owner@example.com",
				"display_alias": "Alice"
			}
		]`))
	}))
	defer server.Close()

	c, _ := client.NewClient(server.URL, "test-token")
	d := newTestTenantMemberData(t, map[string]interface{}{})
	d.SetId("acme/auth0|abc123")

	diags := resourceTenantMemberRead(context.Background(), d, c)
	if diags.HasError() {
		t.Fatalf("resourceTenantMemberRead returned unexpected error diagnostics: %v", diags)
	}
	if d.Id() != "acme/auth0|abc123" {
		t.Errorf("Id() = %q, want unchanged", d.Id())
	}
	if got, want := d.Get("role").(string), "member"; got != want {
		t.Errorf("role = %q, want %q", got, want)
	}
	if got, want := d.Get("user_sub").(string), "auth0|abc123"; got != want {
		t.Errorf("user_sub = %q, want %q (from principal_id)", got, want)
	}
	if got, want := d.Get("member_id").(string), "ra-uuid-1"; got != want {
		t.Errorf("member_id = %q, want %q", got, want)
	}
}

// TestResourceTenantMemberRead_NotFound verifies that a principal absent from the list
// clears the ID without error (membership revoked outside Terraform).
func TestResourceTenantMemberRead_NotFound(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`[{"id": "ra-0", "principal_id": "auth0|other", "role": "viewer"}]`))
	}))
	defer server.Close()

	c, _ := client.NewClient(server.URL, "test-token")
	d := newTestTenantMemberData(t, map[string]interface{}{})
	d.SetId("acme/auth0|abc123")

	diags := resourceTenantMemberRead(context.Background(), d, c)
	if diags.HasError() {
		t.Fatalf("resourceTenantMemberRead returned unexpected error diagnostics: %v", diags)
	}
	if d.Id() != "" {
		t.Errorf("Id() = %q, want empty string when the member is absent from the list", d.Id())
	}
}

// TestResourceTenantMemberRead_InvalidID verifies a state ID without exactly two
// slash-separated parts errors instead of mis-parsing the SplitN result.
func TestResourceTenantMemberRead_InvalidID(t *testing.T) {
	c, _ := client.NewClient("http://unused.invalid", "test-token")
	d := newTestTenantMemberData(t, map[string]interface{}{})
	d.SetId("only-one-part")

	diags := resourceTenantMemberRead(context.Background(), d, c)
	if !diags.HasError() {
		t.Fatal("diags.HasError() = false, want true for a malformed state ID")
	}
}

// TestResourceTenantMemberDelete_Success verifies a successful DELETE clears the ID.
// The principal_id ("auth0|abc123") is url-escaped in the request path, but the httptest
// server's r.URL.Path reports it decoded.
func TestResourceTenantMemberDelete_Success(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodDelete {
			t.Errorf("method = %s, want DELETE", r.Method)
		}
		if r.URL.Path != "/v1/tenants/acme/members/auth0|abc123" {
			t.Errorf("path = %s, want /v1/tenants/acme/members/auth0|abc123", r.URL.Path)
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()

	c, _ := client.NewClient(server.URL, "test-token")
	d := newTestTenantMemberData(t, map[string]interface{}{})
	d.SetId("acme/auth0|abc123")

	diags := resourceTenantMemberDelete(context.Background(), d, c)
	if diags.HasError() {
		t.Fatalf("resourceTenantMemberDelete returned unexpected error diagnostics: %v", diags)
	}
	if d.Id() != "" {
		t.Errorf("Id() = %q, want empty string after a successful delete", d.Id())
	}
}

// TestResourceTenantMemberDelete_NotFoundIsSuccess verifies a 404 on delete is success.
func TestResourceTenantMemberDelete_NotFoundIsSuccess(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer server.Close()

	c, _ := client.NewClient(server.URL, "test-token")
	d := newTestTenantMemberData(t, map[string]interface{}{})
	d.SetId("acme/auth0|abc123")

	diags := resourceTenantMemberDelete(context.Background(), d, c)
	if diags.HasError() {
		t.Fatalf("resourceTenantMemberDelete returned unexpected error diagnostics on 404: %v", diags)
	}
	if d.Id() != "" {
		t.Errorf("Id() = %q, want empty string after a 404 delete", d.Id())
	}
}

// TestResourceTenantMemberDelete_Error verifies a non-404 failure keeps the ID.
func TestResourceTenantMemberDelete_Error(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte(`{"error":"boom"}`))
	}))
	defer server.Close()

	c, _ := client.NewClient(server.URL, "test-token")
	d := newTestTenantMemberData(t, map[string]interface{}{})
	d.SetId("acme/auth0|abc123")

	diags := resourceTenantMemberDelete(context.Background(), d, c)
	if !diags.HasError() {
		t.Fatal("diags.HasError() = false, want true for a 500 on delete")
	}
	if d.Id() == "" {
		t.Error("Id() = \"\", want it to remain set since delete failed")
	}
}
