// Unit tests for region.go.
package client

import (
	"context"
	"net/http"
	"testing"
)

// TestListRegions guards that ListRegions hits the platform-wide path (no tenant)
// and unwraps the {"items": [...]} envelope — unlike every other list endpoint,
// this one is NOT a bare array — including nested zones and nullable agent status.
func TestListRegions(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Errorf("method = %s, want GET", r.Method)
		}
		if r.URL.Path != "/v1/regions" {
			t.Errorf("path = %s, want /v1/regions", r.URL.Path)
		}
		_, _ = w.Write([]byte(`{"items":[{"name":"r1","display_name":"Region One","status":"up","zones":[{"name":"z1","status":"up","agent":{"version":"1.2","last_seen":"now"}},{"name":"z2","status":"down","agent":null}]}]}`))
	})
	regions, err := c.ListRegions(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(regions) != 1 || regions[0].Name != "r1" {
		t.Fatalf("regions = %+v, want one region r1", regions)
	}
	if len(regions[0].Zones) != 2 {
		t.Fatalf("zones = %+v, want two", regions[0].Zones)
	}
	if regions[0].Zones[0].Agent == nil || regions[0].Zones[0].Agent.Version != "1.2" {
		t.Errorf("zone z1 agent = %+v, want version 1.2", regions[0].Zones[0].Agent)
	}
	if regions[0].Zones[1].Agent != nil {
		t.Errorf("zone z2 agent = %+v, want nil (no heartbeat)", regions[0].Zones[1].Agent)
	}
}

// TestListRegions_Error surfaces an API failure.
func TestListRegions_Error(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	})
	if _, err := c.ListRegions(context.Background()); err == nil {
		t.Fatal("err = nil, want an error for HTTP 500")
	}
}

// TestListRegions_InvalidJSON hits the parse-failure branch.
func TestListRegions_InvalidJSON(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`not json`))
	})
	if _, err := c.ListRegions(context.Background()); err == nil {
		t.Fatal("err = nil, want a parse error for a non-JSON body")
	}
}
