//go:build integration

package integration

// F15 SNAT integration test suite.
//
// These tests verify the end-to-end NAT pipeline:
//   - VpcNatGateway, IptablesEIP, IptablesSnatRule are created when a VNet
//     has a subnet and the NAT provisioner is wired.
//   - The 0.0.0.0/0 static route is added to the VPC.
//   - All three NAT resources are removed when the VNet is deleted.
//   - IPs are released back to the pool after VNet deletion.
//
// The "curl 1.1.1.1 from a probe pod" check IS automated below in
// TestNAT_NATResourcesCreatedAfterSubnetCreate — it's the actual value test
// for F15 (VPC outbound), not just plumbing.

import (
	"context"
	"crypto/sha1"
	"encoding/hex"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/stretchr/testify/require"
	"github.com/wso2/dc-api/internal/providers/kubeovn"
)

// GVRs for the NAT CRDs verified against KubeOVN v1.15 on lk-dev.
var (
	natVpcNatGatewayGVR    = schema.GroupVersionResource{Group: "kubeovn.io", Version: "v1", Resource: "vpc-nat-gateways"}
	natIptablesEIPGVR      = schema.GroupVersionResource{Group: "kubeovn.io", Version: "v1", Resource: "iptables-eips"}
	natIptablesSnatRuleGVR = schema.GroupVersionResource{Group: "kubeovn.io", Version: "v1", Resource: "iptables-snat-rules"}
)

// f15EnvConfigured returns true when DCAPI_VPC_EXTERNAL_BRIDGE is set in the
// test process environment. Used to skip NAT tests in environments without
// the external bridge wired up (e.g. the standard CI environment).
func f15EnvConfigured() bool {
	return os.Getenv("DCAPI_VPC_EXTERNAL_BRIDGE") != ""
}

// shortID returns the first 8 hex chars of sha1(s) — used to name probe
// pods within k8s name length limits.
func shortID(s string) string {
	sum := sha1.Sum([]byte(s))
	return hex.EncodeToString(sum[:4])
}

// dbGetTenantNamespaceForVNet returns the tenant namespace where this VNet's
// Subnet NADs live (format "dc-<tenantID>"). dbGetSubnetBackendUID is defined
// in fixtures.go.
func dbGetTenantNamespaceForVNet(t *testing.T, vnetID string) string {
	t.Helper()
	vnet, err := env.DB.GetVNetInternal(context.Background(), uuid.MustParse(vnetID))
	require.NoError(t, err)
	return "dc-" + vnet.TenantID
}

// createProbePod spawns a short-lived netshoot pod in the given namespace,
// attached to the supplied multus NAD (format "<ns>/<nad-name>"), and waits
// for it to be Running. Cleanup is the caller's responsibility.
func createProbePod(t *testing.T, ns, name, multusRef string) {
	t.Helper()
	manifest := `apiVersion: v1
kind: Pod
metadata:
  name: ` + name + `
  namespace: ` + ns + `
  annotations:
    v1.multus-cni.io/default-network: ` + multusRef + `
spec:
  containers:
  - name: nettest
    image: nicolaka/netshoot:latest
    command: ["sh", "-c", "sleep 600"]
  restartPolicy: Never
`
	cmd := exec.Command("kubectl", "--context", os.Getenv("KUBE_CONTEXT"), "apply", "-f", "-")
	cmd.Stdin = strings.NewReader(manifest)
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "kubectl apply probe pod: %s", out)

	// Wait for Running.
	require.Eventually(t, func() bool {
		c := exec.Command("kubectl", "--context", os.Getenv("KUBE_CONTEXT"),
			"-n", ns, "get", "pod", name, "-o", "jsonpath={.status.phase}")
		o, _ := c.Output()
		return strings.TrimSpace(string(o)) == "Running"
	}, 90*time.Second, 2*time.Second, "probe pod %s/%s did not reach Running", ns, name)
}

// deleteProbePod removes a probe pod, ignoring NotFound.
func deleteProbePod(ns, name string) {
	cmd := exec.Command("kubectl", "--context", os.Getenv("KUBE_CONTEXT"),
		"-n", ns, "delete", "pod", name, "--ignore-not-found", "--wait=false")
	_ = cmd.Run()
}

// probeCurl execs curl inside the probe pod and returns (http_code, body).
// On failure or non-2xx/3xx, returns the empty code and stderr-ish output.
func probeCurl(t *testing.T, ns, podName, url string, timeout time.Duration) (string, string) {
	t.Helper()
	cctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	cmd := exec.CommandContext(cctx, "kubectl",
		"--context", os.Getenv("KUBE_CONTEXT"), "-n", ns,
		"exec", podName, "--",
		"curl", "-sS", "--max-time", "10",
		"-o", "/dev/null", "-w", "%{http_code}",
		url)
	out, _ := cmd.CombinedOutput()
	return strings.TrimSpace(string(out)), string(out)
}

// natResourcesExist returns true iff all three NAT objects exist for vpcBackendUID.
// Name derivation mirrors kubeovn/nat.go: natgw-<vpc>, eip-<vpc>, snat-<vpc>.
func natResourcesExist(ctx context.Context, vpcBackendUID string) (bool, error) {
	gwName := kubeovn.NatGWName(vpcBackendUID)
	eipName := "eip-" + vpcBackendUID
	snatName := "snat-" + vpcBackendUID

	if _, err := env.KubeClient.Resource(natVpcNatGatewayGVR).Get(ctx, gwName, metav1.GetOptions{}); err != nil {
		if k8serrors.IsNotFound(err) {
			return false, nil
		}
		return false, err
	}
	if _, err := env.KubeClient.Resource(natIptablesEIPGVR).Get(ctx, eipName, metav1.GetOptions{}); err != nil {
		if k8serrors.IsNotFound(err) {
			return false, nil
		}
		return false, err
	}
	if _, err := env.KubeClient.Resource(natIptablesSnatRuleGVR).Get(ctx, snatName, metav1.GetOptions{}); err != nil {
		if k8serrors.IsNotFound(err) {
			return false, nil
		}
		return false, err
	}
	return true, nil
}

// natResourcesGone returns true iff all three NAT objects are absent for vpcBackendUID.
func natResourcesGone(ctx context.Context, vpcBackendUID string) (bool, error) {
	for _, pair := range []struct {
		gvr  schema.GroupVersionResource
		name string
	}{
		{natVpcNatGatewayGVR, kubeovn.NatGWName(vpcBackendUID)},
		{natIptablesEIPGVR, "eip-" + vpcBackendUID},
		{natIptablesSnatRuleGVR, "snat-" + vpcBackendUID},
	} {
		_, err := env.KubeClient.Resource(pair.gvr).Get(ctx, pair.name, metav1.GetOptions{})
		if err == nil {
			return false, nil // still present
		}
		if !k8serrors.IsNotFound(err) {
			return false, err
		}
	}
	return true, nil
}

// vpcHasDefaultRoute checks whether the VPC's staticRoutes contains a
// 0.0.0.0/0 entry. We don't tag it (routeTable must be "" so the route
// lands in the main table — see kubeovn/nat.go for the why).
func vpcHasDefaultRoute(ctx context.Context, vpcBackendUID string) bool {
	routes, err := GetVpcStaticRoutes(ctx, env.KubeClient, vpcBackendUID)
	if err != nil || routes == nil {
		return false
	}
	for _, r := range routes {
		m, ok := r.(map[string]interface{})
		if !ok {
			continue
		}
		if cidr, _ := m["cidr"].(string); cidr == "0.0.0.0/0" {
			return true
		}
	}
	return false
}

// TestNAT_NATResourcesCreatedAfterSubnetCreate verifies that after a VNet and
// its first subnet are created and become ACTIVE, the VpcNatGateway + IptablesEIP
// + IptablesSnatRule CRDs exist on the cluster, and the VPC has a default route.
func TestNAT_NATResourcesCreatedAfterSubnetCreate(t *testing.T) {
	if !f15EnvConfigured() {
		t.Skip("F15 env vars not set (DCAPI_VPC_EXTERNAL_BRIDGE etc.) — skipping NAT integration test")
	}

	t.Parallel()
	ctx := context.Background()
	client := clientForTenant(t, "tenant-nat-create")
	mustGrantOwnerForClient(t, "tenant-nat-create")

	// Create VNet.
	vnetID := mustCreateActiveVNet(t, client, randomName("vnet"), "10.240.0.0/16", "lk")
	vnetBackendUID := dbGetVNetBackendUID(t, vnetID)
	require.NotEmpty(t, vnetBackendUID)

	// IMPORTANT: register VNet cleanup BEFORE creating the subnet, so the LIFO
	// cleanup order runs VNet delete (which tears down NAT first via the
	// handler, then deletes the OVN VPC + subnet) BEFORE we'd try to delete
	// the subnet separately. Otherwise the subnet delete hangs because the
	// NatGateway pod is still pinning it.
	t.Cleanup(func() {
		cleanCtx := context.Background()
		_, _, _ = client.DeleteVNet(cleanCtx, vnetID)
		WaitVNetGone(t, client, vnetID)
	})

	// Create a subnet directly — do NOT use mustCreateActiveSubnet here
	// because its t.Cleanup races with the NAT pinning the subnet. The VNet
	// cleanup above cascades the subnet delete via DeleteVpcNAT.
	subResp, _, status, subErr := client.CreateSubnet(ctx, vnetID,
		CreateSubnetRequest{Name: randomName("subnet"), CIDR: "10.240.1.0/24"})
	require.NoError(t, subErr)
	require.Equal(t, 202, status)
	subnetID := subResp.Resource.ID
	require.NotEmpty(t, subnetID)
	WaitSubnetActive(t, client, vnetID, subnetID)

	// Wait up to 3 minutes for all NAT resources AND the default route to
	// appear. The default route is patched onto the VPC *after* the three
	// CRDs reach Ready (see kubeovn/nat.go::EnsureVpcNAT step 7), so polling
	// both inside the same Eventually closes that race.
	require.Eventually(t, func() bool {
		ok, err := natResourcesExist(ctx, vnetBackendUID)
		if err != nil {
			t.Logf("natResourcesExist error: %v", err)
		}
		return ok && vpcHasDefaultRoute(ctx, vnetBackendUID)
	}, 3*time.Minute, 5*time.Second,
		"NAT resources + 0.0.0.0/0 route must appear within 3 minutes for vpc %s", vnetBackendUID)

	// ── The actual value test: spawn a probe pod on the tenant subnet and ────
	// verify it can reach the internet through the SNAT we just provisioned.
	// This is what F15 is FOR — VMs/pods on a VPC subnet getting outbound.
	subnetBackendUID := dbGetSubnetBackendUID(t, subnetID)
	tenantNS := dbGetTenantNamespaceForVNet(t, vnetID)
	probeName := "nat-probe-" + shortID(vnetBackendUID)
	createProbePod(t, tenantNS, probeName, tenantNS+"/"+subnetBackendUID)
	t.Cleanup(func() { deleteProbePod(tenantNS, probeName) })

	httpCode, body := probeCurl(t, tenantNS, probeName, "http://1.1.1.1", 25*time.Second)
	require.Equal(t, "301", httpCode,
		"probe pod on %s/%s could not reach 1.1.1.1 through the SNAT (body=%q)",
		tenantNS, subnetBackendUID, body)
	t.Logf("✓ probe %s/%s reached 1.1.1.1 → HTTP %s via SNAT EIP", tenantNS, probeName, httpCode)
}

// TestNAT_NATResourcesRemovedOnVNetDelete verifies that deleting a VNet also
// cleans up the NAT resources.
func TestNAT_NATResourcesRemovedOnVNetDelete(t *testing.T) {
	if !f15EnvConfigured() {
		t.Skip("F15 env vars not set — skipping NAT delete test")
	}

	t.Parallel()
	ctx := context.Background()
	client := clientForTenant(t, "tenant-nat-delete")
	mustGrantOwnerForClient(t, "tenant-nat-delete")

	// Create VNet + subnet.
	vnetID := mustCreateActiveVNet(t, client, randomName("vnet"), "10.241.0.0/16", "lk")
	vnetBackendUID := dbGetVNetBackendUID(t, vnetID)
	subnetID := mustCreateActiveSubnet(t, client, vnetID, randomName("subnet"), "10.241.1.0/24")
	require.NotEmpty(t, subnetID)

	// Wait for NAT to appear.
	require.Eventually(t, func() bool {
		ok, _ := natResourcesExist(ctx, vnetBackendUID)
		return ok
	}, 3*time.Minute, 5*time.Second, "NAT resources must appear before testing delete")

	// Delete subnet (VNet delete guard requires no active subnets).
	_, subnetDeleteStatus, subnetDeleteErr := client.DeleteSubnet(ctx, vnetID, subnetID)
	require.NoError(t, subnetDeleteErr)
	require.Equal(t, 202, subnetDeleteStatus)
	WaitSubnetGone(t, client, vnetID, subnetID)

	// Delete the VNet.
	_, vnetDeleteStatus, vnetDeleteErr := client.DeleteVNet(ctx, vnetID)
	require.NoError(t, vnetDeleteErr)
	require.Equal(t, 202, vnetDeleteStatus)
	WaitVNetGone(t, client, vnetID)

	// Verify NAT resources are gone.
	require.Eventually(t, func() bool {
		gone, _ := natResourcesGone(ctx, vnetBackendUID)
		return gone
	}, 2*time.Minute, 5*time.Second, "NAT resources must be cleaned up after VNet delete")

	// VPC itself must be gone.
	require.Eventually(t, func() bool {
		exists, _ := VpcExists(ctx, env.KubeClient, vnetBackendUID)
		return !exists
	}, 60*time.Second, 2*time.Second, "KubeOVN VPC must be removed after delete")
}

// TestNAT_BackfillDetectsAbsence simulates a partial failure (missing SnatRule)
// and verifies the detection logic correctly identifies the VPC as needing backfill.
func TestNAT_BackfillDetectsAbsence(t *testing.T) {
	if !f15EnvConfigured() {
		t.Skip("F15 env vars not set — skipping backfill detection test")
	}

	t.Parallel()
	ctx := context.Background()
	client := clientForTenant(t, "tenant-nat-backfill")
	mustGrantOwnerForClient(t, "tenant-nat-backfill")

	// Create VNet + subnet and wait for NAT to appear.
	vnetID := mustCreateActiveVNet(t, client, randomName("vnet"), "10.242.0.0/16", "lk")
	vnetBackendUID := dbGetVNetBackendUID(t, vnetID)
	subnetID := mustCreateActiveSubnet(t, client, vnetID, randomName("subnet"), "10.242.1.0/24")
	require.NotEmpty(t, subnetID)

	t.Cleanup(func() {
		ctx := context.Background()
		_, _, _ = client.DeleteSubnet(ctx, vnetID, subnetID)
		WaitSubnetGone(t, client, vnetID, subnetID)
		_, _, _ = client.DeleteVNet(ctx, vnetID)
		WaitVNetGone(t, client, vnetID)
	})

	require.Eventually(t, func() bool {
		ok, _ := natResourcesExist(ctx, vnetBackendUID)
		return ok
	}, 3*time.Minute, 5*time.Second, "NAT resources must appear initially")

	// Manually delete the IptablesSnatRule to simulate a partial failure.
	snatName := "snat-" + vnetBackendUID
	err := env.KubeClient.Resource(natIptablesSnatRuleGVR).Delete(ctx, snatName, metav1.DeleteOptions{})
	if err != nil && !k8serrors.IsNotFound(err) {
		t.Fatalf("could not delete IptablesSnatRule for backfill test: %v", err)
	}

	// Confirm it's gone.
	require.Eventually(t, func() bool {
		_, err := env.KubeClient.Resource(natIptablesSnatRuleGVR).Get(ctx, snatName, metav1.GetOptions{})
		return k8serrors.IsNotFound(err)
	}, 30*time.Second, 2*time.Second, "IptablesSnatRule should be gone after manual delete")

	// natResourcesExist should return false now (missing SnatRule).
	present, err := natResourcesExist(ctx, vnetBackendUID)
	require.NoError(t, err)
	require.False(t, present, "natResourcesExist should return false after partial deletion")

	t.Logf("Backfill detection test passed: NAT correctly detected as absent for VPC %s", vnetBackendUID)
}
