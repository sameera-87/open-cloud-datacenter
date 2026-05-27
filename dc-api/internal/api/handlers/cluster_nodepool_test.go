// Package handlers — cluster_nodepool_test.go
//
// Unit tests for the AKS-style node-pool handler DTOs and the
// CreateClusterRequest.validate() worker-pool extension.
// These tests exercise the validation layer only — no DB or provider required.
// Provider-level tests live in internal/providers/rancher/pool_test.go.
package handlers

import (
	"fmt"
	"testing"

	"github.com/wso2/dc-api/internal/models"
)

// ─────────────────────────── AddNodePoolRequest.validate() ──────────────────

func TestAddNodePoolRequest_Validate_Valid(t *testing.T) {
	cases := []struct {
		name string
		req  AddNodePoolRequest
	}{
		{
			name: "basic worker",
			req:  AddNodePoolRequest{Name: "workers-01", Size: "large", Count: 3},
		},
		{
			name: "single node",
			req:  AddNodePoolRequest{Name: "spot", Size: "small", Count: 1},
		},
		{
			name: "max workers",
			req:  AddNodePoolRequest{Name: "gpu", Size: "xlarge", Count: 50},
		},
		{
			name: "with valid taint",
			req: AddNodePoolRequest{
				Name:  "gpu-pool",
				Size:  "xlarge",
				Count: 2,
				Taints: []models.NodePoolTaint{
					{Key: "nvidia.com/gpu", Effect: "NoSchedule"},
				},
			},
		},
		{
			name: "with labels",
			req: AddNodePoolRequest{
				Name:   "ml",
				Size:   "medium",
				Count:  4,
				Labels: map[string]string{"team": "ml"},
			},
		},
		{
			name: "single char name",
			req:  AddNodePoolRequest{Name: "a", Size: "small", Count: 1},
		},
		{
			name: "with image override",
			req:  AddNodePoolRequest{Name: "workers", Size: "medium", Count: 2, ImageName: "default/ubuntu-22.04"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if err := tc.req.validate(); err != nil {
				t.Errorf("unexpected error: %v", err)
			}
		})
	}
}

func TestAddNodePoolRequest_Validate_RejectsSystemName(t *testing.T) {
	req := AddNodePoolRequest{Name: "system", Size: "large", Count: 1}
	if err := req.validate(); err == nil {
		t.Error("expected error for name 'system', got nil")
	}
}

func TestAddNodePoolRequest_Validate_RejectsInvalidSizes(t *testing.T) {
	cases := []string{"micro", "2xlarge", "", "LARGE"}
	for _, size := range cases {
		req := AddNodePoolRequest{Name: "workers", Size: size, Count: 1}
		if err := req.validate(); err == nil {
			t.Errorf("expected error for size %q, got nil", size)
		}
	}
}

func TestAddNodePoolRequest_Validate_RejectsInvalidCounts(t *testing.T) {
	// Count 0 is rejected.
	req0 := AddNodePoolRequest{Name: "w", Size: "small", Count: 0}
	if err := req0.validate(); err == nil {
		t.Error("expected error for count 0, got nil")
	}

	// Count 51 is rejected.
	req51 := AddNodePoolRequest{Name: "w", Size: "small", Count: 51}
	if err := req51.validate(); err == nil {
		t.Error("expected error for count 51, got nil")
	}
}

func TestAddNodePoolRequest_Validate_RejectsInvalidNames(t *testing.T) {
	cases := []struct {
		name   string
		reason string
	}{
		{"", "empty"},
		{"UPPER", "uppercase"},
		{"1start", "digit start"},
		{"-start", "hyphen start"},
		{"end-", "trailing hyphen"},
		{"a b", "space"},
		{"a_b", "underscore"},
	}
	for _, tc := range cases {
		req := AddNodePoolRequest{Name: tc.name, Size: "small", Count: 1}
		if err := req.validate(); err == nil {
			t.Errorf("expected error for name %q (%s), got nil", tc.name, tc.reason)
		}
	}
}

func TestAddNodePoolRequest_Validate_RejectsInvalidTaintEffect(t *testing.T) {
	req := AddNodePoolRequest{
		Name:  "workers",
		Size:  "small",
		Count: 1,
		Taints: []models.NodePoolTaint{
			{Key: "k", Effect: "InvalidEffect"},
		},
	}
	if err := req.validate(); err == nil {
		t.Error("expected error for invalid taint effect, got nil")
	}
}

// ─────────────────────────── PatchNodePoolRequest.validate() ─────────────────

func TestPatchNodePoolRequest_Validate_Valid(t *testing.T) {
	req := PatchNodePoolRequest{Count: 5}
	if err := req.validate(); err != nil {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestPatchNodePoolRequest_Validate_RejectsNegativeCount(t *testing.T) {
	req := PatchNodePoolRequest{Count: -1}
	if err := req.validate(); err == nil {
		t.Error("expected error for negative count")
	}
}

func TestPatchNodePoolRequest_Validate_RejectsTooLargeCount(t *testing.T) {
	req := PatchNodePoolRequest{Count: 51}
	if err := req.validate(); err == nil {
		t.Error("expected error for count 51")
	}
}

func TestPatchNodePoolRequest_Validate_ValidTaints(t *testing.T) {
	taints := []models.NodePoolTaint{
		{Key: "batch", Effect: "NoExecute"},
		{Key: "gpu", Value: "present", Effect: "NoSchedule"},
	}
	req := PatchNodePoolRequest{Taints: &taints}
	if err := req.validate(); err != nil {
		t.Errorf("unexpected error for valid taints: %v", err)
	}
}

func TestPatchNodePoolRequest_Validate_RejectsInvalidTaintEffect(t *testing.T) {
	taints := []models.NodePoolTaint{{Key: "k", Effect: "Wrong"}}
	req := PatchNodePoolRequest{Taints: &taints}
	if err := req.validate(); err == nil {
		t.Error("expected error for invalid taint effect")
	}
}

// ─────────────────────────── isValidTaintEffect ──────────────────────────────

func TestIsValidTaintEffect(t *testing.T) {
	valid := []string{"NoSchedule", "PreferNoSchedule", "NoExecute"}
	for _, e := range valid {
		if !isValidTaintEffect(e) {
			t.Errorf("expected %q to be valid", e)
		}
	}

	invalid := []string{"", "noschedule", "Always", "BadEffect", "NOSCHEDULE"}
	for _, e := range invalid {
		if isValidTaintEffect(e) {
			t.Errorf("expected %q to be invalid", e)
		}
	}
}

// ─────────────────────────── CreateClusterRequest.validate() — worker pools ──

// minValidClusterReq returns a CreateClusterRequest that passes validation
// using the legacy bridge path (network_name). Worker pool tests start from
// this baseline and mutate only the worker_pools field.
func minValidClusterReq() CreateClusterRequest {
	return CreateClusterRequest{
		Name:        "prod-k8s-01",
		K8sVersion:  "v1.33.10+rke2r3",
		ImageName:   "default/image-rflb5",
		NetworkName: "default/vm-net-100",
		SystemPool:  &SystemPoolSpec{Size: "medium", Count: 3},
	}
}

func TestCreateClusterRequest_Validate_NoWorkerPools(t *testing.T) {
	req := minValidClusterReq()
	if err := req.validate(); err != nil {
		t.Errorf("expected nil error for empty worker_pools, got: %v", err)
	}
}

func TestCreateClusterRequest_Validate_OneValidWorkerPool(t *testing.T) {
	req := minValidClusterReq()
	req.WorkerPools = []AddNodePoolRequest{
		{Name: "workers-01", Size: "large", Count: 3},
	}
	if err := req.validate(); err != nil {
		t.Errorf("expected nil error for one valid worker pool, got: %v", err)
	}
}

func TestCreateClusterRequest_Validate_TenWorkerPools(t *testing.T) {
	req := minValidClusterReq()
	for i := 0; i < 10; i++ {
		req.WorkerPools = append(req.WorkerPools, AddNodePoolRequest{
			Name: fmt.Sprintf("workers-%02d", i),
			Size: "small",
			Count: 1,
		})
	}
	if err := req.validate(); err != nil {
		t.Errorf("expected nil error for 10 worker pools (maxItems), got: %v", err)
	}
}

func TestCreateClusterRequest_Validate_ElevenWorkerPools_Rejected(t *testing.T) {
	req := minValidClusterReq()
	for i := 0; i < 11; i++ {
		req.WorkerPools = append(req.WorkerPools, AddNodePoolRequest{
			Name: fmt.Sprintf("workers-%02d", i),
			Size: "small",
			Count: 1,
		})
	}
	if err := req.validate(); err == nil {
		t.Error("expected error for 11 worker pools (exceeds maxItems=10), got nil")
	}
}

func TestCreateClusterRequest_Validate_DuplicatePoolNames_Rejected(t *testing.T) {
	req := minValidClusterReq()
	req.WorkerPools = []AddNodePoolRequest{
		{Name: "gpu", Size: "xlarge", Count: 2},
		{Name: "gpu", Size: "large", Count: 1}, // duplicate name
	}
	if err := req.validate(); err == nil {
		t.Error("expected error for duplicate worker pool names, got nil")
	}
}

func TestCreateClusterRequest_Validate_SystemNameReserved_Rejected(t *testing.T) {
	req := minValidClusterReq()
	req.WorkerPools = []AddNodePoolRequest{
		{Name: "system", Size: "medium", Count: 1},
	}
	if err := req.validate(); err == nil {
		t.Error("expected error for worker pool named 'system', got nil")
	}
}

func TestCreateClusterRequest_Validate_InvalidWorkerPoolSize_Rejected(t *testing.T) {
	req := minValidClusterReq()
	req.WorkerPools = []AddNodePoolRequest{
		{Name: "workers", Size: "2xlarge", Count: 1},
	}
	if err := req.validate(); err == nil {
		t.Error("expected error for invalid worker pool size, got nil")
	}
}

func TestCreateClusterRequest_Validate_WorkerPoolInvalidTaintEffect_Rejected(t *testing.T) {
	req := minValidClusterReq()
	req.WorkerPools = []AddNodePoolRequest{
		{
			Name:  "workers",
			Size:  "medium",
			Count: 2,
			Taints: []models.NodePoolTaint{
				{Key: "k", Effect: "BadEffect"},
			},
		},
	}
	if err := req.validate(); err == nil {
		t.Error("expected error for invalid taint effect in worker pool, got nil")
	}
}

// ─────────────────────────── poolNameRE ──────────────────────────────────────

func TestPoolNameRE(t *testing.T) {
	valid := []string{"a", "workers-01", "gpu-pool", "a0", "abc123"}
	for _, n := range valid {
		if !poolNameRE.MatchString(n) {
			t.Errorf("expected %q to match poolNameRE", n)
		}
	}

	invalid := []string{"", "A", "1start", "-start", "end-", "has space", "has_underscore"}
	for _, n := range invalid {
		if poolNameRE.MatchString(n) {
			t.Errorf("expected %q NOT to match poolNameRE", n)
		}
	}
}
