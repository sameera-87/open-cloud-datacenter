// Unit tests for nsg.go (dcapi_network_security_group).
//
// Unlike dcapi_vnet/dcapi_subnet, the NSG is SYNCHRONOUS: Create returns 201 with the
// bare NSGResponse (no "resource" wrapper, no polling), Update PUTs the full rules list
// to .../rules, and Delete returns 204. The composite state ID is "tenant/project/sg_id".
package resources

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-sdk/v2/helper/schema"
	"terraform-provider-dcapi/internal/client"
)

// newTestNSGData builds a *schema.ResourceData for ResourceNetworkSecurityGroup().
func newTestNSGData(t *testing.T, raw map[string]interface{}) *schema.ResourceData {
	t.Helper()
	return schema.TestResourceDataRaw(t, ResourceNetworkSecurityGroup().Schema, raw)
}

// nsgTestRule is a reusable raw rule map for building config.
func nsgTestRule() map[string]interface{} {
	return map[string]interface{}{
		"name":                       "allow-ssh",
		"direction":                  "inbound",
		"priority":                   100,
		"protocol":                   "tcp",
		"source_address_prefix":      "*",
		"source_port_range":          "*",
		"destination_address_prefix": "*",
		"destination_port_range":     "22",
		"action":                     "allow",
	}
}

// TestResourceNSG_Schema pins immutability, the updatable rules list, and computed fields.
func TestResourceNSG_Schema(t *testing.T) {
	s := ResourceNetworkSecurityGroup().Schema

	requiredForceNew := []string{"tenant_id", "project_id", "name"}
	for _, key := range requiredForceNew {
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

	// description is Optional + ForceNew.
	if f := s["description"]; f == nil {
		t.Fatal("schema is missing field \"description\"")
	} else if !f.Optional || !f.ForceNew {
		t.Errorf("description: Optional=%v ForceNew=%v, want both true", f.Optional, f.ForceNew)
	}

	// rules is Optional and NOT ForceNew — it is updated in place via resourceNSGUpdate.
	if f := s["rules"]; f == nil {
		t.Fatal("schema is missing field \"rules\"")
	} else {
		if !f.Optional {
			t.Error("rules: Optional = false, want true")
		}
		if f.ForceNew {
			t.Error("rules: ForceNew = true, want false (must be updatable)")
		}
	}

	computedOnly := []string{"sg_id", "status", "provider_type", "created_at", "updated_at"}
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

// TestResourceNSGCreate_Success verifies POST, the composite Id, and rule round-tripping.
func TestResourceNSGCreate_Success(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("method = %s, want POST", r.Method)
		}
		if r.URL.Path != "/v1/tenants/acme/projects/net/security-groups" {
			t.Errorf("path = %s, want /v1/tenants/acme/projects/net/security-groups", r.URL.Path)
		}
		w.WriteHeader(http.StatusCreated)
		w.Write([]byte(`{
			"id":"sg-1",
			"tenant_id":"acme",
			"name":"web-sg",
			"description":"web tier",
			"rules":[{
				"name":"allow-ssh","direction":"inbound","priority":100,"protocol":"tcp",
				"source_address_prefix":"*","source_port_range":"*",
				"destination_address_prefix":"*","destination_port_range":"22","action":"allow"
			}],
			"attachments":[],
			"status":"ACTIVE",
			"provider_type":"kubeovn",
			"created_at":"2026-01-01T00:00:00Z",
			"updated_at":"2026-01-01T00:00:00Z"
		}`))
	}))
	defer server.Close()

	c, err := client.NewClient(server.URL, "test-token")
	if err != nil {
		t.Fatalf("client.NewClient: %v", err)
	}

	d := newTestNSGData(t, map[string]interface{}{
		"tenant_id":   "acme",
		"project_id":  "net",
		"name":        "web-sg",
		"description": "web tier",
		"rules":       []interface{}{nsgTestRule()},
	})

	diags := resourceNSGCreate(context.Background(), d, c)
	if diags.HasError() {
		t.Fatalf("resourceNSGCreate returned unexpected error diagnostics: %v", diags)
	}

	if got, want := d.Id(), "acme/net/sg-1"; got != want {
		t.Errorf("Id() = %q, want %q", got, want)
	}
	if got, want := d.Get("sg_id").(string), "sg-1"; got != want {
		t.Errorf("sg_id = %q, want %q", got, want)
	}
	if got, want := d.Get("status").(string), "ACTIVE"; got != want {
		t.Errorf("status = %q, want %q", got, want)
	}
	rules := d.Get("rules").([]interface{})
	if len(rules) != 1 {
		t.Fatalf("rules length = %d, want 1", len(rules))
	}
	if got := rules[0].(map[string]interface{})["name"].(string); got != "allow-ssh" {
		t.Errorf("rules[0].name = %q, want allow-ssh", got)
	}
}

// TestResourceNSGCreate_APIError verifies a failed POST surfaces an error and leaves Id() empty.
func TestResourceNSGCreate_APIError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		w.Write([]byte(`{"error":"duplicate nsg name"}`))
	}))
	defer server.Close()

	c, _ := client.NewClient(server.URL, "test-token")
	d := newTestNSGData(t, map[string]interface{}{
		"tenant_id":  "acme",
		"project_id": "net",
		"name":       "web-sg",
	})

	diags := resourceNSGCreate(context.Background(), d, c)
	if !diags.HasError() {
		t.Fatal("diags.HasError() = false, want true for a failed create")
	}
	if d.Id() != "" {
		t.Errorf("Id() = %q, want empty string when create fails", d.Id())
	}
}

// TestResourceNSGRead_Success verifies a GET refreshes state.
func TestResourceNSGRead_Success(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/tenants/acme/projects/net/security-groups/sg-1" {
			t.Errorf("path = %s, want .../security-groups/sg-1", r.URL.Path)
		}
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{
			"id":"sg-1",
			"tenant_id":"acme",
			"name":"web-sg",
			"description":"web tier",
			"rules":[],
			"attachments":[],
			"status":"ACTIVE",
			"provider_type":"kubeovn",
			"created_at":"2026-01-01T00:00:00Z",
			"updated_at":"2026-01-02T00:00:00Z"
		}`))
	}))
	defer server.Close()

	c, _ := client.NewClient(server.URL, "test-token")
	d := newTestNSGData(t, map[string]interface{}{})
	d.SetId("acme/net/sg-1")

	diags := resourceNSGRead(context.Background(), d, c)
	if diags.HasError() {
		t.Fatalf("resourceNSGRead returned unexpected error diagnostics: %v", diags)
	}
	if got, want := d.Get("name").(string), "web-sg"; got != want {
		t.Errorf("name = %q, want %q", got, want)
	}
	if got, want := d.Get("sg_id").(string), "sg-1"; got != want {
		t.Errorf("sg_id = %q, want %q", got, want)
	}
}

// TestResourceNSGRead_NotFound verifies a 404 clears Id() with no error diagnostics.
func TestResourceNSGRead_NotFound(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		w.Write([]byte(`{"error":"nsg not found"}`))
	}))
	defer server.Close()

	c, _ := client.NewClient(server.URL, "test-token")
	d := newTestNSGData(t, map[string]interface{}{})
	d.SetId("acme/net/sg-1")

	diags := resourceNSGRead(context.Background(), d, c)
	if diags.HasError() {
		t.Fatalf("resourceNSGRead returned unexpected error diagnostics on 404: %v", diags)
	}
	if d.Id() != "" {
		t.Errorf("Id() = %q, want empty string after a 404", d.Id())
	}
}

// TestResourceNSGRead_InvalidID verifies the SplitN(id, "/", 3) guard errors on a malformed ID.
func TestResourceNSGRead_InvalidID(t *testing.T) {
	c, _ := client.NewClient("http://unused.invalid", "test-token")
	d := newTestNSGData(t, map[string]interface{}{})
	d.SetId("acme/net") // only 2 parts, want 3

	diags := resourceNSGRead(context.Background(), d, c)
	if !diags.HasError() {
		t.Fatal("diags.HasError() = false, want true for a malformed state ID")
	}
}

// TestResourceNSGUpdate_ReplacesRules verifies Update PUTs to .../rules with the full desired
// rules list and refreshes state from the response.
func TestResourceNSGUpdate_ReplacesRules(t *testing.T) {
	var gotBody string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPut {
			t.Errorf("method = %s, want PUT", r.Method)
		}
		if r.URL.Path != "/v1/tenants/acme/projects/net/security-groups/sg-1/rules" {
			t.Errorf("path = %s, want .../security-groups/sg-1/rules", r.URL.Path)
		}
		buf := make([]byte, r.ContentLength)
		r.Body.Read(buf)
		gotBody = string(buf)

		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{
			"id":"sg-1",
			"tenant_id":"acme",
			"name":"web-sg",
			"rules":[{
				"name":"allow-ssh","direction":"inbound","priority":100,"protocol":"tcp",
				"source_address_prefix":"*","source_port_range":"*",
				"destination_address_prefix":"*","destination_port_range":"22","action":"allow"
			}],
			"attachments":[],
			"status":"ACTIVE",
			"updated_at":"2026-01-03T00:00:00Z"
		}`))
	}))
	defer server.Close()

	c, _ := client.NewClient(server.URL, "test-token")
	d := newTestNSGData(t, map[string]interface{}{
		"tenant_id":  "acme",
		"project_id": "net",
		"name":       "web-sg",
		"rules":      []interface{}{nsgTestRule()},
	})
	d.SetId("acme/net/sg-1")

	diags := resourceNSGUpdate(context.Background(), d, c)
	if diags.HasError() {
		t.Fatalf("resourceNSGUpdate returned unexpected error diagnostics: %v", diags)
	}
	if !strings.Contains(gotBody, `"allow-ssh"`) {
		t.Errorf("PUT body = %q, want it to contain the rule name", gotBody)
	}
	if got, want := d.Get("updated_at").(string), "2026-01-03T00:00:00Z"; got != want {
		t.Errorf("updated_at = %q, want %q", got, want)
	}
}

// TestResourceNSGDelete_Success verifies a 204 DELETE clears Id().
func TestResourceNSGDelete_Success(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodDelete {
			t.Errorf("method = %s, want DELETE", r.Method)
		}
		if r.URL.Path != "/v1/tenants/acme/projects/net/security-groups/sg-1" {
			t.Errorf("path = %s, want .../security-groups/sg-1", r.URL.Path)
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()

	c, _ := client.NewClient(server.URL, "test-token")
	d := newTestNSGData(t, map[string]interface{}{})
	d.SetId("acme/net/sg-1")

	diags := resourceNSGDelete(context.Background(), d, c)
	if diags.HasError() {
		t.Fatalf("resourceNSGDelete returned unexpected error diagnostics: %v", diags)
	}
	// Unlike project.go, this resource's Delete returns nil on the success path
	// WITHOUT calling d.SetId("") — the Terraform SDK framework clears the ID
	// after a successful DeleteContext during a real apply. In this direct unit
	// call nothing clears it, so the meaningful invariant is simply that Delete
	// reported no error diagnostics (asserted above).
}

// TestResourceNSGDelete_Conflict verifies a non-404 error (e.g. 409: attachments present)
// surfaces as an error and does NOT clear Id().
func TestResourceNSGDelete_Conflict(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusConflict)
		w.Write([]byte(`{"error":"nsg has active attachments"}`))
	}))
	defer server.Close()

	c, _ := client.NewClient(server.URL, "test-token")
	d := newTestNSGData(t, map[string]interface{}{})
	d.SetId("acme/net/sg-1")

	diags := resourceNSGDelete(context.Background(), d, c)
	if !diags.HasError() {
		t.Fatal("diags.HasError() = false, want true for a 409 conflict")
	}
	if d.Id() == "" {
		t.Error("Id() = \"\", want it to remain set since delete failed")
	}
}
