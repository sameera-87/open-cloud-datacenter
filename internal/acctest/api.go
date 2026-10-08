package acctest

import (
	"context"
	"time"

	"terraform-provider-dcapi/internal/client"
)

// Per-resource ExistsFunc and DeleteFunc implementations, shared by CheckDestroy, Disappears
// steps and the sweepers. Each takes the resource's state ID split on "/"; the ID formats
// come from the resource's d.SetId call in internal/resources.
//
// Deletes of async resources wait for the 404, using the same timeouts as the resource
// schemas, so a following parent delete doesn't race them.

// ── vnet: tenant/project/vnet ────────────────────────────────────────────────

func VNetExists(ctx context.Context, c *client.DCAPIClient, p []string) (bool, error) {
	v, err := c.GetVNet(ctx, p[0], p[1], p[2])
	return v != nil, err
}

func DeleteVNet(ctx context.Context, c *client.DCAPIClient, p []string) error {
	if err := c.DeleteVNet(ctx, p[0], p[1], p[2]); err != nil {
		return err
	}
	return WaitGone(ctx, 5*time.Minute, func(ctx context.Context) (bool, error) { return VNetExists(ctx, c, p) })
}

// ── subnet: tenant/project/vnet/subnet ───────────────────────────────────────

func SubnetExists(ctx context.Context, c *client.DCAPIClient, p []string) (bool, error) {
	s, err := c.GetSubnet(ctx, p[0], p[1], p[2], p[3])
	return s != nil, err
}

func DeleteSubnet(ctx context.Context, c *client.DCAPIClient, p []string) error {
	if err := c.DeleteSubnet(ctx, p[0], p[1], p[2], p[3]); err != nil {
		return err
	}
	return WaitGone(ctx, 15*time.Minute, func(ctx context.Context) (bool, error) { return SubnetExists(ctx, c, p) })
}

// ── network_security_group: tenant/project/sg ────────────────────────────────

func NSGExists(ctx context.Context, c *client.DCAPIClient, p []string) (bool, error) {
	n, err := c.GetNSG(ctx, p[0], p[1], p[2])
	return n != nil, err
}

func DeleteNSG(ctx context.Context, c *client.DCAPIClient, p []string) error {
	return c.DeleteNSG(ctx, p[0], p[1], p[2])
}

// ── nsg_attachment: tenant/project/sg/attachment ─────────────────────────────

// NSGAttachmentExists looks the attachment up in its parent NSG; a missing NSG means it's gone.
func NSGAttachmentExists(ctx context.Context, c *client.DCAPIClient, p []string) (bool, error) {
	n, err := c.GetNSG(ctx, p[0], p[1], p[2])
	if err != nil || n == nil {
		return false, err
	}
	for _, a := range n.Attachments {
		if a.ID == p[3] {
			return true, nil
		}
	}
	return false, nil
}

func DeleteNSGAttachment(ctx context.Context, c *client.DCAPIClient, p []string) error {
	return c.DeleteNSGAttachment(ctx, p[0], p[1], p[2], p[3])
}

// ── route_table: tenant/project/vnet/rt ──────────────────────────────────────

func RouteTableExists(ctx context.Context, c *client.DCAPIClient, p []string) (bool, error) {
	rt, err := c.GetRouteTable(ctx, p[0], p[1], p[2], p[3])
	return rt != nil, err
}

func DeleteRouteTable(ctx context.Context, c *client.DCAPIClient, p []string) error {
	return c.DeleteRouteTable(ctx, p[0], p[1], p[2], p[3])
}

// ── route_table_association: tenant/project/vnet/rt/association ──────────────

// RouteTableAssociationExists looks the association up in its parent route table.
func RouteTableAssociationExists(ctx context.Context, c *client.DCAPIClient, p []string) (bool, error) {
	rt, err := c.GetRouteTable(ctx, p[0], p[1], p[2], p[3])
	if err != nil || rt == nil {
		return false, err
	}
	for _, a := range rt.Associations {
		if a.ID == p[4] {
			return true, nil
		}
	}
	return false, nil
}

func DeleteRouteTableAssociation(ctx context.Context, c *client.DCAPIClient, p []string) error {
	return c.DeleteRouteTableAssociation(ctx, p[0], p[1], p[2], p[3], p[4])
}

// ── vnet_peering: tenant/project/vnet/peering ────────────────────────────────

func VNetPeeringExists(ctx context.Context, c *client.DCAPIClient, p []string) (bool, error) {
	v, err := c.GetVNetPeering(ctx, p[0], p[1], p[2], p[3])
	return v != nil, err
}

func DeleteVNetPeering(ctx context.Context, c *client.DCAPIClient, p []string) error {
	if err := c.DeleteVNetPeering(ctx, p[0], p[1], p[2], p[3]); err != nil {
		return err
	}
	return WaitGone(ctx, 5*time.Minute, func(ctx context.Context) (bool, error) { return VNetPeeringExists(ctx, c, p) })
}

// ── private_dns_zone: tenant/project/vnet/zone ───────────────────────────────

func PrivateDNSZoneExists(ctx context.Context, c *client.DCAPIClient, p []string) (bool, error) {
	z, err := c.GetPrivateDnsZone(ctx, p[0], p[1], p[2], p[3])
	return z != nil, err
}

func DeletePrivateDNSZone(ctx context.Context, c *client.DCAPIClient, p []string) error {
	if err := c.DeletePrivateDnsZone(ctx, p[0], p[1], p[2], p[3]); err != nil {
		return err
	}
	return WaitGone(ctx, 5*time.Minute, func(ctx context.Context) (bool, error) { return PrivateDNSZoneExists(ctx, c, p) })
}

// ── dns_record: tenant/project/vnet/zone/record ──────────────────────────────

func DNSRecordExists(ctx context.Context, c *client.DCAPIClient, p []string) (bool, error) {
	r, err := c.GetDnsRecord(ctx, p[0], p[1], p[2], p[3], p[4])
	return r != nil, err
}

func DeleteDNSRecord(ctx context.Context, c *client.DCAPIClient, p []string) error {
	return c.DeleteDnsRecord(ctx, p[0], p[1], p[2], p[3], p[4])
}

// ── key_vault: tenant/project/kv ─────────────────────────────────────────────

// KeyVaultExists treats a soft-deleted vault as gone when DC-API returns 404 for it.
func KeyVaultExists(ctx context.Context, c *client.DCAPIClient, p []string) (bool, error) {
	kv, err := c.GetKeyVault(ctx, p[0], p[1], p[2])
	return kv != nil, err
}

func DeleteKeyVault(ctx context.Context, c *client.DCAPIClient, p []string) error {
	return c.DeleteKeyVault(ctx, p[0], p[1], p[2])
}

// ── key_vault_secret: tenant/project/kv/key ──────────────────────────────────

// KeyVaultSecretExists: GetKeyVaultSecret returns nil for both 404 and 410 (soft-deleted).
func KeyVaultSecretExists(ctx context.Context, c *client.DCAPIClient, p []string) (bool, error) {
	s, err := c.GetKeyVaultSecret(ctx, p[0], p[1], p[2], p[3])
	return s != nil, err
}

func DeleteKeyVaultSecret(ctx context.Context, c *client.DCAPIClient, p []string) error {
	return c.DeleteKeyVaultSecret(ctx, p[0], p[1], p[2], p[3])
}

// ── private_endpoint: tenant/project/kv/endpoint ─────────────────────────────

func PrivateEndpointExists(ctx context.Context, c *client.DCAPIClient, p []string) (bool, error) {
	ep, err := c.GetPrivateEndpoint(ctx, p[0], p[1], p[2], p[3])
	return ep != nil, err
}

func DeletePrivateEndpoint(ctx context.Context, c *client.DCAPIClient, p []string) error {
	return c.DeletePrivateEndpoint(ctx, p[0], p[1], p[2], p[3])
}

// ── virtual_machine: tenant/project/vm ───────────────────────────────────────

func VMExists(ctx context.Context, c *client.DCAPIClient, p []string) (bool, error) {
	vm, err := c.GetVM(ctx, p[0], p[1], p[2])
	return vm != nil, err
}

func DeleteVM(ctx context.Context, c *client.DCAPIClient, p []string) error {
	if err := c.DeleteVM(ctx, p[0], p[1], p[2]); err != nil {
		return err
	}
	return WaitGone(ctx, 10*time.Minute, func(ctx context.Context) (bool, error) { return VMExists(ctx, c, p) })
}

// ── bastion: tenant/project/bastion ──────────────────────────────────────────

func BastionExists(ctx context.Context, c *client.DCAPIClient, p []string) (bool, error) {
	b, err := c.GetBastion(ctx, p[0], p[1], p[2])
	return b != nil, err
}

func DeleteBastion(ctx context.Context, c *client.DCAPIClient, p []string) error {
	if err := c.DeleteBastion(ctx, p[0], p[1], p[2]); err != nil {
		return err
	}
	return WaitGone(ctx, 10*time.Minute, func(ctx context.Context) (bool, error) { return BastionExists(ctx, c, p) })
}

// ── cluster: tenant/project/cluster ──────────────────────────────────────────

func ClusterExists(ctx context.Context, c *client.DCAPIClient, p []string) (bool, error) {
	cl, err := c.GetCluster(ctx, p[0], p[1], p[2])
	return cl != nil, err
}

func DeleteCluster(ctx context.Context, c *client.DCAPIClient, p []string) error {
	if err := c.DeleteCluster(ctx, p[0], p[1], p[2]); err != nil {
		return err
	}
	return WaitGone(ctx, 20*time.Minute, func(ctx context.Context) (bool, error) { return ClusterExists(ctx, c, p) })
}

// ── node_pool: tenant/project/cluster/pool-name ──────────────────────────────

func NodePoolExists(ctx context.Context, c *client.DCAPIClient, p []string) (bool, error) {
	np, err := c.GetNodePool(ctx, p[0], p[1], p[2], p[3])
	return np != nil, err
}

func DeleteNodePool(ctx context.Context, c *client.DCAPIClient, p []string) error {
	if err := c.DeleteNodePool(ctx, p[0], p[1], p[2], p[3]); err != nil {
		return err
	}
	return WaitGone(ctx, 10*time.Minute, func(ctx context.Context) (bool, error) { return NodePoolExists(ctx, c, p) })
}

// ── service_account: tenant/project/sa ───────────────────────────────────────

func ServiceAccountExists(ctx context.Context, c *client.DCAPIClient, p []string) (bool, error) {
	sa, err := c.GetServiceAccount(ctx, p[0], p[1], p[2])
	return sa != nil, err
}

func DeleteServiceAccount(ctx context.Context, c *client.DCAPIClient, p []string) error {
	return c.DeleteServiceAccount(ctx, p[0], p[1], p[2])
}
