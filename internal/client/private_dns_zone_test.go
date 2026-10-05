// Unit tests for private_dns_zone.go.
package client

import (
	"context"
	"net/http"
	"testing"
)

const dnsZonesBase = "/v1/tenants/t1/projects/p1/vnets/vn1/dns-zones"

// TestCreatePrivateDnsZone guards the POST path and that the inner "resource"
// object is unwrapped and returned.
func TestCreatePrivateDnsZone(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != dnsZonesBase {
			t.Errorf("got %s %s, want POST %s", r.Method, r.URL.Path, dnsZonesBase)
		}
		var got PrivateDnsZoneCreateRequest
		decodeBody(t, r, &got)
		if got.Name != "internal.example.com" {
			t.Errorf("body name = %q, unexpected", got.Name)
		}
		w.WriteHeader(http.StatusAccepted)
		_, _ = w.Write([]byte(`{"resource":{"id":"z-1","name":"internal.example.com","status":"PENDING"},"note":"n"}`))
	})
	zone, err := c.CreatePrivateDnsZone(context.Background(), "t1", "p1", "vn1",
		PrivateDnsZoneCreateRequest{Name: "internal.example.com"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if zone.ID != "z-1" || zone.Status != "PENDING" {
		t.Fatalf("zone = %+v, want id z-1 status PENDING", zone)
	}
}

// TestCreatePrivateDnsZone_MissingResource exercises the explicit guard that a
// 2xx body without a "resource" object is treated as an error, not nil deref.
func TestCreatePrivateDnsZone_MissingResource(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusAccepted)
		_, _ = w.Write([]byte(`{"note":"no resource here"}`))
	})
	if _, err := c.CreatePrivateDnsZone(context.Background(), "t1", "p1", "vn1", PrivateDnsZoneCreateRequest{}); err == nil {
		t.Fatal("err = nil, want an error when the response is missing resource")
	}
}

// TestGetPrivateDnsZone covers 200 and the 404 sentinel.
func TestGetPrivateDnsZone(t *testing.T) {
	ctx := context.Background()

	t.Run("found", func(t *testing.T) {
		c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path != dnsZonesBase+"/z-1" {
				t.Errorf("path = %s, unexpected", r.URL.Path)
			}
			_, _ = w.Write([]byte(`{"id":"z-1","name":"internal.example.com","status":"ACTIVE"}`))
		})
		got, err := c.GetPrivateDnsZone(ctx, "t1", "p1", "vn1", "z-1")
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
		got, err := c.GetPrivateDnsZone(ctx, "t1", "p1", "vn1", "missing")
		if err != nil || got != nil {
			t.Fatalf("got=%v err=%v, want nil,nil on 404", got, err)
		}
	})
}

// TestListPrivateDnsZones checks the bare-array parse.
func TestListPrivateDnsZones(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != dnsZonesBase {
			t.Errorf("path = %s, unexpected", r.URL.Path)
		}
		_, _ = w.Write([]byte(`[{"id":"z-1","name":"a"},{"id":"z-2","name":"b"}]`))
	})
	zones, err := c.ListPrivateDnsZones(context.Background(), "t1", "p1", "vn1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(zones) != 2 {
		t.Fatalf("zones = %+v, want two", zones)
	}
}

// TestDeletePrivateDnsZone checks the DELETE path and error wrapping.
func TestDeletePrivateDnsZone(t *testing.T) {
	ctx := context.Background()

	t.Run("success", func(t *testing.T) {
		c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
			if r.Method != http.MethodDelete || r.URL.Path != dnsZonesBase+"/z-1" {
				t.Errorf("got %s %s, unexpected", r.Method, r.URL.Path)
			}
			w.WriteHeader(http.StatusAccepted)
		})
		if err := c.DeletePrivateDnsZone(ctx, "t1", "p1", "vn1", "z-1"); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
	})

	t.Run("error", func(t *testing.T) {
		c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusInternalServerError)
		})
		if err := c.DeletePrivateDnsZone(ctx, "t1", "p1", "vn1", "z-1"); err == nil {
			t.Fatal("err = nil, want an error for HTTP 500")
		}
	})
}
