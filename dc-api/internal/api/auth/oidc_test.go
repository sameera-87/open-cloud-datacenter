package auth

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/rs/zerolog"
)

// ── mock IdP server ───────────────────────────────────────────────────────────

// newMockIdP spins up a minimal httptest.Server that answers the OIDC
// discovery document. The issuer in the document is set to the server's own
// URL so go-oidc's issuer-match check passes. All endpoints point back at
// the same server — only /authorize is exercised by the login handler test,
// and it doesn't need a real implementation for these unit tests.
func newMockIdP(t *testing.T) *httptest.Server {
	t.Helper()

	mux := http.NewServeMux()
	var srv *httptest.Server

	mux.HandleFunc("/.well-known/openid-configuration", func(w http.ResponseWriter, r *http.Request) {
		base := srv.URL
		doc := map[string]any{
			"issuer":                 base,
			"authorization_endpoint": base + "/authorize",
			"token_endpoint":         base + "/token",
			"jwks_uri":               base + "/jwks",
			"end_session_endpoint":   base + "/logout",
			"response_types_supported": []string{"code"},
			"subject_types_supported": []string{"public"},
			"id_token_signing_alg_values_supported": []string{"RS256"},
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(doc)
	})

	// /jwks — empty key set; sufficient for NewProvider to succeed.
	mux.HandleFunc("/jwks", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"keys":[]}`))
	})

	srv = httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

// newTestService builds a *Service against the mock IdP. It uses the helper
// so handler tests can reuse the same wiring without copy-paste.
func newTestService(t *testing.T, idp *httptest.Server) *Service {
	t.Helper()

	key := make([]byte, 32)
	for i := range key {
		key[i] = byte(i + 1)
	}

	cfg := Config{
		Issuer:             idp.URL,
		ClientID:           "test-client-id",
		ClientSecret:       "test-client-secret",
		RedirectURL:        "https://dcapi.example.com/v1/auth/callback",
		PostLoginRedirect:  "https://app.example.com/",
		PostLogoutRedirect: "https://app.example.com/",
		SessionKey:         key,
	}

	svc, err := NewService(context.Background(), cfg)
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	return svc
}

// ── TestNewService_RequiredFields ─────────────────────────────────────────────

func TestNewService_RequiredFields(t *testing.T) {
	t.Parallel()
	idp := newMockIdP(t)

	goodKey := make([]byte, 32)
	for i := range goodKey {
		goodKey[i] = byte(i + 1)
	}

	baseGood := Config{
		Issuer:            idp.URL,
		ClientID:          "cid",
		ClientSecret:      "csec",
		RedirectURL:       "https://dcapi.example.com/v1/auth/callback",
		PostLoginRedirect: "https://app.example.com/",
		SessionKey:        goodKey,
	}

	cases := []struct {
		name    string
		mutate  func(c *Config)
		wantErr string
	}{
		{
			name:    "missing Issuer",
			mutate:  func(c *Config) { c.Issuer = "" },
			wantErr: "Issuer",
		},
		{
			name:    "missing ClientID",
			mutate:  func(c *Config) { c.ClientID = "" },
			wantErr: "ClientID",
		},
		{
			name:    "missing ClientSecret",
			mutate:  func(c *Config) { c.ClientSecret = "" },
			wantErr: "ClientSecret",
		},
		{
			name:    "missing RedirectURL",
			mutate:  func(c *Config) { c.RedirectURL = "" },
			wantErr: "RedirectURL",
		},
		{
			name:    "missing PostLoginRedirect",
			mutate:  func(c *Config) { c.PostLoginRedirect = "" },
			wantErr: "PostLoginRedirect",
		},
		{
			name:    "SessionKey too short (16 bytes)",
			mutate:  func(c *Config) { c.SessionKey = make([]byte, 16) },
			wantErr: "session key",
		},
		{
			name:    "SessionKey wrong length (0 bytes)",
			mutate:  func(c *Config) { c.SessionKey = nil },
			wantErr: "session key",
		},
		{
			name:    "SessionKey too long (64 bytes)",
			mutate:  func(c *Config) { c.SessionKey = make([]byte, 64) },
			wantErr: "session key",
		},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			cfg := baseGood
			tc.mutate(&cfg)
			_, err := NewService(context.Background(), cfg)
			if err == nil {
				t.Errorf("NewService with %q: expected error containing %q, got nil", tc.name, tc.wantErr)
				return
			}
			if tc.wantErr != "" {
				// Error message must mention the problematic field name — this is
				// what an operator reads when their config is wrong.
				if !containsCaseInsensitive(err.Error(), tc.wantErr) {
					t.Errorf("NewService error %q does not mention %q", err.Error(), tc.wantErr)
				}
			}
		})
	}
}

// containsCaseInsensitive returns true when s contains substr, ignoring case.
func containsCaseInsensitive(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr ||
		len(substr) == 0 ||
		containsCI([]byte(s), []byte(substr)))
}

func containsCI(s, sub []byte) bool {
	if len(sub) == 0 {
		return true
	}
	if len(s) < len(sub) {
		return false
	}
	lower := func(b byte) byte {
		if b >= 'A' && b <= 'Z' {
			return b + 32
		}
		return b
	}
	for i := 0; i <= len(s)-len(sub); i++ {
		match := true
		for j := range sub {
			if lower(s[i+j]) != lower(sub[j]) {
				match = false
				break
			}
		}
		if match {
			return true
		}
	}
	return false
}

// ── TestNewService_HappyPath ──────────────────────────────────────────────────

// Verifies that a fully-configured Service can be created when the IdP is up.
// This is lightweight — the heavier handler tests below exercise the Service
// end-to-end.
func TestNewService_HappyPath(t *testing.T) {
	t.Parallel()
	idp := newMockIdP(t)
	svc := newTestService(t, idp) // panics via t.Fatal on failure

	if svc.Codec() == nil {
		t.Error("Service.Codec() returned nil — codec was not initialised")
	}
}

// ── TestService_AccessTokenFromCookie_Expired ─────────────────────────────────

func TestService_AccessTokenFromCookie_Expired(t *testing.T) {
	t.Parallel()
	idp := newMockIdP(t)
	svc := newTestService(t, idp)

	sess := &Session{
		AccessToken: "expired-access-token",
		ExpiresAt:   time.Now().UTC().Add(-time.Hour),
		Subject:     "sub|expired",
	}
	encoded, err := svc.codec.EncodeSession(sess)
	if err != nil {
		t.Fatalf("EncodeSession: %v", err)
	}

	_, err = svc.AccessTokenFromCookie(encoded)
	if err == nil {
		t.Error("AccessTokenFromCookie: expected error for expired session, got nil")
	}
	if !containsCaseInsensitive(err.Error(), "expired") {
		t.Errorf("AccessTokenFromCookie error %q does not mention 'expired'", err.Error())
	}
}

// ── TestService_AccessTokenFromCookie_HappyPath ───────────────────────────────

func TestService_AccessTokenFromCookie_HappyPath(t *testing.T) {
	t.Parallel()
	idp := newMockIdP(t)
	svc := newTestService(t, idp)

	const wantToken = "fresh-access-token-abcde"
	sess := &Session{
		AccessToken: wantToken,
		ExpiresAt:   time.Now().UTC().Add(time.Hour),
		Subject:     "sub|fresh",
	}
	encoded, err := svc.codec.EncodeSession(sess)
	if err != nil {
		t.Fatalf("EncodeSession: %v", err)
	}

	got, err := svc.AccessTokenFromCookie(encoded)
	if err != nil {
		t.Fatalf("AccessTokenFromCookie: unexpected error: %v", err)
	}
	if got != wantToken {
		t.Errorf("AccessTokenFromCookie: got %q, want %q", got, wantToken)
	}
}

// ── TestService_AccessTokenFromCookieReq_NoCookie ────────────────────────────

func TestService_AccessTokenFromCookieReq_NoCookie(t *testing.T) {
	t.Parallel()
	idp := newMockIdP(t)
	svc := newTestService(t, idp)

	r := httptest.NewRequest(http.MethodGet, "/v1/auth/me", nil)
	// No cookie set.
	tok, ok := svc.AccessTokenFromCookieReq(r)
	if ok {
		t.Errorf("AccessTokenFromCookieReq with no cookie: ok = true, want false")
	}
	if tok != "" {
		t.Errorf("AccessTokenFromCookieReq with no cookie: tok = %q, want empty", tok)
	}
}

// ── TestService_AccessTokenFromCookieReq_HappyPath ───────────────────────────

func TestService_AccessTokenFromCookieReq_HappyPath(t *testing.T) {
	t.Parallel()
	idp := newMockIdP(t)
	svc := newTestService(t, idp)

	const wantToken = "bearer-token-from-cookie-req"
	sess := &Session{
		AccessToken: wantToken,
		ExpiresAt:   time.Now().UTC().Add(time.Hour),
		Subject:     "sub|cookiereq",
	}
	encoded, err := svc.codec.EncodeSession(sess)
	if err != nil {
		t.Fatalf("EncodeSession: %v", err)
	}

	r := httptest.NewRequest(http.MethodGet, "/v1/auth/me", nil)
	r.AddCookie(&http.Cookie{Name: SessionCookieName, Value: encoded})

	tok, ok := svc.AccessTokenFromCookieReq(r)
	if !ok {
		t.Error("AccessTokenFromCookieReq: ok = false, want true")
	}
	if tok != wantToken {
		t.Errorf("AccessTokenFromCookieReq: got %q, want %q", tok, wantToken)
	}
}

// ── TestHandleMe_NoSession_401 ────────────────────────────────────────────────

func TestHandleMe_NoSession_401(t *testing.T) {
	t.Parallel()
	idp := newMockIdP(t)
	svc := newTestService(t, idp)

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/v1/auth/me", nil)

	svc.HandleMe(zerolog.Nop())(w, r)

	if w.Code != http.StatusUnauthorized {
		t.Errorf("HandleMe (no session): status = %d, want %d", w.Code, http.StatusUnauthorized)
	}
}

// ── TestHandleMe_HappyPath ────────────────────────────────────────────────────

func TestHandleMe_HappyPath(t *testing.T) {
	t.Parallel()
	idp := newMockIdP(t)
	svc := newTestService(t, idp)

	sess := &Session{
		AccessToken: "some-token",
		ExpiresAt:   time.Now().UTC().Add(time.Hour),
		Subject:     "sub|me-test",
		Email:       "me@example.com",
	}
	encoded, err := svc.codec.EncodeSession(sess)
	if err != nil {
		t.Fatalf("EncodeSession: %v", err)
	}

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/v1/auth/me", nil)
	r.AddCookie(&http.Cookie{Name: SessionCookieName, Value: encoded})

	svc.HandleMe(zerolog.Nop())(w, r)

	if w.Code != http.StatusOK {
		t.Errorf("HandleMe (happy path): status = %d, want %d", w.Code, http.StatusOK)
	}

	var body map[string]any
	if err := json.NewDecoder(w.Body).Decode(&body); err != nil {
		t.Fatalf("HandleMe response is not valid JSON: %v", err)
	}

	if got, ok := body["sub"].(string); !ok || got != sess.Subject {
		t.Errorf("HandleMe response sub = %q, want %q", got, sess.Subject)
	}
	if got, ok := body["email"].(string); !ok || got != sess.Email {
		t.Errorf("HandleMe response email = %q, want %q", got, sess.Email)
	}
	if _, ok := body["expires_at"]; !ok {
		t.Error("HandleMe response missing 'expires_at' field")
	}
}

// ── TestHandleMe_ExpiredSession_401 ───────────────────────────────────────────
//
// Pins the F7 fix: HandleMe must reject a session whose access token has
// already expired, returning 401 (matching the spec) instead of 200 with
// a past expires_at. Without this guard the SPA's AuthProvider sees a
// successful /me, mounts the authenticated app, and then every /v1/* call
// fails with 401 — a confusing first-render flicker. The check mirrors
// what AccessTokenFromCookieReq already does for the middleware path.

func TestHandleMe_ExpiredSession_401(t *testing.T) {
	t.Parallel()
	idp := newMockIdP(t)
	svc := newTestService(t, idp)

	sess := &Session{
		AccessToken: "stale-token",
		ExpiresAt:   time.Now().UTC().Add(-time.Hour), // expired an hour ago
		Subject:     "sub|expired-test",
		Email:       "expired@example.com",
	}
	encoded, err := svc.codec.EncodeSession(sess)
	if err != nil {
		t.Fatalf("EncodeSession: %v", err)
	}

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/v1/auth/me", nil)
	r.AddCookie(&http.Cookie{Name: SessionCookieName, Value: encoded})

	svc.HandleMe(zerolog.Nop())(w, r)

	if w.Code != http.StatusUnauthorized {
		t.Errorf("HandleMe (expired): status = %d, want %d", w.Code, http.StatusUnauthorized)
	}
}

// ── TestHandleLogin_RedirectsAndSetsCookies ───────────────────────────────────

func TestHandleLogin_RedirectsAndSetsCookies(t *testing.T) {
	t.Parallel()
	idp := newMockIdP(t)
	svc := newTestService(t, idp)

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/v1/auth/login", nil)

	svc.HandleLogin(zerolog.Nop())(w, r)

	if w.Code != http.StatusFound {
		t.Errorf("HandleLogin: status = %d, want %d (302 Found)", w.Code, http.StatusFound)
	}

	// Must redirect to the IdP's authorize endpoint.
	location := w.Header().Get("Location")
	if location == "" {
		t.Fatal("HandleLogin: no Location header")
	}
	if !containsCaseInsensitive(location, "/authorize") {
		t.Errorf("HandleLogin: Location %q does not point at /authorize", location)
	}
	if !containsCaseInsensitive(location, "code_challenge") {
		t.Errorf("HandleLogin: Location %q missing code_challenge param", location)
	}
	if !containsCaseInsensitive(location, "code_challenge_method=S256") {
		t.Errorf("HandleLogin: Location %q missing code_challenge_method=S256", location)
	}
	if !containsCaseInsensitive(location, "nonce") {
		t.Errorf("HandleLogin: Location %q missing nonce param", location)
	}
	if !containsCaseInsensitive(location, "openid") {
		t.Errorf("HandleLogin: Location %q missing openid in scope", location)
	}

	// Must set the state cookie.
	stateCookieFound := false
	for _, ck := range w.Result().Cookies() {
		if ck.Name == stateCookieName {
			stateCookieFound = true
			if !ck.HttpOnly {
				t.Errorf("state cookie HttpOnly = false, want true")
			}
			if ck.Path != "/v1/auth" {
				t.Errorf("state cookie Path = %q, want %q", ck.Path, "/v1/auth")
			}
			if ck.MaxAge <= 0 {
				t.Errorf("state cookie MaxAge = %d, want > 0", ck.MaxAge)
			}
		}
	}
	if !stateCookieFound {
		t.Errorf("HandleLogin: %q cookie not set in response", stateCookieName)
	}
}

// ── TestHandleLogin_SanitizesReturnTo ────────────────────────────────────────

func TestHandleLogin_SanitizesReturnTo(t *testing.T) {
	t.Parallel()
	idp := newMockIdP(t)
	svc := newTestService(t, idp)

	cases := []struct {
		name         string
		returnTo     string
		expectInState bool // if true, the ReturnTo in the state cookie should be non-empty
	}{
		{
			// Evil URL on a different host — must be silently dropped.
			name:          "cross-origin return_to rejected",
			returnTo:      "https://evil.example.com/steal",
			expectInState: false,
		},
		{
			// Same scheme+host as PostLoginRedirect — allowed.
			name:          "same-origin return_to accepted",
			returnTo:      "https://app.example.com/dashboard",
			expectInState: true,
		},
		{
			// Relative URL (no scheme/host) — rejected.
			name:          "relative return_to rejected",
			returnTo:      "/dashboard",
			expectInState: false,
		},
		{
			// No return_to param at all — fine, no ReturnTo in state.
			name:          "missing return_to accepted as empty",
			returnTo:      "",
			expectInState: false,
		},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			target := "/v1/auth/login"
			if tc.returnTo != "" {
				target += "?return_to=" + tc.returnTo
			}

			w := httptest.NewRecorder()
			r := httptest.NewRequest(http.MethodGet, target, nil)
			svc.HandleLogin(zerolog.Nop())(w, r)

			if w.Code != http.StatusFound {
				t.Fatalf("status = %d, want 302", w.Code)
			}

			// Decode the state cookie to inspect ReturnTo.
			var stateCk *http.Cookie
			for _, ck := range w.Result().Cookies() {
				if ck.Name == stateCookieName {
					stateCk = ck
					break
				}
			}
			if stateCk == nil {
				t.Fatal("state cookie not set")
			}

			state, err := svc.codec.DecodeState(stateCk.Value)
			if err != nil {
				t.Fatalf("DecodeState: %v", err)
			}

			if tc.expectInState && state.ReturnTo == "" {
				t.Errorf("returnTo=%q: expected ReturnTo in state cookie, got empty", tc.returnTo)
			}
			if !tc.expectInState && state.ReturnTo != "" {
				t.Errorf("returnTo=%q: expected empty ReturnTo in state, got %q", tc.returnTo, state.ReturnTo)
			}
		})
	}
}

// ── TestHandleLogout_ClearsSessionCookie ─────────────────────────────────────

func TestHandleLogout_ClearsSessionCookie(t *testing.T) {
	t.Parallel()
	idp := newMockIdP(t)
	svc := newTestService(t, idp)

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/v1/auth/logout", nil)

	svc.HandleLogout(zerolog.Nop())(w, r)

	if w.Code != http.StatusFound {
		t.Errorf("HandleLogout: status = %d, want 302 Found", w.Code)
	}

	// The dcapi_session cookie must be cleared (MaxAge <= 0).
	sessionCleared := false
	for _, ck := range w.Result().Cookies() {
		if ck.Name == SessionCookieName {
			sessionCleared = true
			if ck.MaxAge > 0 {
				t.Errorf("session cookie MaxAge = %d, want <= 0 (cleared)", ck.MaxAge)
			}
		}
	}
	if !sessionCleared {
		t.Errorf("HandleLogout: %q cookie not present in response (expected a clearing Set-Cookie)", SessionCookieName)
	}

	// Must redirect to end_session_endpoint or PostLogoutRedirect.
	location := w.Header().Get("Location")
	if location == "" {
		t.Fatal("HandleLogout: no Location header")
	}
	// Our mock IdP advertises /logout as end_session_endpoint, so the
	// redirect should point there. It must also carry client_id and
	// post_logout_redirect_uri.
	if !containsCaseInsensitive(location, "/logout") {
		t.Errorf("HandleLogout: Location %q does not contain /logout", location)
	}
	if !containsCaseInsensitive(location, "post_logout_redirect_uri") {
		t.Errorf("HandleLogout: Location %q missing post_logout_redirect_uri", location)
	}
	if !containsCaseInsensitive(location, "client_id") {
		t.Errorf("HandleLogout: Location %q missing client_id", location)
	}
}

// ── HandleCallback skipped ───────────────────────────────────────────────────
//
// TODO(F7): HandleCallback exercise lives in the live cloud-ui integration
// test once the BFF ships in lk-dev. It requires a working oauth2.Exchange +
// ID-token verification, which demands a fully-faithful mock IdP with real
// RSA key material — disproportionate complexity for a unit test suite that
// already covers every other handler path.

