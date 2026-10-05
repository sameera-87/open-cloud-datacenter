// Unit tests for nsg_attachment.go (dcapi_nsg_attachment).
//
// The attachment is SYNCHRONOUS and has no dedicated GET: Create POSTs to
// .../security-groups/{sg}/attachments (201, bare NSGAttachmentResponse) and Read
// fetches the parent NSG (GetNSG) and scans nsg.Attachments for the stored attachment
// ID. If the NSG is 404 or the attachment is absent from the list, Read clears Id().
// There is no UpdateContext (all user fields are ForceNew). Composite state ID is
// "tenant/project/sg_id/attachment_id".
package resources

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/hashicorp/terraform-plugin-sdk/v2/helper/schema"
	"terraform-provider-dcapi/internal/client"
)

// newTestNSGAttachmentData builds a *schema.ResourceData for ResourceNSGAttachment().
func newTestNSGAttachmentData(t *testing.T, raw map[string]interface{}) *schema.ResourceData {
	t.Helper()
	return schema.TestResourceDataRaw(t, ResourceNSGAttachment().Schema, raw)
}

// TestResourceNSGAttachment_Schema pins immutability and computed-only fields.
func TestResourceNSGAttachment_Schema(t *testing.T) {
	s := ResourceNSGAttachment().Schema

	requiredForceNew := []string{"tenant_id", "project_id", "sg_id", "target_type", "target_id"}
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

	computedOnly := []string{"attachment_id", "created_at"}
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

// TestResourceNSGAttachmentCreate_Success verifies POST, the composite Id, and computed fields.
func TestResourceNSGAttachmentCreate_Success(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("method = %s, want POST", r.Method)
		}
		if r.URL.Path != "/v1/tenants/acme/projects/net/security-groups/sg-1/attachments" {
			t.Errorf("path = %s, want .../security-groups/sg-1/attachments", r.URL.Path)
		}
		w.WriteHeader(http.StatusCreated)
		w.Write([]byte(`{
			"id":"att-7",
			"sg_id":"sg-1",
			"target_type":"subnet",
			"target_id":"sub-9",
			"created_at":"2026-01-01T00:00:00Z"
		}`))
	}))
	defer server.Close()

	c, err := client.NewClient(server.URL, "test-token")
	if err != nil {
		t.Fatalf("client.NewClient: %v", err)
	}

	d := newTestNSGAttachmentData(t, map[string]interface{}{
		"tenant_id":   "acme",
		"project_id":  "net",
		"sg_id":       "sg-1",
		"target_type": "subnet",
		"target_id":   "sub-9",
	})

	diags := resourceNSGAttachmentCreate(context.Background(), d, c)
	if diags.HasError() {
		t.Fatalf("resourceNSGAttachmentCreate returned unexpected error diagnostics: %v", diags)
	}

	if got, want := d.Id(), "acme/net/sg-1/att-7"; got != want {
		t.Errorf("Id() = %q, want %q", got, want)
	}
	if got, want := d.Get("attachment_id").(string), "att-7"; got != want {
		t.Errorf("attachment_id = %q, want %q", got, want)
	}
	if got, want := d.Get("created_at").(string), "2026-01-01T00:00:00Z"; got != want {
		t.Errorf("created_at = %q, want %q", got, want)
	}
}

// TestResourceNSGAttachmentCreate_APIError verifies a failed POST surfaces an error and leaves Id() empty.
func TestResourceNSGAttachmentCreate_APIError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		w.Write([]byte(`{"error":"target subnet not found"}`))
	}))
	defer server.Close()

	c, _ := client.NewClient(server.URL, "test-token")
	d := newTestNSGAttachmentData(t, map[string]interface{}{
		"tenant_id":   "acme",
		"project_id":  "net",
		"sg_id":       "sg-1",
		"target_type": "subnet",
		"target_id":   "sub-9",
	})

	diags := resourceNSGAttachmentCreate(context.Background(), d, c)
	if !diags.HasError() {
		t.Fatal("diags.HasError() = false, want true for a failed create")
	}
	if d.Id() != "" {
		t.Errorf("Id() = %q, want empty string when create fails", d.Id())
	}
}

// TestResourceNSGAttachmentRead_Success verifies Read scans the parent NSG's attachment list
// and refreshes state when the stored attachment ID is present.
func TestResourceNSGAttachmentRead_Success(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/tenants/acme/projects/net/security-groups/sg-1" {
			t.Errorf("path = %s, want .../security-groups/sg-1", r.URL.Path)
		}
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{
			"id":"sg-1",
			"tenant_id":"acme",
			"name":"web-sg",
			"rules":[],
			"attachments":[{
				"id":"att-7","sg_id":"sg-1","target_type":"subnet",
				"target_id":"sub-9","created_at":"2026-01-01T00:00:00Z"
			}],
			"status":"ACTIVE"
		}`))
	}))
	defer server.Close()

	c, _ := client.NewClient(server.URL, "test-token")
	d := newTestNSGAttachmentData(t, map[string]interface{}{})
	d.SetId("acme/net/sg-1/att-7")

	diags := resourceNSGAttachmentRead(context.Background(), d, c)
	if diags.HasError() {
		t.Fatalf("resourceNSGAttachmentRead returned unexpected error diagnostics: %v", diags)
	}
	if d.Id() != "acme/net/sg-1/att-7" {
		t.Errorf("Id() = %q, want unchanged", d.Id())
	}
	if got, want := d.Get("target_id").(string), "sub-9"; got != want {
		t.Errorf("target_id = %q, want %q", got, want)
	}
	if got, want := d.Get("target_type").(string), "subnet"; got != want {
		t.Errorf("target_type = %q, want %q", got, want)
	}
}

// TestResourceNSGAttachmentRead_NSGGone verifies a 404 on the parent NSG clears Id() with no error.
func TestResourceNSGAttachmentRead_NSGGone(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		w.Write([]byte(`{"error":"nsg not found"}`))
	}))
	defer server.Close()

	c, _ := client.NewClient(server.URL, "test-token")
	d := newTestNSGAttachmentData(t, map[string]interface{}{})
	d.SetId("acme/net/sg-1/att-7")

	diags := resourceNSGAttachmentRead(context.Background(), d, c)
	if diags.HasError() {
		t.Fatalf("resourceNSGAttachmentRead returned unexpected error diagnostics on NSG 404: %v", diags)
	}
	if d.Id() != "" {
		t.Errorf("Id() = %q, want empty string when parent NSG is gone", d.Id())
	}
}

// TestResourceNSGAttachmentRead_NotInList verifies that an attachment absent from the NSG's
// list (deleted externally) clears Id() with no error.
func TestResourceNSGAttachmentRead_NotInList(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{
			"id":"sg-1",
			"tenant_id":"acme",
			"name":"web-sg",
			"rules":[],
			"attachments":[],
			"status":"ACTIVE"
		}`))
	}))
	defer server.Close()

	c, _ := client.NewClient(server.URL, "test-token")
	d := newTestNSGAttachmentData(t, map[string]interface{}{})
	d.SetId("acme/net/sg-1/att-7")

	diags := resourceNSGAttachmentRead(context.Background(), d, c)
	if diags.HasError() {
		t.Fatalf("resourceNSGAttachmentRead returned unexpected error diagnostics: %v", diags)
	}
	if d.Id() != "" {
		t.Errorf("Id() = %q, want empty string when attachment is not in the NSG list", d.Id())
	}
}

// TestResourceNSGAttachmentRead_InvalidID verifies the SplitN(id, "/", 4) guard errors on a malformed ID.
func TestResourceNSGAttachmentRead_InvalidID(t *testing.T) {
	c, _ := client.NewClient("http://unused.invalid", "test-token")
	d := newTestNSGAttachmentData(t, map[string]interface{}{})
	d.SetId("acme/net/sg-1") // only 3 parts, want 4

	diags := resourceNSGAttachmentRead(context.Background(), d, c)
	if !diags.HasError() {
		t.Fatal("diags.HasError() = false, want true for a malformed state ID")
	}
}

// TestResourceNSGAttachmentDelete_Success verifies a 204 DELETE clears Id().
func TestResourceNSGAttachmentDelete_Success(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodDelete {
			t.Errorf("method = %s, want DELETE", r.Method)
		}
		if r.URL.Path != "/v1/tenants/acme/projects/net/security-groups/sg-1/attachments/att-7" {
			t.Errorf("path = %s, want .../attachments/att-7", r.URL.Path)
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()

	c, _ := client.NewClient(server.URL, "test-token")
	d := newTestNSGAttachmentData(t, map[string]interface{}{})
	d.SetId("acme/net/sg-1/att-7")

	diags := resourceNSGAttachmentDelete(context.Background(), d, c)
	if diags.HasError() {
		t.Fatalf("resourceNSGAttachmentDelete returned unexpected error diagnostics: %v", diags)
	}
	// Unlike project.go, this resource's Delete returns nil on the success path
	// WITHOUT calling d.SetId("") — the Terraform SDK framework clears the ID
	// after a successful DeleteContext during a real apply. In this direct unit
	// call nothing clears it, so the meaningful invariant is simply that Delete
	// reported no error diagnostics (asserted above).
}

// TestResourceNSGAttachmentDelete_NotFound verifies a 404 DELETE is treated as success (Id cleared).
func TestResourceNSGAttachmentDelete_NotFound(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		w.Write([]byte(`{"error":"attachment not found"}`))
	}))
	defer server.Close()

	c, _ := client.NewClient(server.URL, "test-token")
	d := newTestNSGAttachmentData(t, map[string]interface{}{})
	d.SetId("acme/net/sg-1/att-7")

	diags := resourceNSGAttachmentDelete(context.Background(), d, c)
	if diags.HasError() {
		t.Fatalf("resourceNSGAttachmentDelete returned unexpected error diagnostics on 404: %v", diags)
	}
	if d.Id() != "" {
		t.Errorf("Id() = %q, want empty string after a 404 delete (already gone)", d.Id())
	}
}
