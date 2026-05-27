// Dependency interfaces consumed by the rancher package.
//
// Defined here (in the consumer) rather than in the parent providers package
// so the rancher subpackage doesn't import providers — that would close a
// cycle with providers/factory.go which imports rancher.
//
// The sole producer of both interfaces is *harvester.Client. Go's structural
// typing means harvester doesn't need to import this file; it just satisfies
// the shape.
package rancher

import "context"

// CloudProviderSAEnsurer is the F32 interface implemented by the Harvester
// client. It is injected into the rancher ClusterProvisioner so the rancher
// package never imports the harvester package directly.
//
// A nil value is legal — when nil the orchestrator skips SA bootstrap and
// falls back to the legacy credential path (bridge-mode clusters, no VPC).
type CloudProviderSAEnsurer interface {
	// EnsureCloudProviderSA idempotently creates the ServiceAccount,
	// RoleBinding, and long-lived token Secret needed by the Harvester Cloud
	// Provider plugin. tenantNamespace is "dc-<tenantID>". Returns the raw
	// (not base64-encoded) SA token bytes, ready to embed in a kubeconfig.
	EnsureCloudProviderSA(ctx context.Context, tenantNamespace string) ([]byte, error)
}

// HarvesterAPIInfoProvider is the F32 interface that exposes the Harvester
// apiserver URL and CA certificate data. Injected into the rancher
// ClusterProvisioner so it can build per-cluster SA kubeconfigs without
// importing the harvester package.
type HarvesterAPIInfoProvider interface {
	// HarvesterServerURL returns the Harvester Kubernetes apiserver URL.
	HarvesterServerURL() string
	// HarvesterCACert returns the raw (not base64-encoded) CA certificate
	// bundle for the Harvester cluster, suitable for embedding as
	// certificate-authority-data in a kubeconfig.
	HarvesterCACert() []byte
}
