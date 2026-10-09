// Unit tests for project.go.
//
// This file is a good first place to learn how Terraform SDK resources get tested,
// because it exercises every pattern you'll need for the rest of the resources:
//
//  1. schema.TestResourceDataRaw builds a *schema.ResourceData from a plain
//     map[string]interface{}, the same shape a user's .tf config produces after
//     parsing. It diffs that raw config against a nil ("doesn't exist yet") old
//     state, so every key you put in the map comes back true from d.HasChange(key),
//     and any key you leave out stays at its zero value with HasChange == false.
//     That's exactly what we need to test resourceProjectUpdate's "only send the
//     fields that changed" logic.
//
//  2. httptest.NewServer (see internal/client/client_test.go for the original use
//     of this pattern) gives us a real local HTTP server standing in for DC-API.
//     client.NewClient(server.URL, ...) points a real *client.DCAPIClient at it, so
//     these tests exercise the full CreateContext/ReadContext/... -> DCAPIClient ->
//     HTTP call path, not a hand-rolled mock of the client's methods.
//
// Together, these let us test resourceProjectCreate/Read/Update/Delete exactly as
// Terraform core would call them, without ever touching a real DC-API instance.
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

// newTestProjectData builds a *schema.ResourceData for ResourceProject() from a raw
// config map, the same helper shape used throughout this file's tests.
//
// schema.TestResourceDataRaw diffs `raw` against a nil ("resource doesn't exist
// yet") old state. That has a useful side effect we rely on below: every key you
// put in `raw` comes back true from d.HasChange(key), and any key you leave out
// stays at its zero value with HasChange == false — which is exactly the "did this
// field change" signal resourceProjectUpdate reads.
func newTestProjectData(t *testing.T, raw map[string]interface{}) *schema.ResourceData {
	t.Helper()
	return schema.TestResourceDataRaw(t, ResourceProject().Schema, raw)
}

// TestResourceProject_Schema checks the handful of schema properties that the rest
// of project.go's logic depends on. If someone accidentally drops ForceNew from
// project_id, or Required from tenant_id, Terraform would silently allow illegal
// in-place changes — this test catches that at the schema level, before it ever
// reaches a real apply.
func TestResourceProject_Schema(t *testing.T) {
	s := ResourceProject().Schema

	immutableRequired := []string{"project_id", "tenant_id"}
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

	// cpu_cores/memory_gb/storage_gb are the only fields resourceProjectUpdate
	// actually PATCHes. If ForceNew were accidentally added to one of these, a
	// user changing it would force a destroy/recreate instead of an in-place
	// update — this guards that invariant from the schema side.
	updatable := []string{"cpu_cores", "memory_gb", "storage_gb"}
	for _, key := range updatable {
		f := s[key]
		if f == nil {
			t.Fatalf("schema is missing field %q", key)
		}
		if f.ForceNew {
			t.Errorf("%s: ForceNew = true, want false (must be updatable via PATCH)", key)
		}
		if !f.Optional || !f.Computed {
			t.Errorf("%s: Optional=%v Computed=%v, want both true (API supplies a default)", key, f.Optional, f.Computed)
		}
	}

	computedOnly := []string{"project_uuid", "tenant_uuid", "created_at", "updated_at", "created_by"}
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

// TestResourceProjectCreate_Success verifies the happy path: a POST that succeeds
// should populate d.Id() with "tenant_id/project_id" and copy every API-returned
// field (quotas, UUIDs, timestamps) into resource state via d.Set.
func TestResourceProjectCreate_Success(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("method = %s, want POST", r.Method)
		}
		if r.URL.Path != "/v1/tenants/acme/projects" {
			t.Errorf("path = %s, want /v1/tenants/acme/projects", r.URL.Path)
		}
		w.WriteHeader(http.StatusCreated)
		w.Write([]byte(`{
			"id": "infra",
			"tenant_id": "acme",
			"project_uuid": "11111111-1111-1111-1111-111111111111",
			"tenant_uuid": "22222222-2222-2222-2222-222222222222",
			"name": "Infra",
			"description": "Core infra project",
			"cpu_cores": 20,
			"memory_gb": 64,
			"storage_gb": 500,
			"max_vnets": 10,
			"max_clusters": 2,
			"max_volumes": 50,
			"max_public_ips": 3,
			"created_at": "2026-01-01T00:00:00Z",
			"updated_at": "2026-01-01T00:00:00Z",
			"created_by": "user@example.com"
		}`))
	}))
	defer server.Close()

	c, err := client.NewClient(server.URL, "test-token")
	if err != nil {
		t.Fatalf("client.NewClient: %v", err)
	}

	d := newTestProjectData(t, map[string]interface{}{
		"tenant_id":  "acme",
		"project_id": "infra",
	})

	diags := resourceProjectCreate(context.Background(), d, c)
	if diags.HasError() {
		t.Fatalf("resourceProjectCreate returned unexpected error diagnostics: %v", diags)
	}

	if got, want := d.Id(), "acme/infra"; got != want {
		t.Errorf("Id() = %q, want %q", got, want)
	}
	if got, want := d.Get("project_uuid").(string), "11111111-1111-1111-1111-111111111111"; got != want {
		t.Errorf("project_uuid = %q, want %q", got, want)
	}
	if got, want := d.Get("cpu_cores").(int), 20; got != want {
		t.Errorf("cpu_cores = %d, want %d", got, want)
	}
	if got, want := d.Get("created_by").(string), "user@example.com"; got != want {
		t.Errorf("created_by = %q, want %q", got, want)
	}
}

// TestResourceProjectCreate_APIError verifies that an API failure (e.g. quota
// exceeded, duplicate ID) surfaces as an error diagnostic and never sets an ID —
// Terraform must not believe a resource was created when it wasn't.
func TestResourceProjectCreate_APIError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		w.Write([]byte(`{"error":"project id already exists"}`))
	}))
	defer server.Close()

	c, _ := client.NewClient(server.URL, "test-token")
	d := newTestProjectData(t, map[string]interface{}{
		"tenant_id":  "acme",
		"project_id": "infra",
	})

	diags := resourceProjectCreate(context.Background(), d, c)
	if !diags.HasError() {
		t.Fatal("diags.HasError() = false, want true for a failed create")
	}
	if d.Id() != "" {
		t.Errorf("Id() = %q, want empty string when create fails", d.Id())
	}
}

// TestResourceProjectRead_Success verifies a GET that finds the project refreshes
// every field in state from the API's response.
func TestResourceProjectRead_Success(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/tenants/acme/projects/infra" {
			t.Errorf("path = %s, want /v1/tenants/acme/projects/infra", r.URL.Path)
		}
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{
			"id": "infra",
			"tenant_id": "acme",
			"name": "Infra",
			"cpu_cores": 8,
			"memory_gb": 32,
			"storage_gb": 200,
			"max_vnets": 10,
			"max_clusters": 2,
			"max_volumes": 50,
			"max_public_ips": 3,
			"project_uuid": "11111111-1111-1111-1111-111111111111",
			"tenant_uuid": "22222222-2222-2222-2222-222222222222",
			"created_at": "2026-01-01T00:00:00Z",
			"updated_at": "2026-01-02T00:00:00Z",
			"created_by": "user@example.com"
		}`))
	}))
	defer server.Close()

	c, _ := client.NewClient(server.URL, "test-token")
	d := newTestProjectData(t, map[string]interface{}{})
	d.SetId("acme/infra")

	diags := resourceProjectRead(context.Background(), d, c)
	if diags.HasError() {
		t.Fatalf("resourceProjectRead returned unexpected error diagnostics: %v", diags)
	}
	if d.Id() != "acme/infra" {
		t.Errorf("Id() = %q, want unchanged %q", d.Id(), "acme/infra")
	}
	if got, want := d.Get("cpu_cores").(int), 8; got != want {
		t.Errorf("cpu_cores = %d, want %d", got, want)
	}
	if got, want := d.Get("name").(string), "Infra"; got != want {
		t.Errorf("name = %q, want %q", got, want)
	}
}

// TestResourceProjectRead_NotFound verifies that a 404 (project deleted outside
// Terraform) clears the ID and returns no error diagnostics — this is how the SDK
// is told "drop this from state, but don't fail the plan/apply".
func TestResourceProjectRead_NotFound(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		w.Write([]byte(`{"error":"project not found"}`))
	}))
	defer server.Close()

	c, _ := client.NewClient(server.URL, "test-token")
	d := newTestProjectData(t, map[string]interface{}{})
	d.SetId("acme/infra")

	diags := resourceProjectRead(context.Background(), d, c)
	if diags.HasError() {
		t.Fatalf("resourceProjectRead returned unexpected error diagnostics on 404: %v", diags)
	}
	if d.Id() != "" {
		t.Errorf("Id() = %q, want empty string after a 404 (drift/deleted upstream)", d.Id())
	}
}

// TestResourceProjectRead_InvalidID verifies the defensive parse of d.Id(): a state
// ID without exactly one "/" (e.g. corrupted state, or state imported by hand with
// the wrong format) must produce an error diagnostic instead of panicking on the
// SplitN result.
func TestResourceProjectRead_InvalidID(t *testing.T) {
	c, _ := client.NewClient("http://unused.invalid", "test-token")
	d := newTestProjectData(t, map[string]interface{}{})
	d.SetId("not-a-valid-id")

	diags := resourceProjectRead(context.Background(), d, c)
	if !diags.HasError() {
		t.Fatal("diags.HasError() = false, want true for a malformed state ID")
	}
}

// TestResourceProjectUpdate_OnlySendsChangedFields is the important one for
// understanding resourceProjectUpdate: it only PATCHes cpu_cores/memory_gb/storage_gb
// when d.HasChange reports true for that specific key, using *int so an unset field
// is omitted from the JSON body entirely (via omitempty) rather than sent as 0.
//
// schema.TestResourceDataRaw diffs the raw map against a nil ("brand new") old
// state, so any key present in raw comes back HasChange == true, and any key left
// out stays at its zero value with HasChange == false — which is exactly the
// "only cpu_cores changed" scenario we want to simulate here.
func TestResourceProjectUpdate_OnlySendsChangedFields(t *testing.T) {
	var gotBody string

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPatch {
			t.Errorf("method = %s, want PATCH", r.Method)
		}
		buf := make([]byte, r.ContentLength)
		r.Body.Read(buf)
		gotBody = string(buf)

		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{
			"id": "infra",
			"tenant_id": "acme",
			"cpu_cores": 16,
			"memory_gb": 64,
			"storage_gb": 500,
			"updated_at": "2026-01-03T00:00:00Z"
		}`))
	}))
	defer server.Close()

	c, _ := client.NewClient(server.URL, "test-token")

	// Only cpu_cores is present in the raw config, so only it should register a
	// change; memory_gb/storage_gb are omitted and must stay at HasChange == false.
	d := newTestProjectData(t, map[string]interface{}{
		"tenant_id":  "acme",
		"project_id": "infra",
		"cpu_cores":  16,
	})
	d.SetId("acme/infra")

	if !d.HasChange("cpu_cores") {
		t.Fatal("test setup invalid: expected HasChange(cpu_cores) == true")
	}
	if d.HasChange("memory_gb") || d.HasChange("storage_gb") {
		t.Fatal("test setup invalid: expected memory_gb/storage_gb to have no change")
	}

	diags := resourceProjectUpdate(context.Background(), d, c)
	if diags.HasError() {
		t.Fatalf("resourceProjectUpdate returned unexpected error diagnostics: %v", diags)
	}

	if !strings.Contains(gotBody, `"cpu_cores":16`) {
		t.Errorf("PATCH body = %q, want it to contain cpu_cores:16", gotBody)
	}
	if strings.Contains(gotBody, "memory_gb") || strings.Contains(gotBody, "storage_gb") {
		t.Errorf("PATCH body = %q, want memory_gb/storage_gb omitted since they didn't change", gotBody)
	}

	if got, want := d.Get("cpu_cores").(int), 16; got != want {
		t.Errorf("cpu_cores = %d, want %d", got, want)
	}
}

// TestResourceProjectDelete_Success verifies a successful DELETE clears the ID so
// Terraform removes the resource from state.
func TestResourceProjectDelete_Success(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodDelete {
			t.Errorf("method = %s, want DELETE", r.Method)
		}
		if r.URL.Path != "/v1/tenants/acme/projects/infra" {
			t.Errorf("path = %s, want /v1/tenants/acme/projects/infra", r.URL.Path)
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()

	c, _ := client.NewClient(server.URL, "test-token")
	d := newTestProjectData(t, map[string]interface{}{})
	d.SetId("acme/infra")

	diags := resourceProjectDelete(context.Background(), d, c)
	if diags.HasError() {
		t.Fatalf("resourceProjectDelete returned unexpected error diagnostics: %v", diags)
	}
	if d.Id() != "" {
		t.Errorf("Id() = %q, want empty string after a successful delete", d.Id())
	}
}

// TestResourceProjectDelete_Conflict verifies that a 409 (child resources like
// VMs/clusters/VNets still exist inside the project) surfaces as an error and does
// NOT clear the ID — Terraform must keep tracking a project that failed to delete.
func TestResourceProjectDelete_Conflict(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusConflict)
		w.Write([]byte(`{"error":"project has active child resources"}`))
	}))
	defer server.Close()

	c, _ := client.NewClient(server.URL, "test-token")
	d := newTestProjectData(t, map[string]interface{}{})
	d.SetId("acme/infra")

	diags := resourceProjectDelete(context.Background(), d, c)
	if !diags.HasError() {
		t.Fatal("diags.HasError() = false, want true for a 409 conflict")
	}
	if d.Id() == "" {
		t.Error("Id() = \"\", want it to remain set since delete failed")
	}
}
