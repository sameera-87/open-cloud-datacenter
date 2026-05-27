package render

import (
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	keyvaultv1alpha1 "github.com/wso2/keyvault-operator/api/v1alpha1"
)

// ServiceAccount renders the SA the StatefulSet's pods run as. OpenBao's
// service_registration "kubernetes" block requires the pod identity to
// have Patch access on its own Pod object so it can set the
// openbao-active / openbao-standby label that the active/standby Services
// select on. RBAC for that is set up separately in config/rbac/.
func ServiceAccount(cr *keyvaultv1alpha1.KeyVaultBackend) *corev1.ServiceAccount {
	return &corev1.ServiceAccount{
		TypeMeta: metav1.TypeMeta{
			APIVersion: "v1",
			Kind:       "ServiceAccount",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:      ServiceAccountName(cr),
			Namespace: cr.Namespace,
			Labels:    AllLabels(cr),
		},
	}
}
