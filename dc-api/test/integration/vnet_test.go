//go:build integration

package integration

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestVNet_FullLifecycle(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	client := clientForTenant(t, "tenant-vnet-lc")
	// DELETE requires owner role (M1.5 Chunk 4). Grant it before the test runs
	// so that both the inline delete assertion and the cleanup func succeed.
	mustGrantOwnerForClient(t, "tenant-vnet-lc")
	name := randomName("vnet")

	resp, body, status, err := client.CreateVNet(ctx, CreateVNetRequest{
		Name: name, AddressSpace: []string{"10.200.0.0/16"}, Region: "lk",
		Description: "lifecycle test",
	})
	require.NoError(t, err)
	require.Equal(t, http.StatusAccepted, status, "create: %s", ErrorBody(body))
	require.Equal(t, "PENDING", resp.Resource.Status)
	require.NotEmpty(t, resp.Resource.ID)
	vnetID := resp.Resource.ID

	t.Cleanup(func() {
		ctx := context.Background()
		_, _, _ = client.DeleteVNet(ctx, vnetID)
		WaitVNetGone(t, client, vnetID)
	})

	WaitVNetActive(t, client, vnetID)

	backendUID := dbGetVNetBackendUID(t, vnetID)
	require.NotEmpty(t, backendUID)
	exists, err := VpcExists(ctx, env.KubeClient, backendUID)
	require.NoError(t, err)
	require.True(t, exists, "KubeOVN Vpc %s must exist", backendUID)

	getResp, status, err := client.GetVNet(ctx, vnetID)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, status)
	require.Equal(t, "ACTIVE", getResp.Status)
	require.Equal(t, name, getResp.Name)
	require.Equal(t, []string{"10.200.0.0/16"}, getResp.AddressSpace)

	list, status, err := client.ListVNets(ctx)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, status)
	var found bool
	for _, v := range list {
		if v.ID == vnetID {
			found = true
		}
	}
	require.True(t, found, "vnet must appear in List")

	_, status, err = client.DeleteVNet(ctx, vnetID)
	require.NoError(t, err)
	require.Equal(t, http.StatusAccepted, status)
	WaitVNetGone(t, client, vnetID)

	require.Eventually(t, func() bool {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		exists, _ := VpcExists(ctx, env.KubeClient, backendUID)
		return !exists
	}, 60*time.Second, 2*time.Second, "KubeOVN Vpc %s must be removed after delete", backendUID)
}

func TestVNet_RejectNonRFC1918CIDR(t *testing.T) {
	t.Parallel()
	_, body, status, err := clientForTenant(t, "tenant-vnet-v1").CreateVNet(
		context.Background(),
		CreateVNetRequest{Name: randomName("vnet"), AddressSpace: []string{"8.8.8.0/24"}, Region: "lk"},
	)
	require.NoError(t, err)
	require.Equal(t, http.StatusBadRequest, status, "body: %s", ErrorBody(body))
}

func TestVNet_RejectReservedCIDROverlap(t *testing.T) {
	t.Parallel()
	_, body, status, err := clientForTenant(t, "tenant-vnet-v2").CreateVNet(
		context.Background(),
		CreateVNetRequest{Name: randomName("vnet"), AddressSpace: []string{"192.168.10.0/24"}, Region: "lk"},
	)
	require.NoError(t, err)
	require.Equal(t, http.StatusBadRequest, status, "body: %s", ErrorBody(body))
}

func TestVNet_RejectInvalidRegion(t *testing.T) {
	t.Parallel()
	_, body, status, err := clientForTenant(t, "tenant-vnet-v3").CreateVNet(
		context.Background(),
		CreateVNetRequest{Name: randomName("vnet"), AddressSpace: []string{"10.201.0.0/16"}, Region: "mars"},
	)
	require.NoError(t, err)
	require.Equal(t, http.StatusBadRequest, status, "body: %s", ErrorBody(body))
}
