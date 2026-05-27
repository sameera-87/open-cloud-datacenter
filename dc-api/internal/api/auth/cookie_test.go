package auth

import (
	"bytes"
	"testing"
)

// ── helpers ──────────────────────────────────────────────────────────────────

func mustCodec(t *testing.T) *CookieCodec {
	t.Helper()
	key := bytes.Repeat([]byte("k"), 32)
	c, err := NewCookieCodec(key)
	if err != nil {
		t.Fatalf("NewCookieCodec: %v", err)
	}
	return c
}

// ── TestCookieCodec_RoundTrip ─────────────────────────────────────────────────

func TestCookieCodec_RoundTrip(t *testing.T) {
	t.Parallel()
	c := mustCodec(t)

	cases := []struct {
		name      string
		plaintext []byte
	}{
		{"empty", []byte{}},
		{"ascii", []byte("hello world")},
		{"binary", []byte{0x00, 0xff, 0xde, 0xad, 0xbe, 0xef}},
		{"json-like", []byte(`{"a":"tok","r":"refresh","s":"sub@test"}`)},
		{"256 bytes", bytes.Repeat([]byte("x"), 256)},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			sealed, err := c.Seal(tc.plaintext)
			if err != nil {
				t.Fatalf("Seal: %v", err)
			}
			got, err := c.Open(sealed)
			if err != nil {
				t.Fatalf("Open: %v", err)
			}
			if !bytes.Equal(got, tc.plaintext) {
				t.Errorf("round-trip mismatch:\n  got  %v\n  want %v", got, tc.plaintext)
			}
		})
	}
}

// ── TestCookieCodec_TamperDetected ───────────────────────────────────────────

// Open accepts a base64url string. Flipping a single character in the
// base64url string always changes the decoded bytes, which AES-GCM's
// authentication tag will detect and reject.
func TestCookieCodec_TamperDetected(t *testing.T) {
	t.Parallel()
	c := mustCodec(t)

	plaintext := []byte("sensitive payload that must not be readable after tampering")
	sealed, err := c.Seal(plaintext)
	if err != nil {
		t.Fatalf("Seal: %v", err)
	}

	raw := []byte(sealed)
	// Positions to corrupt: first char (hits nonce), middle, last char.
	positions := []int{0, len(raw) / 2, len(raw) - 1}
	for _, pos := range positions {
		tampered := make([]byte, len(raw))
		copy(tampered, raw)
		// Flip between 'a' and 'b' — both are valid base64url chars so the
		// decoder doesn't reject the string outright; the AEAD tag does.
		if tampered[pos] == 'a' {
			tampered[pos] = 'b'
		} else {
			tampered[pos] = 'a'
		}
		_, err := c.Open(string(tampered))
		if err == nil {
			t.Errorf("Open accepted tampered cookie at byte position %d — want error", pos)
		}
	}
}

// ── TestCookieCodec_RequiresExactKeyLength ────────────────────────────────────

func TestCookieCodec_RequiresExactKeyLength(t *testing.T) {
	t.Parallel()

	badLengths := []int{0, 16, 31, 33, 64}
	for _, n := range badLengths {
		key := bytes.Repeat([]byte("k"), n)
		_, err := NewCookieCodec(key)
		if err == nil {
			t.Errorf("NewCookieCodec with key len %d: expected error, got nil", n)
		}
	}

	// Exactly 32 bytes must succeed.
	key := bytes.Repeat([]byte("k"), 32)
	_, err := NewCookieCodec(key)
	if err != nil {
		t.Errorf("NewCookieCodec with key len 32: unexpected error: %v", err)
	}
}

// ── TestCookieCodec_DistinctNoncePerSeal ─────────────────────────────────────

func TestCookieCodec_DistinctNoncePerSeal(t *testing.T) {
	t.Parallel()
	c := mustCodec(t)

	plaintext := []byte("same plaintext every time")

	sealed1, err := c.Seal(plaintext)
	if err != nil {
		t.Fatalf("Seal #1: %v", err)
	}
	sealed2, err := c.Seal(plaintext)
	if err != nil {
		t.Fatalf("Seal #2: %v", err)
	}

	if sealed1 == sealed2 {
		t.Error("two Seal calls on identical plaintext produced identical output — nonce is not random")
	}
}
