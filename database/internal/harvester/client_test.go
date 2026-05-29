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
	"encoding/base64"
	"strings"
	"testing"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic/fake"
)

func TestCreatePostgresVMDoesNotCreateSecretWhenImageResolutionFails(t *testing.T) {
	ctx := context.Background()
	// client without the fake image
	client := newTestHarvesterClient()

	_, secretName, _, err := client.CreatePostgresVM(ctx, testVMCreateParams())
	if err == nil {
		t.Fatalf("CreatePostgresVM returned nil error, want image resolution error")
	}

	// check wether secret is created : Expected = No secret created (NotFoundError)
	if _, err := client.Dynamic.Resource(secretGVR).Namespace("tenant-a").Get(ctx, secretName, metav1.GetOptions{}); !apierrors.IsNotFound(err) {
		t.Fatalf("credentials Secret lookup error = %v, want NotFound", err)
	}
}

func TestCreatePostgresVMCreatesSecretAndReturnsStoredCA(t *testing.T) {
	ctx := context.Background()
	client := newTestHarvesterClient(testVMImage())

	vmName, secretName, caCertPEM, err := client.CreatePostgresVM(ctx, testVMCreateParams())
	if err != nil {
		t.Fatalf("CreatePostgresVM returned error: %v", err)
	}
	if vmName != "pg-orders" {
		t.Fatalf("VM name = %q, want pg-orders", vmName)
	}
	if secretName != "pg-orders-credentials" {
		t.Fatalf("Secret name = %q, want pg-orders-credentials", secretName)
	}
	if caCertPEM == "" {
		t.Fatalf("CA cert is empty")
	}

	// fetch the created fake secret
	secret, err := client.Dynamic.Resource(secretGVR).Namespace("tenant-a").Get(ctx, secretName, metav1.GetOptions{})
	if err != nil {
		t.Fatalf("get credentials Secret: %v", err)
	}
	// In client.createOrReuseCredentialSecret() we submit stringData to the API server
	// We Read stringData because fake client doesn't encode the submitted "stringData" into Base64 "data" as the real API server
	secretCA, _, _ := unstructured.NestedString(secret.Object, "stringData", "ca_cert")
	if secretCA == "" {
		t.Fatalf("created Secret has no stringData.ca_cert")
	}
	// check the CA returned by CreatePostgresVM == the CA stored in the Secret
	if caCertPEM != secretCA {
		t.Fatalf("returned CA does not match Secret CA")
	}
	// fetch the VM
	if _, err := client.Dynamic.Resource(vmGVR).Namespace("tenant-a").Get(ctx, vmName, metav1.GetOptions{}); err != nil {
		t.Fatalf("get created VM: %v", err)
	}
}

func TestCreatePostgresVMReusesExistingSecretCA(t *testing.T) {
	ctx := context.Background()
	existingCA := "existing-ca"
	client := newTestHarvesterClient(testVMImage(), testCredentialSecret(existingCA))

	_, _, caCertPEM, err := client.CreatePostgresVM(ctx, testVMCreateParams())
	if err != nil {
		t.Fatalf("CreatePostgresVM returned error: %v", err)
	}
	if caCertPEM != existingCA {
		t.Fatalf("returned CA = %q, want existing Secret CA %q", caCertPEM, existingCA)
	}
}

func TestCreatePostgresVMFailsForMalformedExistingSecret(t *testing.T) {
	ctx := context.Background()
	secret := newUnstructured("v1", "Secret", "pg-orders-credentials", "tenant-a")
	// No CA cert in the secret intentionally , hence malformed for the cotroller
	_ = unstructured.SetNestedField(secret.Object, "Opaque", "type")
	client := newTestHarvesterClient(testVMImage(), secret)

	vmName, _, _, err := client.CreatePostgresVM(ctx, testVMCreateParams())
	if err == nil {
		t.Fatalf("CreatePostgresVM returned nil error, want missing ca_cert error")
	}
	if !strings.Contains(err.Error(), "missing ca_cert") {
		t.Fatalf("error = %v, want missing ca_cert", err)
	}
	// check wether VM creation was failed.
	if _, getErr := client.Dynamic.Resource(vmGVR).Namespace("tenant-a").Get(ctx, vmName, metav1.GetOptions{}); !apierrors.IsNotFound(getErr) {
		t.Fatalf("VM lookup error = %v, want NotFound", getErr)
	}
}

func TestCreatePostgresVMPreservesKubeOVNSettings(t *testing.T) {
	ctx := context.Background()
	client := newTestHarvesterClient(testVMImage())
	client.MgmtLogicalSwitch = "ovn-default"
	params := testVMCreateParams()
	params.DNSServerIP = "10.96.0.10/32"

	vmName, _, _, err := client.CreatePostgresVM(ctx, params)
	if err != nil {
		t.Fatalf("CreatePostgresVM returned error: %v", err)
	}
	vm, err := client.Dynamic.Resource(vmGVR).Namespace("tenant-a").Get(ctx, vmName, metav1.GetOptions{})
	if err != nil {
		t.Fatalf("get created VM: %v", err)
	}

	logicalSwitch, _, _ := unstructured.NestedString(vm.Object, "spec", "template", "metadata", "annotations", "ovn.kubernetes.io/logical_switch")
	if logicalSwitch != "ovn-default" {
		t.Fatalf("logical switch annotation = %q, want ovn-default", logicalSwitch)
	}
	dnsPolicy, _, _ := unstructured.NestedString(vm.Object, "spec", "template", "spec", "dnsPolicy")
	if dnsPolicy != "None" {
		t.Fatalf("dnsPolicy = %q, want None", dnsPolicy)
	}
	nameservers, _, _ := unstructured.NestedStringSlice(vm.Object, "spec", "template", "spec", "dnsConfig", "nameservers")
	if len(nameservers) != 1 || nameservers[0] != "10.96.0.10" {
		t.Fatalf("nameservers = %v, want [10.96.0.10]", nameservers)
	}
}

func TestDeployMonitoringIsIdempotent(t *testing.T) {
	client := NewClient(fake.NewSimpleDynamicClient(runtime.NewScheme()), "https://grafana.example")

	for i := range 2 {
		svcName, smName, grafanaURL, promTarget, err := client.DeployMonitoring(context.Background(), "orders", "tenant-a", "192.168.40.50")
		if err != nil {
			t.Fatalf("DeployMonitoring call %d returned error: %v", i+1, err)
		}
		if svcName != "pg-orders-metrics" {
			t.Fatalf("service name = %q, want pg-orders-metrics", svcName)
		}
		if smName != "pg-orders-monitor" {
			t.Fatalf("ServiceMonitor name = %q, want pg-orders-monitor", smName)
		}
		if grafanaURL != "https://grafana.example/d/dbaas-orders/postgresql-orders" {
			t.Fatalf("Grafana URL = %q", grafanaURL)
		}
		if promTarget != "pg-orders-metrics.tenant-a.svc:9187" {
			t.Fatalf("Prometheus target = %q", promTarget)
		}
	}

	svc, err := client.Dynamic.Resource(serviceGVR).Namespace("tenant-a").Get(context.Background(), "pg-orders-metrics", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("get Service: %v", err)
	}
	if selector, found, err := unstructured.NestedMap(svc.Object, "spec", "selector"); err != nil || found || len(selector) != 0 {
		t.Fatalf("Service selector = %v, found=%t, err=%v; want no selector", selector, found, err)
	}

	ep, err := client.Dynamic.Resource(endpointsGVR).Namespace("tenant-a").Get(context.Background(), "pg-orders-metrics", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("get Endpoints: %v", err)
	}
	subsets, found, err := unstructured.NestedSlice(ep.Object, "subsets")
	if err != nil || !found || len(subsets) != 1 {
		t.Fatalf("Endpoint subsets = %v, found=%t, err=%v", subsets, found, err)
	}
	subset := subsets[0].(map[string]any)
	addresses := subset["addresses"].([]any)
	address := addresses[0].(map[string]any)
	if address["ip"] != "192.168.40.50" {
		t.Fatalf("Endpoint IP = %v, want 192.168.40.50", address["ip"])
	}
}

// Helpers
func newTestHarvesterClient(objs ...runtime.Object) *Client {
	listKinds := map[schema.GroupVersionResource]string{
		vmImageGVR: "VirtualMachineImageList",
	}
	return NewClient(fake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), listKinds, objs...), "")
}

func testVMCreateParams() VMCreateParams {
	return VMCreateParams{
		ID:             "orders",
		Namespace:      "tenant-a",
		CPUCores:       2,
		MemoryMB:       4096,
		OSImage:        "ubuntu-22.04",
		DataVolumeRef:  "pg-orders-data",
		NADName:        "tenant-a/vm-network",
		MasterUser:     "dbadmin",
		DBName:         "orders",
		Port:           5432,
		MaxConnections: 100,
	}
}

func testVMImage() *unstructured.Unstructured {
	img := newUnstructured("harvesterhci.io/v1beta1", "VirtualMachineImage", "ubuntu-22.04", "default")
	// set status.storageClassName to indicate the fake VM Image is ready
	_ = unstructured.SetNestedField(img.Object, "longhorn-image-ubuntu", "status", "storageClassName")
	return img
}

func testCredentialSecret(caCert string) *unstructured.Unstructured {
	secret := newUnstructured("v1", "Secret", "pg-orders-credentials", "tenant-a")
	_ = unstructured.SetNestedField(secret.Object, "Opaque", "type")
	_ = unstructured.SetNestedStringMap(secret.Object, map[string]string{
		"ca_cert": base64.StdEncoding.EncodeToString([]byte(caCert)),
	}, "data")
	return secret
}
