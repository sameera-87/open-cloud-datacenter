package harvester

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	harvesterhciov1beta1 "github.com/harvester/harvester/pkg/apis/harvesterhci.io/v1beta1"
	harvesterfake "github.com/harvester/harvester/pkg/generated/clientset/versioned/fake"
	monitoringv1 "github.com/prometheus-operator/prometheus-operator/pkg/apis/monitoring/v1"
	dbaasv1 "github.com/wso2/open-cloud-datacenter/crds/dbaas/api/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	kubefake "k8s.io/client-go/kubernetes/fake"
	clienttesting "k8s.io/client-go/testing"
	kubevirtv1 "kubevirt.io/api/core/v1"
)

func TestTypedCreatePostgresVMDoesNotCreateSecretWhenImageResolutionFails(t *testing.T) {
	ctx := context.Background()
	client := newTestTypedClient()

	_, credSecretName, cloudInitSecretName, _, err := client.CreatePostgresVM(ctx, testVMCreateParams())
	if err == nil {
		t.Fatalf("CreatePostgresVM returned nil error, want image resolution error")
	}
	if _, err := client.KubeClient.CoreV1().Secrets("tenant-a").Get(ctx, credSecretName, metav1.GetOptions{}); !apierrors.IsNotFound(err) {
		t.Fatalf("credentials Secret should not exist after image-resolution failure, got: %v", err)
	}
	if _, err := client.KubeClient.CoreV1().Secrets("tenant-a").Get(ctx, cloudInitSecretName, metav1.GetOptions{}); !apierrors.IsNotFound(err) {
		t.Fatalf("cloudinit Secret should not exist after image-resolution failure, got: %v", err)
	}
}

func TestTypedCreateDataVolumeReservesHarvesterDataPVCNameAndResizeUpdatesVMTemplate(t *testing.T) {
	ctx := context.Background()
	client := newTestTypedClient(testTypedVMImage())

	dvName, err := client.CreateDataVolume(ctx, "orders", "tenant-a", 10, "harvester-longhorn")
	if err != nil {
		t.Fatalf("CreateDataVolume returned error: %v", err)
	}
	if dvName != "pg-orders-data" {
		t.Fatalf("DataVolume name = %q, want pg-orders-data", dvName)
	}
	params := testVMCreateParams()
	params.DataVolumeRef = dvName
	if _, _, _, _, err := client.CreatePostgresVM(ctx, params); err != nil {
		t.Fatalf("CreatePostgresVM returned error: %v", err)
	}
	vm, err := client.Clientset.KubevirtV1().VirtualMachines("tenant-a").Get(ctx, "pg-orders", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("get VM: %v", err)
	}
	templates, err := typedVolumeClaimTemplates(vm)
	if err != nil {
		t.Fatalf("volume claim templates: %v", err)
	}
	dataTemplate := findPVCTemplate(templates, dvName)
	if dataTemplate == nil {
		t.Fatalf("data PVC template %s not found", dvName)
	}
	storage := dataTemplate.Spec.Resources.Requests[corev1.ResourceStorage]
	if got := storage.String(); got != "20Gi" {
		t.Fatalf("Data PVC template storage = %q, want 20Gi", got)
	}

	if err := client.ResizeDataVolume(ctx, "tenant-a", dvName, 30); err != nil {
		t.Fatalf("ResizeDataVolume returned error: %v", err)
	}
	vm, err = client.Clientset.KubevirtV1().VirtualMachines("tenant-a").Get(ctx, "pg-orders", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("get resized VM: %v", err)
	}
	templates, err = typedVolumeClaimTemplates(vm)
	if err != nil {
		t.Fatalf("resized volume claim templates: %v", err)
	}
	dataTemplate = findPVCTemplate(templates, dvName)
	if dataTemplate == nil {
		t.Fatalf("data PVC template %s not found after resize", dvName)
	}
	storage = dataTemplate.Spec.Resources.Requests[corev1.ResourceStorage]
	if got := storage.String(); got != "30Gi" {
		t.Fatalf("Data PVC template storage after resize = %q, want 30Gi", got)
	}
}

func TestTypedCreatePostgresVMCreatesBothSecretsAndReturnsCA(t *testing.T) {
	ctx := context.Background()
	client := newTestTypedClient(testTypedVMImage())

	vmName, credSecretName, cloudInitSecretName, caCertPEM, err := client.CreatePostgresVM(ctx, testVMCreateParams())
	if err != nil {
		t.Fatalf("CreatePostgresVM returned error: %v", err)
	}
	if vmName != "pg-orders" || credSecretName != "pg-orders-credentials" || cloudInitSecretName != "pg-orders-cloudinit" {
		t.Fatalf("unexpected names: vm=%q cred=%q cloudinit=%q", vmName, credSecretName, cloudInitSecretName)
	}
	if caCertPEM == "" {
		t.Fatalf("CA cert is empty")
	}

	credSecret, err := client.KubeClient.CoreV1().Secrets("tenant-a").Get(ctx, credSecretName, metav1.GetOptions{})
	if err != nil {
		t.Fatalf("get credentials Secret: %v", err)
	}
	if credSecret.StringData["ca_cert"] != caCertPEM {
		t.Fatalf("returned CA does not match Secret CA")
	}
	cloudInitSecret, err := client.KubeClient.CoreV1().Secrets("tenant-a").Get(ctx, cloudInitSecretName, metav1.GetOptions{})
	if err != nil {
		t.Fatalf("get cloudinit Secret: %v", err)
	}
	if cloudInitSecret.StringData["userdata"] == "" {
		t.Fatalf("cloudinit Secret has no stringData.userdata")
	}
	if _, err := client.Clientset.KubevirtV1().VirtualMachines("tenant-a").Get(ctx, vmName, metav1.GetOptions{}); err != nil {
		t.Fatalf("get created VM: %v", err)
	}
}

func TestTypedCreatePostgresVMPreservesVMShape(t *testing.T) {
	ctx := context.Background()
	client := newTestTypedClient(testTypedVMImage())
	client.MgmtLogicalSwitch = "ovn-default"
	params := testVMCreateParams()
	params.DNSServerIP = "10.96.0.10/32"

	vmName, _, cloudInitSecretName, _, err := client.CreatePostgresVM(ctx, params)
	if err != nil {
		t.Fatalf("CreatePostgresVM returned error: %v", err)
	}
	vm, err := client.Clientset.KubevirtV1().VirtualMachines("tenant-a").Get(ctx, vmName, metav1.GetOptions{})
	if err != nil {
		t.Fatalf("get created VM: %v", err)
	}

	if vm.Spec.Template.ObjectMeta.Annotations["ovn.kubernetes.io/logical_switch"] != "ovn-default" {
		t.Fatalf("logical switch annotation = %q, want ovn-default", vm.Spec.Template.ObjectMeta.Annotations["ovn.kubernetes.io/logical_switch"])
	}
	if vm.Spec.Template.Spec.DNSPolicy != corev1.DNSNone {
		t.Fatalf("dnsPolicy = %q, want None", vm.Spec.Template.Spec.DNSPolicy)
	}
	if vm.Spec.Template.Spec.Domain.Memory != nil && vm.Spec.Template.Spec.Domain.Memory.Guest != nil {
		t.Fatalf("memory.guest is set before Harvester admission: %s", vm.Spec.Template.Spec.Domain.Memory.Guest.String())
	}
	memoryLimit := vm.Spec.Template.Spec.Domain.Resources.Limits[corev1.ResourceMemory]
	if got := memoryLimit.String(); got != "4Gi" {
		t.Fatalf("memory limit = %q, want 4Gi", got)
	}
	if got := vm.Spec.Template.Spec.DNSConfig.Nameservers; len(got) != 1 || got[0] != "10.96.0.10" {
		t.Fatalf("nameservers = %v, want [10.96.0.10]", got)
	}
	if got := len(vm.Spec.DataVolumeTemplates); got != 0 {
		t.Fatalf("dataVolumeTemplates = %d, want 0 for Harvester-native volumeClaimTemplates path", got)
	}
	templates, err := typedVolumeClaimTemplates(vm)
	if err != nil {
		t.Fatalf("volume claim templates: %v", err)
	}
	if got := len(templates); got != 2 {
		t.Fatalf("volume claim template count = %d, want 2", got)
	}
	osTemplate := findPVCTemplate(templates, "pg-orders-os")
	if osTemplate == nil {
		t.Fatalf("OS PVC template not found")
	}
	if got := osTemplate.Annotations["harvesterhci.io/imageId"]; got != "default/ubuntu-22.04" {
		t.Fatalf("OS image annotation = %q, want default/ubuntu-22.04", got)
	}
	dataTemplate := findPVCTemplate(templates, "pg-orders-data")
	if dataTemplate == nil {
		t.Fatalf("data PVC template not found")
	}
	if got := dataTemplate.Annotations["harvesterhci.io/imageId"]; got != "" {
		t.Fatalf("data PVC image annotation = %q, want empty", got)
	}
	if dataTemplate.Spec.StorageClassName == nil || *dataTemplate.Spec.StorageClassName != "harvester-longhorn" {
		t.Fatalf("data PVC storageClass = %#v, want harvester-longhorn", dataTemplate.Spec.StorageClassName)
	}
	if !vmVolumeUsesPVC(vm, "os-disk", "pg-orders-os") {
		t.Fatalf("os-disk volume does not use PVC pg-orders-os")
	}
	if !vmVolumeUsesPVC(vm, "pgdata-disk", "pg-orders-data") {
		t.Fatalf("pgdata-disk volume does not use PVC pg-orders-data")
	}
	// Port 5432 must not be forwarded through the masquerade interface — the
	// readiness probe uses the QGA virtio channel, not the pod network.
	if vmInterfaceHasPort(vm, mgmtNetInterface, 5432) {
		t.Fatalf("mgmt-net interface must not expose port 5432 on the pod network")
	}

	// Readiness probe must be configured as an exec probe via the guest agent.
	probe := vm.Spec.Template.Spec.ReadinessProbe
	if probe == nil {
		t.Fatalf("ReadinessProbe is not set")
	}
	if probe.Exec == nil {
		t.Fatalf("ReadinessProbe.Exec is not set")
	}
	if !strings.Contains(strings.Join(probe.Exec.Command, " "), "pg_isready") {
		t.Fatalf("ReadinessProbe command does not contain pg_isready: %v", probe.Exec.Command)
	}
	if probe.InitialDelaySeconds != 30 || probe.PeriodSeconds != 10 || probe.FailureThreshold != 6 {
		t.Fatalf("ReadinessProbe timing initial=%d period=%d failure=%d, want 30/10/6",
			probe.InitialDelaySeconds, probe.PeriodSeconds, probe.FailureThreshold)
	}

	raw, err := json.Marshal(vm)
	if err != nil {
		t.Fatalf("marshal VM: %v", err)
	}
	body := string(raw)
	if !strings.Contains(body, `"secretRef":{"name":"`+cloudInitSecretName+`"}`) {
		t.Fatalf("VM JSON does not contain cloud-init secretRef: %s", body)
	}
	if !strings.Contains(body, `"networkDataSecretRef":{"name":"`+cloudInitSecretName+`"}`) {
		t.Fatalf("VM JSON does not contain cloud-init networkDataSecretRef: %s", body)
	}
}

func TestTypedGetVMIReadinessUsesOnlyDataNetIP(t *testing.T) {
	ctx := context.Background()
	client := newTestTypedClient(&kubevirtv1.VirtualMachineInstance{
		TypeMeta:   metav1.TypeMeta{APIVersion: "kubevirt.io/v1", Kind: "VirtualMachineInstance"},
		ObjectMeta: metav1.ObjectMeta{Name: "pg-orders", Namespace: "tenant-a"},
		Status: kubevirtv1.VirtualMachineInstanceStatus{
			Phase: kubevirtv1.Running,
			Interfaces: []kubevirtv1.VirtualMachineInstanceNetworkInterface{
				{Name: mgmtNetInterface, IP: "10.244.0.10"},
				{Name: dataNetInterface, IP: "192.168.40.50"},
			},
		},
	})

	readiness, err := client.GetVMIReadiness(ctx, "tenant-a", "pg-orders")
	if err != nil {
		t.Fatalf("GetVMIReadiness returned error: %v", err)
	}
	if !readiness.Running || readiness.IP != "192.168.40.50" {
		t.Fatalf("readiness = %+v, want running with data-net IP", readiness)
	}
}

func TestTypedGetVMIReadinessDoesNotFallbackToMgmtNet(t *testing.T) {
	ctx := context.Background()
	client := newTestTypedClient(&kubevirtv1.VirtualMachineInstance{
		TypeMeta:   metav1.TypeMeta{APIVersion: "kubevirt.io/v1", Kind: "VirtualMachineInstance"},
		ObjectMeta: metav1.ObjectMeta{Name: "pg-orders", Namespace: "tenant-a"},
		Status: kubevirtv1.VirtualMachineInstanceStatus{
			Phase:      kubevirtv1.Running,
			Interfaces: []kubevirtv1.VirtualMachineInstanceNetworkInterface{{Name: mgmtNetInterface, IP: "10.244.0.10"}},
		},
	})

	readiness, err := client.GetVMIReadiness(ctx, "tenant-a", "pg-orders")
	if err != nil {
		t.Fatalf("GetVMIReadiness returned error: %v", err)
	}
	if !readiness.Running || readiness.IP != "" {
		t.Fatalf("readiness = %+v, want running with no IP fallback", readiness)
	}
}

func TestTypedGetVMIReadinessSurfacesConditionsAndUID(t *testing.T) {
	ctx := context.Background()
	client := newTestTypedClient(&kubevirtv1.VirtualMachineInstance{
		TypeMeta:   metav1.TypeMeta{APIVersion: "kubevirt.io/v1", Kind: "VirtualMachineInstance"},
		ObjectMeta: metav1.ObjectMeta{Name: "pg-orders", Namespace: "tenant-a", UID: "abc-123"},
		Status: kubevirtv1.VirtualMachineInstanceStatus{
			Phase: kubevirtv1.Running,
			Interfaces: []kubevirtv1.VirtualMachineInstanceNetworkInterface{
				{Name: dataNetInterface, IP: "192.168.40.50"},
			},
			Conditions: []kubevirtv1.VirtualMachineInstanceCondition{
				{Type: kubevirtv1.VirtualMachineInstanceReady, Status: corev1.ConditionTrue},
				{Type: kubevirtv1.VirtualMachineInstanceAgentConnected, Status: corev1.ConditionTrue},
			},
		},
	})

	r, err := client.GetVMIReadiness(ctx, "tenant-a", "pg-orders")
	if err != nil {
		t.Fatalf("GetVMIReadiness returned error: %v", err)
	}
	if !r.Running {
		t.Fatalf("Running = false, want true")
	}
	if r.IP != "192.168.40.50" {
		t.Fatalf("IP = %q, want 192.168.40.50", r.IP)
	}
	if !r.Ready {
		t.Fatalf("Ready = false, want true")
	}
	if !r.AgentConnected {
		t.Fatalf("AgentConnected = false, want true")
	}
	if r.VMIUID != "abc-123" {
		t.Fatalf("VMIUID = %q, want abc-123", r.VMIUID)
	}
}

func TestTypedGetVMIReadinessConditionsDefaultToFalseWhenAbsent(t *testing.T) {
	ctx := context.Background()
	client := newTestTypedClient(&kubevirtv1.VirtualMachineInstance{
		TypeMeta:   metav1.TypeMeta{APIVersion: "kubevirt.io/v1", Kind: "VirtualMachineInstance"},
		ObjectMeta: metav1.ObjectMeta{Name: "pg-orders", Namespace: "tenant-a", UID: "xyz-456"},
		Status: kubevirtv1.VirtualMachineInstanceStatus{
			Phase: kubevirtv1.Running,
			// No conditions set — VMI booting, probes not yet evaluated.
		},
	})

	r, err := client.GetVMIReadiness(ctx, "tenant-a", "pg-orders")
	if err != nil {
		t.Fatalf("GetVMIReadiness returned error: %v", err)
	}
	if r.Ready || r.AgentConnected {
		t.Fatalf("Ready=%v AgentConnected=%v, want both false when conditions absent", r.Ready, r.AgentConnected)
	}
	if r.VMIUID != "xyz-456" {
		t.Fatalf("VMIUID = %q, want xyz-456", r.VMIUID)
	}
}

func TestTypedStartStopAndResizeVM(t *testing.T) {
	ctx := context.Background()
	running := true
	client := newTestTypedClient(&kubevirtv1.VirtualMachine{
		TypeMeta:   metav1.TypeMeta{APIVersion: "kubevirt.io/v1", Kind: "VirtualMachine"},
		ObjectMeta: metav1.ObjectMeta{Name: "pg-orders", Namespace: "tenant-a"},
		Spec: kubevirtv1.VirtualMachineSpec{
			Running: &running,
			Template: &kubevirtv1.VirtualMachineInstanceTemplateSpec{
				Spec: kubevirtv1.VirtualMachineInstanceSpec{
					Domain: kubevirtv1.DomainSpec{
						CPU:    &kubevirtv1.CPU{Cores: 2},
						Memory: &kubevirtv1.Memory{},
					},
				},
			},
		},
	})

	if err := client.StopVM(ctx, "tenant-a", "pg-orders"); err != nil {
		t.Fatalf("StopVM returned error: %v", err)
	}
	vm, _ := client.Clientset.KubevirtV1().VirtualMachines("tenant-a").Get(ctx, "pg-orders", metav1.GetOptions{})
	if vm.Spec.Running == nil || *vm.Spec.Running {
		t.Fatalf("running after StopVM = %v, want false", vm.Spec.Running)
	}
	if err := client.StartVM(ctx, "tenant-a", "pg-orders"); err != nil {
		t.Fatalf("StartVM returned error: %v", err)
	}
	if err := client.ResizeVM(ctx, "tenant-a", "pg-orders", 4, 8192); err != nil {
		t.Fatalf("ResizeVM returned error: %v", err)
	}
	vm, _ = client.Clientset.KubevirtV1().VirtualMachines("tenant-a").Get(ctx, "pg-orders", metav1.GetOptions{})
	if vm.Spec.Running == nil || !*vm.Spec.Running {
		t.Fatalf("running after StartVM = %v, want true", vm.Spec.Running)
	}
	if vm.Spec.Template.Spec.Domain.CPU.Cores != 4 || vm.Spec.Template.Spec.Domain.Memory.Guest.String() != "8Gi" {
		t.Fatalf("resized CPU/memory = %d/%s, want 4/8Gi", vm.Spec.Template.Spec.Domain.CPU.Cores, vm.Spec.Template.Spec.Domain.Memory.Guest.String())
	}
}

func TestTypedDeployMonitoringIsIdempotent(t *testing.T) {
	ctx := context.Background()
	client := newTestTypedClient()
	client.GrafanaURL = "https://grafana.example"

	for i := range 2 {
		svcName, smName, grafanaURL, promTarget, err := client.DeployMonitoring(ctx, "orders", "tenant-a", "192.168.40.50")
		if err != nil {
			t.Fatalf("DeployMonitoring call %d returned error: %v", i+1, err)
		}
		if svcName != "pg-orders-metrics" || smName != "pg-orders-monitor" {
			t.Fatalf("unexpected monitoring names: %q %q", svcName, smName)
		}
		if grafanaURL != "https://grafana.example/d/dbaas-orders/postgresql-orders" {
			t.Fatalf("Grafana URL = %q", grafanaURL)
		}
		if promTarget != "pg-orders-metrics.tenant-a.svc:9187" {
			t.Fatalf("Prometheus target = %q", promTarget)
		}
	}

	svc, err := client.KubeClient.CoreV1().Services("tenant-a").Get(ctx, "pg-orders-metrics", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("get Service: %v", err)
	}
	if len(svc.Spec.Selector) != 0 {
		t.Fatalf("Service selector = %v, want no selector", svc.Spec.Selector)
	}
	if svc.Spec.Ports[0].TargetPort.IntVal != 9187 {
		t.Fatalf("Service targetPort = %v, want 9187", svc.Spec.Ports[0].TargetPort)
	}
	ep, err := client.KubeClient.CoreV1().Endpoints("tenant-a").Get(ctx, "pg-orders-metrics", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("get Endpoints: %v", err)
	}
	if ep.Subsets[0].Addresses[0].IP != "192.168.40.50" {
		t.Fatalf("Endpoint IP = %q, want 192.168.40.50", ep.Subsets[0].Addresses[0].IP)
	}
	sm, err := client.Clientset.MonitoringV1().ServiceMonitors("tenant-a").Get(ctx, "pg-orders-monitor", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("get ServiceMonitor: %v", err)
	}
	if sm.Spec.Endpoints[0].Interval != monitoringv1.Duration("15s") {
		t.Fatalf("ServiceMonitor interval = %q, want 15s", sm.Spec.Endpoints[0].Interval)
	}
}

func TestTypedDeleteSecretAndTeardownIgnoreNotFound(t *testing.T) {
	ctx := context.Background()
	client := newTestTypedClient()
	if err := client.DeleteSecret(ctx, "tenant-a", "missing"); err != nil {
		t.Fatalf("DeleteSecret returned error for missing Secret: %v", err)
	}
	err := client.TeardownAll(ctx, "orders", "tenant-a", dbaasv1.ResourceRefs{
		VMName:              "pg-orders",
		DataVolumeName:      "pg-orders-data",
		SecretName:          "pg-orders-credentials",
		CloudInitSecretName: "pg-orders-cloudinit",
		MetricsServiceName:  "pg-orders-metrics",
		ServiceMonitor:      "pg-orders-monitor",
	})
	if err != nil {
		t.Fatalf("TeardownAll returned error for missing resources: %v", err)
	}
}

func TestTypedTeardownAggregatesDeleteErrors(t *testing.T) {
	ctx := context.Background()
	client := newTestTypedClient(&kubevirtv1.VirtualMachine{
		TypeMeta:   metav1.TypeMeta{APIVersion: "kubevirt.io/v1", Kind: "VirtualMachine"},
		ObjectMeta: metav1.ObjectMeta{Name: "pg-orders", Namespace: "tenant-a"},
	})
	client.Clientset.(*harvesterfake.Clientset).PrependReactor("delete", "virtualmachines", func(action clienttesting.Action) (bool, runtime.Object, error) {
		return true, nil, &apierrors.StatusError{ErrStatus: metav1.Status{Reason: metav1.StatusReasonForbidden, Message: "blocked"}}
	})

	err := client.TeardownAll(ctx, "orders", "tenant-a", dbaasv1.ResourceRefs{VMName: "pg-orders"})
	if err == nil {
		t.Fatalf("TeardownAll returned nil error, want aggregate")
	}
	if !strings.Contains(err.Error(), "virtualmachines/pg-orders") || !strings.Contains(err.Error(), "blocked") {
		t.Fatalf("aggregate error = %q", err.Error())
	}
}

func newTestTypedClient(objs ...runtime.Object) *TypedClient {
	return NewTypedClientWithClientsets(harvesterfake.NewSimpleClientset(objs...), kubefake.NewSimpleClientset(), "")
}

func findPVCTemplate(pvcs []*corev1.PersistentVolumeClaim, name string) *corev1.PersistentVolumeClaim {
	for _, pvc := range pvcs {
		if pvc.Name == name {
			return pvc
		}
	}
	return nil
}

func vmVolumeUsesPVC(vm *kubevirtv1.VirtualMachine, volumeName, claimName string) bool {
	for _, volume := range vm.Spec.Template.Spec.Volumes {
		if volume.Name == volumeName && volume.PersistentVolumeClaim != nil && volume.PersistentVolumeClaim.ClaimName == claimName {
			return true
		}
	}
	return false
}

func vmInterfaceHasPort(vm *kubevirtv1.VirtualMachine, interfaceName string, port int32) bool {
	for _, iface := range vm.Spec.Template.Spec.Domain.Devices.Interfaces {
		if iface.Name != interfaceName {
			continue
		}
		for _, ifacePort := range iface.Ports {
			if ifacePort.Port == port && ifacePort.Protocol == "TCP" {
				return true
			}
		}
	}
	return false
}

func testTypedVMImage() *harvesterhciov1beta1.VirtualMachineImage {
	return &harvesterhciov1beta1.VirtualMachineImage{
		TypeMeta:   metav1.TypeMeta{APIVersion: "harvesterhci.io/v1beta1", Kind: "VirtualMachineImage"},
		ObjectMeta: metav1.ObjectMeta{Name: "ubuntu-22.04", Namespace: "default"},
		Spec:       harvesterhciov1beta1.VirtualMachineImageSpec{DisplayName: "Ubuntu 22.04"},
		Status: harvesterhciov1beta1.VirtualMachineImageStatus{
			StorageClassName: "longhorn-image-ubuntu",
			Conditions: []harvesterhciov1beta1.Condition{
				{Type: harvesterhciov1beta1.ImageImported, Status: corev1.ConditionTrue},
			},
		},
	}
}
