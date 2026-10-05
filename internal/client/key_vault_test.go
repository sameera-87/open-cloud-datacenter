// Unit tests for key_vault.go.
package client

import (
	"context"
	"net/http"
	"testing"
)

const keyVaultsBase = "/v1/tenants/t1/projects/p1/keyvaults"

// TestCreateKeyVault guards the POST path and that the flat (unwrapped) 201 body
// is parsed, including the endpoint fields.
func TestCreateKeyVault(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != keyVaultsBase {
			t.Errorf("got %s %s, want POST %s", r.Method, r.URL.Path, keyVaultsBase)
		}
		var got KeyVaultCreateRequest
		decodeBody(t, r, &got)
		if got.Name != "kv" {
			t.Errorf("body name = %q, want kv", got.Name)
		}
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"id":"kv-1","name":"kv","status":"PENDING","soft_delete_days":30,"endpoint_port":8200}`))
	})
	kv, err := c.CreateKeyVault(context.Background(), "t1", "p1", KeyVaultCreateRequest{Name: "kv"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if kv.ID != "kv-1" || kv.EndpointPort != 8200 || kv.SoftDeleteDays != 30 {
		t.Fatalf("kv = %+v, want id kv-1 port 8200 softdelete 30", kv)
	}
}

// TestGetKeyVault covers 200 and the 404 drift sentinel.
func TestGetKeyVault(t *testing.T) {
	ctx := context.Background()

	t.Run("found", func(t *testing.T) {
		c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path != keyVaultsBase+"/kv-1" {
				t.Errorf("path = %s, unexpected", r.URL.Path)
			}
			_, _ = w.Write([]byte(`{"id":"kv-1","name":"kv","status":"ACTIVE"}`))
		})
		got, err := c.GetKeyVault(ctx, "t1", "p1", "kv-1")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if got == nil || got.Status != "ACTIVE" {
			t.Fatalf("got = %+v, want status ACTIVE", got)
		}
	})

	t.Run("404 returns nil,nil", func(t *testing.T) {
		c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusNotFound)
		})
		got, err := c.GetKeyVault(ctx, "t1", "p1", "missing")
		if err != nil || got != nil {
			t.Fatalf("got=%v err=%v, want nil,nil on 404", got, err)
		}
	})
}

// TestListKeyVaults checks the bare-array parse.
func TestListKeyVaults(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != keyVaultsBase {
			t.Errorf("path = %s, unexpected", r.URL.Path)
		}
		_, _ = w.Write([]byte(`[{"id":"kv-1","name":"a"},{"id":"kv-2","name":"b"}]`))
	})
	kvs, err := c.ListKeyVaults(context.Background(), "t1", "p1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(kvs) != 2 {
		t.Fatalf("kvs = %+v, want two", kvs)
	}
}

// TestDeleteKeyVault checks the DELETE path and error wrapping.
func TestDeleteKeyVault(t *testing.T) {
	ctx := context.Background()

	t.Run("success", func(t *testing.T) {
		c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
			if r.Method != http.MethodDelete || r.URL.Path != keyVaultsBase+"/kv-1" {
				t.Errorf("got %s %s, unexpected", r.Method, r.URL.Path)
			}
			w.WriteHeader(http.StatusNoContent)
		})
		if err := c.DeleteKeyVault(ctx, "t1", "p1", "kv-1"); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
	})

	t.Run("error", func(t *testing.T) {
		c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusConflict)
		})
		if err := c.DeleteKeyVault(ctx, "t1", "p1", "kv-1"); err == nil {
			t.Fatal("err = nil, want an error for HTTP 409")
		}
	})
}

// TestGetKeyVaultCredentials verifies the first-call GET parses role_id/secret_id
// and that a 410 Gone (second call) is NOT special-cased — it surfaces as an error.
func TestGetKeyVaultCredentials(t *testing.T) {
	ctx := context.Background()

	t.Run("first call returns creds", func(t *testing.T) {
		c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path != keyVaultsBase+"/kv-1/credentials" {
				t.Errorf("path = %s, unexpected", r.URL.Path)
			}
			_, _ = w.Write([]byte(`{"role_id":"ro","secret_id":"se","mount_path":"m"}`))
		})
		creds, err := c.GetKeyVaultCredentials(ctx, "t1", "p1", "kv-1")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if creds.RoleID != "ro" || creds.SecretID != "se" {
			t.Fatalf("creds = %+v, want role ro secret se", creds)
		}
	})

	t.Run("410 Gone surfaces as error", func(t *testing.T) {
		c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusGone)
			_, _ = w.Write([]byte(`{"error":"credentials already retrieved"}`))
		})
		if _, err := c.GetKeyVaultCredentials(ctx, "t1", "p1", "kv-1"); err == nil {
			t.Fatal("err = nil, want an error for HTTP 410 (contract: fetched once)")
		}
	})
}

// TestRotateKeyVaultCredentials guards the POST .../rotate path and parse.
func TestRotateKeyVaultCredentials(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != keyVaultsBase+"/kv-1/credentials/rotate" {
			t.Errorf("got %s %s, want POST .../credentials/rotate", r.Method, r.URL.Path)
		}
		_, _ = w.Write([]byte(`{"role_id":"ro","secret_id":"new-secret"}`))
	})
	creds, err := c.RotateKeyVaultCredentials(context.Background(), "t1", "p1", "kv-1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if creds.SecretID != "new-secret" {
		t.Fatalf("secret = %q, want new-secret", creds.SecretID)
	}
}
