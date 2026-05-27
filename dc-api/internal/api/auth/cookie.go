// Package auth implements the BFF (Backend-for-Frontend) OIDC flow that
// cloud-ui uses to sign in via Asgardeo. cloud-ui is a public SPA which
// can't safely hold a client_secret; Asgardeo strips the `groups` claim
// from tokens issued to public clients, so cloud-ui could never resolve
// its tenant. The BFF runs the OAuth2 code-with-PKCE flow server-side
// against a *confidential* Asgardeo client (which gets full claims),
// then hands cloud-ui an HttpOnly session cookie scoped to dc-api.
//
// File layout:
//   - cookie.go   — AES-GCM encrypt/decrypt for short opaque payloads
//   - session.go  — encode/decode the session cookie payload
//   - state.go    — encode/decode the per-login state cookie (PKCE verifier + nonce + return_to)
//   - oidc.go     — Service that wires Provider + Config + cookie codec
//   - handlers.go — /v1/auth/{login,callback,logout,me} HTTP handlers
package auth

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
)

// CookieCodec encrypts and authenticates arbitrary byte payloads using
// AES-GCM. One Codec instance is shared by both the session cookie and
// the per-login state cookie — they're independent payloads but use the
// same encryption key (rotating the key invalidates both at once, which
// is the correct semantic).
//
// The encoded form is base64(nonce || ciphertext). AES-GCM provides
// confidentiality + authenticity in one primitive, so a tampered cookie
// fails Open() with a clear error rather than yielding garbage.
type CookieCodec struct {
	aead cipher.AEAD
}

// NewCookieCodec returns a Codec keyed by `key`. The key MUST be exactly
// 32 bytes — operators generate it once with `openssl rand -base64 32`
// and pass the base64 form via DCAPI_BFF_SESSION_SECRET.
func NewCookieCodec(key []byte) (*CookieCodec, error) {
	if len(key) != 32 {
		return nil, fmt.Errorf("cookie codec key must be 32 bytes (got %d) — generate with `openssl rand -base64 32`", len(key))
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("aes cipher: %w", err)
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("gcm: %w", err)
	}
	return &CookieCodec{aead: aead}, nil
}

// Seal encrypts `plaintext` and returns a base64-encoded value suitable
// for a Set-Cookie header. Every call produces a fresh nonce so two
// identical plaintexts never produce identical cookies.
func (c *CookieCodec) Seal(plaintext []byte) (string, error) {
	nonce := make([]byte, c.aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return "", fmt.Errorf("rand nonce: %w", err)
	}
	ciphertext := c.aead.Seal(nil, nonce, plaintext, nil)
	out := append(nonce, ciphertext...)
	return base64.RawURLEncoding.EncodeToString(out), nil
}

// Open is the inverse of Seal. Returns an error if the cookie has been
// tampered with, was signed under a different key, or is malformed.
// Callers must treat any error as "no valid session" — never log the
// plaintext on failure (it would leak garbage to logs).
func (c *CookieCodec) Open(encoded string) ([]byte, error) {
	raw, err := base64.RawURLEncoding.DecodeString(encoded)
	if err != nil {
		return nil, fmt.Errorf("decode cookie: %w", err)
	}
	ns := c.aead.NonceSize()
	if len(raw) < ns {
		return nil, errors.New("cookie too short")
	}
	nonce, ciphertext := raw[:ns], raw[ns:]
	plaintext, err := c.aead.Open(nil, nonce, ciphertext, nil)
	if err != nil {
		return nil, fmt.Errorf("decrypt cookie: %w", err)
	}
	return plaintext, nil
}
