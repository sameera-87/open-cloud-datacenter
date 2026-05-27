package auth

import (
	"strings"
	"testing"
)

// ── TestState_EncodeDecodeRoundTrip ───────────────────────────────────────────

func TestState_EncodeDecodeRoundTrip(t *testing.T) {
	t.Parallel()
	c := mustCodec(t)

	original := &State{
		CodeVerifier: "dBjftJeZ4CVP-mB92K27uhbUJU1p1r_wW1gFWFOEjXk",
		Nonce:        "abc123nonce",
		ReturnTo:     "https://app.example.com/dashboard",
	}

	encoded, err := c.EncodeState(original)
	if err != nil {
		t.Fatalf("EncodeState: %v", err)
	}

	got, err := c.DecodeState(encoded)
	if err != nil {
		t.Fatalf("DecodeState: %v", err)
	}

	if got.CodeVerifier != original.CodeVerifier {
		t.Errorf("CodeVerifier: got %q, want %q", got.CodeVerifier, original.CodeVerifier)
	}
	if got.Nonce != original.Nonce {
		t.Errorf("Nonce: got %q, want %q", got.Nonce, original.Nonce)
	}
	if got.ReturnTo != original.ReturnTo {
		t.Errorf("ReturnTo: got %q, want %q", got.ReturnTo, original.ReturnTo)
	}
}

// TestState_EncodeDecodeRoundTrip_NoReturnTo verifies the omitempty field
// round-trips correctly when ReturnTo is empty.
func TestState_EncodeDecodeRoundTrip_NoReturnTo(t *testing.T) {
	t.Parallel()
	c := mustCodec(t)

	original := &State{
		CodeVerifier: "verifier",
		Nonce:        "nonce",
		ReturnTo:     "", // omitted in JSON
	}

	encoded, err := c.EncodeState(original)
	if err != nil {
		t.Fatalf("EncodeState: %v", err)
	}
	got, err := c.DecodeState(encoded)
	if err != nil {
		t.Fatalf("DecodeState: %v", err)
	}
	if got.ReturnTo != "" {
		t.Errorf("ReturnTo: got %q, want empty string", got.ReturnTo)
	}
}

// ── TestPKCE_VerifierShape ────────────────────────────────────────────────────

// RFC 7636 §4.1: the verifier is base64url-encoded, no padding, and must be
// 43–128 characters. 32 random bytes → ceil(32*4/3) = 43 chars (exact).
func TestPKCE_VerifierShape(t *testing.T) {
	t.Parallel()

	seen := make(map[string]bool, 100)
	for i := 0; i < 100; i++ {
		v, err := NewPKCEVerifier()
		if err != nil {
			t.Fatalf("NewPKCEVerifier iteration %d: %v", i, err)
		}
		// Length check.
		if len(v) != 43 {
			t.Errorf("iteration %d: verifier length = %d, want 43", i, len(v))
		}
		// Alphabet check — base64url uses A-Z, a-z, 0-9, -, _; no padding.
		for _, ch := range v {
			if !isBase64URLChar(ch) {
				t.Errorf("iteration %d: illegal character %q in verifier %q", i, ch, v)
				break
			}
		}
		// Uniqueness.
		if seen[v] {
			t.Errorf("iteration %d: duplicate verifier %q", i, v)
		}
		seen[v] = true
	}
}

func isBase64URLChar(r rune) bool {
	return (r >= 'A' && r <= 'Z') ||
		(r >= 'a' && r <= 'z') ||
		(r >= '0' && r <= '9') ||
		r == '-' || r == '_'
}

// ── TestPKCE_ChallengeMatchesRFC7636Example ───────────────────────────────────

// RFC 7636 §4.5 published test vector:
//
//	code_verifier   = dBjftJeZ4CVP-mB92K27uhbUJU1p1r_wW1gFWFOEjXk
//	code_challenge  = E9Melhoa2OwvFrEMTJguCHaoeK1t8URWbuGJSstw-cM
func TestPKCE_ChallengeMatchesRFC7636Example(t *testing.T) {
	t.Parallel()

	const (
		verifier  = "dBjftJeZ4CVP-mB92K27uhbUJU1p1r_wW1gFWFOEjXk"
		wantChall = "E9Melhoa2OwvFrEMTJguCHaoeK1t8URWbuGJSstw-cM"
	)

	got := PKCEChallenge(verifier)
	if got != wantChall {
		t.Errorf("PKCEChallenge(%q)\n  got  %q\n  want %q", verifier, got, wantChall)
	}
}

// ── TestNewNonce_Shape ────────────────────────────────────────────────────────

// NewNonce returns 16 random bytes as base64url, which is ceil(16*4/3)=22 chars.
func TestNewNonce_Shape(t *testing.T) {
	t.Parallel()

	seen := make(map[string]bool, 50)
	for i := 0; i < 50; i++ {
		n, err := NewNonce()
		if err != nil {
			t.Fatalf("NewNonce iteration %d: %v", i, err)
		}
		if len(n) != 22 {
			t.Errorf("iteration %d: nonce length = %d, want 22", i, len(n))
		}
		if strings.ContainsAny(n, "+/=") {
			t.Errorf("iteration %d: nonce %q contains standard-base64 characters (want base64url)", i, n)
		}
		if seen[n] {
			t.Errorf("iteration %d: duplicate nonce %q", i, n)
		}
		seen[n] = true
	}
}
