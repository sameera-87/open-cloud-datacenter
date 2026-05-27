//go:build integration

package integration

import (
	"context"
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestRouteTable_FullLifecycle(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	client := clientForTenant(t, "tenant-rt-lc")
	// DELETE requires owner role (M1.5 Chunk 4).
	mustGrantOwnerForClient(t, "tenant-rt-lc")
	vnetID := mustCreateActiveVNet(t, client, randomName("vnet"), "10.240.0.0/16", "lk")
	_ = mustCreateActiveSubnet(t, client, vnetID, randomName("subnet"), "10.240.1.0/24")

	rtResp, body, status, err := client.CreateRouteTable(ctx, vnetID, CreateRouteTableRequest{
		Name: randomName("rt"),
		Routes: []RouteRuleDTO{
			{Name: "default", DestinationCIDR: "0.0.0.0/0", NextHopType: "internet"},
		},
	})
	require.NoError(t, err)
	require.Equal(t, http.StatusCreated, status, "body: %s", ErrorBody(body))
	require.Equal(t, "ACTIVE", rtResp.Status)
	require.Len(t, rtResp.Routes, 1)
	rtID := rtResp.ID

	t.Cleanup(func() {
		_, _ = client.DeleteRouteTable(context.Background(), vnetID, rtID)
	})

	backendUID := dbGetVNetBackendUID(t, vnetID)
	routes, err := GetVpcStaticRoutes(ctx, env.KubeClient, backendUID)
	require.NoError(t, err)
	require.NotEmpty(t, routes, "static routes must appear on VPC after RouteTable create")

	getResp, status, err := client.GetRouteTable(ctx, vnetID, rtID)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, status)
	require.Equal(t, "ACTIVE", getResp.Status)

	status, err = client.DeleteRouteTable(ctx, vnetID, rtID)
	require.NoError(t, err)
	require.Equal(t, http.StatusNoContent, status)
}

func TestRouteTable_TwoStepOrchestration(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	client := clientForTenant(t, "tenant-rt-twostep")
	mustGrantOwnerForClient(t, "tenant-rt-twostep")
	vnetID := mustCreateActiveVNet(t, client, randomName("vnet"), "10.241.0.0/16", "lk")

	rtResp, _, status, err := client.CreateRouteTable(ctx, vnetID, CreateRouteTableRequest{Name: randomName("rt")})
	require.NoError(t, err)
	require.Equal(t, http.StatusCreated, status)
	rtID := rtResp.ID
	t.Cleanup(func() { _, _ = client.DeleteRouteTable(context.Background(), vnetID, rtID) })

	updResp, body, status, err := client.UpdateRouteTableRoutes(ctx, vnetID, rtID, []RouteRuleDTO{
		{Name: "to-internet", DestinationCIDR: "0.0.0.0/0", NextHopType: "internet"},
	})
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, status, "UpdateRoutes: %s", ErrorBody(body))
	require.Len(t, updResp.Routes, 1)
}

func TestRouteTable_AssociationWarningPresent(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	client := clientForTenant(t, "tenant-rt-warn")
	mustGrantOwnerForClient(t, "tenant-rt-warn")
	vnetID := mustCreateActiveVNet(t, client, randomName("vnet"), "10.242.0.0/16", "lk")
	subnetID := mustCreateActiveSubnet(t, client, vnetID, randomName("subnet"), "10.242.1.0/24")

	rtResp, _, status, err := client.CreateRouteTable(ctx, vnetID, CreateRouteTableRequest{Name: randomName("rt")})
	require.NoError(t, err)
	require.Equal(t, http.StatusCreated, status)
	rtID := rtResp.ID
	t.Cleanup(func() { _, _ = client.DeleteRouteTable(context.Background(), vnetID, rtID) })

	assocResp, body, status, err := client.AssociateRouteTable(ctx, vnetID, rtID, subnetID)
	require.NoError(t, err)
	require.Equal(t, http.StatusCreated, status, "body: %s", ErrorBody(body))
	warning, _ := assocResp["warning"].(string)
	require.NotEmpty(t, warning, "association response must contain a 'warning' field per § 14")
}
