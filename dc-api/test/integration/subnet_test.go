//go:build integration

package integration

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/google/uuid"
	"github.com/wso2/dc-api/internal/models"
)

func TestSubnet_FullLifecycle(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	client := clientForTenant(t, "tenant-subnet-lc")
	// DELETE requires owner role (M1.5 Chunk 4). Grant before any delete call.
	mustGrantOwnerForClient(t, "tenant-subnet-lc")
	vnetID := mustCreateActiveVNet(t, client, randomName("vnet"), "10.210.0.0/16", "lk")

	resp, body, status, err := client.CreateSubnet(ctx, vnetID, CreateSubnetRequest{
		Name: randomName("subnet"), CIDR: "10.210.1.0/24",
	})
	require.NoError(t, err)
	require.Equal(t, http.StatusAccepted, status, "body: %s", ErrorBody(body))
	subnetID := resp.Resource.ID

	t.Cleanup(func() {
		ctx := context.Background()
		_, _, _ = client.DeleteSubnet(ctx, vnetID, subnetID)
		WaitSubnetGone(t, client, vnetID, subnetID)
	})

	WaitSubnetActive(t, client, vnetID, subnetID)

	backendUID := dbGetSubnetBackendUID(t, subnetID)
	require.NotEmpty(t, backendUID)
	exists, err := SubnetExists(ctx, env.KubeClient, backendUID)
	require.NoError(t, err)
	require.True(t, exists, "KubeOVN Subnet %s must exist", backendUID)

	getResp, status, err := client.GetSubnet(ctx, vnetID, subnetID)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, status)
	require.Equal(t, "ACTIVE", getResp.Status)
	require.Equal(t, "10.210.1.0/24", getResp.CIDR)
	require.NotEmpty(t, getResp.Gateway)

	_, status, err = client.DeleteSubnet(ctx, vnetID, subnetID)
	require.NoError(t, err)
	require.Equal(t, http.StatusAccepted, status)
	WaitSubnetGone(t, client, vnetID, subnetID)

	require.Eventually(t, func() bool {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		exists, _ := SubnetExists(ctx, env.KubeClient, backendUID)
		return !exists
	}, 60*time.Second, 2*time.Second, "KubeOVN Subnet %s must be removed", backendUID)
}

func TestSubnet_RejectOutsideParentVNet(t *testing.T) {
	t.Parallel()
	client := clientForTenant(t, "tenant-subnet-v1")
	mustGrantOwnerForClient(t, "tenant-subnet-v1")
	vnetID := mustCreateActiveVNet(t, client, randomName("vnet"), "10.211.0.0/16", "lk")
	_, body, status, err := client.CreateSubnet(context.Background(), vnetID, CreateSubnetRequest{
		Name: randomName("subnet"), CIDR: "10.220.1.0/24",
	})
	require.NoError(t, err)
	require.Equal(t, http.StatusBadRequest, status, "body: %s", ErrorBody(body))
}

func TestSubnet_RejectSiblingOverlap(t *testing.T) {
	t.Parallel()
	client := clientForTenant(t, "tenant-subnet-v2")
	mustGrantOwnerForClient(t, "tenant-subnet-v2")
	vnetID := mustCreateActiveVNet(t, client, randomName("vnet"), "10.212.0.0/16", "lk")
	_ = mustCreateActiveSubnet(t, client, vnetID, randomName("subnet"), "10.212.1.0/24")
	_, body, status, err := client.CreateSubnet(context.Background(), vnetID, CreateSubnetRequest{
		Name: randomName("subnet"), CIDR: "10.212.1.0/24",
	})
	require.NoError(t, err)
	require.Equal(t, http.StatusBadRequest, status, "overlap must return 400: %s", ErrorBody(body))
}

// TestSubnet_AllowSameNameAcrossVNets verifies that two VNets owned by the
// same tenant can each have a subnet with the same human-readable name.
// Pre-fix, the kube-ovn Subnet CRD's provider name was derived from
// "subnet-<tenant>-<subnetName>", which collided at the admission webhook
// when a second VNet tried to register the same NAD provider. The fix
// includes the parent VNet's backend UID in the derived name.
func TestSubnet_AllowSameNameAcrossVNets(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	client := clientForTenant(t, "tenant-subnet-dup")
	mustGrantOwnerForClient(t, "tenant-subnet-dup")

	vnetA := mustCreateActiveVNet(t, client, randomName("vnet-a"), "10.214.0.0/16", "lk")
	vnetB := mustCreateActiveVNet(t, client, randomName("vnet-b"), "10.215.0.0/16", "lk")

	// Same subnet name in both VNets — must succeed.
	const sharedName = "sub-app"

	respA, bodyA, statusA, err := client.CreateSubnet(ctx, vnetA, CreateSubnetRequest{
		Name: sharedName, CIDR: "10.214.1.0/24",
	})
	require.NoError(t, err)
	require.Equal(t, http.StatusAccepted, statusA, "subnet A create body: %s", ErrorBody(bodyA))
	subA := respA.Resource.ID

	respB, bodyB, statusB, err := client.CreateSubnet(ctx, vnetB, CreateSubnetRequest{
		Name: sharedName, CIDR: "10.215.1.0/24",
	})
	require.NoError(t, err)
	require.Equal(t, http.StatusAccepted, statusB, "subnet B create body: %s", ErrorBody(bodyB))
	subB := respB.Resource.ID

	t.Cleanup(func() {
		ctx := context.Background()
		_, _, _ = client.DeleteSubnet(ctx, vnetA, subA)
		_, _, _ = client.DeleteSubnet(ctx, vnetB, subB)
		WaitSubnetGone(t, client, vnetA, subA)
		WaitSubnetGone(t, client, vnetB, subB)
		_, _, _ = client.DeleteVNet(ctx, vnetA)
		_, _, _ = client.DeleteVNet(ctx, vnetB)
		WaitVNetGone(t, client, vnetA)
		WaitVNetGone(t, client, vnetB)
	})

	WaitSubnetActive(t, client, vnetA, subA)
	WaitSubnetActive(t, client, vnetB, subB)

	uidA := dbGetSubnetBackendUID(t, subA)
	uidB := dbGetSubnetBackendUID(t, subB)
	require.NotEqual(t, uidA, uidB, "backend UIDs must differ even when the user-visible names match")
}

func TestSubnet_RejectParentVNetPending(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	client := clientForTenant(t, "tenant-subnet-v3")
	mustGrantOwnerForClient(t, "tenant-subnet-v3")
	createResp, _, status, err := client.CreateVNet(ctx, CreateVNetRequest{
		Name: randomName("vnet"), AddressSpace: []string{"10.213.0.0/16"}, Region: "lk",
	})
	require.NoError(t, err)
	require.Equal(t, http.StatusAccepted, status)
	vnetID := createResp.Resource.ID
	t.Cleanup(func() {
		ctx := context.Background()
		WaitVNetActive(t, client, vnetID)
		_, _, _ = client.DeleteVNet(ctx, vnetID)
		WaitVNetGone(t, client, vnetID)
	})
	_, body, status, err := client.CreateSubnet(ctx, vnetID, CreateSubnetRequest{
		Name: randomName("subnet"), CIDR: "10.213.1.0/24",
	})
	require.NoError(t, err)
	require.Equal(t, http.StatusConflict, status, "pending VNet must return 409: %s", ErrorBody(body))
}

// TestSubnet_RejectDeleteWithAttachedResources verifies the pre-flight guard
// added after the 2026-05-16 stuck-subnet incident: subnet delete must refuse
// with 409 when any VM/bastion/cluster row still points at the subnet via the
// resources.subnet_id column. Without this guard, dc-api would mark the subnet
// DELETING, call provider.DeleteSubnet, and the KubeOVN finalizer would refuse
// to release the logical switch while LSPs are pinned — leaving the subnet
// stuck FAILED until the resources are removed.
func TestSubnet_RejectDeleteWithAttachedResources(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	tenant := "tenant-subnet-detach"
	client := clientForTenant(t, tenant)
	mustGrantOwnerForClient(t, tenant)
	vnetID := mustCreateActiveVNet(t, client, randomName("vnet"), "10.214.0.0/16", "lk")
	subnetID := mustCreateActiveSubnet(t, client, vnetID, randomName("subnet"), "10.214.1.0/24")

	// Cleanup is per-step below so the t.Cleanup chain stays correct even if a
	// require.Fail fires partway through.
	t.Cleanup(func() {
		ctx := context.Background()
		_, _, _ = client.DeleteSubnet(ctx, vnetID, subnetID)
		WaitSubnetGone(t, client, vnetID, subnetID)
	})

	// Insert a synthetic bastion row directly via the repo — we don't want to
	// pay the cost of provisioning a real Harvester VM just to exercise the
	// API guard. The status is ACTIVE so any future "skip FAILED rows" tweak
	// to the guard wouldn't silently let the row through.
	subUUID := uuid.MustParse(subnetID)
	vnetUUID := uuid.MustParse(vnetID)
	// Phase 6a: resolve the slug to UUID now that the VNet (and thus the
	// tenants-registry row) is guaranteed to exist.
	tenantSlug := "test-" + tenant
	tenantUUID := tenantUUIDFor(t, tenantSlug)
	res, err := env.DB.Create(ctx, &models.Resource{
		TenantID:     tenantSlug,
		TenantUUID:   tenantUUID,
		OwnerID:      "test-owner",
		Name:         "stuck-bastion-fixture",
		Type:         models.ResourceTypeBastion,
		Status:       models.StatusActive,
		ProviderType: "harvester",
		VNetID:       &vnetUUID,
		SubnetID:     &subUUID,
	})
	require.NoError(t, err, "seed synthetic bastion row")

	// First delete attempt — must 409 and the body must name the fixture so
	// the user knows what to clean up.
	body, status, err := client.DeleteSubnet(ctx, vnetID, subnetID)
	require.NoError(t, err)
	require.Equal(t, http.StatusConflict, status,
		"subnet delete with attached resource must return 409")
	require.Contains(t, string(body), "stuck-bastion-fixture",
		"409 body must name the attached resource: %s", string(body))

	// Re-fetch the subnet — its status must still be ACTIVE (guard ran before
	// the status flip).
	getResp, getStatus, err := client.GetSubnet(ctx, vnetID, subnetID)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, getStatus)
	require.Equal(t, "ACTIVE", getResp.Status,
		"guard must reject before flipping status")

	// Remove the synthetic row and retry — now the delete must succeed.
	_, err = env.DB.Pool().Exec(ctx, `DELETE FROM resources WHERE id = $1`, res.ID)
	require.NoError(t, err, "delete synthetic bastion row")

	_, status, err = client.DeleteSubnet(ctx, vnetID, subnetID)
	require.NoError(t, err)
	require.Equal(t, http.StatusAccepted, status,
		"subnet delete after detaching must succeed")
	WaitSubnetGone(t, client, vnetID, subnetID)
}

// TestSubnet_DeleteGuardListsMultipleAttached verifies the 409 body lists every
// attached resource (not just the first) — the user needs to know everything
// blocking the delete so they can clean it up in one pass.
func TestSubnet_DeleteGuardListsMultipleAttached(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	tenant := "tenant-subnet-multi"
	client := clientForTenant(t, tenant)
	mustGrantOwnerForClient(t, tenant)
	vnetID := mustCreateActiveVNet(t, client, randomName("vnet"), "10.215.0.0/16", "lk")
	subnetID := mustCreateActiveSubnet(t, client, vnetID, randomName("subnet"), "10.215.1.0/24")
	t.Cleanup(func() {
		ctx := context.Background()
		_, _ = env.DB.Pool().Exec(ctx, `DELETE FROM resources WHERE subnet_id = $1`,
			uuid.MustParse(subnetID))
		_, _, _ = client.DeleteSubnet(ctx, vnetID, subnetID)
		WaitSubnetGone(t, client, vnetID, subnetID)
	})

	subUUID := uuid.MustParse(subnetID)
	vnetUUID := uuid.MustParse(vnetID)
	// Phase 6a: resolve slug to UUID (tenant row exists after mustCreateActiveVNet).
	tenantSlug2 := "test-" + tenant
	tenantUUID2 := tenantUUIDFor(t, tenantSlug2)

	// Seed two attached resources of different types.
	for _, fx := range []struct {
		Name string
		Type models.ResourceType
	}{
		{"fixture-vm-1", models.ResourceTypeVM},
		{"fixture-bastion-1", models.ResourceTypeBastion},
	} {
		_, err := env.DB.Create(ctx, &models.Resource{
			TenantID:     tenantSlug2,
			TenantUUID:   tenantUUID2,
			OwnerID:      "test-owner",
			Name:         fx.Name,
			Type:         fx.Type,
			Status:       models.StatusActive,
			ProviderType: "harvester",
			VNetID:       &vnetUUID,
			SubnetID:     &subUUID,
		})
		require.NoError(t, err, "seed fixture %s", fx.Name)
	}

	body, status, err := client.DeleteSubnet(ctx, vnetID, subnetID)
	require.NoError(t, err)
	require.Equal(t, http.StatusConflict, status)

	bodyStr := string(body)
	require.Contains(t, bodyStr, "fixture-vm-1",
		"409 body must name every attached resource")
	require.Contains(t, bodyStr, "fixture-bastion-1",
		"409 body must name every attached resource")
	require.True(t,
		strings.Contains(bodyStr, "VIRTUAL_MACHINE") && strings.Contains(bodyStr, "BASTION"),
		"409 body must include resource types so the user knows where to clean up; got: %s", bodyStr)
}
