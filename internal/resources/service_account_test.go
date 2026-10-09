// Unit tests for service_account.go.
//
// See project_test.go for the shared test patterns. Service accounts have no
// UpdateContext — every field is ForceNew, so there is no update test here. The two
// behaviours worth pinning down are the one-time token (stored at create, preserved
// across reads because the GET body omits it) and the three-part composite ID
// "tenant_id/project_id/sa_id".
package resources

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/hashicorp/terraform-plugin-sdk/v2/helper/schema"
	"terraform-provider-dcapi/internal/client"
)

// newTestServiceAccountData builds a *schema.ResourceData for ResourceServiceAccount().
func newTestServiceAccountData(t *testing.T, raw map[string]interface{}) *schema.ResourceData {
	t.Helper()
	return schema.TestResourceDataRaw(t, ResourceServiceAccount().Schema, raw)
}

// TestResourceServiceAccount_Schema checks that every user-supplied field is immutable
// (ForceNew) since there is no update endpoint, and that the API-only fields are Computed.
func TestResourceServiceAccount_Schema(t *testing.T) {
	s := ResourceServiceAccount().Schema

	requiredImmutable := []string{"tenant_id", "project_id", "name", "role"}
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

	// description is Optional but still immutable.
	if f := s["description"]; f == nil {
		t.Fatal("schema is missing field \"description\"")
	} else {
		if !f.Optional {
			t.Error("description: Optional = false, want true")
		}
		if !f.ForceNew {
			t.Error("description: ForceNew = false, want true (must be immutable)")
		}
	}

	computedOnly := []string{"sa_id", "created_at", "last_used", "token"}
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

	// token must be marked Sensitive — it's a bearer credential.
	if !s["token"].Sensitive {
		t.Error("token: Sensitive = false, want true")
	}
}

// TestResourceServiceAccountCreate_Success verifies the POST populates the three-part
// composite ID and stores sa_id/created_at and the one-time token in state.
func TestResourceServiceAccountCreate_Success(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("method = %s, want POST", r.Method)
		}
		if r.URL.Path != "/v1/tenants/acme/projects/infra/service-accounts" {
			t.Errorf("unexpected path = %s", r.URL.Path)
		}
		w.WriteHeader(http.StatusCreated)
		w.Write([]byte(`{
			"id": "sa-uuid-1",
			"tenant_id": "acme",
			"name": "ci-bot",
			"role": "member",
			"description": "CI pipeline",
			"created_at": "2026-01-01T00:00:00Z",
			"token": "dcapi_sa_sa-uuid-1_secret"
		}`))
	}))
	defer server.Close()

	c, err := client.NewClient(server.URL, "test-token")
	if err != nil {
		t.Fatalf("client.NewClient: %v", err)
	}

	d := newTestServiceAccountData(t, map[string]interface{}{
		"tenant_id":   "acme",
		"project_id":  "infra",
		"name":        "ci-bot",
		"role":        "member",
		"description": "CI pipeline",
	})

	diags := resourceServiceAccountCreate(context.Background(), d, c)
	if diags.HasError() {
		t.Fatalf("resourceServiceAccountCreate returned unexpected error diagnostics: %v", diags)
	}

	if got, want := d.Id(), "acme/infra/sa-uuid-1"; got != want {
		t.Errorf("Id() = %q, want %q", got, want)
	}
	if got, want := d.Get("sa_id").(string), "sa-uuid-1"; got != want {
		t.Errorf("sa_id = %q, want %q", got, want)
	}
	if got, want := d.Get("token").(string), "dcapi_sa_sa-uuid-1_secret"; got != want {
		t.Errorf("token = %q, want %q", got, want)
	}
	if got, want := d.Get("created_at").(string), "2026-01-01T00:00:00Z"; got != want {
		t.Errorf("created_at = %q, want %q", got, want)
	}
}

// TestResourceServiceAccountCreate_APIError verifies a non-2xx leaves Id() empty.
func TestResourceServiceAccountCreate_APIError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		w.Write([]byte(`{"error":"name already exists"}`))
	}))
	defer server.Close()

	c, _ := client.NewClient(server.URL, "test-token")
	d := newTestServiceAccountData(t, map[string]interface{}{
		"tenant_id":  "acme",
		"project_id": "infra",
		"name":       "ci-bot",
		"role":       "member",
	})

	diags := resourceServiceAccountCreate(context.Background(), d, c)
	if !diags.HasError() {
		t.Fatal("diags.HasError() = false, want true for a failed create")
	}
	if d.Id() != "" {
		t.Errorf("Id() = %q, want empty string when create fails", d.Id())
	}
}

// TestResourceServiceAccountRead_Success verifies the GET refreshes state and, crucially,
// preserves the token that was stored at create time (the GET body never includes it).
func TestResourceServiceAccountRead_Success(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/tenants/acme/projects/infra/service-accounts/sa-uuid-1" {
			t.Errorf("unexpected path = %s", r.URL.Path)
		}
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{
			"id": "sa-uuid-1",
			"tenant_id": "acme",
			"name": "ci-bot",
			"role": "member",
			"description": "CI pipeline",
			"created_at": "2026-01-01T00:00:00Z",
			"last_used": "2026-02-01T00:00:00Z"
		}`))
	}))
	defer server.Close()

	c, _ := client.NewClient(server.URL, "test-token")
	d := newTestServiceAccountData(t, map[string]interface{}{})
	d.SetId("acme/infra/sa-uuid-1")
	// Seed the token as create would have; Read must not clobber it.
	d.Set("token", "dcapi_sa_sa-uuid-1_secret")

	diags := resourceServiceAccountRead(context.Background(), d, c)
	if diags.HasError() {
		t.Fatalf("resourceServiceAccountRead returned unexpected error diagnostics: %v", diags)
	}
	if got, want := d.Get("name").(string), "ci-bot"; got != want {
		t.Errorf("name = %q, want %q", got, want)
	}
	if got, want := d.Get("last_used").(string), "2026-02-01T00:00:00Z"; got != want {
		t.Errorf("last_used = %q, want %q", got, want)
	}
	if got, want := d.Get("token").(string), "dcapi_sa_sa-uuid-1_secret"; got != want {
		t.Errorf("token = %q, want it preserved as %q", got, want)
	}
}

// TestResourceServiceAccountRead_NotFound verifies a 404 clears the ID without error.
func TestResourceServiceAccountRead_NotFound(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		w.Write([]byte(`{"error":"service account not found"}`))
	}))
	defer server.Close()

	c, _ := client.NewClient(server.URL, "test-token")
	d := newTestServiceAccountData(t, map[string]interface{}{})
	d.SetId("acme/infra/sa-uuid-1")

	diags := resourceServiceAccountRead(context.Background(), d, c)
	if diags.HasError() {
		t.Fatalf("resourceServiceAccountRead returned unexpected error diagnostics on 404: %v", diags)
	}
	if d.Id() != "" {
		t.Errorf("Id() = %q, want empty string after a 404", d.Id())
	}
}

// TestResourceServiceAccountRead_InvalidID verifies a state ID without exactly three
// slash-separated parts errors instead of panicking on the SplitN result.
func TestResourceServiceAccountRead_InvalidID(t *testing.T) {
	c, _ := client.NewClient("http://unused.invalid", "test-token")
	d := newTestServiceAccountData(t, map[string]interface{}{})
	d.SetId("acme/infra")

	diags := resourceServiceAccountRead(context.Background(), d, c)
	if !diags.HasError() {
		t.Fatal("diags.HasError() = false, want true for a malformed state ID")
	}
}

// TestResourceServiceAccountDelete_Success verifies a successful DELETE clears the ID.
func TestResourceServiceAccountDelete_Success(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodDelete {
			t.Errorf("method = %s, want DELETE", r.Method)
		}
		if r.URL.Path != "/v1/tenants/acme/projects/infra/service-accounts/sa-uuid-1" {
			t.Errorf("unexpected path = %s", r.URL.Path)
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()

	c, _ := client.NewClient(server.URL, "test-token")
	d := newTestServiceAccountData(t, map[string]interface{}{})
	d.SetId("acme/infra/sa-uuid-1")

	diags := resourceServiceAccountDelete(context.Background(), d, c)
	if diags.HasError() {
		t.Fatalf("resourceServiceAccountDelete returned unexpected error diagnostics: %v", diags)
	}
	if d.Id() != "" {
		t.Errorf("Id() = %q, want empty string after a successful delete", d.Id())
	}
}

// TestResourceServiceAccountDelete_NotFoundIsSuccess verifies a 404 on delete is success.
func TestResourceServiceAccountDelete_NotFoundIsSuccess(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer server.Close()

	c, _ := client.NewClient(server.URL, "test-token")
	d := newTestServiceAccountData(t, map[string]interface{}{})
	d.SetId("acme/infra/sa-uuid-1")

	diags := resourceServiceAccountDelete(context.Background(), d, c)
	if diags.HasError() {
		t.Fatalf("resourceServiceAccountDelete returned unexpected error diagnostics on 404: %v", diags)
	}
	if d.Id() != "" {
		t.Errorf("Id() = %q, want empty string after a 404 delete", d.Id())
	}
}

// TestResourceServiceAccountDelete_Error verifies a non-404 failure keeps the ID.
func TestResourceServiceAccountDelete_Error(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte(`{"error":"boom"}`))
	}))
	defer server.Close()

	c, _ := client.NewClient(server.URL, "test-token")
	d := newTestServiceAccountData(t, map[string]interface{}{})
	d.SetId("acme/infra/sa-uuid-1")

	diags := resourceServiceAccountDelete(context.Background(), d, c)
	if !diags.HasError() {
		t.Fatal("diags.HasError() = false, want true for a 500 on delete")
	}
	if d.Id() == "" {
		t.Error("Id() = \"\", want it to remain set since delete failed")
	}
}
