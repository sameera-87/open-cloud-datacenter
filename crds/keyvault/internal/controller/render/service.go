package render

import (
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"

	keyvaultv1alpha1 "github.com/wso2/keyvault-operator/api/v1alpha1"
)

// Three Services exist per Backend:
//
//   - Regular     — picks all pods. Used for non-leader-aware clients.
//   - Active      — selects only pods labeled openbao-active=true.
//                   service_registration "kubernetes" sets that label on
//                   the active Raft leader.
//   - Standby     — selects only openbao-standby=true labelled pods.
//   - Internal    — headless (clusterIP None) Service backing the
//                   StatefulSet (spec.serviceName).
//
// The active/standby Services need publishNotReadyAddresses=true so the
// active pod is discoverable even briefly during leader transition.

const httpPort = 8200
const clusterPort = 8201

func basePorts() []corev1.ServicePort {
	return []corev1.ServicePort{
		{
			Name:        "http",
			Port:        httpPort,
			TargetPort:  intstr.FromInt(httpPort),
			Protocol:    corev1.ProtocolTCP,
			AppProtocol: ptr("HTTP"),
		},
		{
			Name:       "https-internal",
			Port:       clusterPort,
			TargetPort: intstr.FromInt(clusterPort),
			Protocol:   corev1.ProtocolTCP,
		},
	}
}

func newService(cr *keyvaultv1alpha1.KeyVaultBackend, name string, extraLabels, extraSelector map[string]string, clusterIP string) *corev1.Service {
	labels := AllLabels(cr)
	for k, v := range extraLabels {
		labels[k] = v
	}
	selector := SelectorLabels(cr)
	for k, v := range extraSelector {
		selector[k] = v
	}
	return &corev1.Service{
		TypeMeta: metav1.TypeMeta{
			APIVersion: "v1",
			Kind:       "Service",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: cr.Namespace,
			Labels:    labels,
		},
		Spec: corev1.ServiceSpec{
			Type:                     corev1.ServiceTypeClusterIP,
			ClusterIP:                clusterIP,
			Ports:                    basePorts(),
			Selector:                 selector,
			PublishNotReadyAddresses: true,
		},
	}
}

// RegularService — selects all OpenBao pods regardless of leader state.
func RegularService(cr *keyvaultv1alpha1.KeyVaultBackend) *corev1.Service {
	return newService(cr, ServiceRegularName(cr), nil, nil, "")
}

// ActiveService — only the elected Raft leader. Use this for write traffic.
func ActiveService(cr *keyvaultv1alpha1.KeyVaultBackend) *corev1.Service {
	return newService(cr,
		ServiceActiveName(cr),
		map[string]string{"openbao-active": "true"},
		map[string]string{"openbao-active": "true"},
		"")
}

// StandbyService — only the non-leader (forwarding) pods.
func StandbyService(cr *keyvaultv1alpha1.KeyVaultBackend) *corev1.Service {
	return newService(cr,
		ServiceStandbyName(cr),
		map[string]string{"openbao-standby": "true"},
		map[string]string{"openbao-standby": "true"},
		"")
}

// InternalService — headless. StatefulSet uses this for pod DNS
// (openbao-N.<svc>.<ns>.svc.cluster.local) so Raft peers can address
// each other by name.
func InternalService(cr *keyvaultv1alpha1.KeyVaultBackend) *corev1.Service {
	return newService(cr,
		ServiceInternalName(cr),
		map[string]string{"openbao-internal": "true"},
		nil,
		corev1.ClusterIPNone)
}

func ptr[T any](v T) *T { return &v }
