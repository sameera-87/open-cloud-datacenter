// Package providers — Factory function.
//
// ── DESIGN PATTERN: Factory Pattern ───────────────────────────────────────────
//
// The Factory Pattern is a creational pattern: it moves the "how to build this
// object" logic out of the caller and into a dedicated constructor (the factory).
//
// Without a factory, every place that needs a provider would have to know:
//   1. Which concrete type to use (Harvester? Rancher?)
//   2. How to configure it (kubeconfig path? API token?)
//   3. In what order to call constructors
//
// With a factory, the caller just says: "give me a ComputeProvider for this config"
// and the factory handles the rest. The caller is decoupled from the concrete type.
//
// Real-world analogy: You call a taxi app. You don't know if it dispatches a Toyota
// or a Honda. You just get a Car interface — it has a Ride() method.
package providers

import (
	"fmt"

	"github.com/wso2/dc-api/internal/config"
	"github.com/wso2/dc-api/internal/providers/harvester"
	"github.com/wso2/dc-api/internal/providers/kubeovn"
	"github.com/wso2/dc-api/internal/providers/rancher"
)

// NewComputeProvider instantiates the correct ComputeProvider based on cfg.VMProvider.
// The returned value satisfies the ComputeProvider interface.
//
// To add a new VM provider (e.g., OpenStack):
//  1. Create internal/providers/openstack/client.go
//  2. Implement the ComputeProvider interface on its struct
//  3. Add a "openstack" case here
//  4. Nothing else changes — handlers are unaffected.
func NewComputeProvider(cfg *config.Config) (ComputeProvider, error) {
	switch cfg.VMProvider {
	case "harvester":
		return harvester.NewClient(cfg.HarvesterKubeconfig, cfg.HarvesterNamespace)
	default:
		return nil, fmt.Errorf("unknown VM provider %q — supported: harvester", cfg.VMProvider)
	}
}

// NewClusterProvider instantiates the correct ClusterProvider based on cfg.ClusterProvider.
//
// The returned *rancher.Client has its Steve-based provisioner wired with nil
// SA ensurer and API info provider. Call (*rancher.Client).WithHarvesterProviders
// in main.go after both providers are ready to enable the VPC cluster path.
func NewClusterProvider(cfg *config.Config) (ClusterProvider, error) {
	switch cfg.ClusterProvider {
	case "rancher":
		return rancher.NewClient(
			cfg.RancherURL, cfg.RancherToken, cfg.RancherInsecure,
			cfg.RancherHarvesterCredential,
			cfg.ClusterMgmtNAD,
			cfg.ClusterVMNamespace,
			cfg.OperatorSSHKey, cfg.OperatorPassword,
		)
	default:
		return nil, fmt.Errorf("unknown cluster provider %q — supported: rancher", cfg.ClusterProvider)
	}
}

// NewNetworkProvider instantiates the correct NetworkProvider.
//
// The kubeovn driver reuses DCAPI_HARVESTER_KUBECONFIG because KubeOVN runs
// on the same Harvester cluster as the VMs.  A separate kubeconfig can be
// wired here in future if KubeOVN moves to a dedicated control plane.
//
// To add a new network provider (e.g., a future Harvester bundled KubeOVN
// operated via its own API path):
//  1. Create internal/providers/<name>/client.go
//  2. Implement the NetworkProvider interface on its struct
//  3. Add a case here
//  4. Nothing else changes — handlers depend only on the interface.
func NewNetworkProvider(cfg *config.Config) (NetworkProvider, error) {
	switch cfg.NetworkProvider {
	case "kubeovn":
		return kubeovn.New(cfg.HarvesterKubeconfig, cfg.KubeOVNNamespace)
	default:
		return nil, fmt.Errorf("unknown network provider %q — supported: kubeovn", cfg.NetworkProvider)
	}
}
