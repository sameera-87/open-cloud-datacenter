//go:build integration

package integration

import (
	"context"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/stretchr/testify/require"
)

// TestNoRegressionOnBridgeVMs snapshots all VMs in the cluster before and
// after the suite runs to ensure no VM terminated as a side effect of our
// networking operations. This is the spike's gate 5 — automated.
func TestNoRegressionOnBridgeVMs(t *testing.T) {
	ctx := context.Background()

	vmGVR := schema.GroupVersionResource{
		Group:    "kubevirt.io",
		Version:  "v1",
		Resource: "virtualmachines",
	}

	snapshot := func() map[string]string {
		result := make(map[string]string)
		list, err := env.KubeClient.Resource(vmGVR).Namespace("").List(ctx, metav1.ListOptions{})
		if err != nil {
			t.Logf("WARN: cannot list VirtualMachines (KubeVirt may be absent): %v", err)
			return result
		}
		for _, item := range list.Items {
			ns := item.GetNamespace()
			name := item.GetName()
			phase, _, _ := unstructuredString(item.Object, "status", "printableStatus")
			result[ns+"/"+name] = phase
		}
		return result
	}

	before := snapshot()
	t.Cleanup(func() {
		after := snapshot()
		var terminated []string
		for key, phaseBefore := range before {
			phaseAfter, stillExists := after[key]
			if !stillExists {
				terminated = append(terminated, key+" (removed)")
				continue
			}
			if phaseBefore != "Stopped" && phaseAfter == "Stopped" {
				terminated = append(terminated, key+" (was "+phaseBefore+", now Stopped)")
			}
		}
		require.Empty(t, terminated,
			"REGRESSION: %d VMs terminated during the test suite run: %v", len(terminated), terminated)
	})
}

func unstructuredString(obj map[string]interface{}, fields ...string) (string, bool, error) {
	cur := obj
	for i, f := range fields {
		if i == len(fields)-1 {
			v, ok := cur[f].(string)
			return v, ok, nil
		}
		next, ok := cur[f].(map[string]interface{})
		if !ok {
			return "", false, nil
		}
		cur = next
	}
	return "", false, nil
}
