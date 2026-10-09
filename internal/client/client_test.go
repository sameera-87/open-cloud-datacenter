// Unit tests for client.go.
//
// These tests live in "package client" (not "package client_test") because doRequest
// is unexported (lowercase) — a test can only call an unexported function if it's
// compiled as part of the same package. Go automatically includes *_test.go files
// only when running `go test`, never in a normal `go build`, so this doesn't leak
// test code into the compiled provider binary.
//
// Every test here uses httptest.Server: a real, local, in-process HTTP server that
// Go's net/http/httptest package spins up on an ephemeral port. It behaves exactly
// like a real DC-API server as far as DCAPIClient is concerned (it makes a real HTTP
// request over a real socket), but it's fully under the test's control — no network,
// no real backend, no credentials, and it shuts down instantly when the test ends.
// This is what makes it possible to unit-test HTTP client code without ever touching
// a real DC-API instance, and it's fast enough to run on every commit.
package client

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestNewClient_TrimsTrailingSlash checks a tiny but easy-to-regress detail:
// NewClient must strip a trailing "/" from baseURL, otherwise later path-joining
// (baseURL + path, done in doRequest) would produce URLs like "http://host//v1/tenants"
// with a doubled slash whenever a caller passes an endpoint with a trailing slash.
func TestNewClient_TrimsTrailingSlash(t *testing.T) {
	c, err := NewClient("https://dcapi.example.com/", "test-token")
	if err != nil {
		t.Fatalf("NewClient returned an unexpected error: %v", err)
	}

	if c.baseURL != "https://dcapi.example.com" {
		t.Errorf("baseURL = %q, want %q (trailing slash should be trimmed)", c.baseURL, "https://dcapi.example.com")
	}

	if c.token != "test-token" {
		t.Errorf("token = %q, want %q", c.token, "test-token")
	}
}

// TestDoRequest_SuccessGET is the happy-path baseline: a GET request (body == nil)
// against a server that returns 200 with a JSON body should return that body's raw
// bytes and a nil error.
func TestDoRequest_SuccessGET(t *testing.T) {
	// httptest.NewServer starts a real HTTP server on 127.0.0.1:<random free port>
	// and runs it in a background goroutine for the lifetime of this test.
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Assert the request itself was built correctly before responding — a unit
		// test can inspect the *http.Request the client actually sent, not just
		// the response the client got back.
		if r.Method != http.MethodGet {
			t.Errorf("method = %s, want GET", r.Method)
		}
		if r.URL.Path != "/v1/tenants/acme" {
			t.Errorf("path = %s, want /v1/tenants/acme", r.URL.Path)
		}

		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"tenant_id":"acme","status":"ACTIVE"}`))
	}))
	// defer + Close ensures the background server goroutine and its listening socket
	// are cleaned up when this test function returns, even if an assertion fails and
	// t.Errorf is called (Close still runs; only t.Fatalf would skip it, and there's
	// none here).
	defer server.Close()

	// Point the client at the fake server instead of a real DC-API — this is the
	// entire trick that makes this "unit" rather than "requires the internet."
	c := &DCAPIClient{baseURL: server.URL, token: "tok", httpClient: http.DefaultClient}

	body, err := c.doRequest(context.Background(), http.MethodGet, "/v1/tenants/acme", nil)
	if err != nil {
		t.Fatalf("doRequest returned an unexpected error: %v", err)
	}

	got := string(body)
	want := `{"tenant_id":"acme","status":"ACTIVE"}`
	if got != want {
		t.Errorf("body = %q, want %q", got, want)
	}
}

// TestDoRequest_SendsAuthAndContentTypeHeaders verifies doRequest sets the headers
// every resource in internal/resources relies on: a Bearer auth header always, and
// Content-Type: application/json only when there's actually a body to send (GET/DELETE
// calls pass body == nil and must not send a Content-Type for an empty request).
func TestDoRequest_SendsAuthAndContentTypeHeaders(t *testing.T) {
	var gotAuth, gotContentType string

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		gotContentType = r.Header.Get("Content-Type")
		w.WriteHeader(http.StatusCreated)
		w.Write([]byte(`{}`))
	}))
	defer server.Close()

	c := &DCAPIClient{baseURL: server.URL, token: "secret-token", httpClient: http.DefaultClient}

	// A non-nil body (any struct works — json.Marshal will encode it) triggers the
	// Content-Type header inside doRequest.
	type createTenantRequest struct {
		Name string `json:"name"`
	}
	_, err := c.doRequest(context.Background(), http.MethodPost, "/v1/tenants", createTenantRequest{Name: "acme"})
	if err != nil {
		t.Fatalf("doRequest returned an unexpected error: %v", err)
	}

	if gotAuth != "Bearer secret-token" {
		t.Errorf("Authorization header = %q, want %q", gotAuth, "Bearer secret-token")
	}
	if gotContentType != "application/json" {
		t.Errorf("Content-Type header = %q, want %q", gotContentType, "application/json")
	}
}

// TestDoRequest_NoContentTypeWhenBodyIsNil is the mirror-image check of the test
// above: a GET/DELETE call (body == nil) must NOT set Content-Type at all, since
// there is no request body to describe the format of.
func TestDoRequest_NoContentTypeWhenBodyIsNil(t *testing.T) {
	var sawHeader bool

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// http.Header.Values returns an empty slice (not nil) when the header key is
		// absent, so checking its length is the correct way to ask "was this header
		// ever set?" — Get() alone can't distinguish "absent" from "set to empty string".
		sawHeader = len(r.Header.Values("Content-Type")) > 0
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()

	c := &DCAPIClient{baseURL: server.URL, token: "tok", httpClient: http.DefaultClient}

	_, err := c.doRequest(context.Background(), http.MethodDelete, "/v1/tenants/acme", nil)
	if err != nil {
		t.Fatalf("doRequest returned an unexpected error: %v", err)
	}

	if sawHeader {
		t.Error("Content-Type header was set, want it absent for a nil body")
	}
}

// TestDoRequest_ErrorResponses is a table-driven test: instead of writing one
// near-identical test function per error shape, we describe each scenario as a row
// in a slice and run them all through the same test body via t.Run subtests. This
// is the idiomatic Go pattern for "same logic, many input/output combinations" and
// keeps adding a new error case to a one-line row instead of a whole new function.
func TestDoRequest_ErrorResponses(t *testing.T) {
	tests := []struct {
		name       string // subtest name, shown in `go test -v` / `go test -run`
		statusCode int    // HTTP status the fake server responds with
		body       string // raw response body the fake server writes
		wantErrSub string // substring doRequest's returned error must contain
	}{
		{
			name:       "404 with plain apiErrorResponse body",
			statusCode: http.StatusNotFound,
			body:       `{"error":"tenant not found"}`,
			// doRequest's plain-error branch formats this as "DC-API returned HTTP %d: %s".
			wantErrSub: "DC-API returned HTTP 404: tenant not found",
		},
		{
			name:       "400 quota_exceeded body is parsed into cap/allocated/available/requested detail",
			statusCode: http.StatusBadRequest,
			body: `{
				"error": "quota_exceeded",
				"message": "cpu quota exceeded",
				"tenant_cap": {"cpu_cores": 20, "memory_gb": 64, "storage_gb": 500},
				"allocated": {"cpu_cores": 18, "memory_gb": 32, "storage_gb": 100},
				"available": {"cpu_cores": 2, "memory_gb": 32, "storage_gb": 400},
				"requested": {"cpu_cores": 4, "memory_gb": 8, "storage_gb": 50}
			}`,
			// This is doRequest's bespoke quota-formatting branch (client.go:136-148) —
			// it must fire instead of falling through to the generic apiErrorResponse
			// branch below, and it must surface the actual numbers, not just "quota_exceeded".
			wantErrSub: "quota exceeded: cpu quota exceeded — cap: cpu=20 mem=64GB storage=500GB",
		},
		{
			name:       "400 without quota_exceeded falls through to the generic apiErrorResponse branch",
			statusCode: http.StatusBadRequest,
			body:       `{"error":"invalid cidr format"}`,
			// Same status code as the quota case above, but a different "error" value —
			// this proves the quota branch is gated on q.Error == "quota_exceeded" and
			// doesn't accidentally swallow every 400.
			wantErrSub: "DC-API returned HTTP 400: invalid cidr format",
		},
		{
			name:       "500 with a non-JSON body doesn't panic and returns the raw body",
			statusCode: http.StatusInternalServerError,
			body:       "internal server error, please contact support",
			// json.Unmarshal fails here, so doRequest must fall through to returning the
			// raw response text instead of panicking or silently swallowing the error.
			wantErrSub: "DC-API returned HTTP 500: internal server error, please contact support",
		},
		{
			name:       "409 with an empty body still returns a typed, non-nil error",
			statusCode: http.StatusConflict,
			body:       "",
			wantErrSub: "DC-API returned HTTP 409",
		},
	}

	for _, tt := range tests {
		// Capture tt by shadowing it inside the loop body (idiomatic pre-Go-1.22
		// safety habit for closures over loop variables — Go 1.22+ actually gives
		// each iteration its own tt automatically, but writing it explicitly costs
		// nothing and keeps the test readable regardless of Go version).
		tt := tt

		// t.Run registers tt.name as a named subtest — `go test -run TestDoRequest_ErrorResponses/404`
		// runs just that one row, and a failure report clearly names which row failed.
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(tt.statusCode)
				w.Write([]byte(tt.body))
			}))
			defer server.Close()

			c := &DCAPIClient{baseURL: server.URL, token: "tok", httpClient: http.DefaultClient}

			body, err := c.doRequest(context.Background(), http.MethodGet, "/v1/whatever", nil)

			if body != nil {
				t.Errorf("body = %v, want nil on an error response", body)
			}
			if err == nil {
				t.Fatal("err = nil, want a non-nil error for a non-2xx response")
			}
			if !strings.Contains(err.Error(), tt.wantErrSub) {
				t.Errorf("err = %q, want it to contain %q", err.Error(), tt.wantErrSub)
			}
		})
	}
}

// TestDoRequest_ContextCancellationReturnsPromptly proves doRequest respects
// context cancellation instead of hanging. Terraform cancels in-flight operations
// when a user presses Ctrl-C during `terraform apply`, so doRequest must return an
// error quickly rather than blocking forever on a slow/unresponsive server.
func TestDoRequest_ContextCancellationReturnsPromptly(t *testing.T) {
	// This handler intentionally never responds — it blocks until the test's
	// context is cancelled, simulating a DC-API server that has hung or is
	// unreachable. <-r.Context().Done() unblocks as soon as the client-side
	// cancellation propagates to this handler's request context.
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
	}))
	defer server.Close()

	c := &DCAPIClient{baseURL: server.URL, token: "tok", httpClient: http.DefaultClient}

	// Create a context and cancel it immediately — equivalent to a user hitting
	// Ctrl-C right as the request goes out.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := c.doRequest(ctx, http.MethodGet, "/v1/tenants/acme", nil)
	if err == nil {
		t.Fatal("err = nil, want a context-cancellation error")
	}
	if !strings.Contains(err.Error(), "context canceled") {
		t.Errorf("err = %q, want it to mention context cancellation", err.Error())
	}
}

// TestDoRequest_NetworkErrorIsWrapped proves that a transport-level failure (here,
// "nobody is listening on this address at all") produces a wrapped, readable Go
// error instead of doRequest panicking on a nil response.
func TestDoRequest_NetworkErrorIsWrapped(t *testing.T) {
	// httptest.NewServer + immediate Close() gives us a real URL that is guaranteed
	// to refuse connections — a cheap, deterministic way to simulate "DC-API is down"
	// without relying on an arbitrary unused port number.
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	server.Close()

	c := &DCAPIClient{baseURL: server.URL, token: "tok", httpClient: http.DefaultClient}

	_, err := c.doRequest(context.Background(), http.MethodGet, "/v1/tenants/acme", nil)
	if err == nil {
		t.Fatal("err = nil, want a network error since the server is closed")
	}
	// doRequest wraps transport errors as "doRequest %s %s: network error: %w" —
	// assert the wrapping prefix is present, not the exact underlying OS error text
	// (which varies by platform/Go version and isn't the thing this test cares about).
	if !strings.Contains(err.Error(), "network error") {
		t.Errorf("err = %q, want it to be wrapped as a network error", err.Error())
	}
}
