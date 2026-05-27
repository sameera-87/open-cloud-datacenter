// Package render produces Kubernetes manifests for OpenBao deployments owned
// by a KeyVaultBackend CR. The shapes mirror the openbao-helm chart so a
// spec-diff against a Helm-installed reference stays small.
package render

import (
	keyvaultv1alpha1 "github.com/wso2/keyvault-operator/api/v1alpha1"
)

// All child object names derive from the Backend CR's metadata.name —
// equivalent to Helm's releaseName. Operators see e.g. `kvb-acme-vault`
// and immediately see which Backend owns the StatefulSet.

func StatefulSetName(cr *keyvaultv1alpha1.KeyVaultBackend) string { return cr.Name }
func ConfigMapName(cr *keyvaultv1alpha1.KeyVaultBackend) string   { return cr.Name + "-config" }
func ServiceAccountName(cr *keyvaultv1alpha1.KeyVaultBackend) string {
	return cr.Name
}
func ServiceRegularName(cr *keyvaultv1alpha1.KeyVaultBackend) string  { return cr.Name }
func ServiceActiveName(cr *keyvaultv1alpha1.KeyVaultBackend) string   { return cr.Name + "-active" }
func ServiceStandbyName(cr *keyvaultv1alpha1.KeyVaultBackend) string  { return cr.Name + "-standby" }
func ServiceInternalName(cr *keyvaultv1alpha1.KeyVaultBackend) string { return cr.Name + "-internal" }
