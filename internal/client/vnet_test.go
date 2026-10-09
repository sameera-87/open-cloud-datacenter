// Unit tests for vnet.go.
package client

import (
	"context"
	"net/http"
	"testing"
)

const vnetsBase = "/v1/tenants/t1/projects/p1/vnets"

// TestCreateVNet guards the POST path, body forwarding, and that the inner
// "resource" object (including the address_space slice) is unwrapped.
func TestCreateVNet(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != vnetsBase {
			t.Errorf("got %s %s, want POST %s", r.Method, r.URL.Path, vnetsBase)
		}
		var got VNetCreateRequest
		decodeBody(t, r, &got)
		if got.Name != "vn" || len(got.AddressSpace) != 1 || got.Region != "r1" {
			t.Errorf("body = %+v, want name=vn region=r1 one CIDR", got)
		}
		w.WriteHeader(http.StatusAccepted)
		_, _ = w.Write([]byte(`{"resource":{"id":"vn-1","name":"vn","region":"r1","address_space":["10.1.0.0/16"],"status":"PENDING"},"note":"n"}`))
	})
	vn, err := c.CreateVNet(context.Background(), "t1", "p1",
		VNetCreateRequest{Name: "vn", Region: "r1", AddressSpace: []string{"10.1.0.0/16"}})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if vn.ID != "vn-1" || len(vn.AddressSpace) != 1 || vn.AddressSpace[0] != "10.1.0.0/16" {
		t.Fatalf("vnet = %+v, want id vn-1 with CIDR 10.1.0.0/16", vn)
	}
}

// TestCreateVNet_MissingResource exercises the nil-resource guard.
func TestCreateVNet_MissingResource(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusAccepted)
		_, _ = w.Write([]byte(`{"note":"no resource"}`))
	})
	if _, err := c.CreateVNet(context.Background(), "t1", "p1", VNetCreateRequest{}); err == nil {
		t.Fatal("err = nil, want an error when the response is missing resource")
	}
}

// TestGetVNet covers 200 and the 404 sentinel.
func TestGetVNet(t *testing.T) {
	ctx := context.Background()

	t.Run("found", func(t *testing.T) {
		c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path != vnetsBase+"/vn-1" {
				t.Errorf("path = %s, unexpected", r.URL.Path)
			}
			_, _ = w.Write([]byte(`{"id":"vn-1","name":"vn","status":"ACTIVE"}`))
		})
		got, err := c.GetVNet(ctx, "t1", "p1", "vn-1")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if got == nil || got.Status != "ACTIVE" {
			t.Fatalf("got = %+v, want status ACTIVE", got)
		}
	})

	t.Run("404 returns nil,nil", func(t *testing.T) {
		c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusNotFound)
		})
		got, err := c.GetVNet(ctx, "t1", "p1", "missing")
		if err != nil || got != nil {
			t.Fatalf("got=%v err=%v, want nil,nil on 404", got, err)
		}
	})
}

// TestListVNets checks the bare-array parse.
func TestListVNets(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != vnetsBase {
			t.Errorf("path = %s, unexpected", r.URL.Path)
		}
		_, _ = w.Write([]byte(`[{"id":"vn-1","name":"a"},{"id":"vn-2","name":"b"}]`))
	})
	vnets, err := c.ListVNets(context.Background(), "t1", "p1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(vnets) != 2 {
		t.Fatalf("vnets = %+v, want two", vnets)
	}
}

// TestDeleteVNet checks the DELETE path and that a 409 (child subnets exist)
// surfaces as an error the resource layer can detect.
func TestDeleteVNet(t *testing.T) {
	ctx := context.Background()

	t.Run("success", func(t *testing.T) {
		c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
			if r.Method != http.MethodDelete || r.URL.Path != vnetsBase+"/vn-1" {
				t.Errorf("got %s %s, unexpected", r.Method, r.URL.Path)
			}
			w.WriteHeader(http.StatusAccepted)
		})
		if err := c.DeleteVNet(ctx, "t1", "p1", "vn-1"); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
	})

	t.Run("409 errors", func(t *testing.T) {
		c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusConflict)
			_, _ = w.Write([]byte(`{"error":"vnet still has subnets"}`))
		})
		if err := c.DeleteVNet(ctx, "t1", "p1", "vn-1"); err == nil {
			t.Fatal("err = nil, want an error when subnets still exist (409)")
		}
	})
}
