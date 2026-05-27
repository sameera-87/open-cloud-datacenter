//go:build integration

package integration

import (
	"testing"
)

// Pre-M2.5 these two tests inserted into the per-tenant `quotas` table and
// expected the VNet/Subnet create handlers to refuse when max_vnets /
// max_subnets_per_vnet was breached. After M2.5 the authoritative quota
// store moved to the per-project `project_quotas` table (max_vnets,
// max_clusters, max_volumes, max_public_ips). The `quotas` table is dead
// weight that we don't read from anymore.
//
// Wiring create-time enforcement of those project-quota object counts is
// pending — tracked under defense-in-depth issue #200 ("Phase 1 — cheap
// structural wins" → object-count guardrails). Capacity quotas
// (cpu_cores/memory_gb/storage_gb) ARE enforced today via the M2.5 cap
// path; see TestProjectCap_* in projects_cap_test.go.
//
// Skipping these tests rather than deleting them so the intent is
// recoverable when the project-quota enforcement lands.

func TestQuota_RejectVNetOverLimit(t *testing.T) {
	t.Skip("M2.5: per-tenant quotas.max_vnets is no longer authoritative; project_quotas.max_vnets enforcement pending under defense-in-depth #200")
}

func TestQuota_RejectSubnetOverLimit(t *testing.T) {
	t.Skip("M2.5: per-tenant quotas.max_subnets_per_vnet is no longer authoritative; project_quotas enforcement pending under defense-in-depth #200")
}
