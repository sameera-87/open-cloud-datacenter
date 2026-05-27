// Package rancher — cluster.go
//
// ClusterProvisioner provisions RKE2 clusters on Harvester via the Rancher
// Steve API (/v1/* endpoints). This is F32's primary implementation and
// supersedes the gap-prone kubectl-apply path documented in F31.
//
// WHY STEVE?
//
// The kubectl-apply path (provisioning.cattle.io CRs applied directly via the
// Kubernetes API) has a well-known gap: Rancher's v2prov reconciliation loop
// does NOT auto-create the "harvesterconfig-<cluster>" Secret in fleet-default
// that the Harvester Cloud Provider plugin needs to read. The Rancher UI avoids
// this gap by going through Steve, which performs the full server-side
// transaction (Secret creation, v3 management cluster mirror, etc.) in a single
// POST. We do the same.
//
// CLUSTER LAYOUT (dual-NIC for RKE2-on-VPC):
//
//	NIC 0 (management):  operator-configured bridge NAD (DCAPI_CLUSTER_MGMT_NAD)
//	                     used by host-netns processes: cloud-init, RKE2 installer,
//	                     apt-get. Provides outbound internet before F15 SNAT
//	                     is active on the OVN VPC for pods.
//	NIC 1 (cluster):     tenant subnet NAD (dc-<tenantID>/<subnet-backend-uid>).
//	                     OVN-backed; --node-ip pinned here via cloud-init bootcmd.
//	                     All intra-cluster traffic (etcd, apiserver, kubelet) uses
//	                     this IP. After F15 SNAT is active, pod egress also uses
//	                     this IP (OVN SNAT to EIP).
//
// NODE-IP CLOUD-INIT PATTERN:
//
//	The cloud-init bootcmd writes /etc/rancher/rke2/config.yaml.d/01-node-ip.yaml
//	early in the boot sequence, before the rke2-agent/server service starts.
//	The file contains: "node-ip: <OVN-NIC-MAC-derived-IP>" — however, the actual
//	OVN IP is dynamic. The standard approach used by the dcapi-controlplane-rke2
//	cluster is to have RKE2 auto-select the node IP from the OVN NIC by writing
//	the MAC-based interface name into the config. The F14 spike confirmed this
//	approach works.
//
// CLOUD CREDENTIAL:
//
//	A single Harvester cloud credential must be pre-created in Rancher UI:
//	  Cluster Management → Cloud Credentials → Create → Harvester
//	Its name is set via DCAPI_RANCHER_HARVESTER_CREDENTIAL. It is shared across
//	all clusters. The credential carries the Harvester kubeconfig that the
//	Harvester CCM uses to register nodes — its namespace context determines which
//	Harvester namespace CCM looks in for node VMs (see F31 context).
package rancher

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/rs/zerolog/log"
	"github.com/wso2/dc-api/internal/models"
)

const (
	// steveKindCluster is the Steve API kind for RKE2 provisioning clusters.
	steveKindCluster = "provisioning.cattle.io.clusters"
	// steveKindHarvesterConfig is the Steve API kind for Harvester machine configs.
	steveKindHarvesterConfig = "rke-machine-config.cattle.io.harvesterconfigs"
	// steveKindSecret is the Steve API kind for core Kubernetes Secrets.
	// Steve uses the bare plural ("secrets") for core/v1 resources, not the
	// kubectl-style "core.v1.secrets" — that produces HTTP 404 against /v1/.
	steveKindSecret = "secrets"
)

// ClusterProvisioner orchestrates RKE2 cluster creation on Harvester via
// the Rancher Steve API. It holds a SteveClient plus the configuration
// that must match across HarvesterConfig and the Cluster CR.
//
// Instantiate via NewClusterProvisioner.
type ClusterProvisioner struct {
	steve            *SteveClient
	cloudCredential  string // DCAPI_RANCHER_HARVESTER_CREDENTIAL (secret name in fleet-default)
	mgmtNAD          string // operator NAD for management outbound, e.g. "iaas/vm-network-001"
	vmNamespace      string // Harvester namespace where cluster node VMs will live, e.g. "dc-<tenant>"
	operatorSSHKey   string // IaaS break-glass public key (injected into every node via cloud-init)
	operatorPassword string // IaaS break-glass console password
	saEnsurer        CloudProviderSAEnsurer    // nil → skip SA bootstrap (legacy bridge path)
	apiInfoProvider  HarvesterAPIInfoProvider  // nil → skip harvesterconfig Secret creation
}

// ClusterCreateSpec is the input to ClusterProvisioner.CreateCluster.
// It is populated by the handler after resolving UUIDs to backend CRD names.
type ClusterCreateSpec struct {
	// ClusterName is the Rancher cluster name — must be DNS-safe, max 63 chars.
	ClusterName string

	// K8sVersion is the full RKE2 Kubernetes version string, e.g. "v1.29.4+rke2r1".
	K8sVersion string

	// MgmtNAD is the operator NAD for the management (outbound) NIC.
	// Format: "namespace/name", e.g. "iaas/vm-network-001".
	// If empty, falls back to the provisioner's default mgmtNAD.
	MgmtNAD string

	// TenantSubnetNAD is the tenant subnet NAD for the cluster NIC.
	// Format: "namespace/name", e.g. "dc-hiran/subnet-hiran-vm-sn".
	// The Rancher webhook (F14) will inject the OVN MAC-pinning annotations.
	TenantSubnetNAD string

	// NodeImage is the VM image name in Harvester, e.g. "default/ubuntu-22.04".
	// Format: "namespace/image-resource-name" as returned by ListImages.
	NodeImage string

	// VMNamespace overrides the provisioner-level default vmNamespace.
	// Set by the handler to "dc-<tenantID>-<projectID>". Passed into HarvesterConfig.vmNamespace.
	VMNamespace string

	// SystemPool is the control-plane + etcd pool for this cluster. The handler
	// must populate SystemPool.HarvesterConfigName before calling CreateCluster;
	// the provisioner uses that name verbatim when POSTing the HarvesterConfig CR.
	// CPU/MemoryGB/DiskGB are resolved from SystemPool.Size by the handler.
	SystemPool *models.NodePool

	// WorkerPools are optional worker pools to provision alongside the cluster.
	// Each entry must have HarvesterConfigName pre-populated by the handler.
	// The provisioner creates one HarvesterConfig CR per entry before POSTing the
	// Cluster CR with all machinePools (system + workers) in a single request.
	// May be nil or empty for a system-pool-only cluster.
	WorkerPools []*models.NodePool

	// NodeCPU, NodeMemoryGB, NodeDiskGB hold the resolved compute values for the
	// system pool. Set by the handler from models.Sizes[SystemPool.Size].
	NodeCPU      int
	NodeMemoryGB int
	NodeDiskGB   int
}

// ClusterStatus is the parsed status returned by GetClusterStatus.
type ClusterStatus struct {
	// Ready is true when the Rancher provisioning controller has set Ready=True.
	Ready bool

	// Provisioned is true when RKE2 nodes are all joined and the control plane is healthy.
	Provisioned bool

	// Message is the most recent status message from the conditions.
	Message string

	// RancherUID is the management cluster ID (c-m-xxxxx) from status.clusterName.
	// Populated only once the management cluster has been created (a few seconds
	// after the first node registers).
	RancherUID string
}

// NewClusterProvisioner creates a ClusterProvisioner.
//
// steve:            an initialised SteveClient (shares the same base URL + token as *Client).
// cloudCredential:  name of the pre-existing Harvester cloud credential in Rancher.
// mgmtNAD:          operator NAD for the management NIC, e.g. "iaas/vm-network-001".
// vmNamespace:      default Harvester namespace for node VMs (overridable per-cluster via spec).
// operatorSSHKey:   IaaS break-glass SSH public key; empty = not injected.
// operatorPassword: IaaS break-glass console password; empty = not injected.
// saEnsurer:        optional; when non-nil, called on VPC-path clusters to bootstrap the
//                   cloud-provider SA in the tenant namespace. Pass nil for legacy bridge clusters.
// apiInfoProvider:  optional; when non-nil, used to read the Harvester apiserver URL + CA cert
//                   so we can build the per-cluster harvesterconfig Secret. Must be set when
//                   saEnsurer is set.
func NewClusterProvisioner(
	steve *SteveClient,
	cloudCredential, mgmtNAD, vmNamespace, operatorSSHKey, operatorPassword string,
	saEnsurer CloudProviderSAEnsurer,
	apiInfoProvider HarvesterAPIInfoProvider,
) *ClusterProvisioner {
	return &ClusterProvisioner{
		steve:            steve,
		cloudCredential:  cloudCredential,
		mgmtNAD:          mgmtNAD,
		vmNamespace:      vmNamespace,
		operatorSSHKey:   strings.TrimSpace(operatorSSHKey),
		operatorPassword: operatorPassword,
		saEnsurer:        saEnsurer,
		apiInfoProvider:  apiInfoProvider,
	}
}

// ── Create ────────────────────────────────────────────────────────────────────

// CreateCluster provisions an RKE2 cluster via Steve:
//
// VPC path (spec.TenantSubnetNAD != ""):
//  1. Bootstrap the Harvester cloud-provider SA in the tenant namespace.
//  2. POST the harvesterconfig-<name> Secret to fleet-default (pre-creates
//     the secret so the v2prov reconciler finds it already there).
//  3. POST HarvesterConfig for the system pool.
//  4. POST HarvesterConfig for each worker pool (if any).
//  5. POST Cluster CR — references ALL pools' HarvesterConfigs.
//
// Legacy path (spec.TenantSubnetNAD == ""):
//  1. POST HarvesterConfig for the system pool (single-NIC bridge mode).
//  2. POST HarvesterConfig for each worker pool (if any).
//  3. POST Cluster CR.
//
// spec.SystemPool must be non-nil and must have HarvesterConfigName set by the
// handler. Each entry in spec.WorkerPools must also have HarvesterConfigName set.
// Returns "fleet-default/<name>" BackendUID on success.
//
// Failure handling: if the Cluster CR POST fails, ALL HarvesterConfig CRs
// created in steps 3-4 (and the harvesterconfig Secret from step 2) are
// deleted best-effort.
func (p *ClusterProvisioner) CreateCluster(ctx context.Context, spec ClusterCreateSpec) (backendUID string, err error) {
	if spec.SystemPool == nil {
		return "", fmt.Errorf("CreateCluster: spec.SystemPool is required")
	}
	// Use the HarvesterConfigName provided by the handler; fall back gracefully.
	machineConfigName := spec.SystemPool.HarvesterConfigName
	if machineConfigName == "" {
		machineConfigName = "nc-" + spec.ClusterName + "-system-" + shortRand()
	}
	harvesterConfigSecretName := "harvesterconfig-" + spec.ClusterName

	// Resolve effective NADs and namespace.
	mgmtNAD := spec.MgmtNAD
	if mgmtNAD == "" {
		mgmtNAD = p.mgmtNAD
	}
	vmNS := spec.VMNamespace
	if vmNS == "" {
		vmNS = p.vmNamespace
	}

	isVPCPath := spec.TenantSubnetNAD != ""
	var secretCreated bool

	// ── Step 1 (VPC path): SA bootstrap + harvesterconfig Secret ─────────────
	if isVPCPath && p.saEnsurer != nil && p.apiInfoProvider != nil {
		saToken, err := p.saEnsurer.EnsureCloudProviderSA(ctx, vmNS)
		if err != nil {
			return "", fmt.Errorf("ensure cloud-provider SA in %s: %w", vmNS, err)
		}
		log.Info().
			Str("cluster", spec.ClusterName).
			Str("tenant_ns", vmNS).
			Msg("rancher steve: cloud-provider SA bootstrapped")

		if err := p.createHarvesterconfigSecret(ctx, harvesterConfigSecretName, spec.ClusterName, vmNS, saToken); err != nil {
			return "", fmt.Errorf("create harvesterconfig Secret for %q: %w", spec.ClusterName, err)
		}
		secretCreated = true
		log.Info().
			Str("cluster", spec.ClusterName).
			Str("secret", harvesterConfigSecretName).
			Msg("rancher steve: harvesterconfig Secret created")
	}

	// createdHCNames tracks every HarvesterConfig name we POST so we can
	// cascade-clean them all if the Cluster CR POST fails.
	createdHCNames := make([]string, 0, 1+len(spec.WorkerPools))

	// ── Step 2: POST HarvesterConfig for system pool ──────────────────────────
	if err := p.createHarvesterConfig(ctx, machineConfigName, spec, mgmtNAD, vmNS); err != nil {
		secretToClean := ""
		if secretCreated {
			secretToClean = harvesterConfigSecretName
		}
		p.cleanupOnFailure(secretToClean)
		return "", fmt.Errorf("create harvester config %q: %w", machineConfigName, err)
	}
	createdHCNames = append(createdHCNames, machineConfigName)
	log.Info().
		Str("cluster", spec.ClusterName).
		Str("machine_config", machineConfigName).
		Msg("rancher steve: HarvesterConfig created")

	// ── Step 3: POST HarvesterConfig for each worker pool ────────────────────
	for _, wp := range spec.WorkerPools {
		if wp == nil || wp.HarvesterConfigName == "" {
			continue
		}
		// Worker pools use the same NIC layout as AddNodePool: resolve compute
		// values from the pool's size (same logic as client.go::CreateCluster).
		var wpCPU, wpMemGB, wpDiskGB int
		if sz, ok := models.Sizes[wp.Size]; ok {
			wpCPU = sz.CPU
			wpMemGB = sz.MemoryGB
			wpDiskGB = sz.DefaultDiskGB
		}
		if wp.DiskGB != nil && *wp.DiskGB > 0 {
			wpDiskGB = *wp.DiskGB
		}

		if err := p.createHarvesterConfigForPool(ctx, wp.HarvesterConfigName, wpCPU, wpMemGB, wpDiskGB, spec.NodeImage, mgmtNAD, spec.TenantSubnetNAD, vmNS); err != nil {
			// Clean up all CRs created so far.
			secretToClean := ""
			if secretCreated {
				secretToClean = harvesterConfigSecretName
			}
			p.cleanupOnFailure(secretToClean, createdHCNames...)
			return "", fmt.Errorf("create harvester config for worker pool %q: %w", wp.Name, err)
		}
		createdHCNames = append(createdHCNames, wp.HarvesterConfigName)
		log.Info().
			Str("cluster", spec.ClusterName).
			Str("pool", wp.Name).
			Str("machine_config", wp.HarvesterConfigName).
			Msg("rancher steve: worker pool HarvesterConfig created")
	}

	// ── Step 4: POST Cluster CR ───────────────────────────────────────────────
	if err := p.createClusterCR(ctx, spec, machineConfigName); err != nil {
		secretToClean := ""
		if secretCreated {
			secretToClean = harvesterConfigSecretName
		}
		p.cleanupOnFailure(secretToClean, createdHCNames...)
		return "", fmt.Errorf("create cluster CR %q: %w", spec.ClusterName, err)
	}
	log.Info().
		Str("cluster", spec.ClusterName).
		Str("k8s_version", spec.K8sVersion).
		Int("worker_pools", len(spec.WorkerPools)).
		Msg("rancher steve: Cluster CR created")

	return fleetDefault + "/" + spec.ClusterName, nil
}

// cleanupOnFailure deletes the pre-created harvesterconfig Secret and/or one
// or more HarvesterConfig CRs on a partial failure. All deletions are
// best-effort; Steve 404 responses are silently ignored.
func (p *ClusterProvisioner) cleanupOnFailure(harvesterConfigSecretName string, machineConfigNames ...string) {
	cleanCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	if harvesterConfigSecretName != "" {
		if delErr := p.steve.Delete(cleanCtx, steveKindSecret, fleetDefault, harvesterConfigSecretName); delErr != nil && !IsSteveNotFound(delErr) {
			log.Warn().Err(delErr).Str("secret", harvesterConfigSecretName).Msg("rancher steve: cleanup of harvesterconfig Secret failed")
		}
	}
	for _, machineConfigName := range machineConfigNames {
		if machineConfigName == "" {
			continue
		}
		if delErr := p.steve.Delete(cleanCtx, steveKindHarvesterConfig, fleetDefault, machineConfigName); delErr != nil && !IsSteveNotFound(delErr) {
			log.Warn().Err(delErr).Str("machine_config", machineConfigName).Msg("rancher steve: cleanup of orphaned HarvesterConfig failed")
		}
	}
}

// createHarvesterconfigSecret POSTs the harvesterconfig-<cluster> Secret to
// fleet-default BEFORE the Cluster CR. This is the key difference from the
// old kubectl-apply path: we pre-create the Secret with the tenant SA
// kubeconfig so the v2prov reconciler finds it already present (no-op
// instead of creating an incorrect one with the operator credential).
//
// The Secret shape mirrors the reconciler script's write_harvesterconfig
// function exactly (as confirmed by examining the live script on the cluster).
// The credential field contains a kubeconfig YAML for the cloud-provider SA.
func (p *ClusterProvisioner) createHarvesterconfigSecret(ctx context.Context, secretName, clusterName, tenantNS string, saToken []byte) error {
	serverURL := p.apiInfoProvider.HarvesterServerURL()
	caData := p.apiInfoProvider.HarvesterCACert()

	kubeconfig := buildSAKubeconfig(serverURL, caData, tenantNS, saToken)

	body := map[string]interface{}{
		"type": "secret",
		"metadata": map[string]interface{}{
			"name":      secretName,
			"namespace": fleetDefault,
			"labels": map[string]interface{}{
				"dc-api/managed": "true",
			},
			"annotations": map[string]interface{}{
				"v2prov-authorized-secret-deletes-on-cluster-removal": "true",
				"v2prov-secret-authorized-for-cluster":                clusterName,
				"dc-api.wso2.com/managed":                             "true",
				"platform.wso2.com/credential-source-namespace":       tenantNS,
			},
		},
		// stringData is how Steve/Kubernetes accept plain-text secret values.
		"stringData": map[string]interface{}{
			"credential": kubeconfig,
		},
	}

	_, err := p.steve.Create(ctx, steveKindSecret, fleetDefault, body)
	return err
}

// buildSAKubeconfig constructs a kubeconfig YAML string for the
// cloud-provider ServiceAccount. The shape matches what the reconciler
// script's write_harvesterconfig function produces:
//
//	clusters:
//	  - name: local
//	    cluster:
//	      server: <server>
//	      certificate-authority-data: <base64(ca)>
//	users:
//	  - name: harvester-cloud-provider
//	    user:
//	      token: <sa-token>
//	contexts:
//	  - name: local
//	    context:
//	      cluster: local
//	      user: harvester-cloud-provider
//	      namespace: <tenantNS>
//	current-context: local
func buildSAKubeconfig(serverURL string, caData []byte, tenantNS string, saToken []byte) string {
	caB64 := base64.StdEncoding.EncodeToString(caData)
	var sb strings.Builder
	sb.WriteString("apiVersion: v1\n")
	sb.WriteString("kind: Config\n")
	sb.WriteString("clusters:\n")
	sb.WriteString("  - name: local\n")
	sb.WriteString("    cluster:\n")
	sb.WriteString("      server: " + serverURL + "\n")
	sb.WriteString("      certificate-authority-data: " + caB64 + "\n")
	sb.WriteString("users:\n")
	sb.WriteString("  - name: harvester-cloud-provider\n")
	sb.WriteString("    user:\n")
	sb.WriteString("      token: " + string(saToken) + "\n")
	sb.WriteString("contexts:\n")
	sb.WriteString("  - name: local\n")
	sb.WriteString("    context:\n")
	sb.WriteString("      cluster: local\n")
	sb.WriteString("      user: harvester-cloud-provider\n")
	sb.WriteString("      namespace: " + tenantNS + "\n")
	sb.WriteString("current-context: local\n")
	return sb.String()
}

// createHarvesterConfig POSTs a HarvesterConfig to Steve for a single pool.
//
// The HarvesterConfig defines the node VM shape, network attachment, and
// cloud-init userData. The F14 webhook (shipped separately) injects the
// OVN MAC-pin annotations on VMs created by Rancher's machine provisioner
// based on the networkInfo below.
//
// name: the HarvesterConfig CR name (e.g. "nc-<cluster>-system-<rand>")
// cpuCount, memoryGB, diskGB: resolved from the pool's size by the handler.
// nodeImage: "namespace/resource-name" as returned by ListImages.
func (p *ClusterProvisioner) createHarvesterConfig(
	ctx context.Context,
	name string,
	spec ClusterCreateSpec,
	mgmtNAD, vmNS string,
) error {
	// Build networkInfo JSON for dual-NIC configuration.
	// NIC 0: management (outbound internet for installer/cloud-init)
	// NIC 1: tenant OVN subnet (cluster traffic + F15 SNAT for pod egress)
	networkInfo, err := buildNetworkInfo(mgmtNAD, spec.TenantSubnetNAD)
	if err != nil {
		return fmt.Errorf("build network info: %w", err)
	}

	userData := p.buildNodeUserData(spec.TenantSubnetNAD)

	body := map[string]interface{}{
		"type": "rke-machine-config.cattle.io.harvesterconfig",
		"metadata": map[string]interface{}{
			"name":      name,
			"namespace": fleetDefault,
		},
		// Compute resources: HarvesterConfig uses string form for cpu/memory.
		"cpuCount":   fmt.Sprintf("%d", spec.NodeCPU),
		"memorySize": fmt.Sprintf("%d", spec.NodeMemoryGB),
		"diskSize":   fmt.Sprintf("%d", spec.NodeDiskGB),
		"diskBus":    "virtio",
		// Node VM image: "namespace/resource-name" form from Harvester VirtualMachineImage.
		"imageName": spec.NodeImage,
		// vmNamespace: Harvester namespace where the node VMs are created.
		// Must match the namespace that the cloud credential's kubeconfig context
		// targets; otherwise the Harvester CCM cannot see the VMs for node registration.
		"vmNamespace": vmNS,
		// sshUser: login user for Rancher's SSH-based provisioner.
		// "ubuntu" is the correct user for ubuntu-22.04 images from Harvester.
		"sshUser": "ubuntu",
		// networkInfo: JSON-encoded list of NIC configurations.
		// Steve accepts this as a string (not nested JSON) — Rancher serialises it.
		"networkInfo": networkInfo,
		// userData: cloud-init config; applied by Rancher's provisioner at boot.
		"userData": userData,
	}

	_, err = p.steve.Create(ctx, steveKindHarvesterConfig, fleetDefault, body)
	return err
}

// createHarvesterConfigForPool POSTs a HarvesterConfig for an arbitrary pool
// (used by AddNodePool). Takes explicit per-pool NIC and compute values.
func (p *ClusterProvisioner) createHarvesterConfigForPool(
	ctx context.Context,
	name string,
	cpuCount, memoryGB, diskGB int,
	nodeImage, mgmtNAD, tenantSubnetNAD, vmNS string,
) error {
	networkInfo, err := buildNetworkInfo(mgmtNAD, tenantSubnetNAD)
	if err != nil {
		return fmt.Errorf("build network info: %w", err)
	}
	userData := p.buildNodeUserData(tenantSubnetNAD)

	body := map[string]interface{}{
		"type": "rke-machine-config.cattle.io.harvesterconfig",
		"metadata": map[string]interface{}{
			"name":      name,
			"namespace": fleetDefault,
		},
		"cpuCount":    fmt.Sprintf("%d", cpuCount),
		"memorySize":  fmt.Sprintf("%d", memoryGB),
		"diskSize":    fmt.Sprintf("%d", diskGB),
		"diskBus":     "virtio",
		"imageName":   nodeImage,
		"vmNamespace": vmNS,
		"sshUser":     "ubuntu",
		"networkInfo": networkInfo,
		"userData":    userData,
	}
	_, err = p.steve.Create(ctx, steveKindHarvesterConfig, fleetDefault, body)
	return err
}

// buildNetworkInfo returns the JSON-encoded networkInfo string for a HarvesterConfig.
//
// Shape: {"interfaces":[{...},{...}]} — the docker-machine-driver-harvester
// expects this object, NOT a bare JSON array. Passing an array results in:
//
//	error setting machine configuration from flags provided: json: cannot
//	unmarshal array into Go value of type harvester.NetworkInfo
//
// Each NIC has:
//
//	networkName: "<namespace>/<nad-name>" — the Multus NAD reference.
//	networkType: "L2VlanNetwork" for VLAN-tagged NICs; "UntaggedNetwork" for untagged/bridge.
//
// The management NIC goes first (index 0) so cloud-init's metadata discovery
// finds it for DHCP. The OVN subnet NIC is index 1.
func buildNetworkInfo(mgmtNAD, tenantSubnetNAD string) (string, error) {
	nics := []map[string]interface{}{
		{
			"networkName": mgmtNAD,
			// L2VlanNetwork: works for both VLAN-tagged and bridge-mode NADs in Harvester.
			// The actual VLAN tag (if any) is defined on the NAD itself.
			"networkType": "L2VlanNetwork",
		},
	}
	if tenantSubnetNAD != "" {
		nics = append(nics, map[string]interface{}{
			"networkName": tenantSubnetNAD,
			"networkType": "L2VlanNetwork",
		})
	}
	b, err := json.Marshal(map[string]interface{}{"interfaces": nics})
	if err != nil {
		return "", fmt.Errorf("marshal networkInfo: %w", err)
	}
	return string(b), nil
}

// buildNodeUserData returns a cloud-init userData string for RKE2 cluster nodes.
//
// VPC path (tenantSubnetNAD != ""): the Ubuntu cloud image's default netplan
// only configures the first NIC. We need both NICs DHCP'd — enp1s0 for the
// mgmt NAD (outbound internet, Rancher reachability), enp2s0 for the OVN
// tenant subnet (intra-cluster traffic). bootcmd writes a netplan that
// DHCPs both, then pins RKE2's --node-ip to enp2s0's address so kubelet
// binds to the OVN address (not the mgmt one). This was originally proven
// in /tmp/spike-rke2-vpc.yaml during the F14 spike on 2026-05-11; the
// F14 webhook only injects OVN MAC annotations on the VM CRD — it does NOT
// modify the guest OS netplan, contrary to chunk 1's incorrect note.
//
// Legacy bridge path (tenantSubnetNAD == ""): single NIC, no bootcmd needed
// since the default cloud-init netplan handles it.
func (p *ClusterProvisioner) buildNodeUserData(tenantSubnetNAD string) string {
	var b strings.Builder

	b.WriteString("#cloud-config\n")
	b.WriteString("package_update: true\n")

	if p.operatorPassword != "" {
		b.WriteString("ssh_pwauth: true\n")
		b.WriteString("chpasswd:\n")
		b.WriteString("  expire: false\n")
		b.WriteString(fmt.Sprintf("  list: |\n    ubuntu:%s\n", p.operatorPassword))
	}
	if p.operatorSSHKey != "" {
		b.WriteString("ssh_authorized_keys:\n")
		b.WriteString(fmt.Sprintf("  - %s\n", p.operatorSSHKey))
	}

	b.WriteString("packages:\n")
	b.WriteString("  - qemu-guest-agent\n")
	b.WriteString("  - nfs-common\n")
	b.WriteString("  - net-tools\n")
	b.WriteString("  - ipvsadm\n")
	b.WriteString("write_files:\n")
	b.WriteString("  - path: /etc/sysctl.d/99-inotify.conf\n")
	b.WriteString("    content: |\n")
	b.WriteString("      fs.inotify.max_user_watches=524288\n")
	b.WriteString("      fs.inotify.max_user_instances=8192\n")
	b.WriteString("      fs.inotify.max_queued_events=65536\n")
	b.WriteString("    owner: root:root\n")
	b.WriteString("    permissions: '0644'\n")
	b.WriteString("  - path: /etc/modules-load.d/ipvs.conf\n")
	b.WriteString("    content: |\n")
	b.WriteString("      ip_vs\n")
	b.WriteString("      ip_vs_rr\n")
	b.WriteString("      ip_vs_wrr\n")
	b.WriteString("      ip_vs_sh\n")
	b.WriteString("      nf_conntrack\n")
	b.WriteString("    owner: root:root\n")
	b.WriteString("    permissions: '0644'\n")

	// VPC path: write a netplan that DHCPs both NICs, then pin RKE2 node-ip.
	if tenantSubnetNAD != "" {
		b.WriteString("bootcmd:\n")
		b.WriteString("  - |\n")
		b.WriteString("    cat > /etc/netplan/01-dhcp-all.yaml <<NETPLAN\n")
		b.WriteString("    network:\n")
		b.WriteString("      version: 2\n")
		b.WriteString("      renderer: networkd\n")
		b.WriteString("      ethernets:\n")
		b.WriteString("        enp1s0:\n")
		b.WriteString("          dhcp4: true\n")
		b.WriteString("        enp2s0:\n")
		b.WriteString("          dhcp4: true\n")
		b.WriteString("    NETPLAN\n")
		b.WriteString("  - chmod 600 /etc/netplan/01-dhcp-all.yaml\n")
		b.WriteString("  - rm -f /etc/netplan/50-cloud-init.yaml\n")
		b.WriteString("  - netplan apply\n")
		b.WriteString("  - |\n")
		b.WriteString("    for i in $(seq 1 60); do\n")
		b.WriteString("      OVN_IP=$(ip -4 -o addr show dev enp2s0 2>/dev/null | awk '{split($4,a,\"/\"); print a[1]; exit}')\n")
		b.WriteString("      if [ -n \"$OVN_IP\" ]; then break; fi\n")
		b.WriteString("      sleep 2\n")
		b.WriteString("    done\n")
		b.WriteString("    if [ -z \"$OVN_IP\" ]; then\n")
		b.WriteString("      echo \"ERROR: enp2s0 did not acquire DHCP in 120s\" >&2\n")
		b.WriteString("      ip -4 -o addr show >&2\n")
		b.WriteString("      exit 1\n")
		b.WriteString("    fi\n")
		b.WriteString("    mkdir -p /etc/rancher/rke2/config.yaml.d\n")
		b.WriteString("    cat > /etc/rancher/rke2/config.yaml.d/01-node-ip.yaml <<NODEIP\n")
		b.WriteString("    node-ip: ${OVN_IP}\n")
		b.WriteString("    NODEIP\n")
		b.WriteString("    echo \"Pinned node-ip=${OVN_IP}\" >> /var/log/cloud-init-node-ip.log\n")
	}

	b.WriteString("runcmd:\n")
	b.WriteString("  - systemctl enable --now qemu-guest-agent.service\n")
	b.WriteString("  - modprobe ip_vs\n")
	b.WriteString("  - modprobe ip_vs_rr\n")
	b.WriteString("  - modprobe ip_vs_wrr\n")
	b.WriteString("  - modprobe ip_vs_sh\n")
	b.WriteString("  - modprobe nf_conntrack\n")
	b.WriteString("  - sysctl --system\n")

	return b.String()
}

// buildMachinePool builds the machinePool map entry for a provisioning.cattle.io
// Cluster CR. It translates a models.NodePool (with its Role, Count, Taints,
// Labels, and HarvesterConfigName) into the shape Rancher's Steve API expects.
//
// cloudCredential is the cluster-level Harvester credential secret name —
// Rancher requires it on each pool entry in addition to the cluster spec.
func buildMachinePool(pool *models.NodePool, cloudCredential string) map[string]interface{} {
	cp, etcd, worker := poolRoleFlags(pool.Role)
	m := map[string]interface{}{
		"name":                      pool.Name,
		"displayName":               pool.Name,
		"quantity":                  pool.Count,
		"machineOS":                 "linux",
		"drainBeforeDelete":         true,
		"controlPlaneRole":          cp,
		"etcdRole":                  etcd,
		"workerRole":                worker,
		"cloudCredentialSecretName": cloudCredential,
		"machineConfigRef": map[string]interface{}{
			"kind": "HarvesterConfig",
			"name": pool.HarvesterConfigName,
		},
	}

	if len(pool.Taints) > 0 {
		taints := make([]map[string]interface{}, 0, len(pool.Taints))
		for _, t := range pool.Taints {
			taint := map[string]interface{}{
				"key":    t.Key,
				"effect": t.Effect,
			}
			if t.Value != "" {
				taint["value"] = t.Value
			}
			taints = append(taints, taint)
		}
		m["taints"] = taints
	}

	if len(pool.Labels) > 0 {
		labels := make(map[string]interface{}, len(pool.Labels))
		for k, v := range pool.Labels {
			labels[k] = v
		}
		m["labels"] = labels
	}

	return m
}

// buildAllMachinePools constructs the full machinePools slice for the Cluster CR.
// The system pool is always first; worker pools follow in the order provided.
// Nil entries in workerPools are skipped.
func buildAllMachinePools(systemPool *models.NodePool, workerPools []*models.NodePool, cloudCredential string) []interface{} {
	pools := make([]interface{}, 0, 1+len(workerPools))
	pools = append(pools, buildMachinePool(systemPool, cloudCredential))
	for _, wp := range workerPools {
		if wp == nil {
			continue
		}
		pools = append(pools, buildMachinePool(wp, cloudCredential))
	}
	return pools
}

// poolRoleFlags translates a NodePoolRole to the three Rancher boolean flags.
// system → cp=true, etcd=true, worker=false (dedicated control plane + etcd).
// worker → cp=false, etcd=false, worker=true (pure data-plane).
func poolRoleFlags(role models.NodePoolRole) (cp, etcd, worker bool) {
	switch role {
	case models.NodePoolRoleSystem:
		return true, true, false
	case models.NodePoolRoleWorker:
		return false, false, true
	default:
		// Unreachable in production — the handler validates Role before calling.
		// Fail safe: treat unknown as worker-only to avoid accidental etcd splits.
		return false, false, true
	}
}

// createClusterCR POSTs the provisioning.cattle.io.clusters CR to Steve.
//
// This is a single POST that replaces the multi-step kubectl-apply path.
// Steve handles creating the management cluster mirror and wiring up the
// machine pool reference to the HarvesterConfig.
//
// The machineConfigRef.name must exactly match the HarvesterConfig name
// created in createHarvesterConfig. Steve resolves this reference server-side.
func (p *ClusterProvisioner) createClusterCR(ctx context.Context, spec ClusterCreateSpec, machineConfigName string) error {
	// Build the system pool entry, overriding HarvesterConfigName with the
	// name we just created if the spec's pool has a different value.
	systemPool := *spec.SystemPool
	systemPool.HarvesterConfigName = machineConfigName

	// harvesterconfig Secret reference used by the cluster-level
	// machineSelectorConfig to wire the Harvester CCM via cloud-provider-config.
	cloudProviderConfigRef := fmt.Sprintf("secret://%s:%s", fleetDefault, "harvesterconfig-"+spec.ClusterName)

	body := map[string]interface{}{
		"type": "provisioning.cattle.io.cluster",
		"metadata": map[string]interface{}{
			"name":      spec.ClusterName,
			"namespace": fleetDefault,
			"labels": map[string]interface{}{
				"dc-api/managed": "true",
			},
			"annotations": map[string]interface{}{
				"dc-api.wso2.com/managed": "true",
			},
		},
		"spec": map[string]interface{}{
			"kubernetesVersion":         spec.K8sVersion,
			"cloudCredentialSecretName": p.cloudCredential,
			"enableNetworkPolicy":       false,
			"rkeConfig": map[string]interface{}{
				"machineGlobalConfig": map[string]interface{}{
					// Cilium CNI — required for Cilium VXLAN to work over OVN-geneve.
					"cni":                 "cilium",
					"disable-kube-proxy":  false,
					"etcd-expose-metrics": false,
					// F36: control-plane tuning to survive startup contention.
					"kube-apiserver-arg": []string{
						"etcd-healthcheck-timeout=10s",
					},
					"kube-controller-manager-arg": []string{
						"leader-elect-lease-duration=60s",
						"leader-elect-renew-deadline=45s",
						"leader-elect-retry-period=10s",
					},
					"kube-scheduler-arg": []string{
						"leader-elect-lease-duration=60s",
						"leader-elect-renew-deadline=45s",
						"leader-elect-retry-period=10s",
					},
				},
				// One-at-a-time rolling upgrade for control plane + workers.
				"upgradeStrategy": map[string]interface{}{
					"controlPlaneConcurrency": "1",
					"workerConcurrency":       "1",
				},
				// machineSelectorConfig wires the Harvester CCM into the downstream
				// cluster — without this the harvesterconfig-<cluster> Secret we
				// pre-minted is orphaned and the cluster has no cloud provider.
				"machineSelectorConfig": []interface{}{
					map[string]interface{}{
						"config": map[string]interface{}{
							"cloud-provider-config":   cloudProviderConfigRef,
							"cloud-provider-name":     "harvester",
							"protect-kernel-defaults": false,
						},
					},
				},
				// machinePools: system pool first, then any worker pools requested
				// at create time. Additional pools can still be added post-creation
				// via AddNodePool (POST .../clusters/{id}/node-pools).
				"machinePools": buildAllMachinePools(&systemPool, spec.WorkerPools, p.cloudCredential),
			},
		},
	}

	_, err := p.steve.Create(ctx, steveKindCluster, fleetDefault, body)
	return err
}

// ── Pool lifecycle methods ────────────────────────────────────────────────────

// getClusterCR fetches the live Cluster CR and returns the full body
// (as map[string]interface{}) and the metadata.resourceVersion needed for
// an optimistic-concurrency PUT.
func (p *ClusterProvisioner) getClusterCR(ctx context.Context, clusterName string) (map[string]interface{}, string, error) {
	res, err := p.steve.Get(ctx, steveKindCluster, fleetDefault, clusterName)
	if err != nil {
		return nil, "", fmt.Errorf("get cluster CR %q: %w", clusterName, err)
	}

	// Re-marshal the SteveResource into a generic map so we can mutate
	// spec.rkeConfig.machinePools[] without deep-typed structs.
	raw, err := json.Marshal(res)
	if err != nil {
		return nil, "", fmt.Errorf("marshal cluster CR: %w", err)
	}
	var body map[string]interface{}
	if err := json.Unmarshal(raw, &body); err != nil {
		return nil, "", fmt.Errorf("unmarshal cluster CR: %w", err)
	}

	rv := resourceVersion(body)
	return body, rv, nil
}

// putClusterCR PUTs the full Cluster CR body back to Steve. This is the
// Rancher-recommended write pattern (GET-then-PUT, full document replace)
// for provisioning.cattle.io objects. Returns the updated resource on success.
func (p *ClusterProvisioner) putClusterCR(ctx context.Context, clusterName string, body map[string]interface{}) error {
	_, err := p.steve.Update(ctx, steveKindCluster, fleetDefault, clusterName, body)
	return err
}

// resourceVersion extracts metadata.resourceVersion from a generic body map.
// Returns "" if absent (caller should treat as "missing RV").
func resourceVersion(body map[string]interface{}) string {
	meta, _ := body["metadata"].(map[string]interface{})
	if meta == nil {
		return ""
	}
	rv, _ := meta["resourceVersion"].(string)
	return rv
}

// machinePools extracts spec.rkeConfig.machinePools from the body as a
// []interface{}. Returns nil if the path doesn't exist.
func machinePools(body map[string]interface{}) []interface{} {
	spec, _ := body["spec"].(map[string]interface{})
	if spec == nil {
		return nil
	}
	rke, _ := spec["rkeConfig"].(map[string]interface{})
	if rke == nil {
		return nil
	}
	pools, _ := rke["machinePools"].([]interface{})
	return pools
}

// setMachinePools writes pools back into spec.rkeConfig.machinePools in body.
// Creates any missing intermediate maps.
func setMachinePools(body map[string]interface{}, pools []interface{}) {
	spec, _ := body["spec"].(map[string]interface{})
	if spec == nil {
		spec = make(map[string]interface{})
		body["spec"] = spec
	}
	rke, _ := spec["rkeConfig"].(map[string]interface{})
	if rke == nil {
		rke = make(map[string]interface{})
		spec["rkeConfig"] = rke
	}
	rke["machinePools"] = pools
}

// shortRand returns a 5-character alphanumeric string for naming HarvesterConfig
// CRs, matching Rancher UI's naming convention (e.g. "nc-cluster-system-ab3cd").
func shortRand() string {
	const alpha = "abcdefghijklmnopqrstuvwxyz0123456789"
	var b [5]byte
	// Use time-based entropy to avoid importing crypto/rand; this is just a
	// human-readable suffix, not a security primitive.
	seed := time.Now().UnixNano()
	for i := range b {
		b[i] = alpha[seed%int64(len(alpha))]
		seed /= int64(len(alpha))
	}
	return string(b[:])
}

// AddNodePool creates a HarvesterConfig CR for the pool and appends the pool
// to the Cluster CR's spec.rkeConfig.machinePools via GET-then-PUT. If the
// PUT returns 409 (optimistic concurrency conflict), it retries once.
//
// pool.HarvesterConfigName must be set by the caller (the handler pre-generates
// it and persists it to cluster_node_pools before calling this method).
func (p *ClusterProvisioner) AddNodePool(
	ctx context.Context,
	clusterName string,
	pool *models.NodePool,
	mgmtNAD, tenantSubnetNAD, vmNamespace, nodeImage string,
	cpuCount, memoryGB, diskGB int,
) error {
	// ── Step 1: POST HarvesterConfig for the new pool ─────────────────────────
	effMgmt := mgmtNAD
	if effMgmt == "" {
		effMgmt = p.mgmtNAD
	}
	effVMNS := vmNamespace
	if effVMNS == "" {
		effVMNS = p.vmNamespace
	}
	if err := p.createHarvesterConfigForPool(ctx, pool.HarvesterConfigName, cpuCount, memoryGB, diskGB, nodeImage, effMgmt, tenantSubnetNAD, effVMNS); err != nil {
		return fmt.Errorf("add node pool %q: create harvesterconfig: %w", pool.Name, err)
	}

	// ── Step 2: GET cluster, append pool, PUT ─────────────────────────────────
	if err := p.appendPoolToCluster(ctx, clusterName, pool); err != nil {
		// Best-effort cleanup of the HarvesterConfig CR we just created.
		cleanCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if delErr := p.steve.Delete(cleanCtx, steveKindHarvesterConfig, fleetDefault, pool.HarvesterConfigName); delErr != nil && !IsSteveNotFound(delErr) {
			log.Warn().Err(delErr).Str("hc", pool.HarvesterConfigName).Msg("rancher: AddNodePool cleanup of HarvesterConfig failed")
		}
		return err
	}

	log.Info().
		Str("cluster", clusterName).
		Str("pool", pool.Name).
		Str("hc", pool.HarvesterConfigName).
		Msg("rancher: node pool added")
	return nil
}

// appendPoolToCluster performs the GET-then-PUT to append a pool entry to the
// Cluster CR's machinePools slice. Retries once on HTTP 409 (resourceVersion
// conflict) after re-fetching the latest state.
func (p *ClusterProvisioner) appendPoolToCluster(ctx context.Context, clusterName string, pool *models.NodePool) error {
	for attempt := 0; attempt < 2; attempt++ {
		body, _, err := p.getClusterCR(ctx, clusterName)
		if err != nil {
			return err
		}
		pools := machinePools(body)
		// Guard: refuse if the pool name already exists.
		for _, raw := range pools {
			entry, _ := raw.(map[string]interface{})
			if entry["name"] == pool.Name {
				return fmt.Errorf("pool %q already exists in cluster %q", pool.Name, clusterName)
			}
		}
		pools = append(pools, buildMachinePool(pool, p.cloudCredential))
		setMachinePools(body, pools)

		err = p.putClusterCR(ctx, clusterName, body)
		if err == nil {
			return nil
		}
		// Only retry once on 409; any other error propagates immediately.
		if !isSteveConflict(err) || attempt == 1 {
			return fmt.Errorf("PUT cluster CR after appending pool: %w", err)
		}
		log.Warn().Str("cluster", clusterName).Str("pool", pool.Name).Msg("rancher: 409 conflict on pool append — retrying once")
	}
	return nil
}

// ScaleNodePool sets machinePools[i].quantity for the named pool via GET-then-PUT.
// Returns an error if the pool is not found or if both GET-then-PUT attempts fail.
func (p *ClusterProvisioner) ScaleNodePool(ctx context.Context, clusterName, poolName string, newCount int) error {
	for attempt := 0; attempt < 2; attempt++ {
		body, _, err := p.getClusterCR(ctx, clusterName)
		if err != nil {
			return err
		}
		pools := machinePools(body)
		found := false
		for _, raw := range pools {
			entry, ok := raw.(map[string]interface{})
			if !ok {
				continue
			}
			if entry["name"] == poolName {
				entry["quantity"] = newCount
				found = true
				break
			}
		}
		if !found {
			return fmt.Errorf("pool %q not found in cluster %q", poolName, clusterName)
		}
		setMachinePools(body, pools)

		err = p.putClusterCR(ctx, clusterName, body)
		if err == nil {
			log.Info().Str("cluster", clusterName).Str("pool", poolName).Int("count", newCount).Msg("rancher: pool scaled")
			return nil
		}
		if !isSteveConflict(err) || attempt == 1 {
			return fmt.Errorf("PUT cluster CR after scaling pool: %w", err)
		}
		log.Warn().Str("cluster", clusterName).Str("pool", poolName).Msg("rancher: 409 conflict on pool scale — retrying once")
	}
	return nil
}

// UpdateNodePoolTaintsLabels replaces taints and labels for the named pool via
// GET-then-PUT. Retries once on 409. The system pool can be updated here; the
// handler is responsible for refusing that operation at the API layer (R5).
func (p *ClusterProvisioner) UpdateNodePoolTaintsLabels(
	ctx context.Context,
	clusterName, poolName string,
	taints []models.NodePoolTaint,
	labels map[string]string,
) error {
	for attempt := 0; attempt < 2; attempt++ {
		body, _, err := p.getClusterCR(ctx, clusterName)
		if err != nil {
			return err
		}
		pools := machinePools(body)
		found := false
		for _, raw := range pools {
			entry, ok := raw.(map[string]interface{})
			if !ok {
				continue
			}
			if entry["name"] != poolName {
				continue
			}
			found = true
			// Replace taints.
			if len(taints) > 0 {
				ts := make([]map[string]interface{}, 0, len(taints))
				for _, t := range taints {
					taint := map[string]interface{}{"key": t.Key, "effect": t.Effect}
					if t.Value != "" {
						taint["value"] = t.Value
					}
					ts = append(ts, taint)
				}
				entry["taints"] = ts
			} else {
				delete(entry, "taints")
			}
			// Replace labels.
			if len(labels) > 0 {
				ls := make(map[string]interface{}, len(labels))
				for k, v := range labels {
					ls[k] = v
				}
				entry["labels"] = ls
			} else {
				delete(entry, "labels")
			}
			break
		}
		if !found {
			return fmt.Errorf("pool %q not found in cluster %q", poolName, clusterName)
		}
		setMachinePools(body, pools)

		err = p.putClusterCR(ctx, clusterName, body)
		if err == nil {
			log.Info().Str("cluster", clusterName).Str("pool", poolName).Msg("rancher: pool taints/labels updated")
			return nil
		}
		if !isSteveConflict(err) || attempt == 1 {
			return fmt.Errorf("PUT cluster CR after updating taints/labels: %w", err)
		}
		log.Warn().Str("cluster", clusterName).Str("pool", poolName).Msg("rancher: 409 conflict on taints/labels update — retrying once")
	}
	return nil
}

// RemoveNodePool drops the named pool from machinePools[] via GET-then-PUT,
// then cascade-deletes the HarvesterConfig CR. Draining of nodes is async
// (drainBeforeDelete=true was set at creation time); the handler returns 202
// and the reconciler reflects status until the MachineDeployment is gone.
func (p *ClusterProvisioner) RemoveNodePool(ctx context.Context, clusterName, poolName, harvesterConfigName string) error {
	for attempt := 0; attempt < 2; attempt++ {
		body, _, err := p.getClusterCR(ctx, clusterName)
		if err != nil {
			return err
		}
		pools := machinePools(body)
		filtered := make([]interface{}, 0, len(pools))
		found := false
		for _, raw := range pools {
			entry, ok := raw.(map[string]interface{})
			if ok && entry["name"] == poolName {
				found = true
				continue // drop this entry
			}
			filtered = append(filtered, raw)
		}
		if !found {
			// Pool already absent — idempotent success.
			log.Info().Str("cluster", clusterName).Str("pool", poolName).Msg("rancher: pool already absent — skip remove")
			break
		}
		setMachinePools(body, filtered)

		err = p.putClusterCR(ctx, clusterName, body)
		if err == nil {
			break
		}
		if !isSteveConflict(err) || attempt == 1 {
			return fmt.Errorf("PUT cluster CR after removing pool: %w", err)
		}
		log.Warn().Str("cluster", clusterName).Str("pool", poolName).Msg("rancher: 409 conflict on pool remove — retrying once")
	}

	// Delete the HarvesterConfig CR best-effort (don't block drain).
	if harvesterConfigName != "" {
		cleanCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if err := p.steve.Delete(cleanCtx, steveKindHarvesterConfig, fleetDefault, harvesterConfigName); err != nil && !IsSteveNotFound(err) {
			log.Warn().Err(err).Str("hc", harvesterConfigName).Msg("rancher: failed to delete HarvesterConfig after pool removal; will need sweep")
		}
	}

	log.Info().Str("cluster", clusterName).Str("pool", poolName).Msg("rancher: node pool removed")
	return nil
}

// GetNodePoolStatuses returns per-pool status derived from the live Cluster CR
// status conditions. This is a stub for R4 — full reconciler integration is
// deferred to the pool-reconciler follow-up (see FOLLOWUPS.md). The stub
// returns an empty map so the reconciler can call it without error; it does not
// yet translate Rancher machinePool conditions to dc-api NodePoolStatus values.
func (p *ClusterProvisioner) GetNodePoolStatuses(ctx context.Context, clusterName string) (map[string]models.NodePoolStatus, error) {
	// Intentionally minimal for R4. The reconciler integration is a separate chunk.
	// What we do: fetch the cluster status and check top-level ready/stalled
	// conditions as a proxy for all pools. If the cluster is stalled, mark all
	// pools as failed; if ready, mark as ready; otherwise provisioning.
	cs, err := p.GetClusterStatus(ctx, fleetDefault+"/"+clusterName)
	if err != nil {
		if IsSteveNotFound(err) {
			return map[string]models.NodePoolStatus{}, nil
		}
		return nil, fmt.Errorf("GetNodePoolStatuses: get cluster status: %w", err)
	}

	// Determine a cluster-wide proxy status.
	var proxyStatus models.NodePoolStatus
	switch {
	case cs.Message != "" && !cs.Ready:
		// Stalled.
		proxyStatus = models.NodePoolStatusFailed
	case cs.Ready:
		proxyStatus = models.NodePoolStatusReady
	default:
		proxyStatus = models.NodePoolStatusProvisioning
	}

	// Fetch pool names from the live CR so the map is keyed correctly.
	body, _, err := p.getClusterCR(ctx, clusterName)
	if err != nil {
		// If we can't read the CR but GetClusterStatus succeeded, return empty.
		return map[string]models.NodePoolStatus{}, nil
	}
	pools := machinePools(body)
	result := make(map[string]models.NodePoolStatus, len(pools))
	for _, raw := range pools {
		entry, ok := raw.(map[string]interface{})
		if !ok {
			continue
		}
		name, _ := entry["name"].(string)
		if name != "" {
			result[name] = proxyStatus
		}
	}
	return result, nil
}

// isSteveConflict returns true when the Steve error body indicates HTTP 409
// (resourceVersion conflict). Used by GET-then-PUT retry logic.
func isSteveConflict(err error) bool {
	if err == nil {
		return false
	}
	return strings.Contains(err.Error(), "HTTP 409")
}

// deleteHarvesterConfig deletes a HarvesterConfig CR. NotFound is silently ignored.
func (p *ClusterProvisioner) deleteHarvesterConfig(ctx context.Context, name string) error {
	err := p.steve.Delete(ctx, steveKindHarvesterConfig, fleetDefault, name)
	if IsSteveNotFound(err) {
		return nil
	}
	return err
}

// ── Get / Status ──────────────────────────────────────────────────────────────

// GetClusterStatus fetches the current status of a cluster from Steve.
// backendUID is "fleet-default/<cluster-name>" (as stored in the DB).
//
// The returned ClusterStatus.RancherUID is populated once the management
// cluster exists (typically ~30s after the first node registers with Rancher).
func (p *ClusterProvisioner) GetClusterStatus(ctx context.Context, backendUID string) (*ClusterStatus, error) {
	ns, name, err := parseBackendUID(backendUID)
	if err != nil {
		return nil, err
	}

	res, err := p.steve.Get(ctx, steveKindCluster, ns, name)
	if err != nil {
		return nil, fmt.Errorf("steve get cluster %s: %w", backendUID, err)
	}

	return parseClusterStatus(res)
}

// parseClusterStatus decodes the Steve response into a ClusterStatus.
// The provisioning.cattle.io cluster status shape:
//
//	status.ready (bool)
//	status.clusterName (string) — management cluster ID once registered
//	status.conditions[] — array of {type, status, message, reason}
func parseClusterStatus(res *SteveResource) (*ClusterStatus, error) {
	if res.Status == nil {
		// No status yet — cluster was just created.
		return &ClusterStatus{}, nil
	}

	var status struct {
		Ready       bool   `json:"ready"`
		ClusterName string `json:"clusterName"` // management cluster c-m-xxxxx
		Conditions  []struct {
			Type    string `json:"type"`
			Status  string `json:"status"` // "True" | "False" | "Unknown"
			Message string `json:"message"`
		} `json:"conditions"`
	}
	if err := json.Unmarshal(res.Status, &status); err != nil {
		return nil, fmt.Errorf("decode cluster status: %w", err)
	}

	cs := &ClusterStatus{
		Ready:      status.Ready,
		RancherUID: status.ClusterName,
	}
	// Determine provisioned state and surface a message.
	for _, c := range status.Conditions {
		if c.Type == "Provisioned" && c.Status == "True" {
			cs.Provisioned = true
		}
		if c.Type == "Stalled" && c.Status == "True" {
			cs.Message = c.Message
			if cs.Message == "" {
				cs.Message = "cluster stalled"
			}
		}
		// Pick the first non-empty message from any Failed condition as fallback.
		if c.Status == "False" && c.Message != "" && cs.Message == "" {
			cs.Message = c.Message
		}
	}
	return cs, nil
}

// ── Kubeconfig ───────────────────────────────────────────────────────────────

// GetKubeconfig retrieves the kubeconfig for a provisioned cluster.
//
// Two-step: the provisioning cluster's status.clusterName holds the management
// cluster ID (c-m-xxxxx). We then call POST /v3/clusters/<id>?action=generateKubeconfig
// via the Norman v3 path (same as the existing Client.GetKubeconfig). The Steve
// path for kubeconfig is not standardised.
//
// The ClusterProvisioner delegates to the parent Norman client for kubeconfig
// because the generateKubeconfig action lives on the v3 management cluster,
// not on the v1 provisioning cluster. The caller can use either this method
// (which resolves the management ID automatically) or the Norman client's
// GetKubeconfig with the management cluster ID directly.
//
// backendUID: "fleet-default/<cluster-name>" from the DB.
// Returns the kubeconfig YAML string on success.
func (p *ClusterProvisioner) GetKubeconfig(ctx context.Context, backendUID string, normanClient *Client) (string, error) {
	return normanClient.GetKubeconfig(ctx, backendUID)
}

// ── Delete ────────────────────────────────────────────────────────────────────

// DeleteCluster removes a cluster and its associated machine config via Steve.
// The cluster CR deletion cascades to:
//   - The Rancher v3 management cluster resource.
//   - The downstream Harvester VMs (Rancher's machine provisioner deletes them).
//
// dc-api explicitly deletes the harvesterconfig-<name> Secret and HarvesterConfig CR
// because Rancher only auto-cascades the Secret when it created it via Steve itself.
// For dc-api-pre-created Secrets (VPC path), the annotation
// v2prov-authorized-secret-deletes-on-cluster-removal=true instructs Rancher to
// cascade it, but we also delete it explicitly for belt-and-suspenders safety.
//
// backendUID: "fleet-default/<cluster-name>" from the DB.
func (p *ClusterProvisioner) DeleteCluster(ctx context.Context, backendUID string) error {
	ns, name, err := parseBackendUID(backendUID)
	if err != nil {
		return err
	}

	// Ownership guard (F38): refuse to operate on a Cluster CR that doesn't
	// carry dc-api/managed=true. The call chain (handler reads DB row by
	// tenant before passing the BackendUID) is the primary protection — this
	// is belt-and-suspenders against name collisions or future refactor bugs.
	// If the CR is already gone, fall through to cascade-clean in case
	// prior incomplete deletes left stragglers.
	cr, getErr := p.steve.Get(ctx, steveKindCluster, ns, name)
	if getErr != nil {
		if !IsSteveNotFound(getErr) {
			return fmt.Errorf("steve get cluster %s for ownership check: %w", backendUID, getErr)
		}
		log.Warn().Str("cluster", backendUID).Msg("rancher steve: Cluster CR already gone — running cascade-clean for stragglers")
	} else if !hasManagedLabel(cr) {
		return fmt.Errorf("delete cluster %s: Cluster CR exists but is not labelled dc-api/managed=true — refusing", backendUID)
	}

	// Delete the cluster CR first. Rancher cascades downstream VMs.
	if cr != nil {
		if err := p.steve.Delete(ctx, steveKindCluster, ns, name); err != nil {
			return fmt.Errorf("steve delete cluster %s: %w", backendUID, err)
		}
	}

	// Best-effort: delete the HarvesterConfig CR. Rancher may have cascaded it
	// via OwnerReference; NotFound is fine.
	machineConfigName := name + "-pool"
	if err := p.steve.Delete(ctx, steveKindHarvesterConfig, fleetDefault, machineConfigName); err != nil {
		if !IsSteveNotFound(err) {
			log.Warn().Err(err).
				Str("cluster", backendUID).
				Str("machine_config", machineConfigName).
				Msg("rancher steve: failed to delete HarvesterConfig (best-effort — Rancher may have cascaded it)")
		}
	}

	// Best-effort: delete the dc-api-pre-created harvesterconfig Secret.
	// On the VPC path dc-api creates this before Steve; on the legacy bridge
	// path Steve creates it automatically and also cleans it up on cluster
	// delete. We attempt the delete unconditionally — NotFound is fine either way.
	harvesterConfigSecretName := "harvesterconfig-" + name
	if err := p.steve.Delete(ctx, steveKindSecret, fleetDefault, harvesterConfigSecretName); err != nil {
		if !IsSteveNotFound(err) {
			log.Warn().Err(err).
				Str("cluster", backendUID).
				Str("secret", harvesterConfigSecretName).
				Msg("rancher steve: failed to delete harvesterconfig Secret (best-effort)")
		}
	}

	// F38: cascade-clean the CAPI / RKE trail in fleet-default. Healthy
	// clusters have these GC'd by Rancher's own controllers; half-failed
	// creates leave Machine / HarvesterMachine / MachineSet / MachineDeployment
	// / RKEControlPlane / RKEBootstrap and a fleet of per-machine secrets
	// stuck on hung finalizers. cascadeCleanCluster waits a short window for
	// the normal cascade and then sweeps survivors by label / name prefix.
	p.cascadeCleanCluster(ctx, name)

	return nil
}

// hasManagedLabel returns true if the resource's metadata.labels contains
// dc-api/managed=true. Used by DeleteCluster as the ownership guard before
// any destructive operation.
func hasManagedLabel(res *SteveResource) bool {
	if res == nil || res.Metadata == nil {
		return false
	}
	labels, ok := res.Metadata["labels"].(map[string]interface{})
	if !ok {
		return false
	}
	v, _ := labels["dc-api/managed"].(string)
	return v == "true"
}

// ── F38 cascade-clean ─────────────────────────────────────────────────────────

// Steve kinds for the CAPI / RKE trail. All in fleet-default.
const (
	steveKindMachine           = "cluster.x-k8s.io.machines"
	steveKindHarvesterMachine  = "rke-machine.cattle.io.harvestermachines"
	steveKindMachineSet        = "cluster.x-k8s.io.machinesets"
	steveKindMachineDeployment = "cluster.x-k8s.io.machinedeployments"
	steveKindRKEControlPlane   = "rke.cattle.io.rkecontrolplanes"
	steveKindRKEBootstrap      = "rke.cattle.io.rkebootstraps"
)

// cascadeCleanCluster sweeps the CAPI / RKE / secret trail that Rancher's
// controllers normally cascade-delete after the Cluster CR is removed.
// Half-failed clusters leave these objects stuck on finalizers that point at
// delete-jobs that themselves fail. The sweep waits for Rancher's normal
// cascade to do its work, then force-removes finalizers on Machines and
// HarvesterMachines that are still hanging and deletes the rest.
//
// All operations best-effort: 404 / errors are logged at warn but don't fail
// the parent DeleteCluster. The dc-api handler still flips the DB row to
// success once we return — the surviving stragglers are an operability
// concern, not a tenant-facing one.
func (p *ClusterProvisioner) cascadeCleanCluster(ctx context.Context, name string) {
	clusterLabel := "cluster.x-k8s.io/cluster-name=" + name

	// Give Rancher's normal cascade a chance first — usually 30-60s.
	// During that window everything cleans up on its own and we never enter
	// the force-remove path. Poll every 5s.
	waitCtx, cancel := context.WithTimeout(ctx, 90*time.Second)
	defer cancel()
	for {
		machines, _ := p.steve.ListByLabel(ctx, steveKindMachine, fleetDefault, clusterLabel)
		hmachines, _ := p.steve.ListByLabel(ctx, steveKindHarvesterMachine, fleetDefault, clusterLabel)
		if len(machines) == 0 && len(hmachines) == 0 {
			break
		}
		select {
		case <-waitCtx.Done():
			break
		case <-time.After(5 * time.Second):
			continue
		}
		break
	}

	// Force-remove finalizers on any Machine still present.
	if machines, err := p.steve.ListByLabel(ctx, steveKindMachine, fleetDefault, clusterLabel); err == nil {
		for _, m := range machines {
			if err := p.forceFinalizerRemoval(ctx, steveKindMachine, m.Metadata); err != nil {
				log.Warn().Err(err).Str("cluster", name).Str("machine", machineName(m.Metadata)).Msg("rancher steve: cascade-clean failed to clear machine finalizers")
			}
		}
	}
	// Same for HarvesterMachine.
	if hmachines, err := p.steve.ListByLabel(ctx, steveKindHarvesterMachine, fleetDefault, clusterLabel); err == nil {
		for _, h := range hmachines {
			if err := p.forceFinalizerRemoval(ctx, steveKindHarvesterMachine, h.Metadata); err != nil {
				log.Warn().Err(err).Str("cluster", name).Str("harvestermachine", machineName(h.Metadata)).Msg("rancher steve: cascade-clean failed to clear harvestermachine finalizers")
			}
		}
	}

	// Delete remaining MachineSet / MachineDeployment / RKEBootstrap by label.
	for _, kind := range []string{steveKindMachineSet, steveKindMachineDeployment, steveKindRKEBootstrap} {
		items, err := p.steve.ListByLabel(ctx, kind, fleetDefault, clusterLabel)
		if err != nil {
			log.Warn().Err(err).Str("kind", kind).Str("cluster", name).Msg("rancher steve: cascade-clean list failed")
			continue
		}
		for _, it := range items {
			n := machineName(it.Metadata)
			if n == "" {
				continue
			}
			if err := p.steve.Delete(ctx, kind, fleetDefault, n); err != nil && !IsSteveNotFound(err) {
				log.Warn().Err(err).Str("kind", kind).Str("name", n).Msg("rancher steve: cascade-clean delete failed")
			}
		}
	}

	// RKEControlPlane is named after the cluster (1:1, no label needed).
	if err := p.steve.Delete(ctx, steveKindRKEControlPlane, fleetDefault, name); err != nil && !IsSteveNotFound(err) {
		log.Warn().Err(err).Str("cluster", name).Msg("rancher steve: cascade-clean delete RKEControlPlane failed")
	}

	// Sweep per-machine secrets by name prefix. Rancher normally cascades
	// these via owner refs, but when Machines were force-deleted the owner-ref
	// link is severed and the secrets orphan. Enumerate all secrets in
	// fleet-default and delete by prefix match — bounded blast radius.
	secrets, err := p.steve.List(ctx, steveKindSecret, fleetDefault)
	if err != nil {
		log.Warn().Err(err).Str("cluster", name).Msg("rancher steve: cascade-clean list secrets failed")
		return
	}
	prefix := name + "-"
	clusterStateName := name + "-rke-state"
	clusterKubeconfigName := name + "-kubeconfig"
	for _, s := range secrets {
		n := machineName(s.Metadata)
		if n == "" {
			continue
		}
		// Match per-machine names (<cluster>-pool-XXXX-*), plus the cluster-
		// level state and kubeconfig secrets. Strict to avoid scooping up
		// unrelated secrets that happen to share a prefix.
		if !strings.HasPrefix(n, prefix) && n != clusterStateName && n != clusterKubeconfigName {
			continue
		}
		if err := p.steve.Delete(ctx, steveKindSecret, fleetDefault, n); err != nil && !IsSteveNotFound(err) {
			log.Warn().Err(err).Str("secret", n).Msg("rancher steve: cascade-clean delete secret failed")
		}
	}
}

// forceFinalizerRemoval PATCHes metadata.finalizers to [] on a resource.
// Used by cascadeCleanCluster to unblock Machines / HarvesterMachines whose
// delete-jobs are themselves failing. The resource then gets GC'd by k8s.
// Idempotent — 404 on the underlying patch returns nil.
func (p *ClusterProvisioner) forceFinalizerRemoval(ctx context.Context, kind string, meta map[string]interface{}) error {
	name := machineName(meta)
	if name == "" {
		return fmt.Errorf("cannot patch finalizers: empty name in metadata")
	}
	return p.steve.Patch(ctx, kind, fleetDefault, name, []byte(`{"metadata":{"finalizers":[]}}`))
}

// machineName extracts metadata.name from a SteveResource.Metadata map.
// Returns "" if the field is absent or wrong type — caller should treat that
// as "skip this entry".
func machineName(meta map[string]interface{}) string {
	if meta == nil {
		return ""
	}
	n, _ := meta["name"].(string)
	return n
}
