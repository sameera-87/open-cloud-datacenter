/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package harvester

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"strings"
	"sync"

	harvesterbuilder "github.com/harvester/harvester/pkg/builder"
	"github.com/harvester/harvester/pkg/util"
	monitoringv1 "github.com/prometheus-operator/prometheus-operator/pkg/apis/monitoring/v1"
	dbaasv1 "github.com/wso2/open-cloud-datacenter/crds/dbaas/api/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	kubevirtv1 "kubevirt.io/api/core/v1"

	harvesterhciov1beta1 "github.com/harvester/harvester/pkg/apis/harvesterhci.io/v1beta1"
	harvesterclientset "github.com/harvester/harvester/pkg/generated/clientset/versioned"
	kvclientset "kubevirt.io/client-go/kubevirt"
)

// TypedClient manages Harvester resources through Harvester's generated
// clientset and standard Kubernetes typed clients.
type TypedClient struct {
	Clientset         harvesterclientset.Interface
	KubeClient        kubernetes.Interface
	KvClientset       kvclientset.Interface
	GrafanaURL        string
	MgmtLogicalSwitch string
}

var _ ClientInterface = (*TypedClient)(nil)

func NewTypedClient(config *rest.Config, grafanaURL string) (*TypedClient, error) {
	clientset, err := harvesterclientset.NewForConfig(config)
	if err != nil {
		return nil, err
	}
	kubeClient, err := kubernetes.NewForConfig(config)
	if err != nil {
		return nil, err
	}
	kvClientset, err := kvclientset.NewForConfig(config)
	if err != nil {
		return nil, err
	}
	return NewTypedClientWithClientsets(clientset, kubeClient, kvClientset, grafanaURL), nil
}

func NewTypedClientWithClientsets(clientset harvesterclientset.Interface, kubeClient kubernetes.Interface, kvClientset kvclientset.Interface, grafanaURL string) *TypedClient {
	return &TypedClient{Clientset: clientset, KubeClient: kubeClient, KvClientset: kvClientset, GrafanaURL: grafanaURL}
}

func (c *TypedClient) CreateDataVolume(ctx context.Context, id, ns string, sizeGB int, storageClass string) (string, error) {
	dvName := fmt.Sprintf("pg-%s-data", id)
	// In the Harvester-owned storage path, this phase reserves the deterministic
	// data disk PVC/template name. The actual PVC is created later by Harvester
	// from the VM's harvesterhci.io/volumeClaimTemplates annotation.
	return dvName, nil
}

func (c *TypedClient) ResizeDataVolume(ctx context.Context, ns, vmName, dvName string, newSizeGB int) error {
	vm, err := c.Clientset.KubevirtV1().VirtualMachines(ns).Get(ctx, vmName, metav1.GetOptions{})
	if err != nil {
		return err
	}
	pvcs, err := typedVolumeClaimTemplates(vm)
	if err != nil {
		return err
	}
	found := false
	for _, pvc := range pvcs {
		if pvc.Name != dvName {
			continue
		}
		if pvc.Spec.Resources.Requests == nil {
			pvc.Spec.Resources.Requests = corev1.ResourceList{}
		}
		pvc.Spec.Resources.Requests[corev1.ResourceStorage] = resource.MustParse(fmt.Sprintf("%dGi", newSizeGB))
		found = true
		break
	}
	if !found {
		return fmt.Errorf("data volume claim template %s not found on VM %s/%s", dvName, ns, vmName)
	}
	data, err := json.Marshal(pvcs)
	if err != nil {
		return fmt.Errorf("marshal %s: %w", util.AnnotationVolumeClaimTemplates, err)
	}
	if vm.Annotations == nil {
		vm.Annotations = map[string]string{}
	}
	vm.Annotations[util.AnnotationVolumeClaimTemplates] = string(data)
	_, err = c.Clientset.KubevirtV1().VirtualMachines(ns).Update(ctx, vm, metav1.UpdateOptions{})
	return err
}

func (c *TypedClient) CreatePostgresVM(ctx context.Context, p VMCreateParams) (vmName, credSecretName, cloudInitSecretName, caCertPEM string, err error) {
	vmName = fmt.Sprintf("pg-%s", p.ID)
	credSecretName = fmt.Sprintf("pg-%s-credentials", p.ID)
	cloudInitSecretName = fmt.Sprintf("pg-%s-cloudinit", p.ID)

	adminPw := randomString(32)
	replPw := randomString(32)
	exporterPw := randomString(24)
	tls, tlsErr := generateTLS(vmName)
	if tlsErr != nil {
		err = fmt.Errorf("TLS generation: %w", tlsErr)
		return vmName, credSecretName, cloudInitSecretName, caCertPEM, err
	}
	caCertPEM = tls.CACertPEM

	imgNs, imgName, imgSC, err := c.resolveVMImage(ctx, p.OSImage)
	if err != nil {
		return vmName, credSecretName, cloudInitSecretName, caCertPEM, err
	}

	credSecret := &corev1.Secret{
		TypeMeta: metav1.TypeMeta{APIVersion: "v1", Kind: "Secret"},
		ObjectMeta: metav1.ObjectMeta{
			Name:      credSecretName,
			Namespace: p.Namespace,
		},
		Type: corev1.SecretTypeOpaque,
		StringData: map[string]string{
			"admin_user":        p.MasterUser,
			"admin_password":    adminPw,
			"repl_password":     replPw,
			"exporter_password": exporterPw,
			"ca_cert":           tls.CACertPEM,
			"ca_key":            tls.CAKeyPEM,
			"server_cert":       tls.ServerCertPEM,
			"server_key":        tls.ServerKeyPEM,
		},
	}
	if _, e := c.KubeClient.CoreV1().Secrets(p.Namespace).Create(ctx, credSecret, metav1.CreateOptions{}); e != nil {
		if err = ignoreAlreadyExists(e); err != nil {
			return vmName, credSecretName, cloudInitSecretName, caCertPEM, err
		}
	}

	cloudInitSecret := &corev1.Secret{
		TypeMeta: metav1.TypeMeta{APIVersion: "v1", Kind: "Secret"},
		ObjectMeta: metav1.ObjectMeta{
			Name:      cloudInitSecretName,
			Namespace: p.Namespace,
		},
		Type: corev1.SecretTypeOpaque,
		StringData: map[string]string{
			"userdata":    buildCloudInit(p, adminPw, replPw, exporterPw, tls),
			"networkdata": buildNetworkData(p),
		},
	}
	if _, e := c.KubeClient.CoreV1().Secrets(p.Namespace).Create(ctx, cloudInitSecret, metav1.CreateOptions{}); e != nil {
		if err = ignoreAlreadyExists(e); err != nil {
			return vmName, credSecretName, cloudInitSecretName, caCertPEM, err
		}
	}
	running := true
	vm, err := c.buildPostgresVM(p, vmName, cloudInitSecretName, fmt.Sprintf("%s/%s", imgNs, imgName), imgSC, running)
	if err != nil {
		return vmName, credSecretName, cloudInitSecretName, caCertPEM, err
	}
	if _, e := c.Clientset.KubevirtV1().VirtualMachines(p.Namespace).Create(ctx, vm, metav1.CreateOptions{}); e != nil {
		err = ignoreAlreadyExists(e)
	}
	return vmName, credSecretName, cloudInitSecretName, caCertPEM, err
}

func (c *TypedClient) GetVMIReadiness(ctx context.Context, ns, vmName string) (VMIReadiness, error) {
	vmi, err := c.Clientset.KubevirtV1().VirtualMachineInstances(ns).Get(ctx, vmName, metav1.GetOptions{})
	if err != nil {
		return VMIReadiness{}, err
	}

	readiness := VMIReadiness{
		Running: string(vmi.Status.Phase) == vmiPhaseRunning,
		VMIUID:  string(vmi.UID),
	}
	for _, iface := range vmi.Status.Interfaces {
		if iface.Name != dataNetInterface {
			continue
		}
		readiness.IP = iface.IP
		break
	}
	for _, cond := range vmi.Status.Conditions {
		switch cond.Type {
		case kubevirtv1.VirtualMachineInstanceReady:
			readiness.Ready = cond.Status == corev1.ConditionTrue
		case kubevirtv1.VirtualMachineInstanceAgentConnected:
			readiness.AgentConnected = cond.Status == corev1.ConditionTrue
		}
	}
	return readiness, nil
}

// TODO: Not used anymore , clean up later from interface and dynamic client
func (c *TypedClient) DialVMListener(ctx context.Context, ns, vmName string, port int) error {
	// Dial VM port 5432 using TCP using management pod network
	list, err := c.KubeClient.CoreV1().Pods(ns).List(ctx, metav1.ListOptions{
		LabelSelector: fmt.Sprintf("vm.kubevirt.io/name=%s", vmName),
	})
	if err != nil {
		return fmt.Errorf("list launcher pods for %s: %w", vmName, err)
	}
	podIP := ""
	for _, pod := range list.Items {
		if pod.Status.Phase == corev1.PodRunning && pod.Status.PodIP != "" {
			podIP = pod.Status.PodIP
			break
		}
	}
	if podIP == "" {
		return fmt.Errorf("no Running launcher pod with podIP for VM %s", vmName)
	}
	addr := fmt.Sprintf("%s:%d", podIP, port)
	d := net.Dialer{Timeout: dialTimeout}
	conn, err := d.DialContext(ctx, "tcp", addr)
	if err != nil {
		return fmt.Errorf("dial %s: %w", addr, err)
	}
	_ = conn.Close()
	return nil
}

// To align behavior with kubevirt v1.1.1, we set runStrategy to Halted when stopping a VM.
// see harvester/pkg/api/vm/handler.go 142
func (c *TypedClient) StopVM(ctx context.Context, ns, vmName string) error {
	vm, err := c.Clientset.KubevirtV1().VirtualMachines(ns).Get(ctx, vmName, metav1.GetOptions{})
	if err != nil {
		return err
	}
	runStrategy := kubevirtv1.RunStrategyHalted
	vm.Spec.RunStrategy = &runStrategy
	vm.Spec.Running = nil
	_, err = c.Clientset.KubevirtV1().VirtualMachines(ns).Update(ctx, vm, metav1.UpdateOptions{})
	return err
}

// see harvester/pkg/api/vm/handler.go 138
func (c *TypedClient) StartVM(ctx context.Context, ns, vmName string) error {
	return c.KvClientset.KubevirtV1().VirtualMachines(ns).Start(ctx, vmName, &kubevirtv1.StartOptions{})
}

func (c *TypedClient) ResizeVM(ctx context.Context, ns, vmName string, cpuCores, memoryMB int) error {
	vm, err := c.Clientset.KubevirtV1().VirtualMachines(ns).Get(ctx, vmName, metav1.GetOptions{})
	if err != nil {
		return err
	}
	if vm.Spec.Template.Spec.Domain.CPU == nil {
		vm.Spec.Template.Spec.Domain.CPU = &kubevirtv1.CPU{}
	}
	vm.Spec.Template.Spec.Domain.CPU.Cores = uint32(cpuCores)
	if vm.Spec.Template.Spec.Domain.Resources.Limits == nil {
		vm.Spec.Template.Spec.Domain.Resources.Limits = corev1.ResourceList{}
	}
	vm.Spec.Template.Spec.Domain.Resources.Limits[corev1.ResourceCPU] = *resource.NewQuantity(int64(cpuCores), resource.DecimalSI)
	// Memory: set limits only — the Harvester mutating webhook derives domain.memory.guest
	// from resources.limits[memory] on every VM update (pkg/webhook/.../mutator.go).
	vm.Spec.Template.Spec.Domain.Resources.Limits[corev1.ResourceMemory] = resource.MustParse(fmt.Sprintf("%dMi", memoryMB))
	_, err = c.Clientset.KubevirtV1().VirtualMachines(ns).Update(ctx, vm, metav1.UpdateOptions{})
	return err
}

func (c *TypedClient) DeleteSecret(ctx context.Context, ns, name string) error {
	err := c.KubeClient.CoreV1().Secrets(ns).Delete(ctx, name, metav1.DeleteOptions{})
	if apierrors.IsNotFound(err) {
		return nil
	}
	return err
}

func (c *TypedClient) RemoveCloudInitDisk(ctx context.Context, ns, vmName string) error {
	vm, err := c.Clientset.KubevirtV1().VirtualMachines(ns).Get(ctx, vmName, metav1.GetOptions{})
	if err != nil {
		return err
	}

	var disks []kubevirtv1.Disk
	for _, d := range vm.Spec.Template.Spec.Domain.Devices.Disks {
		if d.Name != "cloudinit" {
			disks = append(disks, d)
		}
	}
	var volumes []kubevirtv1.Volume
	for _, v := range vm.Spec.Template.Spec.Volumes {
		if v.Name != "cloudinit" {
			volumes = append(volumes, v)
		}
	}
	vm.Spec.Template.Spec.Domain.Devices.Disks = disks
	vm.Spec.Template.Spec.Volumes = volumes

	_, err = c.Clientset.KubevirtV1().VirtualMachines(ns).Update(ctx, vm, metav1.UpdateOptions{})
	return err
}

func (c *TypedClient) DeployMonitoring(ctx context.Context, id, ns, vmIP string) (svcName, smName, grafanaURL, promTarget string, err error) {
	smName = fmt.Sprintf("pg-%s-monitor", id)
	svcName = fmt.Sprintf("pg-%s-metrics", id)
	grafanaURL = fmt.Sprintf("%s/d/dbaas-%s/postgresql-%s", c.GrafanaURL, id, id)
	promTarget = fmt.Sprintf("%s.%s.svc:9187", svcName, ns)
	if vmIP == "" {
		err = fmt.Errorf("monitoring endpoint IP is required")
		return svcName, smName, grafanaURL, promTarget, err
	}

	if err = c.createOrUpdateService(ctx, id, ns, svcName); err != nil {
		return svcName, smName, grafanaURL, promTarget, err
	}
	if err = c.createOrUpdateEndpoints(ctx, id, ns, svcName, vmIP); err != nil {
		return svcName, smName, grafanaURL, promTarget, err
	}
	err = c.createOrUpdateServiceMonitor(ctx, id, ns, smName)
	return svcName, smName, grafanaURL, promTarget, err
}

func (c *TypedClient) TeardownAll(ctx context.Context, id, ns string, refs dbaasv1.ResourceRefs) error {
	type deleteTask struct {
		resource string
		name     string
		delete   func() error
	}
	tasks := []deleteTask{
		{"servicemonitors", refs.ServiceMonitor, func() error {
			return c.Clientset.MonitoringV1().ServiceMonitors(ns).Delete(ctx, refs.ServiceMonitor, metav1.DeleteOptions{})
		}},
		{"endpoints", refs.MetricsServiceName, func() error {
			return c.KubeClient.CoreV1().Endpoints(ns).Delete(ctx, refs.MetricsServiceName, metav1.DeleteOptions{})
		}},
		{"services", refs.MetricsServiceName, func() error {
			return c.KubeClient.CoreV1().Services(ns).Delete(ctx, refs.MetricsServiceName, metav1.DeleteOptions{})
		}},
		{"virtualmachines", refs.VMName, func() error {
			return c.Clientset.KubevirtV1().VirtualMachines(ns).Delete(ctx, refs.VMName, metav1.DeleteOptions{})
		}},
		{"secrets", refs.SecretName, func() error {
			return c.KubeClient.CoreV1().Secrets(ns).Delete(ctx, refs.SecretName, metav1.DeleteOptions{})
		}},
		{"secrets", refs.CloudInitSecretName, func() error {
			return c.KubeClient.CoreV1().Secrets(ns).Delete(ctx, refs.CloudInitSecretName, metav1.DeleteOptions{})
		}},
	}

	var (
		wg   sync.WaitGroup
		mu   sync.Mutex
		errs []string
	)
	for _, t := range tasks {
		if t.name == "" {
			continue
		}
		wg.Add(1)
		go func(dt deleteTask) {
			defer wg.Done()
			err := dt.delete()
			if err == nil || apierrors.IsNotFound(err) {
				return
			}
			mu.Lock()
			errs = append(errs, fmt.Sprintf("%s/%s: %v", dt.resource, dt.name, err))
			mu.Unlock()
		}(t)
	}
	wg.Wait()
	if len(errs) > 0 {
		return fmt.Errorf("teardown: %s", strings.Join(errs, "; "))
	}
	return nil
}

// Helpers
func (c *TypedClient) resolveVMImage(ctx context.Context, ref string) (ns, name, sc string, err error) {
	if ref == "" {
		return ns, name, sc, fmt.Errorf("empty image reference")
	}

	ns, spec := "default", ref
	if i := strings.Index(ref, "/"); i > 0 {
		ns, spec = ref[:i], ref[i+1:]
	}
	if spec == "" {
		return ns, name, sc, fmt.Errorf("empty image name in reference %q", ref)
	}

	img, e := c.Clientset.HarvesterhciV1beta1().VirtualMachineImages(ns).Get(ctx, spec, metav1.GetOptions{})
	if e == nil {
		return readyVMImageFields(ns, spec, img)
	}
	if !apierrors.IsNotFound(e) {
		return ns, name, sc, e
	}

	// fallback: search by displayName
	list, e := c.Clientset.HarvesterhciV1beta1().VirtualMachineImages(ns).List(ctx, metav1.ListOptions{})
	if e != nil {
		return ns, name, sc, e
	}

	var matched []harvesterhciov1beta1.VirtualMachineImage
	for _, item := range list.Items {
		if item.Spec.DisplayName == spec {
			matched = append(matched, item)
		}
	}

	switch len(matched) {
	case 0:
		return ns, name, sc, fmt.Errorf("no VirtualMachineImage in namespace %q matching name or displayName %q", ns, spec)
	case 1:
		name = matched[0].Name
		return readyVMImageFields(ns, name, &matched[0])
	default:
		return ns, name, sc, fmt.Errorf("ambiguous: %d VirtualMachineImages in namespace %q share displayName %q", len(matched), ns, spec)
	}
}

func readyVMImageFields(ns, name string, img *harvesterhciov1beta1.VirtualMachineImage) (string, string, string, error) {
	if !isVMImageImported(img) {
		return ns, name, "", fmt.Errorf("VirtualMachineImage %s/%s is not imported yet (status.conditions missing ImageImported=True)", ns, name)
	}
	sc, err := resolveImageStorageClassName(img)
	if err != nil {
		return ns, name, sc, err
	}
	return ns, name, sc, nil
}

func isVMImageImported(image *harvesterhciov1beta1.VirtualMachineImage) bool {
	if image == nil {
		return false
	}
	return harvesterhciov1beta1.ImageImported.IsTrue(image)
}

func resolveImageStorageClassName(image *harvesterhciov1beta1.VirtualMachineImage) (string, error) {
	if image == nil {
		return "", fmt.Errorf("nil image")
	}
	if image.Status.StorageClassName != "" {
		return image.Status.StorageClassName, nil
	}
	return "", fmt.Errorf("VM image %s/%s does not have a StorageClass yet (not initialized)",
		image.Namespace, image.Name)
}

func (c *TypedClient) buildPostgresVM(p VMCreateParams, vmName, cloudInitSecretName, imageID, imageSC string, running bool) (*kubevirtv1.VirtualMachine, error) {
	annotations := map[string]string{}
	if c.MgmtLogicalSwitch != "" {
		annotations["ovn.kubernetes.io/logical_switch"] = c.MgmtLogicalSwitch
	}

	runStrategy := kubevirtv1.RunStrategyHalted
	if running {
		runStrategy = kubevirtv1.RunStrategyRerunOnFailure
	}

	labels := map[string]string{dbaasv1.LabelInstance: p.ID, dbaasv1.LabelRole: "primary"}
	templateLabels := map[string]string{dbaasv1.LabelInstance: p.ID}
	osPVCName := fmt.Sprintf("pg-%s-os", p.ID)
	dataPVCName := p.DataVolumeRef
	if dataPVCName == "" {
		dataPVCName = fmt.Sprintf("pg-%s-data", p.ID)
	}
	dataSizeGB := p.DataVolumeSizeGB
	if dataSizeGB <= 0 {
		dataSizeGB = 1
	}
	dataStorageClass := p.DataVolumeStorageClass
	if dataStorageClass == "" {
		dataStorageClass = "longhorn"
	}

	osPVCOption := &harvesterbuilder.PersistentVolumeClaimOption{
		ImageID:          imageID,
		VolumeMode:       corev1.PersistentVolumeBlock,
		AccessMode:       corev1.ReadWriteMany,
		StorageClassName: &imageSC,
	}
	dataPVCOption := &harvesterbuilder.PersistentVolumeClaimOption{
		VolumeMode:       corev1.PersistentVolumeBlock,
		AccessMode:       corev1.ReadWriteMany, // to allow live migration all disks should be ReadWriteMany
		StorageClassName: &dataStorageClass,
	}

	vmBuilder := harvesterbuilder.NewVMBuilder("dbaas-operator").
		Name(vmName).
		Namespace(p.Namespace).
		Labels(labels).
		VirtualMachineInstanceTemplateLabels(templateLabels).
		CPU(p.CPUCores).                         // set spec.template.spec.domain.resources.limits.cpu
		Memory(fmt.Sprintf("%dMi", p.MemoryMB)). // set spec.template.spec.domain.resources.limits.memory
		RunStrategy(runStrategy).
		PVCDisk("os-disk", harvesterbuilder.DiskBusVirtio, false, false, 1, "20Gi", osPVCName, osPVCOption).
		PVCDisk("pgdata-disk", harvesterbuilder.DiskBusVirtio, false, false, 0, fmt.Sprintf("%dGi", dataSizeGB), dataPVCName, dataPVCOption).
		CloudInitDisk("cloudinit", harvesterbuilder.DiskBusVirtio, false, 0, harvesterbuilder.CloudInitSource{
			CloudInitType:         harvesterbuilder.CloudInitTypeNoCloud,
			UserDataSecretName:    cloudInitSecretName,
			NetworkDataSecretName: cloudInitSecretName,
		}).
		NetworkInterface(dataNetInterface, string(kubevirtv1.VirtIO), "", harvesterbuilder.NetworkInterfaceTypeBridge, typedVMNetworkName(p.Namespace, p.NADName))

	vm, err := vmBuilder.VM()
	if err != nil {
		return nil, fmt.Errorf("build VM with Harvester builder helpers: %w", err)
	}
	// Post build fixes
	vm.TypeMeta = metav1.TypeMeta{APIVersion: "kubevirt.io/v1", Kind: "VirtualMachine"}
	vm.Spec.Template.ObjectMeta.Annotations = mergeStringMap(vm.Spec.Template.ObjectMeta.Annotations, annotations) // Kube - OVN Annotation
	vm.Spec.Template.Spec.Domain.CPU.Sockets = 1
	vm.Spec.Template.Spec.Domain.CPU.Threads = 1

	// Readiness probe: pg_isready runs inside the guest via the QEMU guest agent
	// virtio channel — no pod-network port exposure required.
	vm.Spec.Template.Spec.ReadinessProbe = &kubevirtv1.Probe{
		Handler: kubevirtv1.Handler{
			Exec: &corev1.ExecAction{
				Command: []string{
					"/bin/sh", "-c",
					fmt.Sprintf("pg_isready -h 127.0.0.1 -p %d -U %s -d postgres", p.Port, p.MasterUser),
				},
			},
		},
		InitialDelaySeconds: 30,
		PeriodSeconds:       10,
		TimeoutSeconds:      5,
		SuccessThreshold:    1,
		FailureThreshold:    6,
	}

	// on Kube-OVN/VPC networking, the default DNS inherited through KubeVirt/launcher behavior can be wrong for VM bootstrapping.
	// If DNS is wrong, cloud-init may fail during apt install postgresql.. This block forces the VM path to use the intended per-VPC DNS server.
	if p.DNSServerIP != "" { // Only set when Kube-OVN/VPC is used
		dnsIP := p.DNSServerIP
		if i := strings.Index(dnsIP, "/"); i > 0 {
			dnsIP = dnsIP[:i]
		}
		vm.Spec.Template.Spec.DNSPolicy = corev1.DNSNone // to opt out of inheriting cluster DNS in Kube-OVN setup
		vm.Spec.Template.Spec.DNSConfig = &corev1.PodDNSConfig{Nameservers: []string{dnsIP}}
	}

	return vm, nil
}

func (c *TypedClient) createOrUpdateService(ctx context.Context, id, ns, svcName string) error {
	desired := typedMonitoringService(id, ns, svcName)
	if _, err := c.KubeClient.CoreV1().Services(ns).Create(ctx, desired, metav1.CreateOptions{}); err == nil {
		return nil
	} else if !apierrors.IsAlreadyExists(err) {
		return err
	}
	existing, err := c.KubeClient.CoreV1().Services(ns).Get(ctx, svcName, metav1.GetOptions{})
	if err != nil {
		return err
	}
	existing.Labels = desired.Labels
	existing.Spec.Type = desired.Spec.Type
	existing.Spec.ClusterIP = desired.Spec.ClusterIP
	existing.Spec.Ports = desired.Spec.Ports
	existing.Spec.Selector = nil
	_, err = c.KubeClient.CoreV1().Services(ns).Update(ctx, existing, metav1.UpdateOptions{})
	return err
}

func (c *TypedClient) createOrUpdateEndpoints(ctx context.Context, id, ns, svcName, vmIP string) error {
	desired := typedMonitoringEndpoints(id, ns, svcName, vmIP)
	if _, err := c.KubeClient.CoreV1().Endpoints(ns).Create(ctx, desired, metav1.CreateOptions{}); err == nil {
		return nil
	} else if !apierrors.IsAlreadyExists(err) {
		return err
	}
	existing, err := c.KubeClient.CoreV1().Endpoints(ns).Get(ctx, svcName, metav1.GetOptions{})
	if err != nil {
		return err
	}
	existing.Labels = desired.Labels
	existing.Subsets = desired.Subsets
	_, err = c.KubeClient.CoreV1().Endpoints(ns).Update(ctx, existing, metav1.UpdateOptions{})
	return err
}

func (c *TypedClient) createOrUpdateServiceMonitor(ctx context.Context, id, ns, smName string) error {
	desired := typedServiceMonitor(id, ns, smName)
	if _, err := c.Clientset.MonitoringV1().ServiceMonitors(ns).Create(ctx, desired, metav1.CreateOptions{}); err == nil {
		return nil
	} else if !apierrors.IsAlreadyExists(err) {
		return err
	}
	existing, err := c.Clientset.MonitoringV1().ServiceMonitors(ns).Get(ctx, smName, metav1.GetOptions{})
	if err != nil {
		return err
	}
	existing.Labels = desired.Labels
	existing.Spec.Selector = desired.Spec.Selector
	existing.Spec.Endpoints = desired.Spec.Endpoints
	_, err = c.Clientset.MonitoringV1().ServiceMonitors(ns).Update(ctx, existing, metav1.UpdateOptions{})
	return err
}

func typedMonitoringService(id, ns, svcName string) *corev1.Service {
	// create a headless service for the Postgres exporter metrics endpoint
	return &corev1.Service{
		TypeMeta: metav1.TypeMeta{APIVersion: "v1", Kind: "Service"},
		ObjectMeta: metav1.ObjectMeta{
			Name:      svcName,
			Namespace: ns,
			Labels:    map[string]string{dbaasv1.LabelInstance: id, dbaasv1.LabelMetrics: "true"},
		},
		Spec: corev1.ServiceSpec{
			Type:      corev1.ServiceTypeClusterIP,
			ClusterIP: corev1.ClusterIPNone,
			Ports: []corev1.ServicePort{{
				Name:       "metrics",
				Port:       9187,
				TargetPort: intstr.FromInt(9187),
				Protocol:   corev1.ProtocolTCP,
			}},
		},
	}
}

func typedMonitoringEndpoints(id, ns, svcName, vmIP string) *corev1.Endpoints {
	return &corev1.Endpoints{
		TypeMeta: metav1.TypeMeta{APIVersion: "v1", Kind: "Endpoints"},
		ObjectMeta: metav1.ObjectMeta{
			Name:      svcName,
			Namespace: ns,
			Labels:    map[string]string{dbaasv1.LabelInstance: id, dbaasv1.LabelMetrics: "true"},
		},
		Subsets: []corev1.EndpointSubset{{
			Addresses: []corev1.EndpointAddress{{IP: vmIP}},
			Ports: []corev1.EndpointPort{{
				Name:     "metrics",
				Port:     9187,
				Protocol: corev1.ProtocolTCP,
			}},
		}},
	}
}

func typedServiceMonitor(id, ns, smName string) *monitoringv1.ServiceMonitor {
	return &monitoringv1.ServiceMonitor{
		TypeMeta: metav1.TypeMeta{APIVersion: "monitoring.coreos.com/v1", Kind: "ServiceMonitor"},
		ObjectMeta: metav1.ObjectMeta{
			Name:      smName,
			Namespace: ns,
			Labels:    map[string]string{dbaasv1.LabelInstance: id, "release": "prometheus"},
		},
		Spec: monitoringv1.ServiceMonitorSpec{
			Selector: metav1.LabelSelector{
				MatchLabels: map[string]string{dbaasv1.LabelMetrics: "true", dbaasv1.LabelInstance: id},
			},
			Endpoints: []monitoringv1.Endpoint{{
				Port:     "metrics",
				Interval: monitoringv1.Duration("15s"),
				Path:     "/metrics",
			}},
		},
	}
}

func typedVMNetworkName(namespace, nadName string) string {
	if strings.Contains(nadName, "/") {
		return nadName
	}
	return fmt.Sprintf("%s/%s", namespace, nadName)
}

func typedVolumeClaimTemplates(vm *kubevirtv1.VirtualMachine) ([]*corev1.PersistentVolumeClaim, error) {
	raw := vm.Annotations[util.AnnotationVolumeClaimTemplates]
	if raw == "" {
		return nil, fmt.Errorf("VM %s/%s has no %s annotation", vm.Namespace, vm.Name, util.AnnotationVolumeClaimTemplates)
	}
	var pvcs []*corev1.PersistentVolumeClaim
	if err := json.Unmarshal([]byte(raw), &pvcs); err != nil {
		return nil, fmt.Errorf("unmarshal %s: %w", util.AnnotationVolumeClaimTemplates, err)
	}
	return pvcs, nil
}

func mergeStringMap(base map[string]string, overlay map[string]string) map[string]string {
	if len(base) == 0 && len(overlay) == 0 {
		return nil
	}
	out := map[string]string{}
	for k, v := range base {
		out[k] = v
	}
	for k, v := range overlay {
		out[k] = v
	}
	return out
}

func ptr[T any](v T) *T {
	return &v
}
