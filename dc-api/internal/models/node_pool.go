// Package models — node_pool.go
//
// AKS-style node pool domain types. A Cluster has exactly one system pool
// (name="system", role=system, count ∈ {1,3,5}) and zero or more worker
// pools (role=worker, user-supplied name).
//
// The Rancher/Harvester driver translates each NodePool to a machinePool
// entry in the provisioning.cattle.io/Cluster CR plus a separate
// HarvesterConfig CR. That translation lives in the provider layer (R4);
// this file is pure domain — no HTTP or DB tags beyond the json struct tags
// used by the repository layer to marshal JSONB columns.
package models

import (
	"time"

	"github.com/google/uuid"
)

// NodePoolRole is the compute role of the pool within the Kubernetes cluster.
// Users never configure role flags directly — they choose between "system"
// (control-plane + etcd) and "worker".
type NodePoolRole string

const (
	// NodePoolRoleSystem is the single control-plane-plus-etcd pool.
	// Count must be 1, 3, or 5. Name is hardcoded to "system".
	NodePoolRoleSystem NodePoolRole = "system"

	// NodePoolRoleWorker is an additional worker-only pool.
	// Count is 1..50. Name is user-supplied.
	NodePoolRoleWorker NodePoolRole = "worker"
)

// NodePoolStatus is the dc-api-side lifecycle for a node pool.
// The reconciler derives this from Rancher's machinePool conditions.
type NodePoolStatus string

const (
	NodePoolStatusProvisioning NodePoolStatus = "provisioning"
	NodePoolStatusReady        NodePoolStatus = "ready"
	NodePoolStatusScaling      NodePoolStatus = "scaling"
	NodePoolStatusDeleting     NodePoolStatus = "deleting"
	NodePoolStatusFailed       NodePoolStatus = "failed"
)

// NodePoolTaint is a single Kubernetes taint applied to every node in the pool.
// Effect must be one of: NoSchedule | PreferNoSchedule | NoExecute.
type NodePoolTaint struct {
	Key    string `json:"key"`
	Value  string `json:"value,omitempty"`
	Effect string `json:"effect"`
}

// NodePool is the persisted representation of a single machine pool within a
// Cluster. It mirrors the cluster_node_pools table row-for-row.
type NodePool struct {
	// ID is the auto-generated primary key (internal use only).
	ID uuid.UUID `json:"-"`
	// ClusterID references the parent resources.id.
	ClusterID uuid.UUID `json:"-"`

	// Name identifies the pool within a cluster. "system" for the system pool;
	// user-supplied DNS-label for worker pools (≤40 chars).
	Name string `json:"name"`
	// Role is hardcoded server-side; callers cannot override it.
	Role NodePoolRole `json:"role"`
	// Size mirrors models.Sizes keys ("small" | "medium" | "large" | "xlarge").
	Size string `json:"size"`
	// Count is the desired replica count. System pool: {1,3,5}. Worker: 1..50.
	Count int `json:"count"`
	// DiskGB overrides the size's default root disk. Nil means use the catalog default.
	DiskGB *int `json:"disk_gb,omitempty"`

	// Taints is the set of Kubernetes taints applied to every node. Empty slice
	// is stored as '[]'::jsonb and returned as nil/empty in responses.
	Taints []NodePoolTaint `json:"taints,omitempty"`
	// Labels is the set of Kubernetes node labels. Empty map stored as '{}'::jsonb.
	Labels map[string]string `json:"labels,omitempty"`

	// HarvesterConfigName is the Rancher HarvesterConfig CR name for this pool
	// (nc-<cluster>-<pool>-<rand> in fleet-default namespace). Stored so the
	// provider delete path knows which CR to cascade-clean. Not exposed in responses.
	HarvesterConfigName string `json:"-"`

	// Status is the dc-api-side lifecycle state. Derived by the reconciler from
	// Rancher machinePool conditions.
	Status NodePoolStatus `json:"status"`
	// Message is the last reconciler observation (error text or progress note).
	Message string `json:"message,omitempty"`

	CreatedAt time.Time `json:"created_at"`
	// UpdatedAt is set by the touch_updated_at trigger. Not exposed in responses.
	UpdatedAt time.Time `json:"-"`
}
