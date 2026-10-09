// Unit tests for key_vault_secret.go.
package client

import (
	"context"
	"net/http"
	"testing"
)

const kvSecretPath = "/v1/tenants/t1/projects/p1/keyvaults/kv1/secrets/dbpass"

// TestWriteKeyVaultSecret guards the PUT (upsert) path, body forwarding, and that
// the version-bearing response is parsed.
func TestWriteKeyVaultSecret(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPut || r.URL.Path != kvSecretPath {
			t.Errorf("got %s %s, want PUT %s", r.Method, r.URL.Path, kvSecretPath)
		}
		var got KeyVaultSecretWriteRequest
		decodeBody(t, r, &got)
		if got.Value != "s3cr3t" {
			t.Errorf("body value = %q, want s3cr3t", got.Value)
		}
		_, _ = w.Write([]byte(`{"key":"dbpass","value":"s3cr3t","version":2,"metadata":{"env":"prod"}}`))
	})
	sec, err := c.WriteKeyVaultSecret(context.Background(), "t1", "p1", "kv1", "dbpass",
		KeyVaultSecretWriteRequest{Value: "s3cr3t", Metadata: map[string]string{"env": "prod"}})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if sec.Version != 2 || sec.Metadata["env"] != "prod" {
		t.Fatalf("secret = %+v, want version 2 env prod", sec)
	}
}

// TestGetKeyVaultSecret covers 200 plus the TWO sentinel statuses this method
// treats as drift: 404 (never existed) and 410 (soft-deleted) both return nil,nil.
func TestGetKeyVaultSecret(t *testing.T) {
	ctx := context.Background()

	t.Run("found", func(t *testing.T) {
		c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path != kvSecretPath {
				t.Errorf("path = %s, unexpected", r.URL.Path)
			}
			_, _ = w.Write([]byte(`{"key":"dbpass","value":"v","version":1}`))
		})
		got, err := c.GetKeyVaultSecret(ctx, "t1", "p1", "kv1", "dbpass")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if got == nil || got.Value != "v" {
			t.Fatalf("got = %+v, want value v", got)
		}
	})

	for _, status := range []int{http.StatusNotFound, http.StatusGone} {
		status := status
		t.Run("sentinel status returns nil,nil", func(t *testing.T) {
			c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(status)
				_, _ = w.Write([]byte(`{"error":"gone"}`))
			})
			got, err := c.GetKeyVaultSecret(ctx, "t1", "p1", "kv1", "dbpass")
			if err != nil || got != nil {
				t.Fatalf("status %d: got=%v err=%v, want nil,nil", status, got, err)
			}
		})
	}

	t.Run("500 returns error", func(t *testing.T) {
		c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusInternalServerError)
		})
		if _, err := c.GetKeyVaultSecret(ctx, "t1", "p1", "kv1", "dbpass"); err == nil {
			t.Fatal("err = nil, want an error for HTTP 500")
		}
	})
}

// TestDeleteKeyVaultSecret checks the DELETE path and error wrapping.
func TestDeleteKeyVaultSecret(t *testing.T) {
	ctx := context.Background()

	t.Run("success", func(t *testing.T) {
		c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
			if r.Method != http.MethodDelete || r.URL.Path != kvSecretPath {
				t.Errorf("got %s %s, unexpected", r.Method, r.URL.Path)
			}
			w.WriteHeader(http.StatusNoContent)
		})
		if err := c.DeleteKeyVaultSecret(ctx, "t1", "p1", "kv1", "dbpass"); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
	})

	t.Run("error", func(t *testing.T) {
		c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusNotImplemented)
		})
		if err := c.DeleteKeyVaultSecret(ctx, "t1", "p1", "kv1", "dbpass"); err == nil {
			t.Fatal("err = nil, want an error for HTTP 501 (KVI provisioner disabled)")
		}
	})
}
