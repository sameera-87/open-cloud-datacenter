package render

import (
	keyvaultv1alpha1 "github.com/wso2/keyvault-operator/api/v1alpha1"
)

const (
	// AppName is the workload identifier set on app.kubernetes.io/name
	// across every child resource. Mirrors the openbao-helm chart's value
	// so spec-diffs against a Helm reference stay small.
	AppName = "openbao"
	// ComponentServer marks pods that run the OpenBao server (vs the
	// injector or any other future sibling component the chart ships).
	ComponentServer = "server"
)

// CommonLabels are stamped on every child resource (StatefulSet, Services,
// ConfigMap, ServiceAccount). The CR's own dc-api.wso2.com/* labels are
// propagated by ApplyDCAPILabels — kept separate so the helm-style block
// always wins for the workload identifiers and the dc-api block is
// additive only.
func CommonLabels(cr *keyvaultv1alpha1.KeyVaultBackend) map[string]string {
	return map[string]string{
		"app.kubernetes.io/name":       AppName,
		"app.kubernetes.io/instance":   cr.Name,
		"app.kubernetes.io/managed-by": "kvi-controller",
	}
}

// PodLabels = CommonLabels + component:server. Used on the StatefulSet's
// pod template AND in selectors so the regular + internal services target
// every pod.
func PodLabels(cr *keyvaultv1alpha1.KeyVaultBackend) map[string]string {
	out := CommonLabels(cr)
	out["component"] = ComponentServer
	return out
}

// SelectorLabels are the immutable subset used in Service selectors and
// the StatefulSet's spec.selector.matchLabels. Must stay stable across
// reconciles — adding labels to PodLabels is safe, removing or renaming
// these is not.
func SelectorLabels(cr *keyvaultv1alpha1.KeyVaultBackend) map[string]string {
	return map[string]string{
		"app.kubernetes.io/name":     AppName,
		"app.kubernetes.io/instance": cr.Name,
		"component":                  ComponentServer,
	}
}

// ApplyDCAPILabels copies every dc-api.wso2.com/* label from the CR's
// metadata onto a target label map (mutating it). The Backend CR is
// stamped with these labels at creation time by dc-api per the
// integration contract; propagating them satisfies the contract's
// "labels MUST propagate to every child resource" rule.
func ApplyDCAPILabels(cr *keyvaultv1alpha1.KeyVaultBackend, target map[string]string) {
	for k, v := range cr.Labels {
		if len(k) >= len("dc-api.wso2.com/") && k[:len("dc-api.wso2.com/")] == "dc-api.wso2.com/" {
			target[k] = v
		}
	}
}

// AllLabels merges CommonLabels + dc-api.wso2.com/* labels for use on
// metadata of cluster-scoped-looking objects (Services, ConfigMap, SA).
func AllLabels(cr *keyvaultv1alpha1.KeyVaultBackend) map[string]string {
	out := CommonLabels(cr)
	ApplyDCAPILabels(cr, out)
	return out
}
