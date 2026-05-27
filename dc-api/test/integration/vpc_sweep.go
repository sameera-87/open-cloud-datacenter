//go:build integration

package integration

import (
	"context"
	"strings"
	"time"

	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/dynamic"
)

// logger is the subset of *testing.T (and any structured logger) that the
// sweep helpers actually use. Lets the cleanup utility under cmd/cleanup
// call into this file without dragging the testing package into a main binary.
type logger interface {
	Helper()
	Logf(format string, args ...interface{})
}

// noopHelper is a small adapter that satisfies the .Helper() requirement
// for non-test callers. The cleanup utility wraps a stdlib *log.Logger
// in this so it can use SweepKubeOVNVPC / SweepTestTenantArtifacts.
type noopHelper struct {
	LogfFn func(format string, args ...interface{})
}

func (noopHelper) Helper()                                          {}
func (n noopHelper) Logf(format string, args ...interface{})        { n.LogfFn(format, args...) }


// SweepKubeOVNVPC removes a kubeovn Vpc plus every Subnet that references
// it, force-removing finalizers if anything stalls.
//
// Intended as a `t.Cleanup` callback alongside the API DELETE: if the
// API path succeeded the sweep is a no-op (everything is already gone);
// if the API path timed out (>3min `WaitVNetGone`) the sweep clears the
// kubeovn side so the next test run doesn't inherit zombie state.
//
// Pre-F24 a long test session would leave dozens of orphaned VPCs on
// harvester-dev — enough to overload `kube-ovn-controller` reconciliation
// and make brand-new test runs fail with VNet-create timeouts. See
// FOLLOWUPS.md F24 for the original incident write-up.
//
// All steps are idempotent and best-effort — not-found is fine, other
// errors are logged via `t.Logf` but never fail the test (sweep failures
// shouldn't mask the test's actual outcome).
func SweepKubeOVNVPC(t logger, ctx context.Context, client dynamic.Interface, vpcName string) {
	t.Helper()
	if vpcName == "" {
		return
	}

	// 1. Subnets first — kubeovn's Vpc delete is blocked by a finalizer
	//    while any Subnet references it. Find each subnet, then delete.
	subnetList, err := client.Resource(kubeovnSubnetGVR).List(ctx, metav1.ListOptions{})
	if err != nil && !k8serrors.IsNotFound(err) {
		t.Logf("sweep: list subnets: %v", err)
	}
	if subnetList != nil {
		for _, s := range subnetList.Items {
			spec, _ := s.Object["spec"].(map[string]interface{})
			if spec == nil {
				continue
			}
			vpc, _ := spec["vpc"].(string)
			if vpc != vpcName {
				continue
			}
			name := s.GetName()
			deleteAndForceRemoveFinalizers(t, ctx, client, kubeovnSubnetGVR, "", name)
		}
	}

	// 2. The Vpc itself. By now its child Subnets are gone so the
	//    kubeovn finalizer should release. Force-remove if it doesn't.
	deleteAndForceRemoveFinalizers(t, ctx, client, kubeovnVpcGVR, "", vpcName)
}

// deleteAndForceRemoveFinalizers issues a delete on a kubeovn object and,
// if the object still exists after a brief poll, patches `metadata.finalizers`
// to `[]` so K8s can reap it. Returns once the object is gone or after the
// budget is exhausted.
func deleteAndForceRemoveFinalizers(t logger, ctx context.Context, client dynamic.Interface, gvr schema.GroupVersionResource, _ /*namespace*/, name string) {
	t.Helper()

	res := client.Resource(gvr)
	if err := res.Delete(ctx, name, metav1.DeleteOptions{}); err != nil && !k8serrors.IsNotFound(err) {
		t.Logf("sweep: delete %s/%s: %v", gvr.Resource, name, err)
	}

	// Poll briefly. Most well-behaved kubeovn objects clear in under 5s.
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		_, err := res.Get(ctx, name, metav1.GetOptions{})
		if k8serrors.IsNotFound(err) {
			return
		}
		time.Sleep(500 * time.Millisecond)
	}

	// Still around — force-remove finalizers. This is the same safety
	// pattern F16/F43 use in the production driver: we already issued
	// the intentional delete, and ownership-by-label is implicit because
	// these test runs only sweep VPCs they created themselves.
	patch := []byte(`{"metadata":{"finalizers":[]}}`)
	if _, err := res.Patch(ctx, name, types.MergePatchType, patch, metav1.PatchOptions{}); err != nil {
		t.Logf("sweep: force-clear finalizers on %s/%s: %v", gvr.Resource, name, err)
	}
}

// SweepTestTenantArtifacts removes any kubeovn Vpc whose dc-api/tenant
// label starts with one of the test prefixes ("test-tenant-", "test-",
// "vnet-test-tenant-", etc.) and any orphaned subnets that reference them.
//
// Use this as an emergency reset — typically not invoked by tests
// themselves, but a maintainer can call it from cmd/cleanup/main.go when
// a previous session left zombies behind.
func SweepTestTenantArtifacts(t logger, ctx context.Context, client dynamic.Interface, prefixes []string) {
	t.Helper()

	vpcList, err := client.Resource(kubeovnVpcGVR).List(ctx, metav1.ListOptions{
		LabelSelector: "dc-api/managed=true",
	})
	if err != nil {
		t.Logf("sweep-all: list vpcs: %v", err)
		return
	}
	for _, v := range vpcList.Items {
		tenant := v.GetLabels()["dc-api/tenant"]
		if !matchesAnyPrefix(tenant, prefixes) {
			continue
		}
		t.Logf("sweep-all: removing zombie VPC %q (tenant=%q)", v.GetName(), tenant)
		SweepKubeOVNVPC(t, ctx, client, v.GetName())
	}
}

func matchesAnyPrefix(s string, prefixes []string) bool {
	for _, p := range prefixes {
		if strings.HasPrefix(s, p) {
			return true
		}
	}
	return false
}
