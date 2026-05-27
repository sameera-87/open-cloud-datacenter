package auth

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
)

// State is the per-login payload encrypted into the dcapi_oidc_state
// cookie. It travels between /v1/auth/login (sets the cookie + redirects
// to Asgardeo) and /v1/auth/callback (reads the cookie + exchanges the
// code). PKCE binds the verifier to the same browser; nonce binds the
// returned ID token to this exact login attempt; ReturnTo carries the
// post-login redirect URL through the round-trip.
//
// The cookie has a short Max-Age (5 minutes) because it's only useful
// during the redirect window. After /v1/auth/callback consumes it the
// caller clears it explicitly.
type State struct {
	CodeVerifier string `json:"v"`
	Nonce        string `json:"n"`
	ReturnTo     string `json:"r,omitempty"`
}

// EncodeState seals the State into a cookie value.
func (c *CookieCodec) EncodeState(s *State) (string, error) {
	plaintext, err := json.Marshal(s)
	if err != nil {
		return "", fmt.Errorf("marshal state: %w", err)
	}
	return c.Seal(plaintext)
}

// DecodeState reverses EncodeState.
func (c *CookieCodec) DecodeState(encoded string) (*State, error) {
	plaintext, err := c.Open(encoded)
	if err != nil {
		return nil, err
	}
	var s State
	if err := json.Unmarshal(plaintext, &s); err != nil {
		return nil, fmt.Errorf("unmarshal state: %w", err)
	}
	return &s, nil
}

// NewPKCEVerifier produces a fresh 32-byte random code_verifier
// encoded per RFC 7636 §4.1 (base64url, no padding, 43..128 chars).
func NewPKCEVerifier() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// PKCEChallenge derives the S256 challenge from a verifier (RFC 7636 §4.2).
// The challenge travels in the authorize URL; the verifier is held
// server-side in the state cookie. Token exchange presents the verifier
// and Asgardeo recomputes the challenge to confirm a match.
func PKCEChallenge(verifier string) string {
	sum := sha256.Sum256([]byte(verifier))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

// NewNonce returns a fresh 16-byte random nonce, base64url-encoded.
// Asgardeo echoes this into the issued ID token's `nonce` claim — we
// reject the token at callback time if the values don't match, which
// stops a replay where an attacker tricks the user into authenticating
// against a different relying party.
func NewNonce() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}
