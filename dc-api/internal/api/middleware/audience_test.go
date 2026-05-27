package middleware

import "testing"

// TestAudienceMatches covers the multi-audience JWT acceptance logic that
// underpins DCAPI_OIDC_AUDIENCE supporting a list — needed so dc-api can
// honour tokens minted for the dcctl, cloud-ui, and future Terraform-provider
// clients without each operator re-configuring Asgardeo per-client.
func TestAudienceMatches(t *testing.T) {
	tests := []struct {
		name     string
		tokenAud []string
		allowed  []string
		want     bool
	}{
		{
			name:     "single token aud matches single allowed",
			tokenAud: []string{"dcctl-cid"},
			allowed:  []string{"dcctl-cid"},
			want:     true,
		},
		{
			name:     "single token aud matches one of many allowed",
			tokenAud: []string{"cloud-ui-cid"},
			allowed:  []string{"dcctl-cid", "cloud-ui-cid", "tf-cid"},
			want:     true,
		},
		{
			name:     "multi-value token aud — at least one matches",
			tokenAud: []string{"foo", "dcctl-cid", "bar"},
			allowed:  []string{"dcctl-cid"},
			want:     true,
		},
		{
			name:     "no overlap returns false",
			tokenAud: []string{"unknown-cid"},
			allowed:  []string{"dcctl-cid", "cloud-ui-cid"},
			want:     false,
		},
		{
			name:     "empty token aud is rejected",
			tokenAud: []string{},
			allowed:  []string{"dcctl-cid"},
			want:     false,
		},
		{
			name:     "case-sensitive — capital mismatch fails",
			tokenAud: []string{"Cloud-UI-CID"},
			allowed:  []string{"cloud-ui-cid"},
			want:     false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := audienceMatches(tc.tokenAud, tc.allowed)
			if got != tc.want {
				t.Errorf("audienceMatches(%v, %v) = %v, want %v",
					tc.tokenAud, tc.allowed, got, tc.want)
			}
		})
	}
}
