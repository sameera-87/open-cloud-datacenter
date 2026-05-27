//go:build integration

package integration

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func TestNSG_FullLifecycle(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	client := clientForTenant(t, "tenant-nsg-lc")
	// DELETE and Detach require owner role (M1.5 Chunk 4).
	mustGrantOwnerForClient(t, "tenant-nsg-lc")
	vnetID := mustCreateActiveVNet(t, client, randomName("vnet"), "10.220.0.0/16", "lk")
	subnetID := mustCreateActiveSubnet(t, client, vnetID, randomName("subnet"), "10.220.1.0/24")

	nsgResp, body, status, err := client.CreateNSG(ctx, CreateNSGRequest{
		Name: randomName("nsg"),
		Rules: []NSGRuleDTO{{
			Name: "allow-443", Direction: "inbound", Priority: 100, Protocol: "tcp",
			SourceAddressPrefix: "*", SourcePortRange: "*",
			DestinationAddressPrefix: "*", DestinationPortRange: "443", Action: "allow",
		}},
	})
	require.NoError(t, err)
	require.Equal(t, http.StatusCreated, status, "body: %s", ErrorBody(body))
	require.Equal(t, "ACTIVE", nsgResp.Status)
	nsgID := nsgResp.ID

	t.Cleanup(func() { _, _ = client.DeleteNSG(context.Background(), nsgID) })

	attResp, body, status, err := client.AttachNSG(ctx, nsgID, map[string]string{
		"target_type": "subnet", "target_id": subnetID,
	})
	require.NoError(t, err)
	require.Equal(t, http.StatusCreated, status, "attach: %s", ErrorBody(body))
	attID := attResp.ID

	subnetUID := dbGetSubnetBackendUID(t, subnetID)
	require.Eventually(t, func() bool {
		acls, err := GetSubnetACLs(ctx, env.KubeClient, subnetUID)
		return err == nil && ACLsContainNSGTag(acls, nsgID)
	}, 30*time.Second, 1*time.Second, "NSG rules must appear in Subnet.spec.acls")

	status, err = client.DetachNSG(ctx, nsgID, attID)
	require.NoError(t, err)
	require.Equal(t, http.StatusNoContent, status)

	require.Eventually(t, func() bool {
		acls, _ := GetSubnetACLs(ctx, env.KubeClient, subnetUID)
		return !ACLsContainNSGTag(acls, nsgID)
	}, 30*time.Second, 1*time.Second, "NSG rules must be removed from Subnet.spec.acls")

	status, err = client.DeleteNSG(ctx, nsgID)
	require.NoError(t, err)
	require.Equal(t, http.StatusNoContent, status)
}

func TestNSG_TwoStepOrchestration(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	client := clientForTenant(t, "tenant-nsg-2step")
	mustGrantOwnerForClient(t, "tenant-nsg-2step")
	vnetID := mustCreateActiveVNet(t, client, randomName("vnet"), "10.221.0.0/16", "lk")
	subnetID := mustCreateActiveSubnet(t, client, vnetID, randomName("subnet"), "10.221.1.0/24")

	nsgResp, _, status, _ := client.CreateNSG(ctx, CreateNSGRequest{Name: randomName("nsg")})
	require.Equal(t, http.StatusCreated, status)
	nsgID := nsgResp.ID
	t.Cleanup(func() { _, _ = client.DeleteNSG(context.Background(), nsgID) })

	_, _, status, err := client.UpdateNSGRules(ctx, nsgID, []NSGRuleDTO{{
		Name: "block-80", Direction: "inbound", Priority: 200, Protocol: "tcp",
		SourceAddressPrefix: "*", SourcePortRange: "*",
		DestinationAddressPrefix: "*", DestinationPortRange: "80", Action: "deny",
	}})
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, status)

	attResp, _, status, err := client.AttachNSG(ctx, nsgID, map[string]string{
		"target_type": "subnet", "target_id": subnetID,
	})
	require.NoError(t, err)
	require.Equal(t, http.StatusCreated, status)

	subnetUID := dbGetSubnetBackendUID(t, subnetID)
	require.Eventually(t, func() bool {
		acls, _ := GetSubnetACLs(ctx, env.KubeClient, subnetUID)
		return ACLsContainNSGTag(acls, nsgID)
	}, 30*time.Second, 1*time.Second)
	_, _ = client.DetachNSG(ctx, nsgID, attResp.ID)
}

func TestNSG_RejectNICTargetType(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	client := clientForTenant(t, "tenant-nsg-nic")
	mustGrantOwnerForClient(t, "tenant-nsg-nic")
	nsgResp, _, status, _ := client.CreateNSG(ctx, CreateNSGRequest{Name: randomName("nsg")})
	require.Equal(t, http.StatusCreated, status)
	nsgID := nsgResp.ID
	t.Cleanup(func() { _, _ = client.DeleteNSG(context.Background(), nsgID) })

	_, body, status, err := client.AttachNSG(ctx, nsgID, map[string]string{
		"target_type": "nic",
		"target_id":   uuid.New().String(),
	})
	require.NoError(t, err)
	require.Equal(t, http.StatusBadRequest, status, "NIC attachment must return 400 in M2: %s", ErrorBody(body))
}

func TestNSG_RejectDuplicatePriority(t *testing.T) {
	t.Parallel()
	rule := func(name string) NSGRuleDTO {
		return NSGRuleDTO{
			Name: name, Direction: "inbound", Priority: 100, Protocol: "tcp",
			SourceAddressPrefix: "*", SourcePortRange: "*",
			DestinationAddressPrefix: "*", DestinationPortRange: "80", Action: "allow",
		}
	}
	_, body, status, err := clientForTenant(t, "tenant-nsg-duprio").CreateNSG(
		context.Background(),
		CreateNSGRequest{Name: randomName("nsg"), Rules: []NSGRuleDTO{rule("r1"), rule("r2")}},
	)
	require.NoError(t, err)
	require.Equal(t, http.StatusConflict, status, "duplicate priority must return 409: %s", ErrorBody(body))
}

func TestNSG_ACLToggleDeterministic(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	client := clientForTenant(t, "tenant-nsg-toggle")
	mustGrantOwnerForClient(t, "tenant-nsg-toggle")
	vnetID := mustCreateActiveVNet(t, client, randomName("vnet"), "10.222.0.0/16", "lk")
	subnetID := mustCreateActiveSubnet(t, client, vnetID, randomName("subnet"), "10.222.1.0/24")

	nsgResp, _, status, _ := client.CreateNSG(ctx, CreateNSGRequest{
		Name: randomName("nsg"),
		Rules: []NSGRuleDTO{{
			Name: "deny-22", Direction: "inbound", Priority: 300, Protocol: "tcp",
			SourceAddressPrefix: "*", SourcePortRange: "*",
			DestinationAddressPrefix: "*", DestinationPortRange: "22", Action: "deny",
		}},
	})
	require.Equal(t, http.StatusCreated, status)
	nsgID := nsgResp.ID
	t.Cleanup(func() { _, _ = client.DeleteNSG(context.Background(), nsgID) })
	subnetUID := dbGetSubnetBackendUID(t, subnetID)

	hasACL := func() bool {
		acls, _ := GetSubnetACLs(ctx, env.KubeClient, subnetUID)
		return ACLsContainNSGTag(acls, nsgID)
	}

	att1, _, _, _ := client.AttachNSG(ctx, nsgID, map[string]string{"target_type": "subnet", "target_id": subnetID})
	require.Eventually(t, hasACL, 30*time.Second, 1*time.Second, "ACL must appear after attach 1")
	_, _ = client.DetachNSG(ctx, nsgID, att1.ID)
	require.Eventually(t, func() bool { return !hasACL() }, 30*time.Second, 1*time.Second, "ACL must disappear after detach")

	att2, _, _, _ := client.AttachNSG(ctx, nsgID, map[string]string{"target_type": "subnet", "target_id": subnetID})
	require.Eventually(t, hasACL, 30*time.Second, 1*time.Second, "ACL must reappear after attach 2")
	_, _ = client.DetachNSG(ctx, nsgID, att2.ID)
}
