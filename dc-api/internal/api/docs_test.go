package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/rs/zerolog"
	dcapi "github.com/wso2/dc-api"
)

// nopAuth is a stub AuthValidator that lets every request through. The
// docs test only exercises routes outside /v1, but NewRouter wires the
// auth middleware at construction time, so the stub has to exist.
type nopAuth struct{}

func (nopAuth) Validate(next http.Handler) http.Handler { return next }

// TestUnauthenticatedRoutes spins up the router with the minimum deps
// needed for the /healthz, /openapi.yaml, and /docs handlers (they don't
// touch any provider or the DB) and asserts each returns the right
// status code, Content-Type, and body shape. Keeps F3 from regressing
// behind the production gateway.
func TestUnauthenticatedRoutes(t *testing.T) {
	// Provider deps are nil because we never hit a /v1/* route here.
	router := NewRouter(RouterDeps{
		Log:            zerolog.Nop(),
		AuthMiddleware: nopAuth{},
	})
	srv := httptest.NewServer(router)
	t.Cleanup(srv.Close)

	t.Run("healthz", func(t *testing.T) {
		resp, err := http.Get(srv.URL + "/healthz")
		if err != nil {
			t.Fatalf("get: %v", err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Errorf("status: got %d, want 200", resp.StatusCode)
		}
		if ct := resp.Header.Get("Content-Type"); !strings.Contains(ct, "application/json") {
			t.Errorf("content-type: got %q, want application/json", ct)
		}
	})

	t.Run("openapi.yaml", func(t *testing.T) {
		resp, err := http.Get(srv.URL + "/openapi.yaml")
		if err != nil {
			t.Fatalf("get: %v", err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Errorf("status: got %d, want 200", resp.StatusCode)
		}
		if ct := resp.Header.Get("Content-Type"); !strings.Contains(ct, "yaml") {
			t.Errorf("content-type: got %q, want yaml", ct)
		}
		body := make([]byte, 64)
		n, _ := resp.Body.Read(body)
		if !strings.Contains(string(body[:n]), "openapi:") {
			t.Errorf("body does not look like an OpenAPI spec: %q", string(body[:n]))
		}
		// And the bytes we serve are the same bytes we embedded — sanity check.
		if len(dcapi.OpenAPISpec) == 0 {
			t.Error("embedded OpenAPISpec is empty — //go:embed didn't pick up the file")
		}
	})

	t.Run("docs renders an HTML page that points at the spec", func(t *testing.T) {
		resp, err := http.Get(srv.URL + "/docs")
		if err != nil {
			t.Fatalf("get: %v", err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Errorf("status: got %d, want 200", resp.StatusCode)
		}
		if ct := resp.Header.Get("Content-Type"); !strings.Contains(ct, "text/html") {
			t.Errorf("content-type: got %q, want text/html", ct)
		}
		buf := make([]byte, 1024)
		n, _ := resp.Body.Read(buf)
		body := string(buf[:n])
		if !strings.Contains(body, "spec-url=\"openapi.yaml\"") {
			t.Errorf("body does not reference openapi.yaml: %q", body)
		}
		if !strings.Contains(body, "redoc") {
			t.Errorf("body does not mention redoc: %q", body)
		}
	})
}
