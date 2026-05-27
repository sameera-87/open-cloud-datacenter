package kubeovn

import "testing"

// transitLocalIP must return the same /24 for both sides of a peering and
// differentiate the host octet by lexicographic order. These tests pin the
// behaviour for both the F6 allocator path (transitNetwork supplied) and
// the legacy SHA-256 hash path (transitNetwork empty).

func TestTransitLocalIP_AllocatorPath(t *testing.T) {
	cases := []struct {
		name      string
		vpcA      string
		vpcB      string
		network   string
		wantA     string // CIDR returned for vpcA when called with (vpcA, vpcB)
		wantB     string // CIDR returned for vpcB when called with (vpcB, vpcA)
	}{
		{
			name:    "lower name gets .1, higher gets .2",
			vpcA:    "vnet-a",
			vpcB:    "vnet-b",
			network: "100.64.5.0/24",
			wantA:   "100.64.5.1/24",
			wantB:   "100.64.5.2/24",
		},
		{
			name:    "argument order independent",
			vpcA:    "vnet-b", // same pair, opposite call order
			vpcB:    "vnet-a",
			network: "100.64.5.0/24",
			wantA:   "100.64.5.2/24", // vnet-b sorts higher → .2
			wantB:   "100.64.5.1/24",
		},
		{
			name:    "high-index allocation lands in 100.127.x",
			vpcA:    "vnet-a",
			vpcB:    "vnet-b",
			network: "100.127.255.0/24",
			wantA:   "100.127.255.1/24",
			wantB:   "100.127.255.2/24",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			gotA := transitLocalIP(tc.vpcA, tc.vpcB, tc.network)
			gotB := transitLocalIP(tc.vpcB, tc.vpcA, tc.network)
			if gotA != tc.wantA {
				t.Errorf("transitLocalIP(%q, %q, %q) = %q, want %q", tc.vpcA, tc.vpcB, tc.network, gotA, tc.wantA)
			}
			if gotB != tc.wantB {
				t.Errorf("transitLocalIP(%q, %q, %q) = %q, want %q", tc.vpcB, tc.vpcA, tc.network, gotB, tc.wantB)
			}
		})
	}
}

func TestTransitLocalIP_HashFallback(t *testing.T) {
	// Pre-F6 peerings stored their localConnectIP on the Vpc CRD already;
	// the fallback path must keep producing the same value the old code
	// did so a restart doesn't break them. The two sides still differ by
	// host octet (.1 vs .2).
	a := transitLocalIP("vnet-foo", "vnet-bar", "")
	b := transitLocalIP("vnet-bar", "vnet-foo", "")
	if a == b {
		t.Fatalf("both sides returned %q — must differ", a)
	}

	// Same /24 either way:
	netA := a[:len(a)-len("/24")]
	netB := b[:len(b)-len("/24")]
	if netA[:len("100.64.")] != "100.64." {
		t.Errorf("legacy IP %q not in 100.64.0.0/16", a)
	}
	// Drop the last octet from each, compare networks.
	netA = networkPart(netA)
	netB = networkPart(netB)
	if netA != netB {
		t.Errorf("sides advertise different /24s in hash mode: %s vs %s", netA, netB)
	}
}

func TestTransitLocalIP_MalformedNetworkFallsBackToHash(t *testing.T) {
	// A malformed transitNetwork should not crash — it should fall through
	// to the hash path. The allocator never produces a malformed value, but
	// be defensive.
	got := transitLocalIP("vnet-a", "vnet-b", "not-a-cidr")
	if got == "" {
		t.Errorf("malformed network produced empty result")
	}
	// Same as the pure-hash path.
	want := transitLocalIP("vnet-a", "vnet-b", "")
	if got != want {
		t.Errorf("malformed-network path %q differs from hash path %q", got, want)
	}
}

func TestTransitSide(t *testing.T) {
	if got := transitSide("a", "b"); got != 1 {
		t.Errorf("transitSide(a,b) = %d, want 1", got)
	}
	if got := transitSide("b", "a"); got != 2 {
		t.Errorf("transitSide(b,a) = %d, want 2", got)
	}
	// Equal (degenerate) → 1; we never expect this in real callers but the
	// helper shouldn't panic.
	if got := transitSide("x", "x"); got != 1 {
		t.Errorf("transitSide(x,x) = %d, want 1", got)
	}
}

// networkPart returns "100.64.5" given "100.64.5.1".
func networkPart(ip string) string {
	last := len(ip) - 1
	for last >= 0 && ip[last] != '.' {
		last--
	}
	return ip[:last]
}
