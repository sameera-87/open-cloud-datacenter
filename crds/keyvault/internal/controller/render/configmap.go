package render

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	keyvaultv1alpha1 "github.com/wso2/keyvault-operator/api/v1alpha1"
)

// baseOpenbaoHCL is the OpenBao server config rendered into the ConfigMap. The
// container's entrypoint sed-substitutes ${HOST_IP}/${POD_IP}/${HOSTNAME}
// into this file at startup, so any pod-specific values stay as
// placeholders here. Shape mirrors the openbao-helm chart's
// extraconfig-from-values.hcl with one addition: an optional `audit "file"`
// block declared at boot (OpenBao refuses runtime sys/audit API enable as a
// hardening policy — audit config must be declarative).
const baseOpenbaoHCL = `
ui = true

listener "tcp" {
  tls_disable = 1
  address = "[::]:8200"
  cluster_address = "[::]:8201"
}

storage "raft" {
  path = "/openbao/data"
}

service_registration "kubernetes" {}
`

// renderHCL returns the full HCL config for this Backend, including an
// audit "file" block when spec.engineConfig.auditEnabled is true (the
// default). Path defaults to /openbao/audit/audit.log; ensure the
// StatefulSet has a writable mount at that directory.
func renderHCL(cr *keyvaultv1alpha1.KeyVaultBackend) string {
	auditEnabled := cr.Spec.EngineConfig.AuditEnabled == nil || *cr.Spec.EngineConfig.AuditEnabled
	if !auditEnabled {
		return baseOpenbaoHCL
	}
	logPath := cr.Spec.EngineConfig.AuditLogPath
	if logPath == "" {
		logPath = "/openbao/audit/audit.log"
	}
	// OpenBao's HCL parser requires explicit `type` AND `path` fields in
	// the audit block; the block-label syntax that Vault accepts (where
	// the label implicitly becomes the path) is not honoured. `path` is
	// what shows in `bao audit list`. Keeping path == "file" matches the
	// conventional default.
	auditBlock := fmt.Sprintf(`
audit "file" {
  type = "file"
  path = "file"
  options = {
    file_path = %q
  }
}
`, logPath)
	return baseOpenbaoHCL + auditBlock
}

// HCLChecksum returns a short SHA-256 hex of the rendered HCL. Used as a
// pod-template annotation so a ConfigMap-content change forces the
// StatefulSet to roll. StatefulSet does NOT auto-restart pods when a
// referenced ConfigMap changes; this is the standard workaround.
func HCLChecksum(cr *keyvaultv1alpha1.KeyVaultBackend) string {
	sum := sha256.Sum256([]byte(renderHCL(cr)))
	return hex.EncodeToString(sum[:])[:16]
}

// ConfigMap renders the openbao-config ConfigMap. One file:
// extraconfig-from-values.hcl, consumed by the pod entrypoint.
func ConfigMap(cr *keyvaultv1alpha1.KeyVaultBackend) *corev1.ConfigMap {
	return &corev1.ConfigMap{
		TypeMeta: metav1.TypeMeta{
			APIVersion: "v1",
			Kind:       "ConfigMap",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:      ConfigMapName(cr),
			Namespace: cr.Namespace,
			Labels:    AllLabels(cr),
		},
		Data: map[string]string{
			"extraconfig-from-values.hcl": renderHCL(cr),
		},
	}
}

