//go:build integration

package integration

import (
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"math/big"
	"strings"
	"time"
)

// JWTMinter generates per-test-run RSA keys and mints signed JWTs.
// Generated once per TestMain — never persisted to disk.
type JWTMinter struct {
	privKey *rsa.PrivateKey
}

// NewJWTMinter generates a fresh RSA-2048 key pair.
func NewJWTMinter() (*JWTMinter, error) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		return nil, fmt.Errorf("generate test RSA key: %w", err)
	}
	return &JWTMinter{privKey: key}, nil
}

// PublicKeyJWKS returns the public key as JWK Set JSON.
// Pass directly to middleware.NewTestModeAuth.
func (m *JWTMinter) PublicKeyJWKS() []byte {
	pub := &m.privKey.PublicKey
	n := base64.RawURLEncoding.EncodeToString(pub.N.Bytes())
	e := base64.RawURLEncoding.EncodeToString(new(big.Int).SetInt64(int64(pub.E)).Bytes())
	jwks := map[string]interface{}{
		"keys": []interface{}{
			map[string]string{"kty": "RSA", "alg": "RS256", "use": "sig", "kid": "test-1", "n": n, "e": e},
		},
	}
	b, _ := json.Marshal(jwks)
	return b
}

// MintToken mints a JWT for tenantID using the standard "dc-tenant-" group prefix.
func (m *JWTMinter) MintToken(tenantID, userID string) (string, error) {
	return m.MintTokenWithGroups(tenantID, userID, []string{"dc-tenant-" + tenantID})
}

// MintTokenMultiTenant mints a JWT carrying dc-tenant-* groups for every
// tenantID in the slice. Use this in tests that need a single principal with
// access to multiple tenants (e.g. cloud-ui tenant switcher, or asserting that
// the auth middleware autoprovisions rows for ALL groups simultaneously).
//
// The JWT sub is set to userID; all groups are added as "dc-tenant-<t>" entries.
func (m *JWTMinter) MintTokenMultiTenant(userID string, tenantIDs []string) (string, error) {
	groups := make([]string, 0, len(tenantIDs))
	for _, t := range tenantIDs {
		groups = append(groups, "dc-tenant-"+t)
	}
	return m.MintTokenWithGroups(userID, userID, groups)
}

// MintTokenWithGroups mints a JWT with explicit group claims (for admin tests, etc.).
func (m *JWTMinter) MintTokenWithGroups(sub, userID string, groups []string) (string, error) {
	hdr, _ := json.Marshal(map[string]string{"alg": "RS256", "typ": "JWT", "kid": "test-1"})
	pay, _ := json.Marshal(map[string]interface{}{
		"sub": sub, "email": userID + "@test.dc", "groups": groups,
		"iat": time.Now().Unix(), "exp": time.Now().Add(time.Hour).Unix(),
	})
	hE := base64.RawURLEncoding.EncodeToString(hdr)
	pE := base64.RawURLEncoding.EncodeToString(pay)
	h := crypto.SHA256.New()
	h.Write([]byte(hE + "." + pE))
	sig, err := rsa.SignPKCS1v15(rand.Reader, m.privKey, crypto.SHA256, h.Sum(nil))
	if err != nil {
		return "", fmt.Errorf("sign test JWT: %w", err)
	}
	return strings.Join([]string{hE, pE, base64.RawURLEncoding.EncodeToString(sig)}, "."), nil
}
