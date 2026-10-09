// Unit tests for bastion.go.
//
// Like client_test.go, these live in "package client" so they can construct a
// DCAPIClient pointed at an httptest.Server. This file also defines the small
// shared helper newTestClient, reused by the other *_test.go files in this
// package (listServer/shapes already live in list_test.go and are left alone).
package client

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

// newTestClient spins up an httptest.Server with the given handler and returns a
// DCAPIClient wired to it. t.Cleanup closes the server when the test ends, so no
// caller has to remember a defer. The name is deliberately distinct from
// list_test.go's listServer to avoid a redeclaration across files in this package.
func newTestClient(t *testing.T, handler http.HandlerFunc) *DCAPIClient {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	c, err := NewClient(server.URL, "test-token")
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	return c
}

// decodeBody is a tiny helper for handlers that need to assert the JSON request
// body a Create/Update method sent. It fails the test if the body can't be
// decoded into v.
func decodeBody(t *testing.T, r *http.Request, v interface{}) {
	t.Helper()
	data, err := io.ReadAll(r.Body)
	if err != nil {
		t.Fatalf("reading request body: %v", err)
	}
	if err := json.Unmarshal(data, v); err != nil {
		t.Fatalf("decoding request body %q: %v", string(data), err)
	}
}

// TestCreateBastion_HappyPath guards that CreateBastion hits the correct POST
// path, forwards the request body faithfully, and parses the outer 202 wrapper
// (including the shown-once secrets) into the right struct fields.
func TestCreateBastion_HappyPath(t *testing.T) {
	ctx := context.Background()
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("method = %s, want POST", r.Method)
		}
		if r.URL.Path != "/v1/tenants/t1/projects/p1/bastions" {
			t.Errorf("path = %s, want /v1/tenants/t1/projects/p1/bastions", r.URL.Path)
		}
		var got BastionCreateRequest
		decodeBody(t, r, &got)
		if got.Name != "bst" || got.VNetID != "vn1" || got.SubnetID != "sn1" {
			t.Errorf("request body = %+v, want name=bst vnet=vn1 subnet=sn1", got)
		}
		w.WriteHeader(http.StatusAccepted)
		_, _ = w.Write([]byte(`{"resource":{"id":"b-1","name":"bst","status":"PENDING"},"private_key":"PK","console_password":"CP","note":"keep secrets"}`))
	})

	resp, err := c.CreateBastion(ctx, "t1", "p1", BastionCreateRequest{Name: "bst", VNetID: "vn1", SubnetID: "sn1"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.Resource == nil || resp.Resource.ID != "b-1" {
		t.Fatalf("resource = %+v, want ID b-1", resp.Resource)
	}
	if resp.PrivateKey != "PK" || resp.ConsolePassword != "CP" {
		t.Errorf("secrets = %q/%q, want PK/CP", resp.PrivateKey, resp.ConsolePassword)
	}
}

// TestCreateBastion_APIError confirms a non-2xx response is surfaced as a non-nil
// error rather than a zero-valued struct.
func TestCreateBastion_APIError(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":"invalid subnet"}`))
	})
	if _, err := c.CreateBastion(context.Background(), "t1", "p1", BastionCreateRequest{}); err == nil {
		t.Fatal("err = nil, want an error for HTTP 400")
	}
}

// TestGetBastion covers the three contractually distinct outcomes of GetBastion:
// a parsed resource on 200, the (nil, nil) drift sentinel on 404, and a real
// error on any other failure.
func TestGetBastion(t *testing.T) {
	ctx := context.Background()

	t.Run("found", func(t *testing.T) {
		c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
			if r.Method != http.MethodGet {
				t.Errorf("method = %s, want GET", r.Method)
			}
			if r.URL.Path != "/v1/tenants/t1/projects/p1/bastions/b-1" {
				t.Errorf("path = %s, unexpected", r.URL.Path)
			}
			_, _ = w.Write([]byte(`{"id":"b-1","name":"bst","status":"ACTIVE","mgmt_ip":"10.0.0.5"}`))
		})
		got, err := c.GetBastion(ctx, "t1", "p1", "b-1")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if got == nil || got.ID != "b-1" || got.MgmtIP != "10.0.0.5" {
			t.Fatalf("got = %+v, want ID b-1 mgmt_ip 10.0.0.5", got)
		}
	})

	t.Run("404 returns nil,nil", func(t *testing.T) {
		c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"error":"not found"}`))
		})
		got, err := c.GetBastion(ctx, "t1", "p1", "missing")
		if err != nil || got != nil {
			t.Fatalf("got=%v err=%v, want nil,nil on 404", got, err)
		}
	})

	t.Run("500 returns error", func(t *testing.T) {
		c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusInternalServerError)
		})
		if _, err := c.GetBastion(ctx, "t1", "p1", "b-1"); err == nil {
			t.Fatal("err = nil, want an error for HTTP 500")
		}
	})
}

// TestDeleteBastion checks DeleteBastion issues a DELETE to the right path and
// turns an API failure into a wrapped error.
func TestDeleteBastion(t *testing.T) {
	ctx := context.Background()

	t.Run("success", func(t *testing.T) {
		c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
			if r.Method != http.MethodDelete {
				t.Errorf("method = %s, want DELETE", r.Method)
			}
			if r.URL.Path != "/v1/tenants/t1/projects/p1/bastions/b-1" {
				t.Errorf("path = %s, unexpected", r.URL.Path)
			}
			w.WriteHeader(http.StatusAccepted)
		})
		if err := c.DeleteBastion(ctx, "t1", "p1", "b-1"); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
	})

	t.Run("error", func(t *testing.T) {
		c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusConflict)
		})
		if err := c.DeleteBastion(ctx, "t1", "p1", "b-1"); err == nil {
			t.Fatal("err = nil, want an error for HTTP 409")
		}
	})
}

// TestListBastions_Error proves ListBastions wraps an API failure; the two
// list-encoding shapes it accepts are already exercised in list_test.go.
func TestListBastions_Error(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	})
	if _, err := c.ListBastions(context.Background(), "t1", "p1"); err == nil {
		t.Fatal("err = nil, want an error for HTTP 500")
	}
}
