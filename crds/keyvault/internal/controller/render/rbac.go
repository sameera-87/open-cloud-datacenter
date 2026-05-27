package render

import (
	rbacv1 "k8s.io/api/rbac/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	keyvaultv1alpha1 "github.com/wso2/keyvault-operator/api/v1alpha1"
)

// PodPatchRole grants the OpenBao pod's ServiceAccount permission to GET +
// PATCH its own Pod object. OpenBao's `service_registration "kubernetes"`
// block uses this to stamp the openbao-active / openbao-standby labels on
// the pod so the Active / Standby Services can select the leader.
//
// Without these labels even an unsealed, leader-elected pod won't have any
// endpoints in the active Service — clients dialing it would see no
// upstreams. Caught on 2026-05-21 spike-resume run: pod was up but
// service_registration kept logging "403 Forbidden" against the Pods API.
//
// Scoped to the Backend's namespace. resourceNames is omitted because the
// pod's name is templated by the StatefulSet (foo-0 / foo-1 / foo-2); we
// rely on namespace isolation as the boundary.
func PodPatchRole(cr *keyvaultv1alpha1.KeyVaultBackend) *rbacv1.Role {
	return &rbacv1.Role{
		TypeMeta: metav1.TypeMeta{
			APIVersion: "rbac.authorization.k8s.io/v1",
			Kind:       "Role",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:      cr.Name + "-pod-patch",
			Namespace: cr.Namespace,
			Labels:    AllLabels(cr),
		},
		Rules: []rbacv1.PolicyRule{
			{
				APIGroups: []string{""},
				Resources: []string{"pods"},
				Verbs:     []string{"get", "patch", "update"},
			},
		},
	}
}

// PodPatchRoleBinding binds the Backend's ServiceAccount to the
// PodPatchRole above.
func PodPatchRoleBinding(cr *keyvaultv1alpha1.KeyVaultBackend) *rbacv1.RoleBinding {
	return &rbacv1.RoleBinding{
		TypeMeta: metav1.TypeMeta{
			APIVersion: "rbac.authorization.k8s.io/v1",
			Kind:       "RoleBinding",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:      cr.Name + "-pod-patch",
			Namespace: cr.Namespace,
			Labels:    AllLabels(cr),
		},
		Subjects: []rbacv1.Subject{
			{
				Kind:      "ServiceAccount",
				Name:      ServiceAccountName(cr),
				Namespace: cr.Namespace,
			},
		},
		RoleRef: rbacv1.RoleRef{
			APIGroup: "rbac.authorization.k8s.io",
			Kind:     "Role",
			Name:     cr.Name + "-pod-patch",
		},
	}
}
