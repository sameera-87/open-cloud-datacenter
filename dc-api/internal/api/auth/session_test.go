package auth

import (
	"testing"
	"time"
)

// ── TestSession_EncodeDecodeRoundTrip ─────────────────────────────────────────

func TestSession_EncodeDecodeRoundTrip(t *testing.T) {
	t.Parallel()
	c := mustCodec(t)

	now := time.Now().UTC().Truncate(time.Second)
	original := &Session{
		AccessToken:  "eyJ.access.token",
		RefreshToken: "eyJ.refresh.token",
		ExpiresAt:    now.Add(time.Hour),
		Subject:      "sub|abc123",
		Email:        "user@example.com",
	}

	encoded, err := c.EncodeSession(original)
	if err != nil {
		t.Fatalf("EncodeSession: %v", err)
	}

	got, err := c.DecodeSession(encoded)
	if err != nil {
		t.Fatalf("DecodeSession: %v", err)
	}

	if got.AccessToken != original.AccessToken {
		t.Errorf("AccessToken: got %q, want %q", got.AccessToken, original.AccessToken)
	}
	if got.RefreshToken != original.RefreshToken {
		t.Errorf("RefreshToken: got %q, want %q", got.RefreshToken, original.RefreshToken)
	}
	if !got.ExpiresAt.Equal(original.ExpiresAt) {
		t.Errorf("ExpiresAt: got %v, want %v", got.ExpiresAt, original.ExpiresAt)
	}
	if got.Subject != original.Subject {
		t.Errorf("Subject: got %q, want %q", got.Subject, original.Subject)
	}
	if got.Email != original.Email {
		t.Errorf("Email: got %q, want %q", got.Email, original.Email)
	}
}

// ── TestSession_Expired ───────────────────────────────────────────────────────

func TestSession_Expired(t *testing.T) {
	t.Parallel()

	now := time.Now()

	cases := []struct {
		name      string
		expiresAt time.Time
		want      bool
	}{
		{
			// Clearly in the past.
			name:      "expired one hour ago",
			expiresAt: now.Add(-time.Hour),
			want:      true,
		},
		{
			// Still in the future but inside the 30-second skew window —
			// the implementation treats now.After(ExpiresAt - 30s) as expired.
			name:      "expires in 10 seconds (within skew window)",
			expiresAt: now.Add(10 * time.Second),
			want:      true,
		},
		{
			// 60 seconds ahead: ExpiresAt - 30s = now+30s, and now.After(now+30s)
			// is false, so this is definitively NOT expired regardless of nanosecond
			// jitter inside the test. (The "exactly 30s" boundary is too sensitive
			// to sub-millisecond elapsed time; we document the skew semantics via
			// the 10-second case above and leave the boundary untested.)
			name:      "expires in 60 seconds (well outside skew window — not expired)",
			expiresAt: now.Add(60 * time.Second),
			want:      false,
		},
		{
			// Comfortably in the future.
			name:      "expires in one hour",
			expiresAt: now.Add(time.Hour),
			want:      false,
		},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			s := &Session{ExpiresAt: tc.expiresAt}
			if got := s.Expired(); got != tc.want {
				t.Errorf("Session{ExpiresAt:%v}.Expired() = %v, want %v", tc.expiresAt, got, tc.want)
			}
		})
	}
}

// ── TestSession_DecodeRejectsGarbage ─────────────────────────────────────────

func TestSession_DecodeRejectsGarbage(t *testing.T) {
	t.Parallel()
	c := mustCodec(t)

	_, err := c.DecodeSession("not-a-valid-sealed-value!!!")
	if err == nil {
		t.Error("DecodeSession with garbage input: expected error, got nil")
	}
}
