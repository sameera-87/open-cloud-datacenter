//go:build integration

package integration

import (
	"context"
	"fmt"
	"strings"

	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
)

var (
	kubeovnVpcGVR    = schema.GroupVersionResource{Group: "kubeovn.io", Version: "v1", Resource: "vpcs"}
	kubeovnSubnetGVR = schema.GroupVersionResource{Group: "kubeovn.io", Version: "v1", Resource: "subnets"}
	// vpc-peerings.kubeovn.io CRD does NOT exist in KubeOVN v1.15. Peering
	// is configured via the parent Vpc's spec.vpcPeerings field.
)

func VpcExists(ctx context.Context, client dynamic.Interface, name string) (bool, error) {
	_, err := client.Resource(kubeovnVpcGVR).Get(ctx, name, metav1.GetOptions{})
	if k8serrors.IsNotFound(err) {
		return false, nil
	}
	return err == nil, err
}

func SubnetExists(ctx context.Context, client dynamic.Interface, name string) (bool, error) {
	_, err := client.Resource(kubeovnSubnetGVR).Get(ctx, name, metav1.GetOptions{})
	if k8serrors.IsNotFound(err) {
		return false, nil
	}
	return err == nil, err
}

// VpcPeeringExists checks whether a peering exists between the two VPCs
// encoded in the composite backendUID `<vnetA>/<vnetB>`. Looks at the
// vnetA Vpc's spec.vpcPeerings for an entry whose remoteVpc == vnetB.
func VpcPeeringExists(ctx context.Context, client dynamic.Interface, backendUID string) (bool, error) {
	parts := strings.SplitN(backendUID, "/", 2)
	if len(parts) != 2 {
		return false, fmt.Errorf("VpcPeeringExists: backendUID %q must be \"<vnetA>/<vnetB>\"", backendUID)
	}
	vnetA, vnetB := parts[0], parts[1]
	obj, err := client.Resource(kubeovnVpcGVR).Get(ctx, vnetA, metav1.GetOptions{})
	if k8serrors.IsNotFound(err) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	spec, _ := obj.Object["spec"].(map[string]interface{})
	if spec == nil {
		return false, nil
	}
	peerings, _ := spec["vpcPeerings"].([]interface{})
	for _, p := range peerings {
		if m, ok := p.(map[string]interface{}); ok {
			if rv, _ := m["remoteVpc"].(string); rv == vnetB {
				return true, nil
			}
		}
	}
	return false, nil
}

func GetVpcStaticRoutes(ctx context.Context, client dynamic.Interface, vpcName string) ([]interface{}, error) {
	obj, err := client.Resource(kubeovnVpcGVR).Get(ctx, vpcName, metav1.GetOptions{})
	if err != nil {
		return nil, fmt.Errorf("get vpc %s: %w", vpcName, err)
	}
	spec, _ := obj.Object["spec"].(map[string]interface{})
	if spec == nil {
		return nil, nil
	}
	routes, _ := spec["staticRoutes"].([]interface{})
	return routes, nil
}

func GetSubnetACLs(ctx context.Context, client dynamic.Interface, subnetName string) ([]interface{}, error) {
	obj, err := client.Resource(kubeovnSubnetGVR).Get(ctx, subnetName, metav1.GetOptions{})
	if err != nil {
		return nil, fmt.Errorf("get subnet %s: %w", subnetName, err)
	}
	spec, _ := obj.Object["spec"].(map[string]interface{})
	if spec == nil {
		return nil, nil
	}
	acls, _ := spec["acls"].([]interface{})
	return acls, nil
}

// ACLsContainNSGTag checks whether any ACL entry's match string contains the
// nsg-<id> tag. The tag is embedded inside the OVN match expression (as an
// always-false `inport == "nsg-<id>"` clause OR'd with the real match) because
// the KubeOVN Subnet CRD strips any unknown top-level field like "name".
func ACLsContainNSGTag(acls []interface{}, nsgID string) bool {
	tag := "nsg-" + nsgID
	for _, a := range acls {
		m, ok := a.(map[string]interface{})
		if !ok {
			continue
		}
		if match, ok := m["match"].(string); ok && strings.Contains(match, tag) {
			return true
		}
	}
	return false
}

// StaticRoutesContainTag checks whether any route entry's routeTable field starts with tag.
func StaticRoutesContainTag(routes []interface{}, tag string) bool {
	for _, r := range routes {
		m, ok := r.(map[string]interface{})
		if !ok {
			continue
		}
		if rt, ok := m["routeTable"].(string); ok && strings.HasPrefix(rt, tag) {
			return true
		}
	}
	return false
}
