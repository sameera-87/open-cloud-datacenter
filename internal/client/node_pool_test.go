// Unit tests for node_pool.go.
package client

import (
	"context"
	"net/http"
	"testing"
)

const nodePoolsBase = "/v1/tenants/t1/projects/p1/clusters/c1/node-pools"

// TestCreateNodePool guards the POST path, body forwarding, and parse of the
// unwrapped response.
func TestCreateNodePool(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != nodePoolsBase {
			t.Errorf("got %s %s, want POST %s", r.Method, r.URL.Path, nodePoolsBase)
		}
		var got NodePoolCreateRequest
		decodeBody(t, r, &got)
		if got.Name != "pool-a" || got.Count != 2 {
			t.Errorf("body = %+v, want name=pool-a count=2", got)
		}
		w.WriteHeader(http.StatusAccepted)
		_, _ = w.Write([]byte(`{"id":"np-1","name":"pool-a","count":2,"status":"provisioning"}`))
	})
	pool, err := c.CreateNodePool(context.Background(), "t1", "p1", "c1",
		NodePoolCreateRequest{Name: "pool-a", Size: "m", Count: 2})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if pool.ID != "np-1" || pool.Count != 2 {
		t.Fatalf("pool = %+v, want id np-1 count 2", pool)
	}
}

// TestGetNodePool covers 200, the 404 sentinel, and confirms the pool name is
// escaped into the path (it is a name, not a UUID).
func TestGetNodePool(t *testing.T) {
	ctx := context.Background()

	t.Run("found with escaped name", func(t *testing.T) {
		c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
			// url.PathEscape leaves "pool-a" unchanged but the server sees the decoded path.
			if r.URL.Path != nodePoolsBase+"/pool-a" {
				t.Errorf("path = %s, want .../pool-a", r.URL.Path)
			}
			_, _ = w.Write([]byte(`{"id":"np-1","name":"pool-a","count":3,"status":"ready"}`))
		})
		got, err := c.GetNodePool(ctx, "t1", "p1", "c1", "pool-a")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if got == nil || got.Count != 3 {
			t.Fatalf("got = %+v, want count 3", got)
		}
	})

	t.Run("404 returns nil,nil", func(t *testing.T) {
		c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusNotFound)
		})
		got, err := c.GetNodePool(ctx, "t1", "p1", "c1", "pool-a")
		if err != nil || got != nil {
			t.Fatalf("got=%v err=%v, want nil,nil on 404", got, err)
		}
	})
}

// TestUpdateNodePool guards the PATCH path and that full-replace taints/labels are
// sent even when empty (no omitempty on those fields).
func TestUpdateNodePool(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPatch || r.URL.Path != nodePoolsBase+"/pool-a" {
			t.Errorf("got %s %s, want PATCH .../pool-a", r.Method, r.URL.Path)
		}
		var got NodePoolUpdateRequest
		decodeBody(t, r, &got)
		if got.Count != 5 {
			t.Errorf("count = %d, want 5", got.Count)
		}
		_, _ = w.Write([]byte(`{"id":"np-1","name":"pool-a","count":5,"status":"ready"}`))
	})
	pool, err := c.UpdateNodePool(context.Background(), "t1", "p1", "c1", "pool-a",
		NodePoolUpdateRequest{Count: 5, Taints: []NodePoolTaint{}, Labels: map[string]string{}})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if pool.Count != 5 {
		t.Fatalf("pool count = %d, want 5", pool.Count)
	}
}

// TestDeleteNodePool checks the DELETE path and error wrapping.
func TestDeleteNodePool(t *testing.T) {
	ctx := context.Background()

	t.Run("success", func(t *testing.T) {
		c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
			if r.Method != http.MethodDelete || r.URL.Path != nodePoolsBase+"/pool-a" {
				t.Errorf("got %s %s, unexpected", r.Method, r.URL.Path)
			}
			w.WriteHeader(http.StatusAccepted)
		})
		if err := c.DeleteNodePool(ctx, "t1", "p1", "c1", "pool-a"); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
	})

	t.Run("error", func(t *testing.T) {
		c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusInternalServerError)
		})
		if err := c.DeleteNodePool(ctx, "t1", "p1", "c1", "pool-a"); err == nil {
			t.Fatal("err = nil, want an error for HTTP 500")
		}
	})
}

// TestListNodePools_Error proves the error path; both list shapes are covered in
// list_test.go.
func TestListNodePools_Error(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	})
	if _, err := c.ListNodePools(context.Background(), "t1", "p1", "c1"); err == nil {
		t.Fatal("err = nil, want an error for HTTP 500")
	}
}
