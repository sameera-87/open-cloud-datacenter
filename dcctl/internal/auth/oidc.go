// Package auth implements the OIDC Authorization Code + PKCE flow for dcctl.
//
// ── How OIDC CLI Login Works ──────────────────────────────────────────────────
//
// Unlike a web app (which has a server to receive the redirect), a CLI tool is a
// "public client" — it cannot safely store a client secret. We use PKCE
// (Proof Key for Code Exchange) to prove we are the same process that started
// the flow, without needing a secret.
//
// The flow:
//  1. Generate a random code_verifier (64 bytes of random, base64url-encoded).
//  2. Compute code_challenge = BASE64URL(SHA256(code_verifier)).
//  3. Start a local HTTP server on localhost:8085.
//  4. Build the Asgardeo authorization URL with:
//       response_type=code
//       redirect_uri=http://localhost:8085/callback
//       code_challenge=<above>
//       code_challenge_method=S256
//  5. Open the URL in the user's default browser.
//  6. Asgardeo authenticates the user (login page) and redirects to localhost:8085/callback
//     with ?code=<authorization_code>.
//  7. Our local server captures the code, shuts itself down.
//  8. Exchange the code for tokens at the Asgardeo token endpoint,
//     sending code_verifier (not code_challenge) this time.
//  9. Store the access_token and refresh_token in ~/.dcctl/credentials.json.
//
// Why PKCE instead of a client_secret?
//   A client secret embedded in a CLI binary can be extracted by anyone who
//   downloads the binary. PKCE replaces the secret with a per-request random
//   challenge that is only valid for the current authorization flow.
package auth

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"net"
	"net/http"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
	"golang.org/x/oauth2"
)

// PKCEFlow manages a single OIDC Authorization Code + PKCE login flow.
type PKCEFlow struct {
	issuer       string
	clientID     string
	callbackPort int

	// codeVerifier is the random value we generate. We send the hash to Asgardeo,
	// then the raw value during token exchange to prove it's us.
	codeVerifier string
	oauthConfig  *oauth2.Config
	provider     *oidc.Provider
}

// NewPKCEFlow initialises the OIDC provider and OAuth2 config.
// This performs an HTTP request to Asgardeo's /.well-known/openid-configuration
// to discover the token and authorization endpoints.
func NewPKCEFlow(ctx context.Context, issuer, clientID string, callbackPort int) (*PKCEFlow, error) {
	provider, err := oidc.NewProvider(ctx, issuer)
	if err != nil {
		return nil, fmt.Errorf("discover OIDC provider at %s: %w", issuer, err)
	}

	redirectURI := fmt.Sprintf("http://localhost:%d/callback", callbackPort)

	oauthConfig := &oauth2.Config{
		ClientID:    clientID,
		Endpoint:    provider.Endpoint(),
		RedirectURL: redirectURI,
		// openid is required for OIDC. profile and email give us the user's name/email.
		Scopes: []string{oidc.ScopeOpenID, "profile", "email", "groups"},
	}

	return &PKCEFlow{
		issuer:       issuer,
		clientID:     clientID,
		callbackPort: callbackPort,
		oauthConfig:  oauthConfig,
		provider:     provider,
	}, nil
}

// AuthURL generates the Asgardeo authorization URL with PKCE challenge.
// Open this URL in the user's browser.
func (f *PKCEFlow) AuthURL() (string, error) {
	verifier, err := generatePKCEVerifier()
	if err != nil {
		return "", fmt.Errorf("generate PKCE: %w", err)
	}
	f.codeVerifier = verifier

	// oauth2.S256ChallengeOption takes the raw verifier and computes
	// code_challenge = BASE64URL(SHA256(verifier)) internally.
	// Do NOT pre-hash — pass the verifier directly.
	url := f.oauthConfig.AuthCodeURL("state-token",
		oauth2.S256ChallengeOption(verifier),
		oauth2.AccessTypeOffline, // requests a refresh_token
	)
	return url, nil
}

// WaitForCallback starts a local HTTP server and waits for Asgardeo to redirect
// back with the authorization code. Returns the code.
// This call blocks until the code arrives or ctx is cancelled.
func (f *PKCEFlow) WaitForCallback(ctx context.Context) (string, error) {
	codeCh := make(chan string, 1)
	errCh := make(chan error, 1)

	mux := http.NewServeMux()
	mux.HandleFunc("/callback", func(w http.ResponseWriter, r *http.Request) {
		code := r.URL.Query().Get("code")
		if code == "" {
			errStr := r.URL.Query().Get("error_description")
			errCh <- fmt.Errorf("no code in callback: %s", errStr)
			http.Error(w, "Login failed", http.StatusBadRequest)
			return
		}
		codeCh <- code
		// Friendly success page shown in the browser after login.
		fmt.Fprintln(w, "<html><body><h2>Login successful!</h2><p>You can close this tab.</p></body></html>")
	})

	addr := fmt.Sprintf("localhost:%d", f.callbackPort)
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return "", fmt.Errorf("listen on %s for OIDC callback: %w", addr, err)
	}

	srv := &http.Server{
		Handler:      mux,
		ReadTimeout:  30 * time.Second,
		WriteTimeout: 30 * time.Second,
	}

	go func() { _ = srv.Serve(ln) }()
	defer srv.Shutdown(context.Background()) //nolint:errcheck

	select {
	case code := <-codeCh:
		return code, nil
	case err := <-errCh:
		return "", err
	case <-ctx.Done():
		return "", fmt.Errorf("login timed out: %w", ctx.Err())
	}
}

// ExchangeCode exchanges the authorization code for access + refresh tokens.
// Returns the token set.
func (f *PKCEFlow) ExchangeCode(ctx context.Context, code string) (*oauth2.Token, error) {
	// We pass the raw code_verifier here (not the hash).
	// Asgardeo hashes it with SHA256 and compares it against the code_challenge
	// we sent in step 4. This proves it's the same client.
	token, err := f.oauthConfig.Exchange(ctx, code,
		oauth2.VerifierOption(f.codeVerifier),
	)
	if err != nil {
		return nil, fmt.Errorf("exchange OIDC code: %w", err)
	}
	return token, nil
}

// ExtractClaims parses the id_token inside a token set and extracts key claims.
// Returns (tenantID, sub, error).
func (f *PKCEFlow) ExtractClaims(ctx context.Context, token *oauth2.Token) (tenantID, sub string, err error) {
	rawIDToken, ok := token.Extra("id_token").(string)
	if !ok {
		return "", "", fmt.Errorf("no id_token in token response")
	}

	verifier := f.provider.Verifier(&oidc.Config{ClientID: f.clientID})
	idToken, err := verifier.Verify(ctx, rawIDToken)
	if err != nil {
		return "", "", fmt.Errorf("verify id_token: %w", err)
	}

	var claims struct {
		Sub    string   `json:"sub"`
		Groups []string `json:"groups"`
	}
	if err := idToken.Claims(&claims); err != nil {
		return "", "", fmt.Errorf("parse id_token claims: %w", err)
	}

	// Derive tenantID from the first dc-tenant-* group.
	for _, g := range claims.Groups {
		if len(g) > 10 && g[:10] == "dc-tenant-" {
			tenantID = g[10:]
			break
		}
	}

	return tenantID, claims.Sub, nil
}

// ─────────────────────────── PKCE Generation ────────────────────────────────

// generatePKCEVerifier returns a random code_verifier.
// 64 random bytes, base64url-encoded (no padding).
// The challenge (SHA256 hash) is computed by oauth2.S256ChallengeOption.
func generatePKCEVerifier() (string, error) {
	b := make([]byte, 64)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("generate random bytes: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}
