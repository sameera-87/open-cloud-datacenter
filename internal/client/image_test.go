// Unit tests for image.go.
package client

import (
	"context"
	"net/http"
	"testing"
)

// TestListImages guards the tenant-scoped GET path and that the bare-array body
// is parsed into the ID/DisplayName/Namespace fields the data source filters on.
func TestListImages(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Errorf("method = %s, want GET", r.Method)
		}
		if r.URL.Path != "/v1/tenants/t1/images" {
			t.Errorf("path = %s, want /v1/tenants/t1/images", r.URL.Path)
		}
		_, _ = w.Write([]byte(`[{"id":"ns/ubuntu","display_name":"Ubuntu 22.04","namespace":"ns"}]`))
	})
	imgs, err := c.ListImages(context.Background(), "t1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(imgs) != 1 || imgs[0].ID != "ns/ubuntu" || imgs[0].DisplayName != "Ubuntu 22.04" {
		t.Fatalf("images = %+v, want one Ubuntu image", imgs)
	}
}

// TestListImages_Error surfaces an API failure as a non-nil error.
func TestListImages_Error(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	})
	if _, err := c.ListImages(context.Background(), "t1"); err == nil {
		t.Fatal("err = nil, want an error for HTTP 500")
	}
}

// TestListImages_InvalidJSON hits the parse-failure branch.
func TestListImages_InvalidJSON(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"not":"an array"}`))
	})
	if _, err := c.ListImages(context.Background(), "t1"); err == nil {
		t.Fatal("err = nil, want a parse error when the body is not a JSON array")
	}
}
