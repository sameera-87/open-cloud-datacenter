// Unit tests for route_table.go.
package client

import (
	"context"
	"net/http"
	"testing"
)

const routeTablesBase = "/v1/tenants/t1/projects/p1/vnets/vn1/route-tables"

// TestCreateRouteTable guards the POST path, route forwarding, and parse.
func TestCreateRouteTable(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != routeTablesBase {
			t.Errorf("got %s %s, want POST %s", r.Method, r.URL.Path, routeTablesBase)
		}
		var got RouteTableCreateRequest
		decodeBody(t, r, &got)
		if got.Name != "rt" || len(got.Routes) != 1 || got.Routes[0].DestinationCIDR != "0.0.0.0/0" {
			t.Errorf("body = %+v, want name=rt one default route", got)
		}
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"id":"rt-1","name":"rt","routes":[{"name":"default","destination_cidr":"0.0.0.0/0","next_hop_type":"internet"}]}`))
	})
	rt, err := c.CreateRouteTable(context.Background(), "t1", "p1", "vn1", RouteTableCreateRequest{
		Name:   "rt",
		Routes: []RouteEntry{{Name: "default", DestinationCIDR: "0.0.0.0/0", NextHopType: "internet"}},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if rt.ID != "rt-1" || len(rt.Routes) != 1 {
		t.Fatalf("rt = %+v, want id rt-1 one route", rt)
	}
}

// TestGetRouteTable covers 200 (including associations) and the 404 sentinel.
func TestGetRouteTable(t *testing.T) {
	ctx := context.Background()

	t.Run("found", func(t *testing.T) {
		c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path != routeTablesBase+"/rt-1" {
				t.Errorf("path = %s, unexpected", r.URL.Path)
			}
			_, _ = w.Write([]byte(`{"id":"rt-1","name":"rt","associations":[{"id":"a-1","subnet_id":"sn1"}]}`))
		})
		got, err := c.GetRouteTable(ctx, "t1", "p1", "vn1", "rt-1")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if got == nil || len(got.Associations) != 1 || got.Associations[0].SubnetID != "sn1" {
			t.Fatalf("got = %+v, want one association for sn1", got)
		}
	})

	t.Run("404 returns nil,nil", func(t *testing.T) {
		c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusNotFound)
		})
		got, err := c.GetRouteTable(ctx, "t1", "p1", "vn1", "missing")
		if err != nil || got != nil {
			t.Fatalf("got=%v err=%v, want nil,nil on 404", got, err)
		}
	})
}

// TestListRouteTables checks the bare-array parse.
func TestListRouteTables(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != routeTablesBase {
			t.Errorf("path = %s, unexpected", r.URL.Path)
		}
		_, _ = w.Write([]byte(`[{"id":"rt-1","name":"a"},{"id":"rt-2","name":"b"}]`))
	})
	rts, err := c.ListRouteTables(context.Background(), "t1", "p1", "vn1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(rts) != 2 {
		t.Fatalf("rts = %+v, want two", rts)
	}
}

// TestUpdateRouteTable guards the PUT full-replace path.
func TestUpdateRouteTable(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPut || r.URL.Path != routeTablesBase+"/rt-1" {
			t.Errorf("got %s %s, want PUT .../rt-1", r.Method, r.URL.Path)
		}
		var got RouteTableUpdateRequest
		decodeBody(t, r, &got)
		if len(got.Routes) != 2 {
			t.Errorf("routes = %v, want two", got.Routes)
		}
		_, _ = w.Write([]byte(`{"id":"rt-1","routes":[{"name":"a"},{"name":"b"}]}`))
	})
	rt, err := c.UpdateRouteTable(context.Background(), "t1", "p1", "vn1", "rt-1",
		RouteTableUpdateRequest{Routes: []RouteEntry{{Name: "a"}, {Name: "b"}}})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(rt.Routes) != 2 {
		t.Fatalf("routes = %v, want two", rt.Routes)
	}
}

// TestDeleteRouteTable checks the DELETE path and error wrapping.
func TestDeleteRouteTable(t *testing.T) {
	ctx := context.Background()

	t.Run("success", func(t *testing.T) {
		c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
			if r.Method != http.MethodDelete || r.URL.Path != routeTablesBase+"/rt-1" {
				t.Errorf("got %s %s, unexpected", r.Method, r.URL.Path)
			}
			w.WriteHeader(http.StatusNoContent)
		})
		if err := c.DeleteRouteTable(ctx, "t1", "p1", "vn1", "rt-1"); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
	})

	t.Run("error", func(t *testing.T) {
		c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusInternalServerError)
		})
		if err := c.DeleteRouteTable(ctx, "t1", "p1", "vn1", "rt-1"); err == nil {
			t.Fatal("err = nil, want an error for HTTP 500")
		}
	})
}

// TestCreateRouteTableAssociation guards the POST .../associations path and that
// the warning field (e.g. subnet already associated) is parsed.
func TestCreateRouteTableAssociation(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != routeTablesBase+"/rt-1/associations" {
			t.Errorf("got %s %s, want POST .../associations", r.Method, r.URL.Path)
		}
		var got RouteTableAssociationCreateRequest
		decodeBody(t, r, &got)
		if got.SubnetID != "sn1" {
			t.Errorf("subnet = %q, want sn1", got.SubnetID)
		}
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"id":"a-1","route_table_id":"rt-1","subnet_id":"sn1","warning":"replaced previous"}`))
	})
	assoc, err := c.CreateRouteTableAssociation(context.Background(), "t1", "p1", "vn1", "rt-1",
		RouteTableAssociationCreateRequest{SubnetID: "sn1"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if assoc.ID != "a-1" || assoc.Warning != "replaced previous" {
		t.Fatalf("assoc = %+v, want id a-1 with warning", assoc)
	}
}

// TestDeleteRouteTableAssociation checks the DELETE .../associations/{id} path.
func TestDeleteRouteTableAssociation(t *testing.T) {
	ctx := context.Background()

	t.Run("success", func(t *testing.T) {
		c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
			if r.Method != http.MethodDelete || r.URL.Path != routeTablesBase+"/rt-1/associations/a-1" {
				t.Errorf("got %s %s, unexpected", r.Method, r.URL.Path)
			}
			w.WriteHeader(http.StatusNoContent)
		})
		if err := c.DeleteRouteTableAssociation(ctx, "t1", "p1", "vn1", "rt-1", "a-1"); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
	})

	t.Run("error", func(t *testing.T) {
		c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusInternalServerError)
		})
		if err := c.DeleteRouteTableAssociation(ctx, "t1", "p1", "vn1", "rt-1", "a-1"); err == nil {
			t.Fatal("err = nil, want an error for HTTP 500")
		}
	})
}
