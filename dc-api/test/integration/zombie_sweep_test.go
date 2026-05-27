//go:build integration

package integration

import (
	"context"
	"os"
	"testing"
)

// TestZombieSweep is an operator-runnable cleanup. It is NOT a test in
// the usual sense — it walks every kubeovn Vpc tagged `dc-api/managed=true`
// whose `dc-api/tenant` label matches a test prefix and force-deletes it.
//
// Guarded by env var so it never runs accidentally as part of a normal
// `go test -tags integration ./...` invocation. Run it explicitly when
// the cluster has accumulated test debris:
//
//	KUBECONFIG=$HOME/.kube/config KUBE_CONTEXT=harvester-dev \
//	DCAPI_ZOMBIE_SWEEP=1 \
//	go test -tags integration -timeout 5m \
//	    -run TestZombieSweep ./test/integration/...
//
// Override prefixes via DCAPI_ZOMBIE_SWEEP_PREFIXES (comma-separated)
// when test names diverge. The defaults match every prefix the existing
// integration suite uses today.
func TestZombieSweep(t *testing.T) {
	if os.Getenv("DCAPI_ZOMBIE_SWEEP") != "1" {
		t.Skip("set DCAPI_ZOMBIE_SWEEP=1 to run the cluster sweep")
	}

	prefixes := []string{
		"test-",
		"test-tenant-",
		"vnet-test-tenant-",
		"vnet-admin-test-",
	}
	if env := os.Getenv("DCAPI_ZOMBIE_SWEEP_PREFIXES"); env != "" {
		prefixes = splitCSV(env)
	}

	ctx := context.Background()
	SweepTestTenantArtifacts(noopHelper{LogfFn: t.Logf}, ctx, env.KubeClient, prefixes)
}

func splitCSV(s string) []string {
	var out []string
	cur := ""
	for _, c := range s {
		if c == ',' {
			if cur != "" {
				out = append(out, cur)
			}
			cur = ""
			continue
		}
		cur += string(c)
	}
	if cur != "" {
		out = append(out, cur)
	}
	return out
}
