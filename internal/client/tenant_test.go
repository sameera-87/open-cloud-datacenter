// Unit tests for tenant.go.
package client

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"testing"
)

// TestCreateTenant guards the admin POST path and parse.
func TestCreateTenant(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/v1/admin/tenants" {
			t.Errorf("got %s %s, want POST /v1/admin/tenants", r.Method, r.URL.Path)
		}
		var got TenantCreateRequest
		decodeBody(t, r, &got)
		if got.ID != "acme" {
			t.Errorf("body id = %q, want acme", got.ID)
		}
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"id":"acme","tenant_uuid":"u-1","cpu_cores_cap":20}`))
	})
	ten, err := c.CreateTenant(context.Background(), TenantCreateRequest{ID: "acme"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ten.ID != "acme" || ten.TenantUUID != "u-1" || ten.CPUCoresCap != 20 {
		t.Fatalf("tenant = %+v, want id acme uuid u-1 cap 20", ten)
	}
}

// TestGetTenantByID exercises the list-and-scan behaviour: the DC-API has no
// GET-by-id endpoint, so GetTenantByID lists /v1/tenants and returns the matching
// entry, or (nil, nil) when no entry matches.
func TestGetTenantByID(t *testing.T) {
	ctx := context.Background()
	body := `[{"id":"acme","name":"Acme"},{"id":"globex","name":"Globex"}]`

	t.Run("found", func(t *testing.T) {
		c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
			if r.Method != http.MethodGet || r.URL.Path != "/v1/tenants" {
				t.Errorf("got %s %s, want GET /v1/tenants", r.Method, r.URL.Path)
			}
			_, _ = w.Write([]byte(body))
		})
		got, err := c.GetTenantByID(ctx, "globex")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if got == nil || got.ID != "globex" {
			t.Fatalf("got = %+v, want tenant globex", got)
		}
	})

	t.Run("not found returns nil,nil", func(t *testing.T) {
		c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
			_, _ = w.Write([]byte(body))
		})
		got, err := c.GetTenantByID(ctx, "nope")
		if err != nil || got != nil {
			t.Fatalf("got=%v err=%v, want nil,nil when no tenant matches", got, err)
		}
	})

	t.Run("list error", func(t *testing.T) {
		c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusInternalServerError)
		})
		if _, err := c.GetTenantByID(ctx, "acme"); err == nil {
			t.Fatal("err = nil, want an error when the list request fails")
		}
	})
}

// TestUpdateTenant guards the admin PATCH path and the *int pointer semantics:
// a nil field is omitted, a set field is sent.
func TestUpdateTenant(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPatch || r.URL.Path != "/v1/admin/tenants/acme" {
			t.Errorf("got %s %s, want PATCH /v1/admin/tenants/acme", r.Method, r.URL.Path)
		}
		raw, _ := io.ReadAll(r.Body)
		var asMap map[string]json.RawMessage
		if err := json.Unmarshal(raw, &asMap); err != nil {
			t.Fatalf("decoding body %q: %v", string(raw), err)
		}
		if _, ok := asMap["memory_gb_cap"]; !ok {
			t.Errorf("body %q missing memory_gb_cap", string(raw))
		}
		if _, ok := asMap["cpu_cores_cap"]; ok {
			t.Errorf("body %q includes cpu_cores_cap, want it omitted (nil pointer)", string(raw))
		}
		_, _ = w.Write([]byte(`{"id":"acme","memory_gb_cap":128}`))
	})
	mem := 128
	ten, err := c.UpdateTenant(context.Background(), "acme", TenantUpdateRequest{MemoryGBCap: &mem})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ten.MemoryGBCap != 128 {
		t.Fatalf("memory cap = %d, want 128", ten.MemoryGBCap)
	}
}
