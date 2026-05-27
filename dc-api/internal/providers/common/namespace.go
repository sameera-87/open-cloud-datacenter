// Package common holds small helpers shared by multiple provider drivers.
//
// Placing shared utilities here avoids import cycles: harvester and kubeovn
// both need namespace helpers and labels, but neither can import the other
// (they are sibling packages). The common package has no dependency on any
// provider.
package common

import (
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
)

// NamespaceForProject derives the Kubernetes namespace name for a project.
//
// Convention: "dc-<tenantID>-<projectID>"
//   - tenant "choreo-sre", project "prod-infra" → "dc-choreo-sre-prod-infra"
//
// Kubernetes namespace naming rules (RFC 1123 DNS label):
//   - Lowercase alphanumeric and hyphens only
//   - Starts and ends with an alphanumeric character
//   - Maximum 63 characters
//
// The caller is responsible for ensuring tenantID and projectID satisfy these
// rules (the auth middleware enforces it when slugs are parsed).
func NamespaceForProject(tenantID, projectID string) string {
	return "dc-" + strings.ToLower(tenantID) + "-" + strings.ToLower(projectID)
}

// NamespaceForTenant derives the per-tenant Kubernetes namespace name. This
// is the home for tenant-tier managed-service Backends (keyvault HA cluster
// etc.) — see docs/managed-services-integration.md §3.
//
// Convention: "dc-tenant-<tenantID>"
//
// Caller-side rules same as NamespaceForProject — tenantID must already be
// RFC-1123 conformant; the auth middleware enforces this.
func NamespaceForTenant(tenantID string) string {
	return "dc-tenant-" + strings.ToLower(tenantID)
}

// UUIDSuffix returns the first 8 hex characters of a UUID (dashes stripped).
// Used to derive stable, short, derivable names for Kubernetes objects:
//   - UUID "5f3a8c1d-e891-4b2a-9c8d-..." → "5f3a8c1d"
//
// Always derivable from the resource UUID — no separate state needed.
func UUIDSuffix(id uuid.UUID) string {
	return strings.ReplaceAll(id.String(), "-", "")[:8]
}

// ClusterScopedName returns a cluster-scoped Kubernetes resource name using
// the pattern "<kind>-<tenantSlug>-<8-char-uuid>".
// Examples:
//
//	vpc-choreo-sre-5f3a8c1d
//	subnet-choreo-sre-5f3a8c1d
//
// Max length ~49 chars (well within the 63-char Kubernetes limit).
func ClusterScopedName(kind, tenantSlug string, id uuid.UUID) string {
	return fmt.Sprintf("%s-%s-%s", kind, strings.ToLower(tenantSlug), UUIDSuffix(id))
}

// NamespaceScopedName returns a namespace-scoped Kubernetes resource name
// using the pattern "<kind>-<8-char-uuid>".
// Examples:
//
//	nad-5f3a8c1d
//	nat-5f3a8c1d
func NamespaceScopedName(kind string, id uuid.UUID) string {
	return fmt.Sprintf("%s-%s", kind, UUIDSuffix(id))
}

// ─────────────────────────── HarvesterConfig naming ─────────────────────────

// HarvesterConfigName returns a deterministic HarvesterConfig CR name for a
// node pool. Convention: "nc-<clusterName>-<poolName>-<5-char-rand>".
//
// The 5-character suffix matches the naming convention used by the Rancher UI
// and the rancher/cluster.go shortRand() function. It is time-based, not
// crypto-random, since this is a human-readable name, not a security primitive.
//
// The handler pre-generates the name here and persists it to
// cluster_node_pools.harvester_config_name before calling the provider, so
// that the provider delete path knows which CR to cascade-clean.
func HarvesterConfigName(clusterName, poolName string) string {
	const alpha = "abcdefghijklmnopqrstuvwxyz0123456789"
	seed := time.Now().UnixNano()
	var b [5]byte
	for i := range b {
		b[i] = alpha[seed%int64(len(alpha))]
		seed /= int64(len(alpha))
	}
	return fmt.Sprintf("nc-%s-%s-%s", clusterName, poolName, string(b[:]))
}

// ─────────────────────────── Standardised labels ────────────────────────────

// StandardLabels returns the canonical dc-api.wso2.com/* label set that every
// Kubernetes or KubeOVN object dc-api creates must carry.
//
// Parameters:
//   - tenantSlug     human-readable tenant slug (from URL / tenants.id)
//   - projectSlug    human-readable project slug (from URL / projects.id)
//   - tenantUUID     immutable tenant UUID
//   - projectUUID    immutable project UUID
//   - resourceKind   one of: vnet | subnet | vm | cluster | nsg | peering |
//     dns-zone | keyvault | private-endpoint | bastion
//   - resourceName   user-typed friendly name
//   - resourceUUID   dc-api UUID of the resource row
func StandardLabels(
	tenantSlug, projectSlug string,
	tenantUUID, projectUUID, resourceUUID uuid.UUID,
	resourceKind, resourceName string,
) map[string]string {
	return map[string]string{
		"dc-api.wso2.com/tenant":        tenantSlug,
		"dc-api.wso2.com/project":       projectSlug,
		"dc-api.wso2.com/tenant-uuid":   tenantUUID.String(),
		"dc-api.wso2.com/project-uuid":  projectUUID.String(),
		"dc-api.wso2.com/resource-uuid": resourceUUID.String(),
		"dc-api.wso2.com/resource-kind": resourceKind,
		"dc-api.wso2.com/resource-name": resourceName,
	}
}

// StandardAnnotations returns the canonical dc-api.wso2.com/* annotation set.
//
// Parameters:
//   - createdBy  IdP sub (or SA token lookup ID) of the requesting principal
func StandardAnnotations(createdBy string) map[string]string {
	return map[string]string{
		"dc-api.wso2.com/created-by": createdBy,
		"dc-api.wso2.com/created-at": time.Now().UTC().Format(time.RFC3339),
	}
}
