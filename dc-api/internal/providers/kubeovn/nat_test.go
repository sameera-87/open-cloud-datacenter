// Package kubeovn — nat_test.go
//
// Unit tests for the F15 NAT helper functions. These do not require a live
// cluster — they test pure IP arithmetic and naming logic.
//
// Run:
//
//	cd dc-api
//	go test ./internal/providers/kubeovn/... -v -count=1 -run TestNAT
package kubeovn

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestComputeLanIP verifies that ComputeLanIP returns the penultimate usable IP
// for various subnet sizes.
func TestComputeLanIP(t *testing.T) {
	cases := []struct {
		cidr string
		want string
	}{
		{"192.168.1.0/24", "192.168.1.254"},
		{"10.0.0.0/16", "10.0.255.254"},
		{"172.16.0.0/28", "172.16.0.14"},
		{"10.1.2.0/29", "10.1.2.6"}, // /29: .1-.6 usable, penultimate = .6
	}
	for _, tc := range cases {
		ip, err := ComputeLanIP(tc.cidr)
		require.NoError(t, err, "cidr=%s", tc.cidr)
		assert.Equal(t, tc.want, ip.String(), "cidr=%s", tc.cidr)
	}
}

// TestComputeLanIP_TooSmall: /31 → penultimate equals network → error.
func TestComputeLanIP_TooSmall(t *testing.T) {
	_, err := ComputeLanIP("10.0.0.0/31")
	assert.Error(t, err, "/31 subnet should fail: penultimate equals network address")
}

// TestBuildExcludeIPs verifies the simplified excludeIps list: just the
// gateway plus operator-supplied reserved IPs. KubeOVN's IPAM owns the
// rest of the subnet so we don't carve out a pool ourselves.
func TestBuildExcludeIPs(t *testing.T) {
	excl := buildExcludeIPs("192.168.10.254", []string{"192.168.10.15", "192.168.10.37"})
	assert.Equal(t, []string{"192.168.10.254", "192.168.10.15", "192.168.10.37"}, excl)

	// Empty reserved list → gateway only.
	excl = buildExcludeIPs("192.168.10.254", nil)
	assert.Equal(t, []string{"192.168.10.254"}, excl)
}

// TestNatNames verifies stable resource name derivation for short VPC names
// (no hashing needed).
func TestNatNames(t *testing.T) {
	vpcName := "vnet-tenant-myapp"
	assert.Equal(t, "natgw-"+vpcName, natGWName(vpcName))
	assert.Equal(t, "eip-"+vpcName, natEIPName(vpcName))
	assert.Equal(t, "snat-"+vpcName, natSnatName(vpcName))
}

// TestNatGWName_Hashing verifies that a long VPC name is hashed so the
// resulting StatefulSet pod name (vpc-nat-gw-<gwName>-0) stays ≤ 63 chars.
func TestNatGWName_Hashing(t *testing.T) {
	// Long enough that "vpc-nat-gw-natgw-" + name + "-0" would exceed 63.
	long := "vnet-test-tenant-nat-create-test-25a2d4f0-vnet"
	gw := natGWName(long)
	podName := "vpc-nat-gw-" + gw + "-0"
	assert.LessOrEqual(t, len(podName), 63,
		"derived pod name %q must fit in 63 chars (k8s limit)", podName)
}

// TestNatNames_Truncation verifies the CRD metadata.name budget of 253.
func TestNatNames_Truncation(t *testing.T) {
	long := ""
	for i := 0; i < 300; i++ {
		long += "a"
	}
	assert.LessOrEqual(t, len(natGWName(long)), 253)
	assert.LessOrEqual(t, len(natEIPName(long)), 253)
	assert.LessOrEqual(t, len(natSnatName(long)), 253)
}
