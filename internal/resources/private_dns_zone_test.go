// Unit tests for private_dns_zone.go.
//
// Private DNS zones are ASYNC like VNet peerings: Create returns 202 and polls GET until
// status "ACTIVE" before running Read; Delete polls GET until HTTP 404. The httptest
// handlers switch on r.Method and answer the create+GET poll with "ACTIVE" immediately and
// the delete poll with 404 immediately, so each StateChangeConf completes on its first
// refresh.
//
// Create parses the OUTER wrapper {"resource": {...}}; Get/Read parse the BARE object.
package resources

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/hashicorp/terraform-plugin-sdk/v2/helper/schema"
	"terraform-provider-dcapi/internal/client"
)

// newTestPrivateDNSZoneData builds a *schema.ResourceData for ResourcePrivateDnsZone().
func newTestPrivateDNSZoneData(t *testing.T, raw map[string]interface{}) *schema.ResourceData {
	t.Helper()
	return schema.TestResourceDataRaw(t, ResourcePrivateDnsZone().Schema, raw)
}

// privateDNSZoneActiveJSON is the bare zone object (status ACTIVE) returned by GET.
const privateDNSZoneActiveJSON = `{
	"id": "zone-1",
	"vnet_id": "vnet-1",
	"tenant_id": "acme",
	"name": "internal.wso2.com",
	"description": "internal zone",
	"status": "ACTIVE",
	"provider_type": "kubeovn",
	"message": "ok",
	"created_at": "2026-01-01T00:00:00Z",
	"updated_at": "2026-01-02T00:00:00Z"
}`

func TestResourcePrivateDNSZone_Schema(t *testing.T) {
	s := ResourcePrivateDnsZone().Schema

	immutableRequired := []string{"tenant_id", "project_id", "vnet_id", "name"}
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

	// description is Optional but still immutable (ForceNew).
	if f := s["description"]; f == nil {
		t.Fatal("schema is missing field description")
	} else {
		if !f.Optional {
			t.Error("description: Optional = false, want true")
		}
		if !f.ForceNew {
			t.Error("description: ForceNew = false, want true")
		}
	}

	computedOnly := []string{"zone_id", "status", "provider_type", "message", "created_at", "updated_at"}
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

// TestResourcePrivateDNSZoneCreate_Success drives the full async create: POST (202, wrapped),
// the ACTIVE poll, then Read. Asserts POST method/path, the four-part composite ID, and copied
// fields.
func TestResourcePrivateDNSZoneCreate_Success(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodPost:
			if r.URL.Path != "/v1/tenants/acme/projects/infra/vnets/vnet-1/dns-zones" {
				t.Errorf("POST path = %s, want .../vnets/vnet-1/dns-zones", r.URL.Path)
			}
			w.WriteHeader(http.StatusAccepted)
			w.Write([]byte(`{"resource": ` + privateDNSZoneActiveJSON + `, "note": "accepted"}`))
		case http.MethodGet:
			if r.URL.Path != "/v1/tenants/acme/projects/infra/vnets/vnet-1/dns-zones/zone-1" {
				t.Errorf("GET path = %s, want .../dns-zones/zone-1", r.URL.Path)
			}
			w.WriteHeader(http.StatusOK)
			w.Write([]byte(privateDNSZoneActiveJSON))
		default:
			t.Errorf("unexpected method %s", r.Method)
		}
	}))
	defer server.Close()

	c, err := client.NewClient(server.URL, "test-token")
	if err != nil {
		t.Fatalf("client.NewClient: %v", err)
	}

	d := newTestPrivateDNSZoneData(t, map[string]interface{}{
		"tenant_id":   "acme",
		"project_id":  "infra",
		"vnet_id":     "vnet-1",
		"name":        "internal.wso2.com",
		"description": "internal zone",
	})

	diags := resourcePrivateDnsZoneCreate(context.Background(), d, c)
	if diags.HasError() {
		t.Fatalf("create returned unexpected error diagnostics: %v", diags)
	}

	if got, want := d.Id(), "acme/infra/vnet-1/zone-1"; got != want {
		t.Errorf("Id() = %q, want %q", got, want)
	}
	if got, want := d.Get("zone_id").(string), "zone-1"; got != want {
		t.Errorf("zone_id = %q, want %q", got, want)
	}
	if got, want := d.Get("status").(string), "ACTIVE"; got != want {
		t.Errorf("status = %q, want %q", got, want)
	}
}

// TestResourcePrivateDNSZoneCreate_APIError verifies a failed POST surfaces an error and never
// sets an ID.
func TestResourcePrivateDNSZoneCreate_APIError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		w.Write([]byte(`{"error":"zone already exists"}`))
	}))
	defer server.Close()

	c, _ := client.NewClient(server.URL, "test-token")
	d := newTestPrivateDNSZoneData(t, map[string]interface{}{
		"tenant_id":  "acme",
		"project_id": "infra",
		"vnet_id":    "vnet-1",
		"name":       "internal.wso2.com",
	})

	diags := resourcePrivateDnsZoneCreate(context.Background(), d, c)
	if !diags.HasError() {
		t.Fatal("diags.HasError() = false, want true for a failed create")
	}
	if d.Id() != "" {
		t.Errorf("Id() = %q, want empty string when create fails", d.Id())
	}
}

// TestResourcePrivateDNSZoneRead_Success verifies a GET refreshes state fields.
func TestResourcePrivateDNSZoneRead_Success(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/tenants/acme/projects/infra/vnets/vnet-1/dns-zones/zone-1" {
			t.Errorf("path = %s, want .../dns-zones/zone-1", r.URL.Path)
		}
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(privateDNSZoneActiveJSON))
	}))
	defer server.Close()

	c, _ := client.NewClient(server.URL, "test-token")
	d := newTestPrivateDNSZoneData(t, map[string]interface{}{})
	d.SetId("acme/infra/vnet-1/zone-1")

	diags := resourcePrivateDnsZoneRead(context.Background(), d, c)
	if diags.HasError() {
		t.Fatalf("read returned unexpected error diagnostics: %v", diags)
	}
	if got, want := d.Get("name").(string), "internal.wso2.com"; got != want {
		t.Errorf("name = %q, want %q", got, want)
	}
	if got, want := d.Get("description").(string), "internal zone"; got != want {
		t.Errorf("description = %q, want %q", got, want)
	}
}

// TestResourcePrivateDNSZoneRead_NotFound verifies a 404 clears the ID without error.
func TestResourcePrivateDNSZoneRead_NotFound(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		w.Write([]byte(`{"error":"zone not found"}`))
	}))
	defer server.Close()

	c, _ := client.NewClient(server.URL, "test-token")
	d := newTestPrivateDNSZoneData(t, map[string]interface{}{})
	d.SetId("acme/infra/vnet-1/zone-1")

	diags := resourcePrivateDnsZoneRead(context.Background(), d, c)
	if diags.HasError() {
		t.Fatalf("read returned unexpected error diagnostics on 404: %v", diags)
	}
	if d.Id() != "" {
		t.Errorf("Id() = %q, want empty string after a 404", d.Id())
	}
}

// TestResourcePrivateDNSZoneRead_InvalidID verifies a state ID without exactly four parts is an
// error.
func TestResourcePrivateDNSZoneRead_InvalidID(t *testing.T) {
	c, _ := client.NewClient("http://unused.invalid", "test-token")
	d := newTestPrivateDNSZoneData(t, map[string]interface{}{})
	d.SetId("acme/infra/vnet-1") // only three parts

	diags := resourcePrivateDnsZoneRead(context.Background(), d, c)
	if !diags.HasError() {
		t.Fatal("diags.HasError() = false, want true for a malformed state ID")
	}
}

// TestResourcePrivateDNSZoneDelete_Success drives the async delete: DELETE, then the GET poll
// returns 404 so waitForPrivateDnsZoneDeleted completes on the first refresh.
func TestResourcePrivateDNSZoneDelete_Success(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodDelete:
			if r.URL.Path != "/v1/tenants/acme/projects/infra/vnets/vnet-1/dns-zones/zone-1" {
				t.Errorf("DELETE path = %s, want .../dns-zones/zone-1", r.URL.Path)
			}
			w.WriteHeader(http.StatusAccepted)
		case http.MethodGet:
			w.WriteHeader(http.StatusNotFound)
			w.Write([]byte(`{"error":"zone not found"}`))
		default:
			t.Errorf("unexpected method %s", r.Method)
		}
	}))
	defer server.Close()

	c, _ := client.NewClient(server.URL, "test-token")
	d := newTestPrivateDNSZoneData(t, map[string]interface{}{})
	d.SetId("acme/infra/vnet-1/zone-1")

	diags := resourcePrivateDnsZoneDelete(context.Background(), d, c)
	if diags.HasError() {
		t.Fatalf("delete returned unexpected error diagnostics: %v", diags)
	}
}

// TestResourcePrivateDNSZoneDelete_NotFound verifies a 404 on the DELETE itself is treated as
// success and clears the ID.
func TestResourcePrivateDNSZoneDelete_NotFound(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		w.Write([]byte(`{"error":"zone not found"}`))
	}))
	defer server.Close()

	c, _ := client.NewClient(server.URL, "test-token")
	d := newTestPrivateDNSZoneData(t, map[string]interface{}{})
	d.SetId("acme/infra/vnet-1/zone-1")

	diags := resourcePrivateDnsZoneDelete(context.Background(), d, c)
	if diags.HasError() {
		t.Fatalf("delete returned unexpected error diagnostics on 404: %v", diags)
	}
	if d.Id() != "" {
		t.Errorf("Id() = %q, want empty string after a 404 delete", d.Id())
	}
}

// TestResourcePrivateDNSZoneDelete_Error verifies a non-404 DELETE failure surfaces as an error
// and keeps the ID.
func TestResourcePrivateDNSZoneDelete_Error(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte(`{"error":"boom"}`))
	}))
	defer server.Close()

	c, _ := client.NewClient(server.URL, "test-token")
	d := newTestPrivateDNSZoneData(t, map[string]interface{}{})
	d.SetId("acme/infra/vnet-1/zone-1")

	diags := resourcePrivateDnsZoneDelete(context.Background(), d, c)
	if !diags.HasError() {
		t.Fatal("diags.HasError() = false, want true for a 500 delete")
	}
	if d.Id() == "" {
		t.Error("Id() = \"\", want it to remain set since delete failed")
	}
}
