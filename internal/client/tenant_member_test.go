// Unit tests for tenant_member.go.
package client

import (
	"context"
	"net/http"
	"testing"
)

const tenantMembersBase = "/v1/tenants/t1/members"

// TestCreateTenantMember guards the POST path, body forwarding, and parse.
func TestCreateTenantMember(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != tenantMembersBase {
			t.Errorf("got %s %s, want POST %s", r.Method, r.URL.Path, tenantMembersBase)
		}
		var got TenantMemberCreateRequest
		decodeBody(t, r, &got)
		if got.UserSub != "sub-123" || got.Role != "member" {
			t.Errorf("body = %+v, want user_sub=sub-123 role=member", got)
		}
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"id":"ra-1","principal_id":"sub-123","role":"member"}`))
	})
	m, err := c.CreateTenantMember(context.Background(), "t1",
		TenantMemberCreateRequest{UserSub: "sub-123", Role: "member"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if m.ID != "ra-1" || m.PrincipalID != "sub-123" {
		t.Fatalf("member = %+v, want id ra-1 principal sub-123", m)
	}
}

// TestListTenantMembers checks the bare-array parse the Read resolver scans.
func TestListTenantMembers(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != tenantMembersBase {
			t.Errorf("got %s %s, want GET %s", r.Method, r.URL.Path, tenantMembersBase)
		}
		_, _ = w.Write([]byte(`[{"id":"ra-1","principal_id":"sub-1","role":"owner"},{"id":"ra-2","principal_id":"sub-2","role":"viewer"}]`))
	})
	members, err := c.ListTenantMembers(context.Background(), "t1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(members) != 2 || members[1].PrincipalID != "sub-2" {
		t.Fatalf("members = %+v, want two", members)
	}
}

// TestListTenantMembers_Error surfaces an API failure.
func TestListTenantMembers_Error(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	})
	if _, err := c.ListTenantMembers(context.Background(), "t1"); err == nil {
		t.Fatal("err = nil, want an error for HTTP 500")
	}
}

// TestDeleteTenantMember guards that the principal ID (an OIDC sub, not the
// assignment UUID) is URL-escaped into the DELETE path.
func TestDeleteTenantMember(t *testing.T) {
	ctx := context.Background()

	t.Run("escapes principal id", func(t *testing.T) {
		// An OIDC sub can contain characters like "|" that must be percent-encoded.
		c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
			if r.Method != http.MethodDelete {
				t.Errorf("method = %s, want DELETE", r.Method)
			}
			// r.URL.Path is already percent-decoded by net/http, so compare the
			// decoded form; EscapedPath preserves the encoding on the wire.
			if r.URL.Path != tenantMembersBase+"/auth0|abc" {
				t.Errorf("decoded path = %s, want .../auth0|abc", r.URL.Path)
			}
			if r.URL.EscapedPath() != tenantMembersBase+"/auth0%7Cabc" {
				t.Errorf("escaped path = %s, want the | percent-encoded", r.URL.EscapedPath())
			}
			w.WriteHeader(http.StatusNoContent)
		})
		if err := c.DeleteTenantMember(ctx, "t1", "auth0|abc"); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
	})

	t.Run("error", func(t *testing.T) {
		c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusNotFound)
		})
		if err := c.DeleteTenantMember(ctx, "t1", "sub-1"); err == nil {
			t.Fatal("err = nil, want an error for HTTP 404")
		}
	})
}
