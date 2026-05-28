// Package kubeovn — dns.go
//
// F20 per-VPC DNS implementation: EnsureVpcDNSBootstrap (one-time cluster
// setup) and per-VPC EnsureVpcDNS / DeleteVpcDNS / IsVpcDNSPresent.
//
// Architecture (Approach C.2 — see memory/project_f20_spike_outcome.md):
//
//   - One CoreDNS Deployment in kube-system per VPC, named "vpc-dns-<vpcUID>".
//   - Primary NIC: Calico eth0 (default — for upstream internet egress; default
//     dnsPolicy: ClusterFirst is CORRECT for the CoreDNS pod itself).
//   - Secondary NIC: tenant subnet via Multus + KubeOVN NAD, pinned IP at
//     <cidr-network>+2 using per-provider annotation format:
//       <subnet-backend-uid>.<subnet-ns>.ovn.kubernetes.io/ip_address: <ip>
//   - Subnet's dhcpV4Options patched to advertise the pinned IP as dns_server.
//   - Every VM on a VPC subnet MUST have dnsPolicy=None + dnsConfig.Nameservers
//     pointing at this IP to silence KubeVirt's bridge-mode internal DHCP race.
//     That injection lives in the harvester driver (client.go::buildVMManifest).
//
// Critical gotchas from the spike:
//   1. Per-provider annotation format is "<subnet-uid>.<subnet-ns>.ovn.kubernetes.io/ip_address"
//      (NOT the generic "ovn.kubernetes.io/ip_address" — that gets reserved by
//      KubeOVN IPAM but the NIC is never attached on Calico-primary clusters).
//   2. DHCP dns_server must be written as a string (dns_server="<ip>"), NOT set
//      syntax ({ip}) — kube-ovn's parser doesn't handle the set form.
//   3. VpcDns CRD (kubeovn.io/v1.VpcDns) is structurally unavailable on Harvester
//      because kube-ovn-controller runs with --enable-lb=false. Approach C.2 owns
//      its own CoreDNS Deployment and avoids the CRD entirely.
//   4. The CoreDNS pod itself uses default dnsPolicy (ClusterFirst) so it can
//      reach upstream DNS and the Kubernetes API if needed. Only the VMs need None.
package kubeovn

import (
	"context"
	"crypto/sha1"
	"encoding/hex"
	"fmt"
	"net"
	"strings"
	"time"

	"github.com/rs/zerolog/log"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
)

// ── GVRs for DNS bootstrap resources ─────────────────────────────────────────
// configMapGVR() is defined in client.go (shared across the package).

var (
	serviceAccountGVR = schema.GroupVersionResource{Group: "", Version: "v1", Resource: "serviceaccounts"}
	deploymentGVR     = schema.GroupVersionResource{Group: "apps", Version: "v1", Resource: "deployments"}
)

const (
	dnsSAName        = "vpc-dns"
	dnsBootstrapNS   = "kube-system"
	dnsFallbackImage = "coredns/coredns:1.11.3"
)

// DNSConfig holds the per-VPC DNS parameters, loaded from config.Config at startup
// and passed into the Client via WithDNSConfig().
type DNSConfig struct {
	Forwarders   []string // upstream IPs, e.g. ["1.1.1.1","8.8.8.8"]
	Image        string   // CoreDNS image (auto-detected at startup if empty)
	SearchDomain string   // optional VM search domain, e.g. "lk.dc.internal"
}

// WithDNSConfig attaches the F20 DNS config to the client.
// Must be called before EnsureVpcDNSBootstrap or EnsureVpcDNS.
func (c *Client) WithDNSConfig(cfg DNSConfig) {
	c.dnsConf = &cfg
}

// dnsImage returns the configured CoreDNS image, falling back to the built-in
// default. If VPCDNSImage was explicitly configured it wins unconditionally;
// otherwise the auto-detected image set at startup is used.
func (c *Client) dnsImage() string {
	if c.dnsConf != nil && c.dnsConf.Image != "" {
		return c.dnsConf.Image
	}
	return dnsFallbackImage
}

// dnsForwarders returns the upstream DNS forwarder list, or the fallback pair.
func (c *Client) dnsForwarders() []string {
	if c.dnsConf != nil && len(c.dnsConf.Forwarders) > 0 {
		return c.dnsConf.Forwarders
	}
	return []string{"1.1.1.1", "8.8.8.8"}
}

// EnsureVpcDNSBootstrap creates the one-time cluster-level resources needed
// for per-VPC CoreDNS Deployments. Safe to call on every startup — AlreadyExists
// on any resource is treated as success.
//
// Objects created (all in kube-system):
//  1. ServiceAccount "vpc-dns" — no special RBAC; forward-only CoreDNS needs none.
//
// Per-VPC Corefile ConfigMaps used to be created here as a single shared
// "vpc-dns-corefile" CM, but the F20 → per-VPC-Corefile refactor moved
// Corefile creation into EnsureVpcDNS so each VPC can carry its own zone
// records (needed for M3 Key Vault Private Endpoint DNS, and any future
// per-VPC zone). Any pre-refactor "vpc-dns-corefile" CM left behind is
// inert — Deployments now mount the per-VPC CM exclusively.
func (c *Client) EnsureVpcDNSBootstrap(ctx context.Context) error {
	sa := &unstructured.Unstructured{
		Object: map[string]interface{}{
			"apiVersion": "v1",
			"kind":       "ServiceAccount",
			"metadata": map[string]interface{}{
				"name":      dnsSAName,
				"namespace": dnsBootstrapNS,
				"labels": map[string]interface{}{
					"dc-api/managed": "true",
				},
			},
		},
	}
	if _, err := c.dynamic.Resource(serviceAccountGVR).Namespace(dnsBootstrapNS).Create(ctx, sa, metav1.CreateOptions{}); err != nil {
		if !k8serrors.IsAlreadyExists(err) {
			return fmt.Errorf("ensure vpc dns bootstrap: create ServiceAccount %q: %w", dnsSAName, err)
		}
		log.Debug().Str("sa", dnsSAName).Msg("kubeovn: vpc-dns ServiceAccount already exists — skipping create")
	}

	log.Info().
		Strs("forwarders", c.dnsForwarders()).
		Str("image", c.dnsImage()).
		Msg("kubeovn: F20 DNS bootstrap complete (SA ready; Corefile CMs are per-VPC)")
	return nil
}

// ensureVpcCorefileConfigMap idempotently creates a per-VPC Corefile ConfigMap
// named "vpc-dns-corefile-<vpcUID>" in kube-system. The Corefile is the same
// shape as the old shared one — forward-only — but lives in a per-VPC CM so
// future zone records (e.g. M3 Key Vault Private Endpoint hostnames) can be
// added without affecting other VPCs' resolvers.
//
// AlreadyExists is treated as success. Forwarders are not reconciled on
// subsequent calls; operators can `kubectl delete` and let dc-api recreate
// if the upstream list changes.
func (c *Client) ensureVpcCorefileConfigMap(ctx context.Context, vpcUID string) error {
	cmName := vpcDNSCorefileName(vpcUID)

	forwarders := strings.Join(c.dnsForwarders(), " ")
	corefile := fmt.Sprintf(`.:53 {
    errors
    health {
        lameduck 5s
    }
    ready
    forward . %s { max_concurrent 1000 }
    cache 300
    loop
    reload
    loadbalance
}
`, forwarders)

	cm := &unstructured.Unstructured{
		Object: map[string]interface{}{
			"apiVersion": "v1",
			"kind":       "ConfigMap",
			"metadata": map[string]interface{}{
				"name":      cmName,
				"namespace": dnsBootstrapNS,
				"labels": map[string]interface{}{
					"app":            "vpc-dns",
					"vpc":            vpcUID,
					"dc-api/managed": "true",
				},
			},
			"data": map[string]interface{}{
				"Corefile": corefile,
			},
		},
	}
	if _, err := c.dynamic.Resource(configMapGVR()).Namespace(dnsBootstrapNS).Create(ctx, cm, metav1.CreateOptions{}); err != nil {
		if !k8serrors.IsAlreadyExists(err) {
			return fmt.Errorf("ensure vpc corefile cm: create ConfigMap %q: %w", cmName, err)
		}
		log.Debug().Str("cm", cmName).Msg("kubeovn: per-VPC Corefile ConfigMap already exists — skipping create")
	}
	return nil
}

// IsVpcDNSPresent returns true if the CoreDNS Deployment for vpcUID exists in
// kube-system. Used by the startup backfill loop to skip VPCs that already have
// their DNS pod.
func (c *Client) IsVpcDNSPresent(ctx context.Context, vpcUID string) (bool, error) {
	name := vpcDNSDeploymentName(vpcUID)
	_, err := c.dynamic.Resource(deploymentGVR).Namespace(dnsBootstrapNS).Get(ctx, name, metav1.GetOptions{})
	if k8serrors.IsNotFound(err) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("check vpc dns present (%s): get Deployment: %w", name, err)
	}
	return true, nil
}

// EnsureVpcDNS idempotently provisions the per-VPC CoreDNS pod and patches the
// subnet's dhcpV4Options to advertise it. Returns the pinned DNS pod IP so the
// caller can persist it on the VNet row.
//
// Parameters:
//   - vpcUID         : KubeOVN Vpc CRD name (BackendUID from the DB row)
//   - subnetCIDR     : tenant subnet CIDR, e.g. "10.77.1.0/24"
//   - subnetBackendUID: KubeOVN Subnet CRD name (the NAD name in the tenant NS)
//   - tenantNS       : Kubernetes namespace for the tenant ("dc-<tenantID>")
func (c *Client) EnsureVpcDNS(
	ctx context.Context,
	vpcUID string,
	subnetCIDR string,
	subnetBackendUID string,
	tenantNS string,
) (net.IP, error) {
	// Compute the pinned IP: <network>+2. Gateway is +1, NAT uses broadcast-1,
	// so +2 is always safe as long as the CIDR is at least a /29.
	dnsIP, err := computeDNSPodIP(subnetCIDR)
	if err != nil {
		return nil, fmt.Errorf("ensure vpc dns: compute dns pod IP for %q: %w", subnetCIDR, err)
	}

	// ── 0. Ensure the per-VPC Corefile ConfigMap exists before the Deployment
	// references it (otherwise the pod's ConfigMap volume mount would fail
	// transiently on first scheduling).
	if err := c.ensureVpcCorefileConfigMap(ctx, vpcUID); err != nil {
		return nil, fmt.Errorf("ensure vpc dns: %w", err)
	}

	deployName := vpcDNSDeploymentName(vpcUID)
	cmName := vpcDNSCorefileName(vpcUID)

	// ── 1. Create CoreDNS Deployment in kube-system ───────────────────────────
	// Gotcha 1 (from spike): annotation key format must be:
	//   <subnet-backend-uid>.<subnet-ns>.ovn.kubernetes.io/ip_address
	// NOT the generic "ovn.kubernetes.io/ip_address" (that form is ignored by
	// KubeOVN's CNI on Calico-primary clusters).
	ipAnnotationKey := subnetBackendUID + "." + tenantNS + ".ovn.kubernetes.io/ip_address"
	multusRef := tenantNS + "/" + subnetBackendUID

	deploy := &unstructured.Unstructured{
		Object: map[string]interface{}{
			"apiVersion": "apps/v1",
			"kind":       "Deployment",
			"metadata": map[string]interface{}{
				"name":      deployName,
				"namespace": dnsBootstrapNS,
				"labels": map[string]interface{}{
					"app":            "vpc-dns",
					"vpc":            vpcUID,
					"dc-api/managed": "true",
				},
			},
			"spec": map[string]interface{}{
				// Recreate strategy: a single CoreDNS pod per VPC. The ~5s outage
				// window on restarts is acceptable in M2; replicas:2 is the HA path.
				"strategy": map[string]interface{}{
					"type": "Recreate",
				},
				"replicas": int64(1),
				"selector": map[string]interface{}{
					"matchLabels": map[string]interface{}{
						"app": "vpc-dns",
						"vpc": vpcUID,
					},
				},
				"template": map[string]interface{}{
					"metadata": map[string]interface{}{
						"labels": map[string]interface{}{
							"app":            "vpc-dns",
							"vpc":            vpcUID,
							"dc-api/managed": "true",
						},
						"annotations": map[string]interface{}{
							// Multus attaches the secondary NIC on the tenant subnet.
							"k8s.v1.cni.cncf.io/networks": multusRef,
							// Per-provider IP pin — gotcha 1 above.
							ipAnnotationKey: dnsIP.String(),
						},
					},
					"spec": map[string]interface{}{
						"serviceAccountName":           dnsSAName,
						"automountServiceAccountToken": false,
						// Prioritise this pod alongside other cluster-critical add-ons.
						"priorityClassName": "system-cluster-critical",
						// Zero grace period: when this pod is deleted (subnet teardown path)
						// it must be removed immediately so KubeOVN can release the LSP from
						// the logical switch. CoreDNS has no in-flight state to flush.
						"terminationGracePeriodSeconds": int64(0),
						"tolerations": []interface{}{
							map[string]interface{}{
								"key":      "CriticalAddonsOnly",
								"operator": "Exists",
							},
						},
						"containers": []interface{}{
							map[string]interface{}{
								"name":  "coredns",
								"image": c.dnsImage(),
								"args":  []interface{}{"-conf", "/etc/coredns/Corefile"},
								"ports": []interface{}{
									map[string]interface{}{
										"containerPort": int64(53),
										"name":          "dns",
										"protocol":      "UDP",
									},
									map[string]interface{}{
										"containerPort": int64(53),
										"name":          "dns-tcp",
										"protocol":      "TCP",
									},
								},
								"resources": map[string]interface{}{
									"requests": map[string]interface{}{
										"cpu":    "100m",
										"memory": "70Mi",
									},
									"limits": map[string]interface{}{
										"memory": "170Mi",
									},
								},
								"securityContext": map[string]interface{}{
									"allowPrivilegeEscalation": false,
									"capabilities": map[string]interface{}{
										"add":  []interface{}{"NET_BIND_SERVICE"},
										"drop": []interface{}{"ALL"},
									},
									"readOnlyRootFilesystem": true,
								},
								"volumeMounts": []interface{}{
									map[string]interface{}{
										"name":      "config",
										"mountPath": "/etc/coredns",
										"readOnly":  true,
									},
								},
							},
						},
						"volumes": []interface{}{
							map[string]interface{}{
								"name": "config",
								"configMap": map[string]interface{}{
									"name": cmName,
									"items": []interface{}{
										map[string]interface{}{
											"key":  "Corefile",
											"path": "Corefile",
										},
									},
								},
							},
						},
					},
				},
			},
		},
	}

	if _, err := c.dynamic.Resource(deploymentGVR).Namespace(dnsBootstrapNS).Create(ctx, deploy, metav1.CreateOptions{}); err != nil {
		if !k8serrors.IsAlreadyExists(err) {
			return nil, fmt.Errorf("ensure vpc dns: create Deployment %q: %w", deployName, err)
		}
		log.Debug().Str("deploy", deployName).Msg("kubeovn: vpc-dns Deployment already exists — skipping create")
	}

	// ── 2. Patch subnet's dhcpV4Options to include dns_server ─────────────────
	// Gotcha 2 (from spike): OVN stores dns_server as a plain string value, not
	// set syntax. Write dns_server="<ip>", never dns_server={"<ip>"}.
	//
	// We compute the gateway as <network>+1 (matching KubeOVN's convention) to
	// build a correct dhcpV4Options string alongside the DNS entry.
	gwIP, err := networkFirstIP(subnetCIDR)
	if err != nil {
		return nil, fmt.Errorf("ensure vpc dns: compute gateway for dhcp patch: %w", err)
	}

	dhcpOptions := fmt.Sprintf("lease_time=3600,mtu=1400,router=%s,dns_server=%s", gwIP, dnsIP)

	// ── 2a. Read current spec.excludeIPs so we can add dnsPodIP without
	// dropping any existing entries (e.g. the gateway may already be there).
	// We do a Get→union→Patch rather than a blind overwrite so the operation is
	// fully idempotent: running EnsureVpcDNS twice on an already-bootstrapped
	// subnet is safe and produces no duplicate entries.
	subnetObj, err := c.dynamic.Resource(subnetGVR).Get(ctx, subnetBackendUID, metav1.GetOptions{})
	if err != nil {
		return nil, fmt.Errorf("ensure vpc dns: get Subnet %q for excludeIPs: %w", subnetBackendUID, err)
	}

	// Parse existing excludeIPs — it may be absent, nil, or a []interface{}.
	var existingExclude []string
	if rawExclude, _, _ := unstructured.NestedSlice(subnetObj.Object, "spec", "excludeIPs"); rawExclude != nil {
		for _, v := range rawExclude {
			if s, ok := v.(string); ok {
				existingExclude = append(existingExclude, s)
			}
		}
	}

	// Build the new excludeIPs list: existing ∪ {dnsPodIP}.
	dnsPodIPStr := dnsIP.String()
	newExclude := appendIfMissing(existingExclude, dnsPodIPStr)

	// Convert []string → []interface{} for the unstructured patch.
	excludeForPatch := make([]interface{}, len(newExclude))
	for i, ip := range newExclude {
		excludeForPatch[i] = ip
	}

	subnetPatch := map[string]interface{}{
		"spec": map[string]interface{}{
			"enableDHCP":    true,
			"dhcpV4Options": dhcpOptions,
			"excludeIPs":    excludeForPatch,
		},
	}
	patchBytes, err := marshalPatch(subnetPatch)
	if err != nil {
		return nil, fmt.Errorf("ensure vpc dns: marshal subnet dhcp patch: %w", err)
	}
	if _, err := c.dynamic.Resource(subnetGVR).Patch(ctx, subnetBackendUID, types.MergePatchType, patchBytes, metav1.PatchOptions{}); err != nil {
		return nil, fmt.Errorf("ensure vpc dns: patch Subnet %q dhcpV4Options+excludeIPs: %w", subnetBackendUID, err)
	}

	log.Info().
		Str("vpc", vpcUID).
		Str("dns_ip", dnsIP.String()).
		Str("subnet", subnetBackendUID).
		Strs("exclude_ips", newExclude).
		Msg("kubeovn: per-VPC CoreDNS deployed, DHCP patched, and DNS pod IP reserved in excludeIPs")
	return dnsIP, nil
}

// DeleteVpcDNS removes the per-VPC CoreDNS Deployment and Corefile ConfigMap
// for vpcUID from kube-system. Idempotent — NotFound on either is treated as
// success. The Multus secondary NIC is garbage-collected automatically when
// the pod terminates.
//
// Order matters: Deployment first so the pod (and its mount on the ConfigMap)
// terminates before we remove the ConfigMap. Deleting the CM first would
// leave the still-running pod's mount stale until eviction.
func (c *Client) DeleteVpcDNS(ctx context.Context, vpcUID string) error {
	deployName := vpcDNSDeploymentName(vpcUID)
	if err := c.dynamic.Resource(deploymentGVR).Namespace(dnsBootstrapNS).Delete(ctx, deployName, metav1.DeleteOptions{}); err != nil {
		if !k8serrors.IsNotFound(err) {
			return fmt.Errorf("delete vpc dns: delete Deployment %q: %w", deployName, err)
		}
		log.Debug().Str("vpc", vpcUID).Str("deploy", deployName).Msg("kubeovn: vpc-dns Deployment not found — nothing to delete (idempotent)")
	} else {
		log.Info().Str("vpc", vpcUID).Str("deploy", deployName).Msg("kubeovn: per-VPC CoreDNS Deployment deleted")
	}

	cmName := vpcDNSCorefileName(vpcUID)
	if err := c.dynamic.Resource(configMapGVR()).Namespace(dnsBootstrapNS).Delete(ctx, cmName, metav1.DeleteOptions{}); err != nil {
		if !k8serrors.IsNotFound(err) {
			return fmt.Errorf("delete vpc dns: delete ConfigMap %q: %w", cmName, err)
		}
		log.Debug().Str("vpc", vpcUID).Str("cm", cmName).Msg("kubeovn: per-VPC Corefile ConfigMap not found — nothing to delete (idempotent)")
	} else {
		log.Info().Str("vpc", vpcUID).Str("cm", cmName).Msg("kubeovn: per-VPC Corefile ConfigMap deleted")
	}
	return nil
}

// WaitVpcDNSPodsGone polls kube-system until all pods with labels
// app=vpc-dns,vpc=<vpcUID> are gone, or until timeout expires.
//
// Called by the subnet-delete goroutine after DeleteVpcDNS to ensure the
// CoreDNS pod's Multus secondary NIC (which holds a KubeOVN LSP on the
// tenant subnet's logical switch) has been released before we attempt to
// delete the subnet CRD. If the pod is already absent the function returns
// immediately (idempotent / happy path).
//
// If the poll times out, the error is returned but the caller should treat
// it as a warning — the subsequent DeleteSubnet will either succeed anyway
// (KubeOVN cleaned up in the background) or fail with a retryable error.
func (c *Client) WaitVpcDNSPodsGone(ctx context.Context, vpcUID string, timeout time.Duration) error {
	selector := fmt.Sprintf("app=vpc-dns,vpc=%s", vpcUID)
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		list, err := c.dynamic.Resource(podGVR).Namespace(dnsBootstrapNS).List(ctx, metav1.ListOptions{
			LabelSelector: selector,
		})
		if err == nil && len(list.Items) == 0 {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(2 * time.Second):
		}
	}
	return fmt.Errorf("wait vpc dns pods gone: pods matching %q in %s still present after %s", selector, dnsBootstrapNS, timeout)
}

// ── Name derivation helpers ───────────────────────────────────────────────────

// VpcDNSDeploymentName returns the stable Deployment name for a VPC's CoreDNS
// pod. Exported so integration tests can derive the same name without duplicating
// the hash logic.
//
// The Deployment name budget is 63 chars (Kubernetes metadata.name limit).
// "vpc-dns-" prefix = 8 chars, leaving 55 for the vpcUID. Long VPC UIDs that
// exceed this are hashed with SHA-1 (12 hex chars → safe at 20 total).
func VpcDNSDeploymentName(vpcUID string) string { return vpcDNSDeploymentName(vpcUID) }

func vpcDNSDeploymentName(vpcUID string) string {
	const prefix = "vpc-dns-"
	if len(prefix+vpcUID) <= 63 {
		return prefix + vpcUID
	}
	sum := sha1.Sum([]byte(vpcUID))
	return prefix + hex.EncodeToString(sum[:6]) // 12 hex chars → "vpc-dns-XXXXXXXXXXXX" = 20 chars
}

// VpcDNSCorefileName returns the stable per-VPC Corefile ConfigMap name.
// Exported so integration tests can derive the same name.
//
// Prefix budget: "vpc-dns-corefile-" = 17 chars, leaving 46 for the vpcUID.
// Long vpcUIDs are hashed (same SHA-1-12-hex fallback as the Deployment).
func VpcDNSCorefileName(vpcUID string) string { return vpcDNSCorefileName(vpcUID) }

func vpcDNSCorefileName(vpcUID string) string {
	const prefix = "vpc-dns-corefile-"
	if len(prefix+vpcUID) <= 63 {
		return prefix + vpcUID
	}
	sum := sha1.Sum([]byte(vpcUID))
	return prefix + hex.EncodeToString(sum[:6])
}

// ── IP helpers ────────────────────────────────────────────────────────────────

// appendIfMissing returns s with elem appended only if elem is not already
// present. Preserves existing order. Used to build the excludeIPs union
// without duplicating an entry that is already reserved.
func appendIfMissing(s []string, elem string) []string {
	for _, v := range s {
		if v == elem {
			return s
		}
	}
	return append(s, elem)
}

// computeDNSPodIP returns <network>+2 for the given CIDR.
// Gateway is conventionally +1; F15 uses broadcast-1 for NAT; +2 is safe and
// distinct from both. Example: 10.77.1.0/24 → 10.77.1.2.
func computeDNSPodIP(cidr string) (net.IP, error) {
	_, ipNet, err := net.ParseCIDR(cidr)
	if err != nil {
		return nil, fmt.Errorf("parse CIDR %q: %w", cidr, err)
	}
	network := ipNet.IP.To4()
	if network == nil {
		return nil, fmt.Errorf("only IPv4 CIDRs supported (got %q)", cidr)
	}

	// +2: clone, then increment twice.
	ip := cloneIP(network)
	incrementIPv4Slice(ip) // +1 (gateway)
	incrementIPv4Slice(ip) // +2 (DNS pod)

	// Sanity: ensure it stays within the network (rejects /31 and /32).
	if !ipNet.Contains(ip) {
		return nil, fmt.Errorf("CIDR %q is too small to fit a DNS pod IP", cidr)
	}
	return ip, nil
}

// networkFirstIP returns <network>+1 (the gateway IP convention) for a CIDR.
// Used when constructing the dhcpV4Options router= field.
func networkFirstIP(cidr string) (net.IP, error) {
	_, ipNet, err := net.ParseCIDR(cidr)
	if err != nil {
		return nil, fmt.Errorf("parse CIDR %q: %w", cidr, err)
	}
	network := ipNet.IP.To4()
	if network == nil {
		return nil, fmt.Errorf("only IPv4 CIDRs supported (got %q)", cidr)
	}
	gw := cloneIP(network)
	incrementIPv4Slice(gw)
	return gw, nil
}

// AutoDetectCoreDNSImage inspects the cluster's existing CoreDNS Deployment
// (typically "coredns" in "kube-system") and returns its container image.
// Falls back to dnsFallbackImage if the Deployment is not found or the
// image cannot be read.
//
// Call this once at startup, before EnsureVpcDNSBootstrap, and set the result
// via WithDNSConfig. This avoids hardcoding a version that may drift from what
// the cluster already runs.
func (c *Client) AutoDetectCoreDNSImage(ctx context.Context) string {
	// Try the two common CoreDNS deployment names.
	for _, name := range []string{"coredns", "rke2-coredns-coredns"} {
		obj, err := c.dynamic.Resource(deploymentGVR).Namespace(dnsBootstrapNS).Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			continue
		}
		containers, _, _ := unstructured.NestedSlice(obj.Object, "spec", "template", "spec", "containers")
		for _, ci := range containers {
			cm, ok := ci.(map[string]interface{})
			if !ok {
				continue
			}
			if img, ok := cm["image"].(string); ok && strings.Contains(img, "coredns") {
				log.Info().Str("image", img).Str("from_deploy", name).Msg("kubeovn: auto-detected CoreDNS image")
				return img
			}
		}
	}
	log.Warn().Str("fallback", dnsFallbackImage).Msg("kubeovn: could not auto-detect CoreDNS image — using fallback")
	return dnsFallbackImage
}
