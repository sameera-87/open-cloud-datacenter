package v1alpha1

import (
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// KeyVaultInstanceSpec defines the desired state of a user-facing Key Vault.
//
// A KeyVaultInstance is a KV-v2 mount path inside a shared per-tenant
// KeyVaultBackend plus an AppRole scoped to that mount. The user never sees
// the Backend; they see independent KeyVaultInstance Resources at the
// project URL.
type KeyVaultInstanceSpec struct {
	// BackendRef points at the KeyVaultBackend in dc-tenant-<slug>.
	// dc-api sets this; users never specify it.
	// +required
	BackendRef BackendReference `json:"backendRef"`

	// SoftDeleteDays is honoured by OpenBao's KV-v2 metadata
	// delete_version_after configuration. Default 30, min 7, max 90.
	// +kubebuilder:validation:Minimum=7
	// +kubebuilder:validation:Maximum=90
	// +kubebuilder:default=30
	// +optional
	SoftDeleteDays int `json:"softDeleteDays,omitempty"`
}

// BackendReference points at a KeyVaultBackend CR.
type BackendReference struct {
	// Name of the KeyVaultBackend CR.
	// +required
	Name string `json:"name"`

	// Namespace of the KeyVaultBackend CR (typically dc-tenant-<slug>).
	// +required
	Namespace string `json:"namespace"`
}

// KeyVaultInstanceStatus defines the observed state of a Key Vault.
type KeyVaultInstanceStatus struct {
	// Phase mirrors the managed-services contract enum.
	// +kubebuilder:validation:Enum=Pending;Provisioning;Ready;Failed;Terminating
	// +optional
	Phase string `json:"phase,omitempty"`

	// Conditions follow standard Kubernetes status conventions.
	// +listType=map
	// +listMapKey=type
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`

	// Endpoint.Address is the Backend's in-cluster address (consumed by dc-api's
	// PrivateEndpoint handler to configure proxy upstreams).
	// Endpoint.SecretRef points at the AppRole credential Secret in the same
	// namespace as this CR.
	// +optional
	Endpoint *KeyVaultInstanceEndpoint `json:"endpoint,omitempty"`

	// MountPath inside the Backend (e.g. tenants/<tenant-uuid>/<resource-uuid>).
	// Set once on first successful reconcile; immutable thereafter.
	// +optional
	MountPath string `json:"mountPath,omitempty"`

	// Resources tracks owned objects (the credentials Secret) for idempotency.
	// +optional
	Resources []ResourceRef `json:"resources,omitempty"`

	// Message is the human-readable explanation of the current phase.
	// +optional
	Message string `json:"message,omitempty"`
}

// KeyVaultInstanceEndpoint describes how to reach this Key Vault.
type KeyVaultInstanceEndpoint struct {
	// Address is the Backend's in-cluster address (NOT a customer-VPC IP).
	Address string `json:"address"`
	// Port is the OpenBao API port.
	Port int `json:"port"`
	// SecretRef points at the credentials Secret in the KeyVaultInstance's namespace.
	// Data keys: role_id, secret_id, mount_path, backend_address, backend_port.
	SecretRef *corev1.LocalObjectReference `json:"secretRef,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:shortName=kvi
// +kubebuilder:printcolumn:name="Phase",type="string",JSONPath=".status.phase"
// +kubebuilder:printcolumn:name="Backend",type="string",JSONPath=".spec.backendRef.name"
// +kubebuilder:printcolumn:name="MountPath",type="string",JSONPath=".status.mountPath"
// +kubebuilder:printcolumn:name="Age",type="date",JSONPath=".metadata.creationTimestamp"

// KeyVaultInstance is a user-facing key vault — a KV-v2 mount + AppRole
// inside a Backend.
type KeyVaultInstance struct {
	metav1.TypeMeta `json:",inline"`
	// +optional
	metav1.ObjectMeta `json:"metadata,omitzero"`

	// +required
	Spec KeyVaultInstanceSpec `json:"spec"`

	// +optional
	Status KeyVaultInstanceStatus `json:"status,omitzero"`
}

// +kubebuilder:object:root=true

// KeyVaultInstanceList contains a list of KeyVaultInstance.
type KeyVaultInstanceList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitzero"`
	Items           []KeyVaultInstance `json:"items"`
}

func init() {
	SchemeBuilder.Register(&KeyVaultInstance{}, &KeyVaultInstanceList{})
}
