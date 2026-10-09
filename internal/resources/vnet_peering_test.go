// Unit tests for vnet_peering.go.
//
// VNet peerings are ASYNC: Create returns 202 and then polls GET until status "ACTIVE"
// (waitForVNetPeeringActive) before running Read; Delete returns and then polls GET until
// HTTP 404 (waitForVNetPeeringDeleted). So the httptest handlers below switch on
// r.Method / r.URL.Path and answer the create+GET poll with status "ACTIVE" immediately,
// and the delete poll with 404 immediately, so the StateChangeConf completes on its first
// refresh with no real waiting.
//
// Create parses the OUTER wrapper {"resource": {...}}; Get/Read parse the BARE object.
// See project_test.go for the shared httptest + schema.TestResourceDataRaw patterns.
package resources

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/hashicorp/terraform-plugin-sdk/v2/helper/schema"
	"terraform-provider-dcapi/internal/client"
)

// newTestVNetPeeringData builds a *schema.ResourceData for ResourceVNetPeering().
func newTestVNetPeeringData(t *testing.T, raw map[string]interface{}) *schema.ResourceData {
	t.Helper()
	return schema.TestResourceDataRaw(t, ResourceVNetPeering().Schema, raw)
}

// vnetPeeringActiveJSON is the bare peering object (status ACTIVE) returned by GET.
const vnetPeeringActiveJSON = `{
	"id": "peer-1",
	"vnet_id": "vnet-1",
	"peer_vnet_id": "vnet-2",
	"tenant_id": "acme",
	"name": "to-vnet-2",
	"allow_forwarded_traffic": true,
	"status": "ACTIVE",
	"provider_type": "kubeovn",
	"message": "ok",
	"warning": "",
	"created_at": "2026-01-01T00:00:00Z",
	"updated_at": "2026-01-02T00:00:00Z"
}`

func TestResourceVNetPeering_Schema(t *testing.T) {
	s := ResourceVNetPeering().Schema

	immutableRequired := []string{"tenant_id", "project_id", "vnet_id", "name", "peer_vnet_id"}
	for _, key := range immutableRequired {
		f := s[key]
		if f == nil {
			t.Fatalf("schema is missing field %q", key)
		}
		if !f.Required {
			t.Errorf("%s: Required = false, want true", key)
		}
		if !f.ForceNew {
			t.Errorf("%s: ForceNew = false, want true (must be immutable)", key)
		}
	}

	// allow_forwarded_traffic is Optional but still immutable (ForceNew).
	if f := s["allow_forwarded_traffic"]; f == nil {
		t.Fatal("schema is missing field allow_forwarded_traffic")
	} else {
		if !f.Optional {
			t.Error("allow_forwarded_traffic: Optional = false, want true")
		}
		if !f.ForceNew {
			t.Error("allow_forwarded_traffic: ForceNew = false, want true")
		}
	}

	computedOnly := []string{"peering_id", "status", "provider_type", "message", "warning", "created_at", "updated_at"}
	for _, key := range computedOnly {
		f := s[key]
		if f == nil {
			t.Fatalf("schema is missing field %q", key)
		}
		if !f.Computed {
			t.Errorf("%s: Computed = false, want true", key)
		}
		if f.Required || f.Optional {
			t.Errorf("%s: Required=%v Optional=%v, want both false (API-only field)", key, f.Required, f.Optional)
		}
	}
}

// TestResourceVNetPeeringCreate_Success drives the full async create: POST (202, wrapped),
// then the ACTIVE poll, then Read. It asserts the POST method/path, the four-part composite
// ID, and that fields are copied into state.
func TestResourceVNetPeeringCreate_Success(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodPost:
			if r.URL.Path != "/v1/tenants/acme/projects/infra/vnets/vnet-1/peerings" {
				t.Errorf("POST path = %s, want .../vnets/vnet-1/peerings", r.URL.Path)
			}
			w.WriteHeader(http.StatusAccepted)
			w.Write([]byte(`{"resource": ` + vnetPeeringActiveJSON + `, "note": "accepted"}`))
		case http.MethodGet:
			if r.URL.Path != "/v1/tenants/acme/projects/infra/vnets/vnet-1/peerings/peer-1" {
				t.Errorf("GET path = %s, want .../peerings/peer-1", r.URL.Path)
			}
			w.WriteHeader(http.StatusOK)
			w.Write([]byte(vnetPeeringActiveJSON))
		default:
			t.Errorf("unexpected method %s", r.Method)
		}
	}))
	defer server.Close()

	c, err := client.NewClient(server.URL, "test-token")
	if err != nil {
		t.Fatalf("client.NewClient: %v", err)
	}

	d := newTestVNetPeeringData(t, map[string]interface{}{
		"tenant_id":               "acme",
		"project_id":              "infra",
		"vnet_id":                 "vnet-1",
		"name":                    "to-vnet-2",
		"peer_vnet_id":            "vnet-2",
		"allow_forwarded_traffic": true,
	})

	diags := resourceVNetPeeringCreate(context.Background(), d, c)
	if diags.HasError() {
		t.Fatalf("create returned unexpected error diagnostics: %v", diags)
	}

	if got, want := d.Id(), "acme/infra/vnet-1/peer-1"; got != want {
		t.Errorf("Id() = %q, want %q", got, want)
	}
	if got, want := d.Get("peering_id").(string), "peer-1"; got != want {
		t.Errorf("peering_id = %q, want %q", got, want)
	}
	if got, want := d.Get("status").(string), "ACTIVE"; got != want {
		t.Errorf("status = %q, want %q", got, want)
	}
	if got, want := d.Get("provider_type").(string), "kubeovn"; got != want {
		t.Errorf("provider_type = %q, want %q", got, want)
	}
}

// TestResourceVNetPeeringCreate_APIError verifies a failed POST surfaces an error and never
// sets an ID.
func TestResourceVNetPeeringCreate_APIError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		w.Write([]byte(`{"error":"overlapping address space"}`))
	}))
	defer server.Close()

	c, _ := client.NewClient(server.URL, "test-token")
	d := newTestVNetPeeringData(t, map[string]interface{}{
		"tenant_id":    "acme",
		"project_id":   "infra",
		"vnet_id":      "vnet-1",
		"name":         "to-vnet-2",
		"peer_vnet_id": "vnet-2",
	})

	diags := resourceVNetPeeringCreate(context.Background(), d, c)
	if !diags.HasError() {
		t.Fatal("diags.HasError() = false, want true for a failed create")
	}
	if d.Id() != "" {
		t.Errorf("Id() = %q, want empty string when create fails", d.Id())
	}
}

// TestResourceVNetPeeringRead_Success verifies a GET refreshes every field in state.
func TestResourceVNetPeeringRead_Success(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/tenants/acme/projects/infra/vnets/vnet-1/peerings/peer-1" {
			t.Errorf("path = %s, want .../peerings/peer-1", r.URL.Path)
		}
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(vnetPeeringActiveJSON))
	}))
	defer server.Close()

	c, _ := client.NewClient(server.URL, "test-token")
	d := newTestVNetPeeringData(t, map[string]interface{}{})
	d.SetId("acme/infra/vnet-1/peer-1")

	diags := resourceVNetPeeringRead(context.Background(), d, c)
	if diags.HasError() {
		t.Fatalf("read returned unexpected error diagnostics: %v", diags)
	}
	if got, want := d.Get("name").(string), "to-vnet-2"; got != want {
		t.Errorf("name = %q, want %q", got, want)
	}
	if got, want := d.Get("peer_vnet_id").(string), "vnet-2"; got != want {
		t.Errorf("peer_vnet_id = %q, want %q", got, want)
	}
	if got, want := d.Get("allow_forwarded_traffic").(bool), true; got != want {
		t.Errorf("allow_forwarded_traffic = %v, want %v", got, want)
	}
}

// TestResourceVNetPeeringRead_NotFound verifies a 404 (GetVNetPeering -> (nil,nil)) clears
// the ID with no error diagnostics.
func TestResourceVNetPeeringRead_NotFound(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		w.Write([]byte(`{"error":"peering not found"}`))
	}))
	defer server.Close()

	c, _ := client.NewClient(server.URL, "test-token")
	d := newTestVNetPeeringData(t, map[string]interface{}{})
	d.SetId("acme/infra/vnet-1/peer-1")

	diags := resourceVNetPeeringRead(context.Background(), d, c)
	if diags.HasError() {
		t.Fatalf("read returned unexpected error diagnostics on 404: %v", diags)
	}
	if d.Id() != "" {
		t.Errorf("Id() = %q, want empty string after a 404", d.Id())
	}
}

// TestResourceVNetPeeringRead_InvalidID verifies a state ID without exactly four parts is an
// error.
func TestResourceVNetPeeringRead_InvalidID(t *testing.T) {
	c, _ := client.NewClient("http://unused.invalid", "test-token")
	d := newTestVNetPeeringData(t, map[string]interface{}{})
	d.SetId("acme/infra") // only two parts

	diags := resourceVNetPeeringRead(context.Background(), d, c)
	if !diags.HasError() {
		t.Fatal("diags.HasError() = false, want true for a malformed state ID")
	}
}

// TestResourceVNetPeeringDelete_Success drives the async delete: DELETE, then the GET poll
// returns 404 so waitForVNetPeeringDeleted completes on the first refresh.
func TestResourceVNetPeeringDelete_Success(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodDelete:
			if r.URL.Path != "/v1/tenants/acme/projects/infra/vnets/vnet-1/peerings/peer-1" {
				t.Errorf("DELETE path = %s, want .../peerings/peer-1", r.URL.Path)
			}
			w.WriteHeader(http.StatusAccepted)
		case http.MethodGet:
			w.WriteHeader(http.StatusNotFound)
			w.Write([]byte(`{"error":"peering not found"}`))
		default:
			t.Errorf("unexpected method %s", r.Method)
		}
	}))
	defer server.Close()

	c, _ := client.NewClient(server.URL, "test-token")
	d := newTestVNetPeeringData(t, map[string]interface{}{})
	d.SetId("acme/infra/vnet-1/peer-1")

	diags := resourceVNetPeeringDelete(context.Background(), d, c)
	if diags.HasError() {
		t.Fatalf("delete returned unexpected error diagnostics: %v", diags)
	}
}

// TestResourceVNetPeeringDelete_NotFound verifies a 404 on the DELETE itself is treated as
// success and clears the ID.
func TestResourceVNetPeeringDelete_NotFound(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		w.Write([]byte(`{"error":"peering not found"}`))
	}))
	defer server.Close()

	c, _ := client.NewClient(server.URL, "test-token")
	d := newTestVNetPeeringData(t, map[string]interface{}{})
	d.SetId("acme/infra/vnet-1/peer-1")

	diags := resourceVNetPeeringDelete(context.Background(), d, c)
	if diags.HasError() {
		t.Fatalf("delete returned unexpected error diagnostics on 404: %v", diags)
	}
	if d.Id() != "" {
		t.Errorf("Id() = %q, want empty string after a 404 delete", d.Id())
	}
}

// TestResourceVNetPeeringDelete_Error verifies a non-404 DELETE failure surfaces as an error
// and keeps the ID.
func TestResourceVNetPeeringDelete_Error(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte(`{"error":"boom"}`))
	}))
	defer server.Close()

	c, _ := client.NewClient(server.URL, "test-token")
	d := newTestVNetPeeringData(t, map[string]interface{}{})
	d.SetId("acme/infra/vnet-1/peer-1")

	diags := resourceVNetPeeringDelete(context.Background(), d, c)
	if !diags.HasError() {
		t.Fatal("diags.HasError() = false, want true for a 500 delete")
	}
	if d.Id() == "" {
		t.Error("Id() = \"\", want it to remain set since delete failed")
	}
}
