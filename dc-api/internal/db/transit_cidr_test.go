package db

import "testing"

// TransitCIDRFromIndex must map [0..16383] across 100.64.0.0/10 with no gaps
// and no overlap. The peering handler relies on these CIDRs being:
//   - all /24 (KubeOVN expects a CIDR, not a host IP),
//   - inside 100.64.0.0/10 (RFC 6598 Shared Address Space),
//   - strictly distinct per cidr_index (UNIQUE constraint enforcement).

func TestTransitCIDRFromIndex_Boundaries(t *testing.T) {
	cases := []struct {
		idx  int
		want string
	}{
		{0, "100.64.0.0/24"},
		{1, "100.64.1.0/24"},
		{255, "100.64.255.0/24"},
		{256, "100.65.0.0/24"},
		{16383, "100.127.255.0/24"},
	}
	for _, tc := range cases {
		if got := TransitCIDRFromIndex(tc.idx); got != tc.want {
			t.Errorf("TransitCIDRFromIndex(%d) = %q, want %q", tc.idx, got, tc.want)
		}
	}
}

func TestTransitCIDRFromIndex_Clamps(t *testing.T) {
	// Out-of-range inputs clamp to the nearest valid bucket rather than
	// returning a CIDR outside 100.64.0.0/10. The DB CHECK constraint
	// already prevents bad indices reaching here, so this is belt-and-braces.
	if got := TransitCIDRFromIndex(-1); got != "100.64.0.0/24" {
		t.Errorf("TransitCIDRFromIndex(-1) = %q, want 100.64.0.0/24", got)
	}
	if got := TransitCIDRFromIndex(99999); got != "100.127.255.0/24" {
		t.Errorf("TransitCIDRFromIndex(99999) = %q, want 100.127.255.0/24", got)
	}
}

func TestTransitCIDRFromIndex_UniquePerIndex(t *testing.T) {
	// Sample 1000 indices and confirm no duplicates — same property the
	// UNIQUE constraint enforces server-side.
	seen := make(map[string]int, 1000)
	for i := 0; i < 1000; i++ {
		idx := i * 16 // spread across the range
		got := TransitCIDRFromIndex(idx)
		if other, dup := seen[got]; dup {
			t.Fatalf("collision: idx %d and %d both produced %q", other, idx, got)
		}
		seen[got] = idx
	}
}
