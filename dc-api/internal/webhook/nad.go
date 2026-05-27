// Package webhook implements the mutating admission webhook for KubeVirt
// VirtualMachine resources.  See mutate.go for the core logic.
package webhook

import (
	"context"
	"encoding/json"
	"fmt"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
)

// nadGVR is the GroupVersionResource for Multus NetworkAttachmentDefinitions.
var nadGVR = schema.GroupVersionResource{
	Group:    "k8s.cni.cncf.io",
	Version:  "v1",
	Resource: "network-attachment-definitions",
}

// nadConfig is the parsed JSON from NetworkAttachmentDefinition.spec.config.
// Only the "type" field is used for the OVN detection check; other fields
// are intentionally ignored so we don't break on non-standard NAD configs.
type nadConfig struct {
	Type string `json:"type"`
}

// NADLookup defines how the mutator resolves a NetworkAttachmentDefinition
// to its CNI type.  The interface is narrow so unit tests can inject a
// simple fake without spinning up an apiserver.
type NADLookup interface {
	// CNIType returns the "type" field from the NAD's spec.config JSON.
	// Returns ("", nil) if the NAD does not exist.
	// Returns ("", err) on any transport/unmarshal error.
	CNIType(ctx context.Context, namespace, name string) (string, error)
}

// DynamicNADLookup implements NADLookup using the Kubernetes dynamic client.
// This is the production implementation wired up in cmd/webhook/main.go.
type DynamicNADLookup struct {
	client dynamic.Interface
}

// NewDynamicNADLookup constructs a DynamicNADLookup from the given dynamic client.
func NewDynamicNADLookup(client dynamic.Interface) *DynamicNADLookup {
	return &DynamicNADLookup{client: client}
}

// CNIType fetches the NAD and returns the "type" value from its spec.config.
// If the NAD is not found, ("", nil) is returned — the caller treats missing
// NADs as non-OVN and skips mutation.
func (d *DynamicNADLookup) CNIType(ctx context.Context, namespace, name string) (string, error) {
	obj, err := d.client.Resource(nadGVR).Namespace(namespace).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return "", fmt.Errorf("get NAD %s/%s: %w", namespace, name, err)
	}

	raw, found, err := unstructuredString(obj.Object, "spec", "config")
	if err != nil || !found || raw == "" {
		return "", nil
	}

	var cfg nadConfig
	if err := json.Unmarshal([]byte(raw), &cfg); err != nil {
		return "", fmt.Errorf("parse NAD config %s/%s: %w", namespace, name, err)
	}

	return cfg.Type, nil
}

// unstructuredString retrieves a nested string from an unstructured object map.
// Returns ("", false, nil) when any intermediate key is absent.
// Returns ("", false, err) when an intermediate value is not a map.
func unstructuredString(obj map[string]interface{}, fields ...string) (string, bool, error) {
	cur := obj
	for i, f := range fields {
		v, ok := cur[f]
		if !ok {
			return "", false, nil
		}
		if i == len(fields)-1 {
			s, ok := v.(string)
			if !ok {
				return "", false, fmt.Errorf("field %q is not a string", f)
			}
			return s, true, nil
		}
		m, ok := v.(map[string]interface{})
		if !ok {
			return "", false, fmt.Errorf("field %q is not a map", f)
		}
		cur = m
	}
	return "", false, nil
}
