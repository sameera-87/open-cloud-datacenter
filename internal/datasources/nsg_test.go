// Unit tests for nsg.go (data source).
//
// dcapi_network_security_group lists GET .../security-groups (bare array) and filters by
// name. Match sets Id "tenant_id/project_id/sg_id" and flattens the nested rules and
// attachments arrays into Terraform list blocks. No match is a hard error.
package datasources

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/hashicorp/terraform-plugin-sdk/v2/helper/schema"
	"terraform-provider-dcapi/internal/client"
)

func newTestNSGDSData(t *testing.T, raw map[string]interface{}) *schema.ResourceData {
	t.Helper()
	return schema.TestResourceDataRaw(t, DataSourceNSG().Schema, raw)
}

// TestDataSourceNSG_Schema pins the Required lookups and Computed outputs (including the
// nested rules/attachments list blocks).
func TestDataSourceNSG_Schema(t *testing.T) {
	s := DataSourceNSG().Schema

	for _, key := range []string{"tenant_id", "project_id", "name"} {
		if f := s[key]; f == nil || !f.Required {
			t.Errorf("%s: want a Required lookup field, got %+v", key, f)
		}
	}
	for _, key := range []string{"sg_id", "description", "rules", "attachments",
		"status", "provider_type", "created_at", "updated_at"} {
		f := s[key]
		if f == nil {
			t.Fatalf("schema is missing field %q", key)
		}
		if !f.Computed {
			t.Errorf("%s: Computed = false, want true", key)
		}
		if f.Required || f.Optional {
			t.Errorf("%s: Required=%v Optional=%v, want both false", key, f.Required, f.Optional)
		}
	}
}

// TestDataSourceNSGRead_Success verifies the name filter, composite Id, and that the
// nested rule/attachment objects are flattened field-for-field into state.
func TestDataSourceNSGRead_Success(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/tenants/acme/projects/infra/security-groups" {
			t.Errorf("path = %s, want .../security-groups", r.URL.Path)
		}
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`[
			{"id":"sg-other","name":"other"},
			{"id":"sg-5","name":"web-nsg","description":"web rules","status":"ACTIVE",
			 "provider_type":"kubeovn","created_at":"2026-01-01T00:00:00Z","updated_at":"2026-01-02T00:00:00Z",
			 "rules":[{"name":"allow-https","direction":"inbound","priority":100,"protocol":"tcp",
				"source_address_prefix":"0.0.0.0/0","source_port_range":"*",
				"destination_address_prefix":"10.1.1.0/24","destination_port_range":"443","action":"allow"}],
			 "attachments":[{"id":"att-1","sg_id":"sg-5","target_type":"subnet","target_id":"sn-9",
				"created_at":"2026-01-01T00:00:00Z"}]}
		]`))
	}))
	defer server.Close()

	c, _ := client.NewClient(server.URL, "test-token")
	d := newTestNSGDSData(t, map[string]interface{}{"tenant_id": "acme", "project_id": "infra", "name": "web-nsg"})

	diags := dataSourceNSGRead(context.Background(), d, c)
	if diags.HasError() {
		t.Fatalf("dataSourceNSGRead returned unexpected error diagnostics: %v", diags)
	}
	if got, want := d.Id(), "acme/infra/sg-5"; got != want {
		t.Errorf("Id() = %q, want %q", got, want)
	}
	if got, want := d.Get("sg_id").(string), "sg-5"; got != want {
		t.Errorf("sg_id = %q, want %q", got, want)
	}

	rules := d.Get("rules").([]interface{})
	if len(rules) != 1 {
		t.Fatalf("len(rules) = %d, want 1", len(rules))
	}
	r0 := rules[0].(map[string]interface{})
	if r0["name"].(string) != "allow-https" || r0["priority"].(int) != 100 {
		t.Errorf("rule[0] = %v, want name=allow-https priority=100", r0)
	}

	atts := d.Get("attachments").([]interface{})
	if len(atts) != 1 {
		t.Fatalf("len(attachments) = %d, want 1", len(atts))
	}
	a0 := atts[0].(map[string]interface{})
	if a0["target_id"].(string) != "sn-9" {
		t.Errorf("attachment[0] target_id = %q, want sn-9", a0["target_id"])
	}
}

// TestDataSourceNSGRead_NotFound verifies an unmatched name is a hard error.
func TestDataSourceNSGRead_NotFound(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`[{"id":"sg-other","name":"other"}]`))
	}))
	defer server.Close()

	c, _ := client.NewClient(server.URL, "test-token")
	d := newTestNSGDSData(t, map[string]interface{}{"tenant_id": "acme", "project_id": "infra", "name": "ghost"})

	diags := dataSourceNSGRead(context.Background(), d, c)
	if !diags.HasError() {
		t.Fatal("diags.HasError() = false, want true for an NSG name not in the list")
	}
}

// TestDataSourceNSGRead_APIError verifies a failed list request surfaces as an error.
func TestDataSourceNSGRead_APIError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte(`{"error":"boom"}`))
	}))
	defer server.Close()

	c, _ := client.NewClient(server.URL, "test-token")
	d := newTestNSGDSData(t, map[string]interface{}{"tenant_id": "acme", "project_id": "infra", "name": "web-nsg"})

	diags := dataSourceNSGRead(context.Background(), d, c)
	if !diags.HasError() {
		t.Fatal("diags.HasError() = false, want true for a 500 response")
	}
}
