// Unit tests for private_endpoint.go.
package client

import (
	"context"
	"net/http"
	"testing"
)

const privateEndpointsBase = "/v1/tenants/t1/projects/p1/keyvaults/kv1/private-endpoints"

// TestCreatePrivateEndpoint guards the POST path, body forwarding, and parse.
func TestCreatePrivateEndpoint(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != privateEndpointsBase {
			t.Errorf("got %s %s, want POST %s", r.Method, r.URL.Path, privateEndpointsBase)
		}
		var got PrivateEndpointCreateRequest
		decodeBody(t, r, &got)
		if got.Name != "ep" || got.VNetID != "vn1" || got.SubnetID != "sn1" {
			t.Errorf("body = %+v, want name=ep vnet=vn1 subnet=sn1", got)
		}
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"id":"ep-1","name":"ep","ip_address":"10.0.0.9","status":"ACTIVE"}`))
	})
	ep, err := c.CreatePrivateEndpoint(context.Background(), "t1", "p1", "kv1",
		PrivateEndpointCreateRequest{Name: "ep", VNetID: "vn1", SubnetID: "sn1"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ep.ID != "ep-1" || ep.IPAddress != "10.0.0.9" {
		t.Fatalf("ep = %+v, want id ep-1 ip 10.0.0.9", ep)
	}
}

// TestCreatePrivateEndpoint_NotImplemented confirms a 501 (provisioner disabled)
// is surfaced as a plain error.
func TestCreatePrivateEndpoint_NotImplemented(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotImplemented)
		_, _ = w.Write([]byte(`{"error":"endpoint provisioner not enabled"}`))
	})
	if _, err := c.CreatePrivateEndpoint(context.Background(), "t1", "p1", "kv1", PrivateEndpointCreateRequest{}); err == nil {
		t.Fatal("err = nil, want an error for HTTP 501")
	}
}

// TestGetPrivateEndpoint covers 200 and the 404 sentinel.
func TestGetPrivateEndpoint(t *testing.T) {
	ctx := context.Background()

	t.Run("found", func(t *testing.T) {
		c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path != privateEndpointsBase+"/ep-1" {
				t.Errorf("path = %s, unexpected", r.URL.Path)
			}
			_, _ = w.Write([]byte(`{"id":"ep-1","name":"ep","hostname":"ep.internal"}`))
		})
		got, err := c.GetPrivateEndpoint(ctx, "t1", "p1", "kv1", "ep-1")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if got == nil || got.Hostname != "ep.internal" {
			t.Fatalf("got = %+v, want hostname ep.internal", got)
		}
	})

	t.Run("404 returns nil,nil", func(t *testing.T) {
		c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusNotFound)
		})
		got, err := c.GetPrivateEndpoint(ctx, "t1", "p1", "kv1", "missing")
		if err != nil || got != nil {
			t.Fatalf("got=%v err=%v, want nil,nil on 404", got, err)
		}
	})
}

// TestDeletePrivateEndpoint checks the DELETE path and error wrapping.
func TestDeletePrivateEndpoint(t *testing.T) {
	ctx := context.Background()

	t.Run("success", func(t *testing.T) {
		c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
			if r.Method != http.MethodDelete || r.URL.Path != privateEndpointsBase+"/ep-1" {
				t.Errorf("got %s %s, unexpected", r.Method, r.URL.Path)
			}
			w.WriteHeader(http.StatusNoContent)
		})
		if err := c.DeletePrivateEndpoint(ctx, "t1", "p1", "kv1", "ep-1"); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
	})

	t.Run("error", func(t *testing.T) {
		c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusInternalServerError)
		})
		if err := c.DeletePrivateEndpoint(ctx, "t1", "p1", "kv1", "ep-1"); err == nil {
			t.Fatal("err = nil, want an error for HTTP 500")
		}
	})
}

// TestListPrivateEndpoints_Error proves the error path; both list shapes are
// covered in list_test.go.
func TestListPrivateEndpoints_Error(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	})
	if _, err := c.ListPrivateEndpoints(context.Background(), "t1", "p1", "kv1"); err == nil {
		t.Fatal("err = nil, want an error for HTTP 500")
	}
}
