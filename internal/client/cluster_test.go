// Unit tests for cluster.go.
package client

import (
	"context"
	"net/http"
	"testing"
)

// TestCreateCluster_HappyPath guards the POST path, that the system-pool spec in
// the request body is forwarded, and that the nested resource wrapper is parsed.
func TestCreateCluster_HappyPath(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("method = %s, want POST", r.Method)
		}
		if r.URL.Path != "/v1/tenants/t1/projects/p1/clusters" {
			t.Errorf("path = %s, unexpected", r.URL.Path)
		}
		var got ClusterCreateRequest
		decodeBody(t, r, &got)
		if got.Name != "c" || got.SystemPool.Count != 3 {
			t.Errorf("body = %+v, want name=c system_pool.count=3", got)
		}
		w.WriteHeader(http.StatusAccepted)
		_, _ = w.Write([]byte(`{"resource":{"id":"cl-1","name":"c","status":"PENDING","system_pool":{"id":"sp-1","count":3},"total_node_count":3},"note":"n"}`))
	})

	resp, err := c.CreateCluster(context.Background(), "t1", "p1", ClusterCreateRequest{
		Name:       "c",
		SystemPool: ClusterSystemPool{Size: "m", Count: 3},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.Resource == nil || resp.Resource.ID != "cl-1" || resp.Resource.SystemPool == nil || resp.Resource.SystemPool.Count != 3 {
		t.Fatalf("resource = %+v, want ID cl-1 with system_pool.count 3", resp.Resource)
	}
}

// TestCreateCluster_InvalidJSON exercises the parse-failure branch: a 2xx with a
// body that is not valid JSON must yield a parse error, not a panic.
func TestCreateCluster_InvalidJSON(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusAccepted)
		_, _ = w.Write([]byte(`not json`))
	})
	if _, err := c.CreateCluster(context.Background(), "t1", "p1", ClusterCreateRequest{}); err == nil {
		t.Fatal("err = nil, want a parse error for a non-JSON 202 body")
	}
}

// TestGetCluster covers the 200 / 404-sentinel / error outcomes.
func TestGetCluster(t *testing.T) {
	ctx := context.Background()

	t.Run("found", func(t *testing.T) {
		c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path != "/v1/tenants/t1/projects/p1/clusters/cl-1" {
				t.Errorf("path = %s, unexpected", r.URL.Path)
			}
			_, _ = w.Write([]byte(`{"id":"cl-1","name":"c","status":"ACTIVE","total_node_count":4}`))
		})
		got, err := c.GetCluster(ctx, "t1", "p1", "cl-1")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if got == nil || got.TotalNodeCount != 4 {
			t.Fatalf("got = %+v, want total_node_count 4", got)
		}
	})

	t.Run("404 returns nil,nil", func(t *testing.T) {
		c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusNotFound)
		})
		got, err := c.GetCluster(ctx, "t1", "p1", "missing")
		if err != nil || got != nil {
			t.Fatalf("got=%v err=%v, want nil,nil on 404", got, err)
		}
	})
}

// TestGetClusterKubeconfig verifies the raw-YAML read: the body is returned
// verbatim on 200 and ("", nil) is the 404 sentinel.
func TestGetClusterKubeconfig(t *testing.T) {
	ctx := context.Background()

	t.Run("returns raw yaml", func(t *testing.T) {
		c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path != "/v1/tenants/t1/projects/p1/clusters/cl-1/kubeconfig" {
				t.Errorf("path = %s, unexpected", r.URL.Path)
			}
			_, _ = w.Write([]byte("apiVersion: v1\nkind: Config\n"))
		})
		got, err := c.GetClusterKubeconfig(ctx, "t1", "p1", "cl-1")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if got != "apiVersion: v1\nkind: Config\n" {
			t.Errorf("kubeconfig = %q, want the raw YAML body", got)
		}
	})

	t.Run("404 returns empty string", func(t *testing.T) {
		c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusNotFound)
		})
		got, err := c.GetClusterKubeconfig(ctx, "t1", "p1", "cl-1")
		if err != nil || got != "" {
			t.Fatalf("got=%q err=%v, want \"\",nil on 404", got, err)
		}
	})

	t.Run("409 returns error", func(t *testing.T) {
		c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusConflict)
		})
		if _, err := c.GetClusterKubeconfig(ctx, "t1", "p1", "cl-1"); err == nil {
			t.Fatal("err = nil, want an error when the cluster is not yet ACTIVE (409)")
		}
	})
}

// TestDeleteCluster checks DELETE path and error wrapping.
func TestDeleteCluster(t *testing.T) {
	ctx := context.Background()

	t.Run("success", func(t *testing.T) {
		c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
			if r.Method != http.MethodDelete || r.URL.Path != "/v1/tenants/t1/projects/p1/clusters/cl-1" {
				t.Errorf("got %s %s, unexpected", r.Method, r.URL.Path)
			}
			w.WriteHeader(http.StatusAccepted)
		})
		if err := c.DeleteCluster(ctx, "t1", "p1", "cl-1"); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
	})

	t.Run("error", func(t *testing.T) {
		c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusInternalServerError)
		})
		if err := c.DeleteCluster(ctx, "t1", "p1", "cl-1"); err == nil {
			t.Fatal("err = nil, want an error for HTTP 500")
		}
	})
}

// TestListClusters_Error proves the error path; both list shapes are covered in
// list_test.go.
func TestListClusters_Error(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	})
	if _, err := c.ListClusters(context.Background(), "t1", "p1"); err == nil {
		t.Fatal("err = nil, want an error for HTTP 500")
	}
}
