// Unit tests for project.go.
package client

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"testing"
)

const projectsBase = "/v1/tenants/t1/projects"

// TestCreateProject guards the POST path, body forwarding, and parse of the quota
// fields in the response.
func TestCreateProject(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != projectsBase {
			t.Errorf("got %s %s, want POST %s", r.Method, r.URL.Path, projectsBase)
		}
		var got ProjectCreateRequest
		decodeBody(t, r, &got)
		if got.ID != "infra" || got.CPUCores != 8 {
			t.Errorf("body = %+v, want id=infra cpu=8", got)
		}
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"id":"infra","tenant_id":"t1","project_uuid":"u-1","cpu_cores":8,"memory_gb":16,"storage_gb":100}`))
	})
	p, err := c.CreateProject(context.Background(), "t1", ProjectCreateRequest{ID: "infra", CPUCores: 8})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if p.ID != "infra" || p.ProjectUUID != "u-1" || p.CPUCores != 8 {
		t.Fatalf("project = %+v, want id infra uuid u-1 cpu 8", p)
	}
}

// TestCreateProject_Error surfaces an API failure (e.g. quota_exceeded).
func TestCreateProject_Error(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":"invalid project id"}`))
	})
	if _, err := c.CreateProject(context.Background(), "t1", ProjectCreateRequest{}); err == nil {
		t.Fatal("err = nil, want an error for HTTP 400")
	}
}

// TestGetProjectByID covers 200 and the 404 sentinel.
func TestGetProjectByID(t *testing.T) {
	ctx := context.Background()

	t.Run("found", func(t *testing.T) {
		c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path != projectsBase+"/infra" {
				t.Errorf("path = %s, unexpected", r.URL.Path)
			}
			_, _ = w.Write([]byte(`{"id":"infra","tenant_id":"t1","memory_gb":16}`))
		})
		got, err := c.GetProjectByID(ctx, "t1", "infra")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if got == nil || got.MemoryGB != 16 {
			t.Fatalf("got = %+v, want memory_gb 16", got)
		}
	})

	t.Run("404 returns nil,nil", func(t *testing.T) {
		c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusNotFound)
		})
		got, err := c.GetProjectByID(ctx, "t1", "missing")
		if err != nil || got != nil {
			t.Fatalf("got=%v err=%v, want nil,nil on 404", got, err)
		}
	})
}

// TestUpdateProject guards the PATCH path and the pointer-field semantics: a nil
// *int field is omitted from the body (leave unchanged), a set one is sent.
func TestUpdateProject(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPatch || r.URL.Path != projectsBase+"/infra" {
			t.Errorf("got %s %s, want PATCH .../infra", r.Method, r.URL.Path)
		}
		raw, _ := io.ReadAll(r.Body)
		var asMap map[string]json.RawMessage
		if err := json.Unmarshal(raw, &asMap); err != nil {
			t.Fatalf("decoding body %q: %v", string(raw), err)
		}
		if _, ok := asMap["cpu_cores"]; !ok {
			t.Errorf("body %q missing cpu_cores, want it present", string(raw))
		}
		if _, ok := asMap["memory_gb"]; ok {
			t.Errorf("body %q includes memory_gb, want it omitted (nil pointer)", string(raw))
		}
		_, _ = w.Write([]byte(`{"id":"infra","cpu_cores":12}`))
	})
	cpu := 12
	p, err := c.UpdateProject(context.Background(), "t1", "infra", ProjectUpdateRequest{CPUCores: &cpu})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if p.CPUCores != 12 {
		t.Fatalf("cpu = %d, want 12", p.CPUCores)
	}
}

// TestDeleteProject checks the DELETE path and that a 409 (child resources exist)
// errors.
func TestDeleteProject(t *testing.T) {
	ctx := context.Background()

	t.Run("success", func(t *testing.T) {
		c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
			if r.Method != http.MethodDelete || r.URL.Path != projectsBase+"/infra" {
				t.Errorf("got %s %s, unexpected", r.Method, r.URL.Path)
			}
			w.WriteHeader(http.StatusNoContent)
		})
		if err := c.DeleteProject(ctx, "t1", "infra"); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
	})

	t.Run("409 errors", func(t *testing.T) {
		c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusConflict)
		})
		if err := c.DeleteProject(ctx, "t1", "infra"); err == nil {
			t.Fatal("err = nil, want an error when child resources still exist (409)")
		}
	})
}
