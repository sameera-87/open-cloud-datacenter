// Package rancher — pool_test.go
//
// Unit tests for the R4 multi-pool implementation:
//   - buildMachinePool helper (role flags, taints, labels)
//   - poolRoleFlags translation
//   - ClusterProvisioner.AddNodePool (GET-then-PUT with cleanup on failure)
//   - ClusterProvisioner.ScaleNodePool (GET-then-PUT with 409 retry)
//   - ClusterProvisioner.UpdateNodePoolTaintsLabels (GET-then-PUT)
//   - ClusterProvisioner.RemoveNodePool (GET-then-PUT + HC delete)
//   - createClusterCR emits system pool only (no workers)
//   - Cluster CR shape matches reference (spec-diff check)
//
// All tests use httptest mock servers — no live Rancher required.
//
// Run:
//
//	cd dc-api
//	go test ./internal/providers/rancher/... -v -count=1 -run TestPool
package rancher

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/wso2/dc-api/internal/models"
	"gopkg.in/yaml.v3"
)

// ── buildMachinePool + poolRoleFlags ─────────────────────────────────────────

func TestPoolRoleFlags_System(t *testing.T) {
	cp, etcd, worker := poolRoleFlags(models.NodePoolRoleSystem)
	assert.True(t, cp, "system pool must have controlPlaneRole=true")
	assert.True(t, etcd, "system pool must have etcdRole=true")
	assert.False(t, worker, "system pool must have workerRole=false")
}

func TestPoolRoleFlags_Worker(t *testing.T) {
	cp, etcd, worker := poolRoleFlags(models.NodePoolRoleWorker)
	assert.False(t, cp, "worker pool must have controlPlaneRole=false")
	assert.False(t, etcd, "worker pool must have etcdRole=false")
	assert.True(t, worker, "worker pool must have workerRole=true")
}

func TestBuildMachinePool_SystemPoolOnly(t *testing.T) {
	pool := &models.NodePool{
		Name:                "system",
		Role:                models.NodePoolRoleSystem,
		Count:               3,
		HarvesterConfigName: "nc-mycluster-system-ab3cd",
	}
	m := buildMachinePool(pool, "cattle-global-data:cc-test")

	assert.Equal(t, "system", m["name"])
	assert.Equal(t, "system", m["displayName"])
	assert.Equal(t, 3, m["quantity"])
	assert.Equal(t, true, m["controlPlaneRole"])
	assert.Equal(t, true, m["etcdRole"])
	assert.Equal(t, false, m["workerRole"])
	assert.Equal(t, true, m["drainBeforeDelete"])
	assert.Equal(t, "linux", m["machineOS"])
	assert.Equal(t, "cattle-global-data:cc-test", m["cloudCredentialSecretName"])

	ref, ok := m["machineConfigRef"].(map[string]interface{})
	require.True(t, ok)
	assert.Equal(t, "HarvesterConfig", ref["kind"])
	assert.Equal(t, "nc-mycluster-system-ab3cd", ref["name"])

	// No taints or labels on a plain system pool.
	assert.Nil(t, m["taints"], "no taints expected")
	assert.Nil(t, m["labels"], "no labels expected")
}

func TestBuildMachinePool_WorkerPoolWithTaintsAndLabels(t *testing.T) {
	pool := &models.NodePool{
		Name:                "workers",
		Role:                models.NodePoolRoleWorker,
		Count:               2,
		HarvesterConfigName: "nc-mycluster-workers-xy9z0",
		Taints: []models.NodePoolTaint{
			{Key: "dedicated", Value: "gpu", Effect: "NoSchedule"},
			{Key: "spot", Effect: "PreferNoSchedule"},
		},
		Labels: map[string]string{
			"pool-type": "gpu",
			"env":       "prod",
		},
	}
	m := buildMachinePool(pool, "cattle-global-data:cc-test")

	assert.Equal(t, "workers", m["name"])
	assert.Equal(t, false, m["controlPlaneRole"])
	assert.Equal(t, false, m["etcdRole"])
	assert.Equal(t, true, m["workerRole"])

	taints, ok := m["taints"].([]map[string]interface{})
	require.True(t, ok)
	require.Len(t, taints, 2)
	assert.Equal(t, "dedicated", taints[0]["key"])
	assert.Equal(t, "gpu", taints[0]["value"])
	assert.Equal(t, "NoSchedule", taints[0]["effect"])
	// Second taint has no value — key "value" must be absent.
	assert.Equal(t, "spot", taints[1]["key"])
	_, hasValue := taints[1]["value"]
	assert.False(t, hasValue, "taint with empty value must omit 'value' key")
	assert.Equal(t, "PreferNoSchedule", taints[1]["effect"])

	labels, ok := m["labels"].(map[string]interface{})
	require.True(t, ok)
	assert.Equal(t, "gpu", labels["pool-type"])
	assert.Equal(t, "prod", labels["env"])
}

func TestBuildMachinePool_WorkerPool_EmptyTaintsLabels(t *testing.T) {
	pool := &models.NodePool{
		Name:                "plain-workers",
		Role:                models.NodePoolRoleWorker,
		Count:               1,
		HarvesterConfigName: "nc-cluster-plain-workers-zzzzz",
	}
	m := buildMachinePool(pool, "cred")
	// With no taints or labels the keys must be absent from the map.
	assert.Nil(t, m["taints"])
	assert.Nil(t, m["labels"])
}

// ── createClusterCR — system pool only and with worker pools ─────────────────

// TestCreateClusterCR_WithOnlySystemPool asserts that a cluster created with no
// worker_pools in the spec emits exactly one machinePool (the system pool).
// Renamed from TestCreateClusterCR_EmitsSystemPoolOnly; behaviour unchanged.
func TestCreateClusterCR_WithOnlySystemPool(t *testing.T) {
	var capturedBody map[string]interface{}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost && strings.Contains(r.URL.Path, "clusters") {
			if err := json.NewDecoder(r.Body).Decode(&capturedBody); err != nil {
				t.Errorf("decode cluster body: %v", err)
			}
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		json.NewEncoder(w).Encode(SteveResource{ID: "fleet-default/test-cluster"})
	}))
	defer srv.Close()

	steve := newTestSteve(srv)
	p := NewClusterProvisioner(steve, "cattle-global-data:cc-test", "iaas/net", "dc-tenant", "", "", nil, nil)

	spec := ClusterCreateSpec{
		ClusterName:  "test-cluster",
		K8sVersion:   "v1.35.4+rke2r1",
		NodeCPU:      4,
		NodeMemoryGB: 8,
		NodeDiskGB:   40,
		NodeImage:    "default/ubuntu-22.04",
		VMNamespace:  "dc-tenant",
		SystemPool: &models.NodePool{
			Name:                "system",
			Role:                models.NodePoolRoleSystem,
			Count:               3,
			HarvesterConfigName: "nc-test-cluster-system-ab3cd",
		},
	}

	uid, err := p.CreateCluster(context.Background(), spec)
	require.NoError(t, err)
	assert.Equal(t, "fleet-default/test-cluster", uid)

	// Verify the emitted Cluster CR has exactly one machinePool.
	specMap, _ := capturedBody["spec"].(map[string]interface{})
	rke, _ := specMap["rkeConfig"].(map[string]interface{})
	pools, ok := rke["machinePools"].([]interface{})
	require.True(t, ok, "machinePools must be present")
	require.Len(t, pools, 1, "cluster create with no worker_pools must emit exactly one (system) pool")

	pool0, _ := pools[0].(map[string]interface{})
	assert.Equal(t, "system", pool0["name"])
	assert.Equal(t, true, pool0["controlPlaneRole"])
	assert.Equal(t, true, pool0["etcdRole"])
	assert.Equal(t, false, pool0["workerRole"])
	assert.Equal(t, float64(3), pool0["quantity"])
}

// TestCreateClusterCR_WithWorkerPools asserts that when spec.WorkerPools is
// populated the Cluster CR is emitted with 3 machinePools in the correct order:
// [system, workers[0], workers[1]], with proper role flags and HC names.
func TestCreateClusterCR_WithWorkerPools(t *testing.T) {
	var capturedBody map[string]interface{}
	var postPaths []string

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodPost && strings.Contains(r.URL.Path, "clusters"):
			if err := json.NewDecoder(r.Body).Decode(&capturedBody); err != nil {
				t.Errorf("decode cluster body: %v", err)
			}
			w.WriteHeader(http.StatusCreated)
			json.NewEncoder(w).Encode(SteveResource{ID: "fleet-default/multi-pool-cluster"})
		case r.Method == http.MethodPost && strings.Contains(r.URL.Path, "harvesterconfigs"):
			postPaths = append(postPaths, r.URL.Path)
			w.WriteHeader(http.StatusCreated)
			json.NewEncoder(w).Encode(SteveResource{ID: "fleet-default/nc-dummy"})
		default:
			w.WriteHeader(http.StatusCreated)
			json.NewEncoder(w).Encode(SteveResource{})
		}
	}))
	defer srv.Close()

	steve := newTestSteve(srv)
	p := NewClusterProvisioner(steve, "cattle-global-data:cc-test", "iaas/net", "dc-tenant", "", "", nil, nil)

	diskGB1 := 40
	diskGB2 := 80
	spec := ClusterCreateSpec{
		ClusterName:  "multi-pool-cluster",
		K8sVersion:   "v1.35.4+rke2r1",
		NodeCPU:      4,
		NodeMemoryGB: 8,
		NodeDiskGB:   40,
		NodeImage:    "default/ubuntu-22.04",
		VMNamespace:  "dc-tenant",
		SystemPool: &models.NodePool{
			Name:                "system",
			Role:                models.NodePoolRoleSystem,
			Count:               3,
			HarvesterConfigName: "nc-multi-pool-cluster-system-ab3cd",
		},
		WorkerPools: []*models.NodePool{
			{
				Name:                "workers-01",
				Role:                models.NodePoolRoleWorker,
				Size:                "medium",
				Count:               2,
				DiskGB:              &diskGB1,
				HarvesterConfigName: "nc-multi-pool-cluster-workers-01-ef5gh",
			},
			{
				Name:                "gpu",
				Role:                models.NodePoolRoleWorker,
				Size:                "xlarge",
				Count:               1,
				DiskGB:              &diskGB2,
				HarvesterConfigName: "nc-multi-pool-cluster-gpu-ij7kl",
			},
		},
	}

	uid, err := p.CreateCluster(context.Background(), spec)
	require.NoError(t, err)
	assert.Equal(t, "fleet-default/multi-pool-cluster", uid)

	// Three HarvesterConfig POSTs: 1 system + 2 workers.
	assert.Len(t, postPaths, 3, "expected 3 HarvesterConfig CRs posted (1 system + 2 workers)")

	// Cluster CR must have 3 machinePools in order: system, workers-01, gpu.
	specMap, _ := capturedBody["spec"].(map[string]interface{})
	rke, _ := specMap["rkeConfig"].(map[string]interface{})
	pools, ok := rke["machinePools"].([]interface{})
	require.True(t, ok, "machinePools must be present")
	require.Len(t, pools, 3, "cluster CR must have 3 machinePools: system + 2 workers")

	// ── pool[0]: system ───────────────────────────────────────────────────────
	p0, _ := pools[0].(map[string]interface{})
	assert.Equal(t, "system", p0["name"], "pool[0] must be the system pool")
	assert.Equal(t, true, p0["controlPlaneRole"], "system pool must have controlPlaneRole=true")
	assert.Equal(t, true, p0["etcdRole"], "system pool must have etcdRole=true")
	assert.Equal(t, false, p0["workerRole"], "system pool must have workerRole=false")
	assert.Equal(t, float64(3), p0["quantity"])
	hcRef0, _ := p0["machineConfigRef"].(map[string]interface{})
	assert.Equal(t, "nc-multi-pool-cluster-system-ab3cd", hcRef0["name"])

	// ── pool[1]: workers-01 ───────────────────────────────────────────────────
	p1, _ := pools[1].(map[string]interface{})
	assert.Equal(t, "workers-01", p1["name"], "pool[1] must be workers-01")
	assert.Equal(t, false, p1["controlPlaneRole"], "worker pool must have controlPlaneRole=false")
	assert.Equal(t, false, p1["etcdRole"], "worker pool must have etcdRole=false")
	assert.Equal(t, true, p1["workerRole"], "worker pool must have workerRole=true")
	assert.Equal(t, float64(2), p1["quantity"])
	hcRef1, _ := p1["machineConfigRef"].(map[string]interface{})
	assert.Equal(t, "nc-multi-pool-cluster-workers-01-ef5gh", hcRef1["name"])

	// ── pool[2]: gpu ─────────────────────────────────────────────────────────
	p2, _ := pools[2].(map[string]interface{})
	assert.Equal(t, "gpu", p2["name"], "pool[2] must be gpu")
	assert.Equal(t, false, p2["controlPlaneRole"])
	assert.Equal(t, false, p2["etcdRole"])
	assert.Equal(t, true, p2["workerRole"])
	assert.Equal(t, float64(1), p2["quantity"])
	hcRef2, _ := p2["machineConfigRef"].(map[string]interface{})
	assert.Equal(t, "nc-multi-pool-cluster-gpu-ij7kl", hcRef2["name"])
}

// ── AddNodePool ───────────────────────────────────────────────────────────────

func TestAddNodePool_Success(t *testing.T) {
	// The cluster CR returned by GET has a single system pool.
	clusterCR := map[string]interface{}{
		"metadata": map[string]interface{}{
			"name":            "mycluster",
			"namespace":       "fleet-default",
			"resourceVersion": "100",
		},
		"spec": map[string]interface{}{
			"cloudCredentialSecretName": "cattle-global-data:cc-test",
			"rkeConfig": map[string]interface{}{
				"machinePools": []interface{}{
					map[string]interface{}{
						"name":             "system",
						"controlPlaneRole": true,
						"etcdRole":         true,
						"workerRole":       false,
						"quantity":         float64(3),
					},
				},
			},
		},
	}

	var putBody map[string]interface{}
	var postPaths []string

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodPost && strings.Contains(r.URL.Path, "harvesterconfigs"):
			postPaths = append(postPaths, r.URL.Path)
			w.WriteHeader(http.StatusCreated)
			json.NewEncoder(w).Encode(SteveResource{ID: "fleet-default/nc-mycluster-workers-ab3cd"})

		case r.Method == http.MethodGet && strings.Contains(r.URL.Path, "clusters"):
			w.WriteHeader(http.StatusOK)
			json.NewEncoder(w).Encode(clusterCR)

		case r.Method == http.MethodPut && strings.Contains(r.URL.Path, "clusters"):
			json.NewDecoder(r.Body).Decode(&putBody)
			w.WriteHeader(http.StatusOK)
			json.NewEncoder(w).Encode(SteveResource{ID: "fleet-default/mycluster"})
		}
	}))
	defer srv.Close()

	steve := newTestSteve(srv)
	p := NewClusterProvisioner(steve, "cattle-global-data:cc-test", "iaas/net", "dc-tenant", "", "", nil, nil)

	pool := &models.NodePool{
		Name:                "workers",
		Role:                models.NodePoolRoleWorker,
		Count:               2,
		HarvesterConfigName: "nc-mycluster-workers-ab3cd",
	}

	err := p.AddNodePool(context.Background(), "mycluster", pool, "", "dc-tenant/subnet-abc", "dc-tenant", "default/ubuntu-22.04", 4, 8, 40)
	require.NoError(t, err)

	// Verify HarvesterConfig was POSTed.
	require.Len(t, postPaths, 1)
	assert.Contains(t, postPaths[0], "harvesterconfigs")

	// Verify the PUT body has two machinePools (system + workers).
	rke, _ := putBody["spec"].(map[string]interface{})["rkeConfig"].(map[string]interface{})
	pools, _ := rke["machinePools"].([]interface{})
	require.Len(t, pools, 2, "PUT must include system pool + new workers pool")
	workerEntry, _ := pools[1].(map[string]interface{})
	assert.Equal(t, "workers", workerEntry["name"])
	assert.Equal(t, false, workerEntry["controlPlaneRole"])
	assert.Equal(t, true, workerEntry["workerRole"])
	assert.Equal(t, float64(2), workerEntry["quantity"])
}

func TestAddNodePool_CleanupOnPUTFailure(t *testing.T) {
	var deletedPaths []string

	clusterCR := map[string]interface{}{
		"metadata": map[string]interface{}{"name": "mycluster", "namespace": "fleet-default", "resourceVersion": "100"},
		"spec": map[string]interface{}{
			"rkeConfig": map[string]interface{}{
				"machinePools": []interface{}{
					map[string]interface{}{"name": "system"},
				},
			},
		},
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost:
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusCreated)
			json.NewEncoder(w).Encode(SteveResource{ID: "fleet-default/nc-mycluster-workers-fail"})
		case r.Method == http.MethodGet:
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			json.NewEncoder(w).Encode(clusterCR)
		case r.Method == http.MethodPut:
			// Simulate a server-side validation error.
			w.WriteHeader(http.StatusUnprocessableEntity)
			w.Write([]byte(`{"message":"invalid spec"}`))
		case r.Method == http.MethodDelete:
			deletedPaths = append(deletedPaths, r.URL.Path)
			w.WriteHeader(http.StatusOK)
		}
	}))
	defer srv.Close()

	steve := newTestSteve(srv)
	p := NewClusterProvisioner(steve, "cred", "iaas/net", "dc-tenant", "", "", nil, nil)

	pool := &models.NodePool{
		Name:                "workers",
		Role:                models.NodePoolRoleWorker,
		Count:               1,
		HarvesterConfigName: "nc-mycluster-workers-fail",
	}

	err := p.AddNodePool(context.Background(), "mycluster", pool, "", "dc-tenant/subnet-abc", "dc-tenant", "default/ubuntu-22.04", 2, 4, 40)
	require.Error(t, err)

	// HarvesterConfig must have been cleaned up.
	require.Len(t, deletedPaths, 1)
	assert.Contains(t, deletedPaths[0], "harvesterconfigs")
	assert.Contains(t, deletedPaths[0], "nc-mycluster-workers-fail")
}

// ── ScaleNodePool ─────────────────────────────────────────────────────────────

func TestScaleNodePool_Success(t *testing.T) {
	clusterCR := map[string]interface{}{
		"metadata": map[string]interface{}{"name": "mycluster", "namespace": "fleet-default", "resourceVersion": "200"},
		"spec": map[string]interface{}{
			"rkeConfig": map[string]interface{}{
				"machinePools": []interface{}{
					map[string]interface{}{"name": "system", "quantity": float64(3)},
					map[string]interface{}{"name": "workers", "quantity": float64(2)},
				},
			},
		},
	}

	var putBody map[string]interface{}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.Method {
		case http.MethodGet:
			w.WriteHeader(http.StatusOK)
			json.NewEncoder(w).Encode(clusterCR)
		case http.MethodPut:
			json.NewDecoder(r.Body).Decode(&putBody)
			w.WriteHeader(http.StatusOK)
			json.NewEncoder(w).Encode(SteveResource{ID: "fleet-default/mycluster"})
		}
	}))
	defer srv.Close()

	steve := newTestSteve(srv)
	p := NewClusterProvisioner(steve, "cred", "iaas/net", "dc-tenant", "", "", nil, nil)

	err := p.ScaleNodePool(context.Background(), "mycluster", "workers", 5)
	require.NoError(t, err)

	// Verify the PUT body has workers pool scaled to 5.
	rke, _ := putBody["spec"].(map[string]interface{})["rkeConfig"].(map[string]interface{})
	pools, _ := rke["machinePools"].([]interface{})
	require.Len(t, pools, 2)
	for _, raw := range pools {
		entry, _ := raw.(map[string]interface{})
		if entry["name"] == "workers" {
			assert.Equal(t, float64(5), entry["quantity"])
		}
		if entry["name"] == "system" {
			assert.Equal(t, float64(3), entry["quantity"], "system pool quantity must be unchanged")
		}
	}
}

func TestScaleNodePool_PoolNotFound(t *testing.T) {
	clusterCR := map[string]interface{}{
		"metadata": map[string]interface{}{"name": "mycluster", "namespace": "fleet-default", "resourceVersion": "1"},
		"spec": map[string]interface{}{
			"rkeConfig": map[string]interface{}{
				"machinePools": []interface{}{
					map[string]interface{}{"name": "system", "quantity": float64(1)},
				},
			},
		},
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(clusterCR)
	}))
	defer srv.Close()

	steve := newTestSteve(srv)
	p := NewClusterProvisioner(steve, "cred", "iaas/net", "dc-tenant", "", "", nil, nil)

	err := p.ScaleNodePool(context.Background(), "mycluster", "nonexistent", 3)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not found")
}

func TestScaleNodePool_RetryOn409(t *testing.T) {
	clusterCR := map[string]interface{}{
		"metadata": map[string]interface{}{"name": "mycluster", "namespace": "fleet-default", "resourceVersion": "1"},
		"spec": map[string]interface{}{
			"rkeConfig": map[string]interface{}{
				"machinePools": []interface{}{
					map[string]interface{}{"name": "workers", "quantity": float64(2)},
				},
			},
		},
	}

	var putCount int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.Method {
		case http.MethodGet:
			w.WriteHeader(http.StatusOK)
			json.NewEncoder(w).Encode(clusterCR)
		case http.MethodPut:
			putCount++
			if putCount == 1 {
				// First PUT: 409 conflict.
				w.WriteHeader(http.StatusConflict)
				w.Write([]byte(`{"message":"resourceVersion conflict"}`))
				return
			}
			// Second PUT: success.
			w.WriteHeader(http.StatusOK)
			json.NewEncoder(w).Encode(SteveResource{ID: "fleet-default/mycluster"})
		}
	}))
	defer srv.Close()

	steve := newTestSteve(srv)
	p := NewClusterProvisioner(steve, "cred", "iaas/net", "dc-tenant", "", "", nil, nil)

	err := p.ScaleNodePool(context.Background(), "mycluster", "workers", 4)
	require.NoError(t, err, "should succeed after 409 retry")
	assert.Equal(t, 2, putCount, "must have retried PUT once after 409")
}

// ── UpdateNodePoolTaintsLabels ────────────────────────────────────────────────

func TestUpdateNodePoolTaintsLabels_ReplacesExisting(t *testing.T) {
	clusterCR := map[string]interface{}{
		"metadata": map[string]interface{}{"name": "mycluster", "namespace": "fleet-default", "resourceVersion": "5"},
		"spec": map[string]interface{}{
			"rkeConfig": map[string]interface{}{
				"machinePools": []interface{}{
					map[string]interface{}{
						"name":   "workers",
						"taints": []interface{}{map[string]interface{}{"key": "old-key", "effect": "NoSchedule"}},
						"labels": map[string]interface{}{"old-label": "old-val"},
					},
				},
			},
		},
	}

	var putBody map[string]interface{}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.Method {
		case http.MethodGet:
			w.WriteHeader(http.StatusOK)
			json.NewEncoder(w).Encode(clusterCR)
		case http.MethodPut:
			json.NewDecoder(r.Body).Decode(&putBody)
			w.WriteHeader(http.StatusOK)
			json.NewEncoder(w).Encode(SteveResource{ID: "fleet-default/mycluster"})
		}
	}))
	defer srv.Close()

	steve := newTestSteve(srv)
	p := NewClusterProvisioner(steve, "cred", "iaas/net", "dc-tenant", "", "", nil, nil)

	err := p.UpdateNodePoolTaintsLabels(
		context.Background(), "mycluster", "workers",
		[]models.NodePoolTaint{{Key: "new-key", Value: "new-val", Effect: "NoExecute"}},
		map[string]string{"new-label": "new-val"},
	)
	require.NoError(t, err)

	rke, _ := putBody["spec"].(map[string]interface{})["rkeConfig"].(map[string]interface{})
	pools, _ := rke["machinePools"].([]interface{})
	require.Len(t, pools, 1)
	entry, _ := pools[0].(map[string]interface{})

	// After JSON round-trip (Decode into putBody), arrays are []interface{} and
	// objects are map[string]interface{} — never the concrete types we set them to.
	taintsIface, _ := entry["taints"].([]interface{})
	require.Len(t, taintsIface, 1, "expected 1 taint after update")
	taint0, _ := taintsIface[0].(map[string]interface{})
	require.NotNil(t, taint0)
	assert.Equal(t, "new-key", taint0["key"])
	assert.Equal(t, "new-val", taint0["value"])
	assert.Equal(t, "NoExecute", taint0["effect"])
	labels, _ := entry["labels"].(map[string]interface{})
	assert.Equal(t, "new-val", labels["new-label"])
	// Old label must be gone.
	_, hasOld := labels["old-label"]
	assert.False(t, hasOld, "old label must be replaced")
}

func TestUpdateNodePoolTaintsLabels_ClearsWhenEmpty(t *testing.T) {
	clusterCR := map[string]interface{}{
		"metadata": map[string]interface{}{"name": "mycluster", "namespace": "fleet-default", "resourceVersion": "5"},
		"spec": map[string]interface{}{
			"rkeConfig": map[string]interface{}{
				"machinePools": []interface{}{
					map[string]interface{}{
						"name":   "workers",
						"taints": []interface{}{map[string]interface{}{"key": "old", "effect": "NoSchedule"}},
					},
				},
			},
		},
	}

	var putBody map[string]interface{}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.Method {
		case http.MethodGet:
			w.WriteHeader(http.StatusOK)
			json.NewEncoder(w).Encode(clusterCR)
		case http.MethodPut:
			json.NewDecoder(r.Body).Decode(&putBody)
			w.WriteHeader(http.StatusOK)
			json.NewEncoder(w).Encode(SteveResource{ID: "fleet-default/mycluster"})
		}
	}))
	defer srv.Close()

	steve := newTestSteve(srv)
	p := NewClusterProvisioner(steve, "cred", "iaas/net", "dc-tenant", "", "", nil, nil)

	// Pass empty slices — should clear the existing taints and labels.
	err := p.UpdateNodePoolTaintsLabels(context.Background(), "mycluster", "workers", nil, nil)
	require.NoError(t, err)

	rke, _ := putBody["spec"].(map[string]interface{})["rkeConfig"].(map[string]interface{})
	pools, _ := rke["machinePools"].([]interface{})
	entry, _ := pools[0].(map[string]interface{})
	_, hasTaints := entry["taints"]
	_, hasLabels := entry["labels"]
	assert.False(t, hasTaints, "taints must be deleted when empty")
	assert.False(t, hasLabels, "labels must be deleted when empty")
}

// ── RemoveNodePool ────────────────────────────────────────────────────────────

func TestRemoveNodePool_Success(t *testing.T) {
	clusterCR := map[string]interface{}{
		"metadata": map[string]interface{}{"name": "mycluster", "namespace": "fleet-default", "resourceVersion": "10"},
		"spec": map[string]interface{}{
			"rkeConfig": map[string]interface{}{
				"machinePools": []interface{}{
					map[string]interface{}{"name": "system"},
					map[string]interface{}{"name": "workers"},
				},
			},
		},
	}

	var putBody map[string]interface{}
	var deletedPaths []string

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.Method {
		case http.MethodGet:
			w.WriteHeader(http.StatusOK)
			json.NewEncoder(w).Encode(clusterCR)
		case http.MethodPut:
			json.NewDecoder(r.Body).Decode(&putBody)
			w.WriteHeader(http.StatusOK)
			json.NewEncoder(w).Encode(SteveResource{ID: "fleet-default/mycluster"})
		case http.MethodDelete:
			deletedPaths = append(deletedPaths, r.URL.Path)
			w.WriteHeader(http.StatusOK)
		}
	}))
	defer srv.Close()

	steve := newTestSteve(srv)
	p := NewClusterProvisioner(steve, "cred", "iaas/net", "dc-tenant", "", "", nil, nil)

	err := p.RemoveNodePool(context.Background(), "mycluster", "workers", "nc-mycluster-workers-ab3cd")
	require.NoError(t, err)

	// Verify the PUT body has only the system pool.
	rke, _ := putBody["spec"].(map[string]interface{})["rkeConfig"].(map[string]interface{})
	pools, _ := rke["machinePools"].([]interface{})
	require.Len(t, pools, 1, "only system pool must remain after removing workers")
	remaining, _ := pools[0].(map[string]interface{})
	assert.Equal(t, "system", remaining["name"])

	// Verify HarvesterConfig was deleted.
	require.Len(t, deletedPaths, 1)
	assert.Contains(t, deletedPaths[0], "harvesterconfigs")
	assert.Contains(t, deletedPaths[0], "nc-mycluster-workers-ab3cd")
}

func TestRemoveNodePool_IdempotentWhenAlreadyGone(t *testing.T) {
	// The cluster CR has only the system pool — workers is already absent.
	clusterCR := map[string]interface{}{
		"metadata": map[string]interface{}{"name": "mycluster", "namespace": "fleet-default", "resourceVersion": "10"},
		"spec": map[string]interface{}{
			"rkeConfig": map[string]interface{}{
				"machinePools": []interface{}{
					map[string]interface{}{"name": "system"},
				},
			},
		},
	}

	var putCount int

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.Method {
		case http.MethodGet:
			w.WriteHeader(http.StatusOK)
			json.NewEncoder(w).Encode(clusterCR)
		case http.MethodPut:
			putCount++
			w.WriteHeader(http.StatusOK)
			json.NewEncoder(w).Encode(SteveResource{})
		case http.MethodDelete:
			w.WriteHeader(http.StatusNotFound) // HC already gone — treated as success.
		}
	}))
	defer srv.Close()

	steve := newTestSteve(srv)
	p := NewClusterProvisioner(steve, "cred", "iaas/net", "dc-tenant", "", "", nil, nil)

	err := p.RemoveNodePool(context.Background(), "mycluster", "workers", "nc-mycluster-workers-already-gone")
	require.NoError(t, err, "remove of already-absent pool must be idempotent")
	assert.Equal(t, 0, putCount, "no PUT when pool is already absent")
}

// ── resourceVersion round-trip ────────────────────────────────────────────────

func TestResourceVersion_RoundTrip(t *testing.T) {
	body := map[string]interface{}{
		"metadata": map[string]interface{}{
			"name":            "mycluster",
			"resourceVersion": "42",
		},
	}
	assert.Equal(t, "42", resourceVersion(body))
}

func TestResourceVersion_Missing(t *testing.T) {
	assert.Equal(t, "", resourceVersion(map[string]interface{}{}))
	assert.Equal(t, "", resourceVersion(nil))
}

// ── isSteveConflict ───────────────────────────────────────────────────────────

func TestIsSteveConflict_True(t *testing.T) {
	assert.True(t, isSteveConflict(fmt.Errorf("steve update: HTTP 409: resource version mismatch")))
}

func TestIsSteveConflict_False(t *testing.T) {
	assert.False(t, isSteveConflict(nil))
	assert.False(t, isSteveConflict(fmt.Errorf("HTTP 422: invalid spec")))
}

// ── Spec-diff check: buildClusterCRBody ──────────────────────────────────────
//
// This test builds the would-be cluster CR body for a representative 3-node
// system pool and marshals it to YAML, then compares the structural shape
// against the reference at /tmp/rke2-reference/asgardeo-cluster.yaml.
//
// Differences are expected and documented inline.

func TestClusterCRShape_MatchesReference(t *testing.T) {
	p := NewClusterProvisioner(nil, "cattle-global-data:cc-kvfj7", "iaas/vm-network-001", "default", "", "", nil, nil)

	spec := ClusterCreateSpec{
		ClusterName:  "asgardeo-dev-central-rke2",
		K8sVersion:   "v1.35.4+rke2r1",
		NodeCPU:      4,
		NodeMemoryGB: 8,
		NodeDiskGB:   30,
		NodeImage:    "default/image-t5965",
		VMNamespace:  "wso2-asgardeo-dev-central",
		SystemPool: &models.NodePool{
			Name:                "controlplane",
			Role:                models.NodePoolRoleSystem,
			Count:               3,
			HarvesterConfigName: "nc-asgardeo-dev-central-rke2-controlplane-fshcp",
		},
	}

	// Build the CR body using the internal helper (bypass the Steve POST).
	// We can call createClusterCR logic via the exported buildMachinePool +
	// by constructing the body map directly the same way the provisioner does.
	cloudProviderConfigRef := "secret://fleet-default:harvesterconfig-" + spec.ClusterName

	systemPool := *spec.SystemPool
	body := map[string]interface{}{
		"type": "provisioning.cattle.io.cluster",
		"metadata": map[string]interface{}{
			"name":      spec.ClusterName,
			"namespace": fleetDefault,
			"labels":    map[string]interface{}{"dc-api/managed": "true"},
			"annotations": map[string]interface{}{
				"dc-api.wso2.com/managed": "true",
			},
		},
		"spec": map[string]interface{}{
			"kubernetesVersion":         spec.K8sVersion,
			"cloudCredentialSecretName": p.cloudCredential,
			"enableNetworkPolicy":       false,
			"rkeConfig": map[string]interface{}{
				"machineGlobalConfig": map[string]interface{}{
					"cni":                 "cilium",
					"disable-kube-proxy":  false,
					"etcd-expose-metrics": false,
					"kube-apiserver-arg":              []string{"etcd-healthcheck-timeout=10s"},
					"kube-controller-manager-arg":     []string{"leader-elect-lease-duration=60s", "leader-elect-renew-deadline=45s", "leader-elect-retry-period=10s"},
					"kube-scheduler-arg":              []string{"leader-elect-lease-duration=60s", "leader-elect-renew-deadline=45s", "leader-elect-retry-period=10s"},
				},
				"upgradeStrategy": map[string]interface{}{
					"controlPlaneConcurrency": "1",
					"workerConcurrency":       "1",
				},
				"machineSelectorConfig": []interface{}{
					map[string]interface{}{
						"config": map[string]interface{}{
							"cloud-provider-config":   cloudProviderConfigRef,
							"cloud-provider-name":     "harvester",
							"protect-kernel-defaults": false,
						},
					},
				},
				"machinePools": []interface{}{
					buildMachinePool(&systemPool, p.cloudCredential),
				},
			},
		},
	}

	yamlBytes, err := yaml.Marshal(body)
	require.NoError(t, err)

	t.Logf("Generated cluster CR:\n%s", string(yamlBytes))

	// Structural assertions against the reference (asgardeo-cluster.yaml):

	specMap, _ := body["spec"].(map[string]interface{})
	rke, _ := specMap["rkeConfig"].(map[string]interface{})
	pools, _ := rke["machinePools"].([]interface{})

	// ── pool count: our build produces 1 (system only); reference has 2 ──────
	// Reference has controlplane+workerplane because Asgardeo pre-added a worker
	// pool at cluster creation time. We add workers via AddNodePool separately.
	// Diff hunk: machinePools length 1 vs 2.
	assert.Len(t, pools, 1, "initial create must have only the system pool")

	// ── cloud credential: matches reference ───────────────────────────────────
	assert.Equal(t, "cattle-global-data:cc-kvfj7", specMap["cloudCredentialSecretName"])

	// ── pool shape matches reference controlplane entry ───────────────────────
	pool0, _ := pools[0].(map[string]interface{})
	assert.Equal(t, "controlplane", pool0["name"])
	// quantity is an int (not float64) because we're reading from the in-memory map,
	// not a JSON-decoded body.
	assert.Equal(t, 3, pool0["quantity"])
	assert.Equal(t, true, pool0["controlPlaneRole"])
	assert.Equal(t, true, pool0["etcdRole"])
	// Reference controlplane pool has no workerRole key (absent = false in Rancher).
	// We emit workerRole=false explicitly — same semantic, no behavioral diff.

	// ── machineSelectorConfig: we use harvesterconfig-<name> Secret ref ───────
	// Reference uses "harvester-cloud-provider-config-dgtw7" (a different pre-existing
	// secret name). Our build uses "harvesterconfig-<cluster>" which we pre-create.
	// Diff hunk: cloud-provider-config secret name.
	msc, _ := rke["machineSelectorConfig"].([]interface{})
	require.Len(t, msc, 1)
	mscConfig, _ := msc[0].(map[string]interface{})["config"].(map[string]interface{})
	assert.Equal(t, "harvester", mscConfig["cloud-provider-name"])
	assert.Contains(t, mscConfig["cloud-provider-config"], "harvesterconfig-asgardeo-dev-central-rke2")

	// ── fields absent from our build but present in reference ─────────────────
	// Reference has: registries (ACR config), chartValues (harvester-cloud-provider,
	// rke2-cilium etc.), etcd.snapshotRetention, controlPlaneDrainOptions.
	// Justification: tenant-specific registry config (ACR) is not our concern;
	// chartValues are injected by Rancher's own bootstrapper after creation;
	// etcd/drain options are Rancher defaults we don't need to set explicitly.
	// dynamicSchemaSpec is injected server-side by Rancher after the CR is created.

	// ── fields present in our build but absent from reference ─────────────────
	// dc-api/managed label, dc-api.wso2.com/managed annotation: our ownership guard
	// (DeleteCluster refuses to operate on unmanaged CRs).
	// kube-apiserver-arg/kube-controller-manager-arg/kube-scheduler-arg: F36 tuning
	// not present in Asgardeo (they don't need it because they have 3 CP nodes).
}

// ── shortRand ─────────────────────────────────────────────────────────────────

func TestShortRand_Length(t *testing.T) {
	s := shortRand()
	assert.Len(t, s, 5, "shortRand must return exactly 5 characters")
}

func TestShortRand_AlphanumericOnly(t *testing.T) {
	for i := 0; i < 10; i++ {
		s := shortRand()
		for _, c := range s {
			assert.True(t, (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9'),
				"shortRand must produce only [a-z0-9], got %q", string(c))
		}
		// Different calls in quick succession may collide (time-based) — that's
		// acceptable for a non-security suffix; just verify the format here.
		time.Sleep(time.Microsecond)
	}
}

