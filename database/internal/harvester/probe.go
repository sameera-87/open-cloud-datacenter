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
	"fmt"
	"net"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

// podGVR is the GVR for the v1 Pod resource, used to find the
// virt-launcher pod that backs a VirtualMachineInstance.
var podGVR = schema.GroupVersionResource{Group: "", Version: "v1", Resource: "pods"}

// dialTimeout is how long a single TCP-connect attempt waits for SYN-ACK
// before being declared failed. Short on purpose: the controller's
// reconcile loop will retry, and a long block here would stall every
// reconcile that touches a still-booting VM.
const dialTimeout = 3 * time.Second

// DialVMListener confirms PostgreSQL inside the VM is accepting TCP
// connections by dialing the launcher pod's pod-network IP at the
// configured Postgres port.
//
// This works because every DB VM (see vmInterfaces) has a second NIC,
// mgmt-net, running in KubeVirt masquerade mode on the cluster's pod
// network with the Postgres port exposed via KubeVirt port-forwarding.
// The launcher pod's IP is reachable from any other pod in the cluster,
// including the dbaas controller — so the check is just a single
// net.DialTimeout, with no probe Pod, no Multus dance, and no
// dependency on DHCP being present on the data VLAN.
//
// Returns nil on a successful TCP handshake. Any other outcome (no
// running launcher pod, no podIP yet, connection refused, timeout) is
// returned as an error so the reconciler treats it as "not ready yet,
// retry next reconcile".
func (c *Client) DialVMListener(ctx context.Context, ns, vmName string, port int) error {
	pods := c.Dynamic.Resource(podGVR).Namespace(ns)
	list, err := pods.List(ctx, metav1.ListOptions{
		LabelSelector: fmt.Sprintf("vm.kubevirt.io/name=%s", vmName),
	})
	if err != nil {
		return fmt.Errorf("list launcher pods for %s: %w", vmName, err)
	}

	podIP := ""
	for _, p := range list.Items {
		phase, _, _ := unstructured.NestedString(p.Object, "status", "phase")
		ip, _, _ := unstructured.NestedString(p.Object, "status", "podIP")
		if phase == "Running" && ip != "" {
			podIP = ip
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
