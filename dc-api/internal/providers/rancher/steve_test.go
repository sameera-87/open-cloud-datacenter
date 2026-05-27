// Package rancher — steve_test.go
//
// Unit tests for the Steve API client and ClusterProvisioner helpers.
// All tests use httptest mock servers — no live Rancher required.
//
// Run:
//
//	cd dc-api
//	go test ./internal/providers/rancher/... -v -count=1 -run TestSteve
package rancher

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/wso2/dc-api/internal/models"
)

// ── Fakes ─────────────────────────────────────────────────────────────────────

// fakeSAEnsurer satisfies providers.CloudProviderSAEnsurer for unit tests.
type fakeSAEnsurer struct {
	token []byte
	err   error
	calls int
}

func (f *fakeSAEnsurer) EnsureCloudProviderSA(_ context.Context, _ string) ([]byte, error) {
	f.calls++
	return f.token, f.err
}

// fakeAPIInfo satisfies providers.HarvesterAPIInfoProvider for unit tests.
type fakeAPIInfo struct {
	serverURL string
	caCert    []byte
}

func (f *fakeAPIInfo) HarvesterServerURL() string { return f.serverURL }
func (f *fakeAPIInfo) HarvesterCACert() []byte    { return f.caCert }

// newTestSteve returns a SteveClient pointed at the given test server.
// The test server must be closed by the caller.
func newTestSteve(srv *httptest.Server) *SteveClient {
	return NewSteveClient(srv.URL, "test-token", &http.Client{Timeout: 5 * time.Second})
}

// ── Create ────────────────────────────────────────────────────────────────────

func TestSteveCreate_Success(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodPost, r.Method)
		assert.Equal(t, "/v1/provisioning.cattle.io.clusters/fleet-default", r.URL.Path)
		assert.Equal(t, "Bearer test-token", r.Header.Get("Authorization"))
		assert.Equal(t, "application/json", r.Header.Get("Content-Type"))

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		json.NewEncoder(w).Encode(SteveResource{
			ID:   "fleet-default/my-cluster",
			Type: "provisioning.cattle.io.cluster",
		})
	}))
	defer srv.Close()

	client := newTestSteve(srv)
	body := map[string]interface{}{"type": "provisioning.cattle.io.cluster", "metadata": map[string]interface{}{"name": "my-cluster"}}
	res, err := client.Create(context.Background(), "provisioning.cattle.io.clusters", "fleet-default", body)
	require.NoError(t, err)
	assert.Equal(t, "fleet-default/my-cluster", res.ID)
}

func TestSteveCreate_HTTPError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnprocessableEntity)
		w.Write([]byte(`{"message":"validation error"}`))
	}))
	defer srv.Close()

	client := newTestSteve(srv)
	_, err := client.Create(context.Background(), "provisioning.cattle.io.clusters", "fleet-default", map[string]interface{}{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "HTTP 422")
}

// ── Get ───────────────────────────────────────────────────────────────────────

func TestSteveGet_Success(t *testing.T) {
	statusPayload := json.RawMessage(`{"ready":true,"clusterName":"c-m-abc123"}`)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodGet, r.Method)
		assert.Equal(t, "/v1/provisioning.cattle.io.clusters/fleet-default/my-cluster", r.URL.Path)

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(SteveResource{
			ID:     "fleet-default/my-cluster",
			Type:   "provisioning.cattle.io.cluster",
			Status: statusPayload,
		})
	}))
	defer srv.Close()

	client := newTestSteve(srv)
	res, err := client.Get(context.Background(), "provisioning.cattle.io.clusters", "fleet-default", "my-cluster")
	require.NoError(t, err)
	assert.Equal(t, "fleet-default/my-cluster", res.ID)
	// Verify Status JSON is preserved as raw message.
	assert.Contains(t, string(res.Status), "c-m-abc123")
}

func TestSteveGet_NotFound(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		w.Write([]byte(`{"type":"error","status":"404"}`))
	}))
	defer srv.Close()

	client := newTestSteve(srv)
	_, err := client.Get(context.Background(), "provisioning.cattle.io.clusters", "fleet-default", "nonexistent")
	require.Error(t, err)
	assert.True(t, IsSteveNotFound(err), "expected SteveNotFoundError, got: %v", err)
}

// ── Delete ────────────────────────────────────────────────────────────────────

func TestSteveDelete_Success(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodDelete, r.Method)
		assert.Equal(t, "/v1/provisioning.cattle.io.clusters/fleet-default/my-cluster", r.URL.Path)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	client := newTestSteve(srv)
	err := client.Delete(context.Background(), "provisioning.cattle.io.clusters", "fleet-default", "my-cluster")
	require.NoError(t, err)
}

func TestSteveDelete_NotFoundIsSuccess(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	// 404 on delete must be treated as idempotent success.
	client := newTestSteve(srv)
	err := client.Delete(context.Background(), "provisioning.cattle.io.clusters", "fleet-default", "gone")
	require.NoError(t, err)
}

// ── List ──────────────────────────────────────────────────────────────────────

func TestSteveList_Success(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodGet, r.Method)
		assert.Equal(t, "/v1/provisioning.cattle.io.clusters/fleet-default", r.URL.Path)

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(SteveCollection{
			Type:  "collection",
			Count: 2,
			Data: []SteveResource{
				{ID: "fleet-default/cluster-a", Type: "provisioning.cattle.io.cluster"},
				{ID: "fleet-default/cluster-b", Type: "provisioning.cattle.io.cluster"},
			},
		})
	}))
	defer srv.Close()

	client := newTestSteve(srv)
	items, err := client.List(context.Background(), "provisioning.cattle.io.clusters", "fleet-default")
	require.NoError(t, err)
	assert.Len(t, items, 2)
	assert.Equal(t, "fleet-default/cluster-a", items[0].ID)
	assert.Equal(t, "fleet-default/cluster-b", items[1].ID)
}

func TestSteveList_Empty(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(SteveCollection{Type: "collection", Count: 0, Data: []SteveResource{}})
	}))
	defer srv.Close()

	client := newTestSteve(srv)
	items, err := client.List(context.Background(), "provisioning.cattle.io.clusters", "fleet-default")
	require.NoError(t, err)
	assert.Empty(t, items)
}

// ── Auth header ───────────────────────────────────────────────────────────────

func TestSteveClient_BearerTokenHeader(t *testing.T) {
	var capturedAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedAuth = r.Header.Get("Authorization")
		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(SteveCollection{Type: "collection", Count: 0, Data: nil})
	}))
	defer srv.Close()

	client := NewSteveClient(srv.URL, "my-secret-token", &http.Client{Timeout: 5 * time.Second})
	_, _ = client.List(context.Background(), "anything", "anywhere")

	assert.Equal(t, "Bearer my-secret-token", capturedAuth)
}

// ── Update ────────────────────────────────────────────────────────────────────

func TestSteveUpdate_Success(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodPut, r.Method)
		assert.Equal(t, "/v1/provisioning.cattle.io.clusters/fleet-default/my-cluster", r.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(SteveResource{ID: "fleet-default/my-cluster"})
	}))
	defer srv.Close()

	client := newTestSteve(srv)
	res, err := client.Update(context.Background(), "provisioning.cattle.io.clusters", "fleet-default", "my-cluster",
		map[string]interface{}{"metadata": map[string]interface{}{"resourceVersion": "12345"}})
	require.NoError(t, err)
	assert.Equal(t, "fleet-default/my-cluster", res.ID)
}

// ── SteveNotFoundError ────────────────────────────────────────────────────────

func TestSteveNotFoundError_Message(t *testing.T) {
	err := &SteveNotFoundError{Kind: "provisioning.cattle.io.clusters", NS: "fleet-default", Name: "gone"}
	assert.Equal(t, "steve: provisioning.cattle.io.clusters fleet-default/gone not found", err.Error())
}

func TestIsSteveNotFound_True(t *testing.T) {
	err := &SteveNotFoundError{Kind: "k", NS: "ns", Name: "n"}
	assert.True(t, IsSteveNotFound(err))
}

func TestIsSteveNotFound_False(t *testing.T) {
	assert.False(t, IsSteveNotFound(nil))
	assert.False(t, IsSteveNotFound(assert.AnError))
}

// ── truncate helper ───────────────────────────────────────────────────────────

func TestTruncate_ShortString(t *testing.T) {
	assert.Equal(t, "hello", truncate([]byte("hello"), 10))
}

func TestTruncate_LongString(t *testing.T) {
	result := truncate([]byte(strings.Repeat("x", 600)), 512)
	assert.Equal(t, 512+len([]rune("…")), len([]rune(result)))
	assert.True(t, strings.HasSuffix(result, "…"))
}

// ── ClusterProvisioner helpers ────────────────────────────────────────────────

func TestBuildNetworkInfo_DualNIC(t *testing.T) {
	ni, err := buildNetworkInfo("iaas/vm-network-001", "dc-hiran/subnet-hiran-vm-sn")
	require.NoError(t, err)

	var wrapper struct {
		Interfaces []map[string]interface{} `json:"interfaces"`
	}
	require.NoError(t, json.Unmarshal([]byte(ni), &wrapper))
	require.Len(t, wrapper.Interfaces, 2)

	assert.Equal(t, "iaas/vm-network-001", wrapper.Interfaces[0]["networkName"])
	assert.Equal(t, "L2VlanNetwork", wrapper.Interfaces[0]["networkType"])
	assert.Equal(t, "dc-hiran/subnet-hiran-vm-sn", wrapper.Interfaces[1]["networkName"])
	assert.Equal(t, "L2VlanNetwork", wrapper.Interfaces[1]["networkType"])
}

func TestBuildNetworkInfo_SingleNIC_NoTenantSubnet(t *testing.T) {
	ni, err := buildNetworkInfo("iaas/vm-network-001", "")
	require.NoError(t, err)

	var wrapper struct {
		Interfaces []map[string]interface{} `json:"interfaces"`
	}
	require.NoError(t, json.Unmarshal([]byte(ni), &wrapper))
	// When no tenant subnet NAD, only the management NIC is added.
	assert.Len(t, wrapper.Interfaces, 1)
	assert.Equal(t, "iaas/vm-network-001", wrapper.Interfaces[0]["networkName"])
}

func TestParseClusterStatus_Ready(t *testing.T) {
	statusJSON := json.RawMessage(`{
		"ready": true,
		"clusterName": "c-m-abc123",
		"conditions": [
			{"type": "Provisioned", "status": "True", "message": ""},
			{"type": "Ready",       "status": "True", "message": ""}
		]
	}`)
	res := &SteveResource{Status: statusJSON}
	cs, err := parseClusterStatus(res)
	require.NoError(t, err)
	assert.True(t, cs.Ready)
	assert.True(t, cs.Provisioned)
	assert.Equal(t, "c-m-abc123", cs.RancherUID)
	assert.Empty(t, cs.Message)
}

func TestParseClusterStatus_Stalled(t *testing.T) {
	statusJSON := json.RawMessage(`{
		"ready": false,
		"clusterName": "",
		"conditions": [
			{"type": "Stalled", "status": "True", "message": "machine provisioner error: disk full"}
		]
	}`)
	res := &SteveResource{Status: statusJSON}
	cs, err := parseClusterStatus(res)
	require.NoError(t, err)
	assert.False(t, cs.Ready)
	assert.Equal(t, "machine provisioner error: disk full", cs.Message)
}

func TestParseClusterStatus_Pending_NoStatus(t *testing.T) {
	res := &SteveResource{Status: nil}
	cs, err := parseClusterStatus(res)
	require.NoError(t, err)
	assert.False(t, cs.Ready)
	assert.False(t, cs.Provisioned)
	assert.Empty(t, cs.RancherUID)
}

func TestClusterProvisioner_BuildNodeUserData_IncludesQemuGuestAgent(t *testing.T) {
	p := NewClusterProvisioner(nil, "cred", "iaas/net", "default", "", "", nil, nil)
	ud := p.buildNodeUserData("dc-tenant/subnet-x")
	assert.Contains(t, ud, "#cloud-config")
	assert.Contains(t, ud, "qemu-guest-agent")
	assert.Contains(t, ud, "ip_vs")
	// No SSH key or password by default.
	assert.NotContains(t, ud, "ssh_authorized_keys")
	assert.NotContains(t, ud, "ssh_pwauth")
}

func TestClusterProvisioner_BuildNodeUserData_InjectsSSHKey(t *testing.T) {
	p := NewClusterProvisioner(nil, "cred", "iaas/net", "default", "ssh-ed25519 AAAA test@key", "secret", nil, nil)
	ud := p.buildNodeUserData("")
	assert.Contains(t, ud, "ssh-ed25519 AAAA test@key")
	assert.Contains(t, ud, "ssh_pwauth: true")
	assert.Contains(t, ud, "ubuntu:secret")
}

// TestClusterProvisioner_CreateCluster_CallsSteveTwice verifies that CreateCluster
// makes exactly two Steve POSTs: one for HarvesterConfig, one for the Cluster CR.
// It also verifies the HarvesterConfig cleanup fires on Cluster CR failure.
func TestClusterProvisioner_CreateCluster_CleanupOnClusterCRFailure(t *testing.T) {
	var callCount int
	var deletedPath string

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount++
		switch {
		case r.Method == http.MethodPost && strings.Contains(r.URL.Path, "harvesterconfigs"):
			// HarvesterConfig creation succeeds.
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusCreated)
			json.NewEncoder(w).Encode(SteveResource{ID: "fleet-default/my-cluster-pool"})

		case r.Method == http.MethodPost && strings.Contains(r.URL.Path, "clusters"):
			// Cluster CR creation fails — simulates auth or validation error.
			w.WriteHeader(http.StatusUnprocessableEntity)
			w.Write([]byte(`{"message":"invalid cluster spec"}`))

		case r.Method == http.MethodDelete:
			// Cleanup DELETE for the orphaned HarvesterConfig.
			deletedPath = r.URL.Path
			w.WriteHeader(http.StatusOK)
		}
	}))
	defer srv.Close()

	steve := newTestSteve(srv)
	// saEnsurer=nil, apiInfoProvider=nil → skips harvesterconfig Secret pre-creation.
	// This tests the legacy path's cleanup: POST harvesterconfig, POST cluster (fail), DELETE harvesterconfig.
	p := NewClusterProvisioner(steve, "my-cred", "iaas/net", "dc-tenant", "", "", nil, nil)

	spec := ClusterCreateSpec{
		ClusterName:     "my-cluster",
		K8sVersion:      "v1.29.4+rke2r1",
		MgmtNAD:         "iaas/vm-network-001",
		TenantSubnetNAD: "dc-tenant/subnet-x",
		NodeCPU:         4,
		NodeMemoryGB:    8,
		NodeDiskGB:      40,
		NodeImage:       "default/ubuntu-22.04",
		VMNamespace:     "dc-tenant",
		SystemPool: &models.NodePool{
			Name:                "system",
			Role:                models.NodePoolRoleSystem,
			Count:               1,
			HarvesterConfigName: "my-cluster-pool",
		},
	}

	_, err := p.CreateCluster(context.Background(), spec)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "create cluster CR")

	// Three calls: POST harvesterconfig, POST cluster (fail), DELETE harvesterconfig (cleanup).
	// harvesterconfig Secret pre-creation is skipped because saEnsurer is nil.
	assert.Equal(t, 3, callCount, "expected 3 HTTP calls: create harvesterconfig, create cluster (failed), delete harvesterconfig (cleanup)")
	assert.Contains(t, deletedPath, "harvesterconfigs")
	assert.Contains(t, deletedPath, "my-cluster-pool")
}

func TestClusterProvisioner_CreateCluster_Success(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		if strings.Contains(r.URL.Path, "harvesterconfigs") {
			json.NewEncoder(w).Encode(SteveResource{ID: "fleet-default/my-cluster-pool"})
		} else {
			json.NewEncoder(w).Encode(SteveResource{ID: "fleet-default/my-cluster"})
		}
	}))
	defer srv.Close()

	steve := newTestSteve(srv)
	p := NewClusterProvisioner(steve, "my-cred", "iaas/net", "dc-tenant", "", "", nil, nil)

	spec := ClusterCreateSpec{
		ClusterName:  "my-cluster",
		K8sVersion:   "v1.29.4+rke2r1",
		NodeCPU:      4,
		NodeMemoryGB: 8,
		NodeDiskGB:   40,
		NodeImage:    "default/ubuntu-22.04",
		VMNamespace:  "dc-tenant",
		SystemPool: &models.NodePool{
			Name:                "system",
			Role:                models.NodePoolRoleSystem,
			Count:               2,
			HarvesterConfigName: "my-cluster-pool",
		},
	}

	uid, err := p.CreateCluster(context.Background(), spec)
	require.NoError(t, err)
	assert.Equal(t, "fleet-default/my-cluster", uid)
}

func TestClusterProvisioner_DeleteCluster_Success(t *testing.T) {
	var deletedPaths []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodDelete:
			deletedPaths = append(deletedPaths, r.URL.Path)
			w.WriteHeader(http.StatusOK)
			return
		case http.MethodGet:
			w.Header().Set("Content-Type", "application/json")
			// F38 ownership-check Get on the Cluster CR. Return a CR labelled
			// dc-api/managed=true so DeleteCluster proceeds past the guard.
			if strings.Contains(r.URL.Path, "/provisioning.cattle.io.clusters/fleet-default/my-cluster") {
				_ = json.NewEncoder(w).Encode(SteveResource{
					ID:   "fleet-default/my-cluster",
					Type: "provisioning.cattle.io.cluster",
					Metadata: map[string]interface{}{
						"name":      "my-cluster",
						"namespace": "fleet-default",
						"labels":    map[string]interface{}{"dc-api/managed": "true"},
					},
				})
				return
			}
			// Anything else GET in this test = the cascade-clean ListByLabel /
			// List calls. Return empty collection so the wait loop exits on
			// the first iteration and no extra deletes get queued.
			_ = json.NewEncoder(w).Encode(SteveCollection{Type: "collection"})
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	steve := newTestSteve(srv)
	p := NewClusterProvisioner(steve, "cred", "iaas/net", "default", "", "", nil, nil)

	err := p.DeleteCluster(context.Background(), "fleet-default/my-cluster")
	require.NoError(t, err)

	// Expected deletes:
	//   1. Cluster CR
	//   2. HarvesterConfig CR
	//   3. harvesterconfig Secret
	//   4. F38 cascade-clean: RKEControlPlane (unconditional delete by name)
	// MachineSet / MachineDeployment / RKEBootstrap / per-machine secrets are
	// list-then-delete and the mock returns empty collections, so no extra
	// deletes for those.
	require.Len(t, deletedPaths, 4)
	assert.Contains(t, deletedPaths[0], "clusters")
	assert.Contains(t, deletedPaths[0], "my-cluster")
	assert.Contains(t, deletedPaths[1], "harvesterconfigs")
	assert.Contains(t, deletedPaths[1], "my-cluster-pool")
	assert.Contains(t, deletedPaths[2], "secrets")
	assert.Contains(t, deletedPaths[3], "rkecontrolplanes")
	assert.Contains(t, deletedPaths[3], "my-cluster")
}

// ── buildSAKubeconfig ────────────────────────────────────────────────────────

func TestBuildSAKubeconfig_ContainsExpectedFields(t *testing.T) {
	serverURL := "https://192.168.10.6:6443"
	caData := []byte("fake-ca-cert")
	token := []byte("sa-token-value")
	tenantNS := "dc-hiran"

	kc := buildSAKubeconfig(serverURL, caData, tenantNS, token)

	assert.Contains(t, kc, "apiVersion: v1")
	assert.Contains(t, kc, "kind: Config")
	assert.Contains(t, kc, "server: "+serverURL)
	assert.Contains(t, kc, "token: sa-token-value")
	assert.Contains(t, kc, "namespace: dc-hiran")
	assert.Contains(t, kc, "current-context: local")
	assert.Contains(t, kc, "name: harvester-cloud-provider")
}

// ── VPC path: SA bootstrap wired into CreateCluster ─────────────────────────

// TestClusterProvisioner_CreateCluster_VPCPath_CallsSAEnsurer verifies that
// when saEnsurer and apiInfoProvider are set and TenantSubnetNAD is non-empty,
// CreateCluster:
//  1. Calls EnsureCloudProviderSA.
//  2. POSTs the harvesterconfig Secret (secrets path).
//  3. POSTs the HarvesterConfig.
//  4. POSTs the Cluster CR.
//
// Total: 4 POST calls (no DELETE because all succeed).
func TestClusterProvisioner_CreateCluster_VPCPath_CallsSAEnsurer(t *testing.T) {
	var postPaths []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			postPaths = append(postPaths, r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		json.NewEncoder(w).Encode(SteveResource{ID: "fleet-default/test-id"})
	}))
	defer srv.Close()

	ensurer := &fakeSAEnsurer{token: []byte("test-sa-token")}
	apiInfo := &fakeAPIInfo{serverURL: "https://192.168.10.6:6443", caCert: []byte("ca")}

	steve := newTestSteve(srv)
	p := NewClusterProvisioner(steve, "cred", "iaas/net", "dc-tenant", "", "", ensurer, apiInfo)

	spec := ClusterCreateSpec{
		ClusterName:     "vpc-cluster",
		K8sVersion:      "v1.33.10+rke2r3",
		NodeCPU:         4,
		NodeMemoryGB:    8,
		NodeDiskGB:      40,
		NodeImage:       "default/ubuntu-22.04",
		TenantSubnetNAD: "dc-tenant/subnet-abc",
		VMNamespace:     "dc-tenant",
		SystemPool: &models.NodePool{
			Name:                "system",
			Role:                models.NodePoolRoleSystem,
			Count:               1,
			HarvesterConfigName: "nc-vpc-cluster-system-ab3cd",
		},
	}

	uid, err := p.CreateCluster(context.Background(), spec)
	require.NoError(t, err)
	assert.Equal(t, "fleet-default/vpc-cluster", uid)

	// SA ensurer must have been called exactly once.
	assert.Equal(t, 1, ensurer.calls)

	// Must have 3 POSTs: secrets (harvesterconfig), harvesterconfigs, clusters.
	require.Len(t, postPaths, 3)
	assert.Contains(t, postPaths[0], "secrets")
	assert.Contains(t, postPaths[1], "harvesterconfigs")
	assert.Contains(t, postPaths[2], "clusters")
}

// TestClusterProvisioner_CreateCluster_VPCPath_SAError verifies that a failure
// in EnsureCloudProviderSA aborts CreateCluster before any Steve POSTs.
func TestClusterProvisioner_CreateCluster_VPCPath_SAError(t *testing.T) {
	var postCount int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			postCount++
		}
		w.WriteHeader(http.StatusCreated)
		json.NewEncoder(w).Encode(SteveResource{})
	}))
	defer srv.Close()

	ensurer := &fakeSAEnsurer{err: assert.AnError}
	apiInfo := &fakeAPIInfo{serverURL: "https://192.168.10.6:6443"}

	steve := newTestSteve(srv)
	p := NewClusterProvisioner(steve, "cred", "iaas/net", "dc-tenant", "", "", ensurer, apiInfo)

	spec := ClusterCreateSpec{
		ClusterName:     "fail-cluster",
		K8sVersion:      "v1.33.10+rke2r3",
		TenantSubnetNAD: "dc-tenant/subnet-abc",
		VMNamespace:     "dc-tenant",
		SystemPool: &models.NodePool{
			Name:                "system",
			Role:                models.NodePoolRoleSystem,
			Count:               1,
			HarvesterConfigName: "nc-fail-cluster-system-a1b2c",
		},
	}

	_, err := p.CreateCluster(context.Background(), spec)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "ensure cloud-provider SA")
	// No Steve POSTs should have been made.
	assert.Equal(t, 0, postCount)
}

// TestClusterProvisioner_CreateCluster_LegacyPath_NoSAEnsurer verifies that
// when saEnsurer is nil, CreateCluster skips SA bootstrap regardless of
// TenantSubnetNAD being set — falls back to 2 POSTs (harvesterconfig + cluster).
func TestClusterProvisioner_CreateCluster_LegacyPath_NoSAEnsurer(t *testing.T) {
	var postPaths []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			postPaths = append(postPaths, r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		json.NewEncoder(w).Encode(SteveResource{ID: "fleet-default/legacy-cluster"})
	}))
	defer srv.Close()

	// nil saEnsurer AND nil apiInfoProvider → legacy path.
	steve := newTestSteve(srv)
	p := NewClusterProvisioner(steve, "cred", "iaas/net", "dc-tenant", "", "", nil, nil)

	spec := ClusterCreateSpec{
		ClusterName:     "legacy-cluster",
		K8sVersion:      "v1.33.10+rke2r3",
		NodeCPU:         2,
		NodeMemoryGB:    4,
		NodeDiskGB:      40,
		NodeImage:       "default/ubuntu-22.04",
		TenantSubnetNAD: "dc-tenant/subnet-abc", // set but saEnsurer nil → skipped
		VMNamespace:     "dc-tenant",
		SystemPool: &models.NodePool{
			Name:                "system",
			Role:                models.NodePoolRoleSystem,
			Count:               1,
			HarvesterConfigName: "nc-legacy-cluster-system-a1b2c",
		},
	}

	uid, err := p.CreateCluster(context.Background(), spec)
	require.NoError(t, err)
	assert.Equal(t, "fleet-default/legacy-cluster", uid)

	// Only 2 POSTs: harvesterconfigs + clusters (no secrets POST).
	require.Len(t, postPaths, 2)
	assert.Contains(t, postPaths[0], "harvesterconfigs")
	assert.Contains(t, postPaths[1], "clusters")
}
