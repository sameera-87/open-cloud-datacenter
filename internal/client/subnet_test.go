// Unit tests for subnet.go.
package client

import (
	"context"
	"net/http"
	"testing"
)

const subnetsBase = "/v1/tenants/t1/projects/p1/vnets/vn1/subnets"

// TestCreateSubnet guards the POST path, body forwarding, and that the inner
// "resource" object is unwrapped.
func TestCreateSubnet(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != subnetsBase {
			t.Errorf("got %s %s, want POST %s", r.Method, r.URL.Path, subnetsBase)
		}
		var got SubnetCreateRequest
		decodeBody(t, r, &got)
		if got.Name != "sn" || got.CIDR != "10.1.1.0/24" {
			t.Errorf("body = %+v, want name=sn cidr=10.1.1.0/24", got)
		}
		w.WriteHeader(http.StatusAccepted)
		_, _ = w.Write([]byte(`{"resource":{"id":"sn-1","name":"sn","cidr":"10.1.1.0/24","status":"PENDING"},"note":"n"}`))
	})
	sn, err := c.CreateSubnet(context.Background(), "t1", "p1", "vn1",
		SubnetCreateRequest{Name: "sn", CIDR: "10.1.1.0/24"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if sn.ID != "sn-1" || sn.CIDR != "10.1.1.0/24" {
		t.Fatalf("subnet = %+v, want id sn-1 cidr 10.1.1.0/24", sn)
	}
}

// TestCreateSubnet_MissingResource exercises the nil-resource guard.
func TestCreateSubnet_MissingResource(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusAccepted)
		_, _ = w.Write([]byte(`{"note":"no resource"}`))
	})
	if _, err := c.CreateSubnet(context.Background(), "t1", "p1", "vn1", SubnetCreateRequest{}); err == nil {
		t.Fatal("err = nil, want an error when the response is missing resource")
	}
}

// TestGetSubnet covers 200 and the 404 sentinel.
func TestGetSubnet(t *testing.T) {
	ctx := context.Background()

	t.Run("found", func(t *testing.T) {
		c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path != subnetsBase+"/sn-1" {
				t.Errorf("path = %s, unexpected", r.URL.Path)
			}
			_, _ = w.Write([]byte(`{"id":"sn-1","name":"sn","gateway":"10.1.1.1"}`))
		})
		got, err := c.GetSubnet(ctx, "t1", "p1", "vn1", "sn-1")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if got == nil || got.Gateway != "10.1.1.1" {
			t.Fatalf("got = %+v, want gateway 10.1.1.1", got)
		}
	})

	t.Run("404 returns nil,nil", func(t *testing.T) {
		c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusNotFound)
		})
		got, err := c.GetSubnet(ctx, "t1", "p1", "vn1", "missing")
		if err != nil || got != nil {
			t.Fatalf("got=%v err=%v, want nil,nil on 404", got, err)
		}
	})
}

// TestListSubnets checks the bare-array parse.
func TestListSubnets(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != subnetsBase {
			t.Errorf("path = %s, unexpected", r.URL.Path)
		}
		_, _ = w.Write([]byte(`[{"id":"sn-1","name":"a"},{"id":"sn-2","name":"b"}]`))
	})
	subnets, err := c.ListSubnets(context.Background(), "t1", "p1", "vn1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(subnets) != 2 {
		t.Fatalf("subnets = %+v, want two", subnets)
	}
}

// TestDeleteSubnet checks the DELETE path and that a 409 (NSG attachments) errors.
func TestDeleteSubnet(t *testing.T) {
	ctx := context.Background()

	t.Run("success", func(t *testing.T) {
		c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
			if r.Method != http.MethodDelete || r.URL.Path != subnetsBase+"/sn-1" {
				t.Errorf("got %s %s, unexpected", r.Method, r.URL.Path)
			}
			w.WriteHeader(http.StatusAccepted)
		})
		if err := c.DeleteSubnet(ctx, "t1", "p1", "vn1", "sn-1"); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
	})

	t.Run("409 errors", func(t *testing.T) {
		c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusConflict)
		})
		if err := c.DeleteSubnet(ctx, "t1", "p1", "vn1", "sn-1"); err == nil {
			t.Fatal("err = nil, want an error when NSG attachments exist (409)")
		}
	})
}
