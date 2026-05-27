// Package client — name-resolution helpers for VPC-shaped resources.
//
// Most dcctl commands accept either a UUID or a display name on the CLI. The
// DC-API server expects UUIDs, so these helpers do a typed list-and-match
// before any mutating call. They live next to client.New() so commands can
// reach them via apiClient.ResolveVNetID(...) without importing the generated
// package directly.
package client

import (
	"context"
	"fmt"
	"net/http"

	"github.com/google/uuid"
)

// ResolveVNetID accepts a VNet name OR UUID and returns the UUID string.
// A non-UUID name triggers a typed ListVNets and a linear scan.
func (c *Client) ResolveVNetID(tenantID, projectID, idOrName string) (string, error) {
	if _, err := uuid.Parse(idOrName); err == nil {
		return idOrName, nil
	}
	resp, err := c.Typed.ListVNetsWithResponse(context.Background(), tenantID, projectID)
	if err != nil {
		return "", fmt.Errorf("list vnets for name resolution: %w", err)
	}
	if resp.StatusCode() != http.StatusOK || resp.JSON200 == nil {
		return "", fmt.Errorf("list vnets returned HTTP %d", resp.StatusCode())
	}
	for _, v := range *resp.JSON200 {
		if v.Name == idOrName {
			return v.Id.String(), nil
		}
	}
	return "", fmt.Errorf("VNet %q not found", idOrName)
}

// ResolveSubnetID accepts a subnet name OR UUID within a known VNet UUID and
// returns the subnet UUID string.
func (c *Client) ResolveSubnetID(tenantID, projectID, vnetIDStr, idOrName string) (string, error) {
	if _, err := uuid.Parse(idOrName); err == nil {
		return idOrName, nil
	}
	vnetID, err := uuid.Parse(vnetIDStr)
	if err != nil {
		return "", fmt.Errorf("invalid VNet id %q: %w", vnetIDStr, err)
	}
	resp, err := c.Typed.ListSubnetsWithResponse(context.Background(), tenantID, projectID, vnetID)
	if err != nil {
		return "", fmt.Errorf("list subnets for name resolution: %w", err)
	}
	if resp.StatusCode() != http.StatusOK || resp.JSON200 == nil {
		return "", fmt.Errorf("list subnets returned HTTP %d", resp.StatusCode())
	}
	for _, s := range *resp.JSON200 {
		if s.Name == idOrName {
			return s.Id.String(), nil
		}
	}
	return "", fmt.Errorf("subnet %q not found in VNet %s", idOrName, vnetIDStr)
}

// ResolvePeeringID accepts a peering name OR UUID within a known VNet UUID
// and returns the peering UUID string.
func (c *Client) ResolvePeeringID(tenantID, projectID, vnetIDStr, idOrName string) (string, error) {
	if _, err := uuid.Parse(idOrName); err == nil {
		return idOrName, nil
	}
	vnetID, err := uuid.Parse(vnetIDStr)
	if err != nil {
		return "", fmt.Errorf("invalid VNet id %q: %w", vnetIDStr, err)
	}
	resp, err := c.Typed.ListPeeringsWithResponse(context.Background(), tenantID, projectID, vnetID)
	if err != nil {
		return "", fmt.Errorf("list peerings for name resolution: %w", err)
	}
	if resp.StatusCode() != http.StatusOK || resp.JSON200 == nil {
		return "", fmt.Errorf("list peerings returned HTTP %d", resp.StatusCode())
	}
	for _, p := range *resp.JSON200 {
		if p.Name == idOrName {
			return p.Id.String(), nil
		}
	}
	return "", fmt.Errorf("peering %q not found in VNet %s", idOrName, vnetIDStr)
}
