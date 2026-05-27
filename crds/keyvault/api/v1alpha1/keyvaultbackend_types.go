// Package v1alpha1 contains API Schema definitions for the keyvault v1alpha1 API group.
//
// KeyVaultBackend represents a per-tenant OpenBao HA cluster — the long-lived
// "backend" that hosts many user-facing KeyVault mounts. One Backend per tenant;
// lives in dc-tenant-<slug>. Lifecycle managed by KVI controller per
// docs/kvi-controller-design.md.
package v1alpha1

import (
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// KeyVaultBackendSpec defines the desired state of a per-tenant OpenBao cluster.
//
// Capacity fields are intentionally optional with controller-sane defaults:
// the user-facing KeyVault API has no cap surface — Backend capacity is a
// platform-operator concern, not a tenant one. dc-api creates Backends with
// empty caps; admission fills the defaults here.
type KeyVaultBackendSpec struct {
	// CPU is the total CPU budget for the StatefulSet (divided across replicas).
	// +kubebuilder:default="500m"
	// +optional
	CPU resource.Quantity `json:"cpu,omitempty"`

	// MemoryGB is the total memory budget for the StatefulSet (divided across replicas).
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:default=4
	// +optional
	MemoryGB int `json:"memoryGB,omitempty"`

	// StorageGB is the size of each replica's Raft PVC.
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:default=10
	// +optional
	StorageGB int `json:"storageGB,omitempty"`

	// EngineConfig holds OpenBao-specific tuning.
	// +optional
	EngineConfig BackendEngineConfig `json:"engineConfig,omitempty"`
}

// BackendEngineConfig holds OpenBao-specific tuning for a Backend.
type BackendEngineConfig struct {
	// HAReplicas is the number of pods in the Raft cluster. Must be odd. Default 3.
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:validation:Maximum=7
	// +kubebuilder:default=3
	// +optional
	HAReplicas int `json:"haReplicas,omitempty"`

	// StorageClass is the Longhorn (or other) class for Raft PVCs. Default "longhorn".
	// +kubebuilder:default=longhorn
	// +optional
	StorageClass string `json:"storageClass,omitempty"`

	// AuditEnabled toggles the file audit device. Default true.
	// +kubebuilder:default=true
	// +optional
	AuditEnabled *bool `json:"auditEnabled,omitempty"`

	// AuditLogPath is the file path inside each pod where audit writes.
	// +kubebuilder:default=/openbao/audit/audit.log
	// +optional
	AuditLogPath string `json:"auditLogPath,omitempty"`
}

// KeyVaultBackendStatus defines the observed state of a Backend.
type KeyVaultBackendStatus struct {
	// Phase mirrors the managed-services contract enum.
	// +kubebuilder:validation:Enum=Pending;Provisioning;Ready;Failed;Terminating
	// +optional
	Phase string `json:"phase,omitempty"`

	// Conditions follow standard Kubernetes status conventions.
	// +listType=map
	// +listMapKey=type
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`

	// Endpoint is the in-cluster address of the active leader.
	// Populated once the Raft cluster elects a leader.
	// +optional
	Endpoint *BackendEndpoint `json:"endpoint,omitempty"`

	// KeyMaterialRef points at the Secret holding unseal keys + root token.
	// Set once after init; subsequent reconciles never rewrite this.
	// +optional
	KeyMaterialRef *corev1.LocalObjectReference `json:"keyMaterialRef,omitempty"`

	// Resources tracks every owned object for idempotent reconciliation.
	// +optional
	Resources []ResourceRef `json:"resources,omitempty"`

	// Message is the human-readable explanation of the current phase.
	// +optional
	Message string `json:"message,omitempty"`
}

// BackendEndpoint is the in-cluster address of the active Raft leader.
type BackendEndpoint struct {
	// Address is the DNS name (e.g. openbao-active.dc-tenant-acme.svc.cluster.local).
	Address string `json:"address"`
	// Port is the OpenBao API port (8200 by default).
	Port int `json:"port"`
}

// ResourceRef identifies a Kubernetes object owned by this CR (for idempotency).
type ResourceRef struct {
	APIVersion string `json:"apiVersion"`
	Kind       string `json:"kind"`
	Namespace  string `json:"namespace,omitempty"`
	Name       string `json:"name"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:shortName=kvb
// +kubebuilder:printcolumn:name="Phase",type="string",JSONPath=".status.phase"
// +kubebuilder:printcolumn:name="Endpoint",type="string",JSONPath=".status.endpoint.address"
// +kubebuilder:printcolumn:name="Age",type="date",JSONPath=".metadata.creationTimestamp"

// KeyVaultBackend is a per-tenant OpenBao HA cluster.
type KeyVaultBackend struct {
	metav1.TypeMeta `json:",inline"`
	// +optional
	metav1.ObjectMeta `json:"metadata,omitzero"`

	// +required
	Spec KeyVaultBackendSpec `json:"spec"`

	// +optional
	Status KeyVaultBackendStatus `json:"status,omitzero"`
}

// +kubebuilder:object:root=true

// KeyVaultBackendList contains a list of KeyVaultBackend.
type KeyVaultBackendList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitzero"`
	Items           []KeyVaultBackend `json:"items"`
}

func init() {
	SchemeBuilder.Register(&KeyVaultBackend{}, &KeyVaultBackendList{})
}
