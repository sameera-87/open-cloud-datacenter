// Unit tests for vnet_peering.go.
package client

import (
	"context"
	"net/http"
	"testing"
)

const vnetPeeringsBase = "/v1/tenants/t1/projects/p1/vnets/vn1/peerings"

// TestCreateVNetPeering guards the POST path, body forwarding, and that the inner
// "resource" object is unwrapped.
func TestCreateVNetPeering(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != vnetPeeringsBase {
			t.Errorf("got %s %s, want POST %s", r.Method, r.URL.Path, vnetPeeringsBase)
		}
		var got VNetPeeringCreateRequest
		decodeBody(t, r, &got)
		if got.Name != "px" || got.PeerVNetID != "vn2" {
			t.Errorf("body = %+v, want name=px peer=vn2", got)
		}
		w.WriteHeader(http.StatusAccepted)
		_, _ = w.Write([]byte(`{"resource":{"id":"pr-1","name":"px","peer_vnet_id":"vn2","status":"PENDING"},"note":"n"}`))
	})
	pr, err := c.CreateVNetPeering(context.Background(), "t1", "p1", "vn1",
		VNetPeeringCreateRequest{Name: "px", PeerVNetID: "vn2"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if pr.ID != "pr-1" || pr.PeerVNetID != "vn2" {
		t.Fatalf("peering = %+v, want id pr-1 peer vn2", pr)
	}
}

// TestCreateVNetPeering_MissingResource exercises the nil-resource guard.
func TestCreateVNetPeering_MissingResource(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusAccepted)
		_, _ = w.Write([]byte(`{"note":"no resource"}`))
	})
	if _, err := c.CreateVNetPeering(context.Background(), "t1", "p1", "vn1", VNetPeeringCreateRequest{}); err == nil {
		t.Fatal("err = nil, want an error when the response is missing resource")
	}
}

// TestGetVNetPeering covers 200 and the 404 sentinel.
func TestGetVNetPeering(t *testing.T) {
	ctx := context.Background()

	t.Run("found", func(t *testing.T) {
		c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path != vnetPeeringsBase+"/pr-1" {
				t.Errorf("path = %s, unexpected", r.URL.Path)
			}
			_, _ = w.Write([]byte(`{"id":"pr-1","name":"px","allow_forwarded_traffic":true,"status":"ACTIVE"}`))
		})
		got, err := c.GetVNetPeering(ctx, "t1", "p1", "vn1", "pr-1")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if got == nil || !got.AllowForwardedTraffic {
			t.Fatalf("got = %+v, want allow_forwarded_traffic true", got)
		}
	})

	t.Run("404 returns nil,nil", func(t *testing.T) {
		c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusNotFound)
		})
		got, err := c.GetVNetPeering(ctx, "t1", "p1", "vn1", "missing")
		if err != nil || got != nil {
			t.Fatalf("got=%v err=%v, want nil,nil on 404", got, err)
		}
	})
}

// TestListVNetPeerings checks the bare-array parse.
func TestListVNetPeerings(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != vnetPeeringsBase {
			t.Errorf("path = %s, unexpected", r.URL.Path)
		}
		_, _ = w.Write([]byte(`[{"id":"pr-1","name":"a"},{"id":"pr-2","name":"b"}]`))
	})
	peerings, err := c.ListVNetPeerings(context.Background(), "t1", "p1", "vn1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(peerings) != 2 {
		t.Fatalf("peerings = %+v, want two", peerings)
	}
}

// TestDeleteVNetPeering checks the DELETE path and error wrapping.
func TestDeleteVNetPeering(t *testing.T) {
	ctx := context.Background()

	t.Run("success", func(t *testing.T) {
		c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
			if r.Method != http.MethodDelete || r.URL.Path != vnetPeeringsBase+"/pr-1" {
				t.Errorf("got %s %s, unexpected", r.Method, r.URL.Path)
			}
			w.WriteHeader(http.StatusAccepted)
		})
		if err := c.DeleteVNetPeering(ctx, "t1", "p1", "vn1", "pr-1"); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
	})

	t.Run("error", func(t *testing.T) {
		c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusInternalServerError)
		})
		if err := c.DeleteVNetPeering(ctx, "t1", "p1", "vn1", "pr-1"); err == nil {
			t.Fatal("err = nil, want an error for HTTP 500")
		}
	})
}
