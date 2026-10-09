// Unit tests for vm.go.
package client

import (
	"context"
	"net/http"
	"testing"
)

const vmsBase = "/v1/tenants/t1/projects/p1/virtual-machines"

// TestCreateVM guards the POST path, body forwarding, and that the wrapper plus
// one-time secrets are parsed.
func TestCreateVM(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != vmsBase {
			t.Errorf("got %s %s, want POST %s", r.Method, r.URL.Path, vmsBase)
		}
		var got VMCreateRequest
		decodeBody(t, r, &got)
		if got.Name != "web" || got.ImageName != "ns/ubuntu" {
			t.Errorf("body = %+v, want name=web image=ns/ubuntu", got)
		}
		w.WriteHeader(http.StatusAccepted)
		_, _ = w.Write([]byte(`{"resource":{"id":"vm-1","name":"web","status":"PENDING"},"private_key":"PK","console_password":"CP"}`))
	})
	resp, err := c.CreateVM(context.Background(), "t1", "p1",
		VMCreateRequest{Name: "web", Size: "m", ImageName: "ns/ubuntu"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.Resource == nil || resp.Resource.ID != "vm-1" {
		t.Fatalf("resource = %+v, want id vm-1", resp.Resource)
	}
	if resp.PrivateKey != "PK" || resp.ConsolePassword != "CP" {
		t.Errorf("secrets = %q/%q, want PK/CP", resp.PrivateKey, resp.ConsolePassword)
	}
}

// TestCreateVM_MissingResourceID exercises the explicit guard that a 2xx body
// without resource.id is an error (the resource layer must not persist a VM with
// no ID).
func TestCreateVM_MissingResourceID(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusAccepted)
		_, _ = w.Write([]byte(`{"resource":{"name":"web"},"note":"no id"}`))
	})
	if _, err := c.CreateVM(context.Background(), "t1", "p1", VMCreateRequest{}); err == nil {
		t.Fatal("err = nil, want an error when resource.id is missing")
	}
}

// TestGetVM covers 200 and the 404 sentinel.
func TestGetVM(t *testing.T) {
	ctx := context.Background()

	t.Run("found", func(t *testing.T) {
		c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path != vmsBase+"/vm-1" {
				t.Errorf("path = %s, unexpected", r.URL.Path)
			}
			_, _ = w.Write([]byte(`{"id":"vm-1","name":"web","status":"ACTIVE","ip_address":"10.0.0.2"}`))
		})
		got, err := c.GetVM(ctx, "t1", "p1", "vm-1")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if got == nil || got.IPAddress != "10.0.0.2" {
			t.Fatalf("got = %+v, want ip 10.0.0.2", got)
		}
	})

	t.Run("404 returns nil,nil", func(t *testing.T) {
		c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusNotFound)
		})
		got, err := c.GetVM(ctx, "t1", "p1", "missing")
		if err != nil || got != nil {
			t.Fatalf("got=%v err=%v, want nil,nil on 404", got, err)
		}
	})
}

// TestDeleteVM checks the DELETE path and error wrapping.
func TestDeleteVM(t *testing.T) {
	ctx := context.Background()

	t.Run("success", func(t *testing.T) {
		c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
			if r.Method != http.MethodDelete || r.URL.Path != vmsBase+"/vm-1" {
				t.Errorf("got %s %s, unexpected", r.Method, r.URL.Path)
			}
			w.WriteHeader(http.StatusAccepted)
		})
		if err := c.DeleteVM(ctx, "t1", "p1", "vm-1"); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
	})

	t.Run("error", func(t *testing.T) {
		c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusInternalServerError)
		})
		if err := c.DeleteVM(ctx, "t1", "p1", "vm-1"); err == nil {
			t.Fatal("err = nil, want an error for HTTP 500")
		}
	})
}

// TestListVMs_Error proves the error path; both list shapes are covered in
// list_test.go.
func TestListVMs_Error(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	})
	if _, err := c.ListVMs(context.Background(), "t1", "p1"); err == nil {
		t.Fatal("err = nil, want an error for HTTP 500")
	}
}
