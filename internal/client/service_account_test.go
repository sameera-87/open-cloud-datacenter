// Unit tests for service_account.go.
package client

import (
	"context"
	"net/http"
	"testing"
)

const serviceAccountsBase = "/v1/tenants/t1/projects/p1/service-accounts"

// TestCreateServiceAccount guards the POST path, body forwarding, and that the
// one-time token is parsed from the 201 body.
func TestCreateServiceAccount(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != serviceAccountsBase {
			t.Errorf("got %s %s, want POST %s", r.Method, r.URL.Path, serviceAccountsBase)
		}
		var got ServiceAccountCreateRequest
		decodeBody(t, r, &got)
		if got.Name != "ci" || got.Role != "member" {
			t.Errorf("body = %+v, want name=ci role=member", got)
		}
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"id":"sa-1","name":"ci","role":"member","token":"one-time-token"}`))
	})
	sa, err := c.CreateServiceAccount(context.Background(), "t1", "p1",
		ServiceAccountCreateRequest{Name: "ci", Role: "member"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if sa.ID != "sa-1" || sa.Token != "one-time-token" {
		t.Fatalf("sa = %+v, want id sa-1 with token", sa)
	}
}

// TestGetServiceAccount covers 200 (including the nullable last_used) and the 404
// sentinel.
func TestGetServiceAccount(t *testing.T) {
	ctx := context.Background()

	t.Run("found with null last_used", func(t *testing.T) {
		c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path != serviceAccountsBase+"/sa-1" {
				t.Errorf("path = %s, unexpected", r.URL.Path)
			}
			_, _ = w.Write([]byte(`{"id":"sa-1","name":"ci","role":"member","last_used":null}`))
		})
		got, err := c.GetServiceAccount(ctx, "t1", "p1", "sa-1")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if got == nil || got.LastUsed != nil {
			t.Fatalf("got = %+v, want last_used nil (never authenticated)", got)
		}
	})

	t.Run("404 returns nil,nil", func(t *testing.T) {
		c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusNotFound)
		})
		got, err := c.GetServiceAccount(ctx, "t1", "p1", "missing")
		if err != nil || got != nil {
			t.Fatalf("got=%v err=%v, want nil,nil on 404", got, err)
		}
	})
}

// TestDeleteServiceAccount checks the DELETE path and error wrapping.
func TestDeleteServiceAccount(t *testing.T) {
	ctx := context.Background()

	t.Run("success", func(t *testing.T) {
		c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
			if r.Method != http.MethodDelete || r.URL.Path != serviceAccountsBase+"/sa-1" {
				t.Errorf("got %s %s, unexpected", r.Method, r.URL.Path)
			}
			w.WriteHeader(http.StatusNoContent)
		})
		if err := c.DeleteServiceAccount(ctx, "t1", "p1", "sa-1"); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
	})

	t.Run("error", func(t *testing.T) {
		c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusInternalServerError)
		})
		if err := c.DeleteServiceAccount(ctx, "t1", "p1", "sa-1"); err == nil {
			t.Fatal("err = nil, want an error for HTTP 500")
		}
	})
}

// TestListServiceAccounts_Error proves the error path; both list shapes are
// covered in list_test.go.
func TestListServiceAccounts_Error(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
	})
	if _, err := c.ListServiceAccounts(context.Background(), "t1", "p1"); err == nil {
		t.Fatal("err = nil, want an error for HTTP 403 (requires an owner SA)")
	}
}
