package render

import (
	"fmt"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	keyvaultv1alpha1 "github.com/wso2/keyvault-operator/api/v1alpha1"
)

const (
	openbaoImage          = "quay.io/openbao/openbao:2.5.3"
	defaultStorageClass   = "longhorn"
	defaultHAReplicas     = 3
	defaultUserID         = int64(100)
	defaultGroupID        = int64(1000)
	terminationGracePeriod = int64(10)
)

// entrypointScript mirrors the openbao-helm chart entrypoint. It
// sed-substitutes pod-specific env vars into the config file (sourced
// from the ConfigMap) and then exec's the OpenBao server. Keeping it
// identical to the chart's script keeps the spec-diff against a Helm
// reference small.
const entrypointScript = `cp /openbao/config/extraconfig-from-values.hcl /tmp/storageconfig.hcl;
[ -n "${HOST_IP}" ] && sed -Ei "s|HOST_IP|${HOST_IP?}|g" /tmp/storageconfig.hcl;
[ -n "${POD_IP}" ] && sed -Ei "s|POD_IP|${POD_IP?}|g" /tmp/storageconfig.hcl;
[ -n "${HOSTNAME}" ] && sed -Ei "s|HOSTNAME|${HOSTNAME?}|g" /tmp/storageconfig.hcl;
[ -n "${API_ADDR}" ] && sed -Ei "s|API_ADDR|${API_ADDR?}|g" /tmp/storageconfig.hcl;
[ -n "${TRANSIT_ADDR}" ] && sed -Ei "s|TRANSIT_ADDR|${TRANSIT_ADDR?}|g" /tmp/storageconfig.hcl;
[ -n "${RAFT_ADDR}" ] && sed -Ei "s|RAFT_ADDR|${RAFT_ADDR?}|g" /tmp/storageconfig.hcl;
/usr/local/bin/docker-entrypoint.sh bao server -config=/tmp/storageconfig.hcl
`

// StatefulSet renders the OpenBao HA StatefulSet for a KeyVaultBackend.
// Replicas, per-pod resources, and storage come from the CR spec.
func StatefulSet(cr *keyvaultv1alpha1.KeyVaultBackend) *appsv1.StatefulSet {
	replicas := int32(defaultHAReplicas)
	if cr.Spec.EngineConfig.HAReplicas > 0 {
		replicas = int32(cr.Spec.EngineConfig.HAReplicas)
	}

	storageClass := defaultStorageClass
	if cr.Spec.EngineConfig.StorageClass != "" {
		storageClass = cr.Spec.EngineConfig.StorageClass
	}

	storageGB := cr.Spec.StorageGB
	if storageGB < 1 {
		storageGB = 2
	}

	// Per-pod resources = total / replicas (rounded down for CPU,
	// floor 1 GiB for memory). Set both requests and limits to the
	// same values — OpenBao is latency-sensitive, throttling hurts.
	perPodCPU := divideQuantity(cr.Spec.CPU, int64(replicas))
	perPodMemory := resource.MustParse(fmt.Sprintf("%dGi", maxInt(cr.Spec.MemoryGB/int(replicas), 1)))

	podLabels := PodLabels(cr)
	ApplyDCAPILabels(cr, podLabels)

	return &appsv1.StatefulSet{
		TypeMeta: metav1.TypeMeta{
			APIVersion: "apps/v1",
			Kind:       "StatefulSet",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:      StatefulSetName(cr),
			Namespace: cr.Namespace,
			Labels:    AllLabels(cr),
		},
		Spec: appsv1.StatefulSetSpec{
			Replicas:            ptr(replicas),
			ServiceName:         ServiceInternalName(cr),
			PodManagementPolicy: appsv1.OrderedReadyPodManagement,
			UpdateStrategy: appsv1.StatefulSetUpdateStrategy{
				Type: appsv1.OnDeleteStatefulSetStrategyType,
			},
			PersistentVolumeClaimRetentionPolicy: &appsv1.StatefulSetPersistentVolumeClaimRetentionPolicy{
				WhenDeleted: appsv1.RetainPersistentVolumeClaimRetentionPolicyType,
				WhenScaled:  appsv1.RetainPersistentVolumeClaimRetentionPolicyType,
			},
			Selector: &metav1.LabelSelector{
				MatchLabels: SelectorLabels(cr),
			},
			VolumeClaimTemplates: []corev1.PersistentVolumeClaim{
				{
					ObjectMeta: metav1.ObjectMeta{Name: "data"},
					Spec: corev1.PersistentVolumeClaimSpec{
						AccessModes: []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce},
						Resources: corev1.VolumeResourceRequirements{
							Requests: corev1.ResourceList{
								corev1.ResourceStorage: resource.MustParse(fmt.Sprintf("%dGi", storageGB)),
							},
						},
						StorageClassName: ptr(storageClass),
					},
				},
			},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels: podLabels,
					Annotations: map[string]string{
						// HCL checksum forces a rolling restart when the
						// rendered ConfigMap content changes (audit-enable
						// toggle, audit path edit). StatefulSet does NOT
						// auto-restart pods on ConfigMap changes; this is
						// the standard workaround.
						"keyvault.opencloud.wso2.com/hcl-checksum": HCLChecksum(cr),
					},
				},
				Spec: corev1.PodSpec{
					ServiceAccountName:            ServiceAccountName(cr),
					TerminationGracePeriodSeconds: ptr(terminationGracePeriod),
					SecurityContext: &corev1.PodSecurityContext{
						FSGroup:      ptr(defaultGroupID),
						RunAsGroup:   ptr(defaultGroupID),
						RunAsUser:    ptr(defaultUserID),
						RunAsNonRoot: ptr(true),
						SeccompProfile: &corev1.SeccompProfile{
							Type: corev1.SeccompProfileTypeRuntimeDefault,
						},
					},
					Volumes: []corev1.Volume{
						{
							Name: "config",
							VolumeSource: corev1.VolumeSource{
								ConfigMap: &corev1.ConfigMapVolumeSource{
									LocalObjectReference: corev1.LocalObjectReference{
										Name: ConfigMapName(cr),
									},
									DefaultMode: ptr(int32(0o644)),
								},
							},
						},
						{Name: "home", VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}}},
						// audit holds the file audit device's log. EmptyDir is
						// per-pod and ephemeral on pod restart; for durable
						// audit retention stream this to a SIEM via a sidecar
						// (tracked as a follow-up).
						{Name: "audit", VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}}},
					},
					Containers: []corev1.Container{
						{
							Name:            "openbao",
							Image:           openbaoImage,
							ImagePullPolicy: corev1.PullIfNotPresent,
							Command:         []string{"/bin/sh", "-ec"},
							Args:            []string{entrypointScript},
							Env:             podEnv(cr),
							Ports: []corev1.ContainerPort{
								{Name: "http", ContainerPort: 8200, Protocol: corev1.ProtocolTCP},
								{Name: "https-internal", ContainerPort: 8201, Protocol: corev1.ProtocolTCP},
								{Name: "http-rep", ContainerPort: 8202, Protocol: corev1.ProtocolTCP},
							},
							ReadinessProbe: &corev1.Probe{
								ProbeHandler: corev1.ProbeHandler{
									Exec: &corev1.ExecAction{
										Command: []string{"/bin/sh", "-ec", "bao status -tls-skip-verify"},
									},
								},
								InitialDelaySeconds: 5,
								PeriodSeconds:       5,
								TimeoutSeconds:      3,
								FailureThreshold:    2,
								SuccessThreshold:    1,
							},
							Lifecycle: &corev1.Lifecycle{
								PreStop: &corev1.LifecycleHandler{
									Exec: &corev1.ExecAction{
										Command: []string{"/bin/sh", "-c", "sleep 5 && kill -SIGTERM $(pidof bao)"},
									},
								},
							},
							SecurityContext: &corev1.SecurityContext{
								AllowPrivilegeEscalation: ptr(false),
							},
							Resources: corev1.ResourceRequirements{
								Requests: corev1.ResourceList{
									corev1.ResourceCPU:    perPodCPU,
									corev1.ResourceMemory: perPodMemory,
								},
								Limits: corev1.ResourceList{
									corev1.ResourceCPU:    perPodCPU,
									corev1.ResourceMemory: perPodMemory,
								},
							},
							VolumeMounts: []corev1.VolumeMount{
								{Name: "data", MountPath: "/openbao/data"},
								{Name: "config", MountPath: "/openbao/config"},
								{Name: "home", MountPath: "/home/openbao"},
								{Name: "audit", MountPath: "/openbao/audit"},
							},
						},
					},
				},
			},
		},
	}
}

func podEnv(cr *keyvaultv1alpha1.KeyVaultBackend) []corev1.EnvVar {
	return []corev1.EnvVar{
		{Name: "HOST_IP", ValueFrom: &corev1.EnvVarSource{FieldRef: &corev1.ObjectFieldSelector{APIVersion: "v1", FieldPath: "status.hostIP"}}},
		{Name: "POD_IP", ValueFrom: &corev1.EnvVarSource{FieldRef: &corev1.ObjectFieldSelector{APIVersion: "v1", FieldPath: "status.podIP"}}},
		{Name: "BAO_K8S_POD_NAME", ValueFrom: &corev1.EnvVarSource{FieldRef: &corev1.ObjectFieldSelector{APIVersion: "v1", FieldPath: "metadata.name"}}},
		{Name: "BAO_K8S_NAMESPACE", ValueFrom: &corev1.EnvVarSource{FieldRef: &corev1.ObjectFieldSelector{APIVersion: "v1", FieldPath: "metadata.namespace"}}},
		{Name: "BAO_ADDR", Value: "http://127.0.0.1:8200"},
		{Name: "BAO_API_ADDR", Value: "http://$(POD_IP):8200"},
		{Name: "SKIP_CHOWN", Value: "true"},
		{Name: "SKIP_SETCAP", Value: "true"},
		{Name: "HOSTNAME", ValueFrom: &corev1.EnvVarSource{FieldRef: &corev1.ObjectFieldSelector{APIVersion: "v1", FieldPath: "metadata.name"}}},
		{Name: "BAO_CLUSTER_ADDR", Value: fmt.Sprintf("https://$(HOSTNAME).%s:8201", ServiceInternalName(cr))},
		{Name: "HOME", Value: "/home/openbao"},
	}
}

// divideQuantity returns q/divisor as a Kubernetes Quantity.
// Used to split a total CPU spec across pods.
func divideQuantity(q resource.Quantity, divisor int64) resource.Quantity {
	if divisor < 1 {
		divisor = 1
	}
	milli := q.MilliValue() / divisor
	if milli < 100 {
		milli = 100 // floor 100m per pod
	}
	return *resource.NewMilliQuantity(milli, resource.DecimalSI)
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}
