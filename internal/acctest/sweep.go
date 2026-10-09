package acctest

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"

	"terraform-provider-dcapi/internal/client"
)

// Sweepers delete every object named acc-<DCAPI_ACC_RUN_ID>-* that a crashed or killed test
// left behind (docs/testsuite/06). Run them with:
//
//	go test ./internal/acctest -run '^$' -sweep=<project id> -sweep-allow-failures
//
// The framework passes the -sweep value to every sweeper as its "region". We use it for the
// project slug instead, and refuse to run unless it equals DCAPI_ACC_PROJECT_ID.
//
// Dependencies run first, so children are always deleted before their parents:
//
//	node_pool → cluster ─────────────────────────────────┐
//	virtual_machine, bastion ────────────────────────────┤
//	private_endpoint → key_vault                         │
//	nsg_attachment → network_security_group              ├─► subnet ─► vnet
//	route_table_association → route_table ───────────────┤
//	dns_record → private_dns_zone ───────────────────────┤
//	vnet_peering ────────────────────────────────────────┘
//	service_account (independent)
func init() {
	add := func(name string, deps []string, f func(ctx context.Context, c *client.DCAPIClient) error) {
		resource.AddTestSweepers(name, &resource.Sweeper{Name: name, Dependencies: deps, F: sweepFunc(name, f)})
	}

	add("dcapi_node_pool", nil, sweepNodePools)
	add("dcapi_cluster", []string{"dcapi_node_pool"}, sweepClusters)
	add("dcapi_virtual_machine", nil, sweepVMs)
	add("dcapi_bastion", nil, sweepBastions)
	add("dcapi_private_endpoint", nil, sweepPrivateEndpoints)
	add("dcapi_key_vault", []string{"dcapi_private_endpoint"}, sweepKeyVaults)
	add("dcapi_nsg_attachment", nil, sweepNSGAttachments)
	add("dcapi_network_security_group", []string{"dcapi_nsg_attachment"}, sweepNSGs)
	add("dcapi_route_table_association", nil, sweepRouteTableAssociations)
	add("dcapi_route_table", []string{"dcapi_route_table_association"}, sweepRouteTables)
	add("dcapi_dns_record", nil, sweepDNSRecords)
	add("dcapi_private_dns_zone", []string{"dcapi_dns_record"}, sweepPrivateDNSZones)
	add("dcapi_vnet_peering", nil, sweepVNetPeerings)
	add("dcapi_subnet", []string{
		"dcapi_virtual_machine", "dcapi_bastion", "dcapi_cluster",
		"dcapi_private_endpoint", "dcapi_nsg_attachment", "dcapi_route_table_association",
	}, sweepSubnets)
	add("dcapi_vnet", []string{
		"dcapi_subnet", "dcapi_route_table", "dcapi_vnet_peering", "dcapi_private_dns_zone",
	}, sweepVNets)
	add("dcapi_service_account", nil, sweepServiceAccounts)
}

// sweepFunc wraps a sweeper with the safety checks every sweeper shares.
func sweepFunc(name string, f func(ctx context.Context, c *client.DCAPIClient) error) resource.SweeperFunc {
	return func(project string) error {
		if RunID() == "" {
			return fmt.Errorf("%s: DCAPI_ACC_RUN_ID is empty; refusing to sweep (it would match nothing safely)", name)
		}
		if project != ProjectID() {
			return fmt.Errorf("%s: -sweep=%q does not match DCAPI_ACC_PROJECT_ID=%q; refusing to sweep", name, project, ProjectID())
		}
		c, err := NewAPIClient()
		if err != nil {
			return err
		}
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
		defer cancel()
		return f(ctx, c)
	}
}

// shouldSweep is the one filter every sweeper uses: only this run's objects.
func shouldSweep(name string) bool {
	return RunID() != "" && strings.HasPrefix(name, RunPrefix())
}

// target is one object to delete, identified by its state-ID-style parts.
type target struct {
	label string
	parts []string
}

// deleteAll sends every delete first, then waits for each object to be gone. Waiting after
// all deletes are sent keeps slow deletes (a last subnet can take 15 minutes) from adding up.
// exists may be nil for synchronous deletes.
func deleteAll(ctx context.Context, c *client.DCAPIClient, kind string, targets []target,
	del DeleteFunc, exists ExistsFunc, timeout time.Duration) error {

	var errs []error
	var sent []target
	for _, t := range targets {
		log.Printf("[INFO] sweeping %s %s", kind, t.label)
		if err := del(ctx, c, t.parts); err != nil {
			errs = append(errs, fmt.Errorf("deleting %s %s: %w", kind, t.label, err))
			continue
		}
		sent = append(sent, t)
	}
	if exists != nil {
		for _, t := range sent {
			t := t
			if err := WaitGone(ctx, timeout, func(ctx context.Context) (bool, error) { return exists(ctx, c, t.parts) }); err != nil {
				errs = append(errs, fmt.Errorf("waiting for %s %s to be deleted: %w", kind, t.label, err))
			}
		}
	}
	return errors.Join(errs...)
}

// runVNets returns this run's VNets. Children of these VNets (subnets, route tables,
// peerings, zones) belong to this run too, whatever their own names are.
func runVNets(ctx context.Context, c *client.DCAPIClient) ([]client.VNetResponse, error) {
	all, err := c.ListVNets(ctx, TenantID(), ProjectID())
	if err != nil {
		return nil, err
	}
	var out []client.VNetResponse
	for _, v := range all {
		if shouldSweep(v.Name) {
			out = append(out, v)
		}
	}
	return out, nil
}

func sweepNodePools(ctx context.Context, c *client.DCAPIClient) error {
	clusters, err := c.ListClusters(ctx, TenantID(), ProjectID())
	if err != nil {
		return err
	}
	var targets []target
	for _, cl := range clusters {
		if !shouldSweep(cl.Name) {
			continue
		}
		pools, err := c.ListNodePools(ctx, TenantID(), ProjectID(), cl.ID)
		if err != nil {
			return err
		}
		for _, np := range pools {
			if np.Role == "system" || np.Name == "system" {
				continue // deleted with the cluster
			}
			targets = append(targets, target{cl.Name + "/" + np.Name, []string{TenantID(), ProjectID(), cl.ID, np.Name}})
		}
	}
	return deleteAll(ctx, c, "node pool", targets, rawDeleteNodePool, NodePoolExists, 10*time.Minute)
}

func sweepClusters(ctx context.Context, c *client.DCAPIClient) error {
	clusters, err := c.ListClusters(ctx, TenantID(), ProjectID())
	if err != nil {
		return err
	}
	var targets []target
	for _, cl := range clusters {
		if shouldSweep(cl.Name) {
			targets = append(targets, target{cl.Name, []string{TenantID(), ProjectID(), cl.ID}})
		}
	}
	return deleteAll(ctx, c, "cluster", targets, rawDeleteCluster, ClusterExists, 20*time.Minute)
}

func sweepVMs(ctx context.Context, c *client.DCAPIClient) error {
	vms, err := c.ListVMs(ctx, TenantID(), ProjectID())
	if err != nil {
		return err
	}
	var targets []target
	for _, vm := range vms {
		if shouldSweep(vm.Name) {
			targets = append(targets, target{vm.Name, []string{TenantID(), ProjectID(), vm.ID}})
		}
	}
	return deleteAll(ctx, c, "virtual machine", targets, rawDeleteVM, VMExists, 10*time.Minute)
}

func sweepBastions(ctx context.Context, c *client.DCAPIClient) error {
	bastions, err := c.ListBastions(ctx, TenantID(), ProjectID())
	if err != nil {
		return err
	}
	var targets []target
	for _, b := range bastions {
		if shouldSweep(b.Name) {
			targets = append(targets, target{b.Name, []string{TenantID(), ProjectID(), b.ID}})
		}
	}
	return deleteAll(ctx, c, "bastion", targets, rawDeleteBastion, BastionExists, 10*time.Minute)
}

func sweepPrivateEndpoints(ctx context.Context, c *client.DCAPIClient) error {
	kvs, err := c.ListKeyVaults(ctx, TenantID(), ProjectID())
	if err != nil {
		return err
	}
	var targets []target
	for _, kv := range kvs {
		if !shouldSweep(kv.Name) {
			continue
		}
		eps, err := c.ListPrivateEndpoints(ctx, TenantID(), ProjectID(), kv.ID)
		if err != nil {
			return err
		}
		for _, ep := range eps {
			targets = append(targets, target{kv.Name + "/" + ep.Name, []string{TenantID(), ProjectID(), kv.ID, ep.ID}})
		}
	}
	return deleteAll(ctx, c, "private endpoint", targets, DeletePrivateEndpoint, PrivateEndpointExists, 5*time.Minute)
}

// sweepKeyVaults deletes this run's vaults. Their secrets go with them.
func sweepKeyVaults(ctx context.Context, c *client.DCAPIClient) error {
	kvs, err := c.ListKeyVaults(ctx, TenantID(), ProjectID())
	if err != nil {
		return err
	}
	var targets []target
	for _, kv := range kvs {
		if shouldSweep(kv.Name) {
			targets = append(targets, target{kv.Name, []string{TenantID(), ProjectID(), kv.ID}})
		}
	}
	return deleteAll(ctx, c, "key vault", targets, DeleteKeyVault, nil, 0)
}

func sweepNSGAttachments(ctx context.Context, c *client.DCAPIClient) error {
	nsgs, err := c.ListNSGs(ctx, TenantID(), ProjectID())
	if err != nil {
		return err
	}
	var targets []target
	for _, n := range nsgs {
		if !shouldSweep(n.Name) {
			continue
		}
		// The list response may omit attachments, so read each NSG.
		full, err := c.GetNSG(ctx, TenantID(), ProjectID(), n.ID)
		if err != nil || full == nil {
			continue
		}
		for _, a := range full.Attachments {
			targets = append(targets, target{n.Name + "/" + a.ID, []string{TenantID(), ProjectID(), n.ID, a.ID}})
		}
	}
	return deleteAll(ctx, c, "NSG attachment", targets, DeleteNSGAttachment, nil, 0)
}

func sweepNSGs(ctx context.Context, c *client.DCAPIClient) error {
	nsgs, err := c.ListNSGs(ctx, TenantID(), ProjectID())
	if err != nil {
		return err
	}
	var targets []target
	for _, n := range nsgs {
		if shouldSweep(n.Name) {
			targets = append(targets, target{n.Name, []string{TenantID(), ProjectID(), n.ID}})
		}
	}
	return deleteAll(ctx, c, "NSG", targets, DeleteNSG, nil, 0)
}

func sweepRouteTableAssociations(ctx context.Context, c *client.DCAPIClient) error {
	vnets, err := runVNets(ctx, c)
	if err != nil {
		return err
	}
	var targets []target
	for _, v := range vnets {
		rts, err := c.ListRouteTables(ctx, TenantID(), ProjectID(), v.ID)
		if err != nil {
			return err
		}
		for _, rt := range rts {
			full, err := c.GetRouteTable(ctx, TenantID(), ProjectID(), v.ID, rt.ID)
			if err != nil || full == nil {
				continue
			}
			for _, a := range full.Associations {
				targets = append(targets, target{rt.Name + "/" + a.ID, []string{TenantID(), ProjectID(), v.ID, rt.ID, a.ID}})
			}
		}
	}
	return deleteAll(ctx, c, "route table association", targets, DeleteRouteTableAssociation, nil, 0)
}

func sweepRouteTables(ctx context.Context, c *client.DCAPIClient) error {
	vnets, err := runVNets(ctx, c)
	if err != nil {
		return err
	}
	var targets []target
	for _, v := range vnets {
		rts, err := c.ListRouteTables(ctx, TenantID(), ProjectID(), v.ID)
		if err != nil {
			return err
		}
		for _, rt := range rts {
			targets = append(targets, target{rt.Name, []string{TenantID(), ProjectID(), v.ID, rt.ID}})
		}
	}
	return deleteAll(ctx, c, "route table", targets, DeleteRouteTable, nil, 0)
}

func sweepDNSRecords(ctx context.Context, c *client.DCAPIClient) error {
	vnets, err := runVNets(ctx, c)
	if err != nil {
		return err
	}
	var targets []target
	for _, v := range vnets {
		zones, err := c.ListPrivateDnsZones(ctx, TenantID(), ProjectID(), v.ID)
		if err != nil {
			return err
		}
		for _, z := range zones {
			records, err := c.ListDnsRecords(ctx, TenantID(), ProjectID(), v.ID, z.ID)
			if err != nil {
				return err
			}
			for _, r := range records {
				targets = append(targets, target{z.Name + "/" + r.Name, []string{TenantID(), ProjectID(), v.ID, z.ID, r.ID}})
			}
		}
	}
	return deleteAll(ctx, c, "DNS record", targets, DeleteDNSRecord, nil, 0)
}

func sweepPrivateDNSZones(ctx context.Context, c *client.DCAPIClient) error {
	vnets, err := runVNets(ctx, c)
	if err != nil {
		return err
	}
	var targets []target
	for _, v := range vnets {
		zones, err := c.ListPrivateDnsZones(ctx, TenantID(), ProjectID(), v.ID)
		if err != nil {
			return err
		}
		for _, z := range zones {
			targets = append(targets, target{z.Name, []string{TenantID(), ProjectID(), v.ID, z.ID}})
		}
	}
	return deleteAll(ctx, c, "private DNS zone", targets, rawDeletePrivateDNSZone, PrivateDNSZoneExists, 5*time.Minute)
}

func sweepVNetPeerings(ctx context.Context, c *client.DCAPIClient) error {
	vnets, err := runVNets(ctx, c)
	if err != nil {
		return err
	}
	var targets []target
	for _, v := range vnets {
		peerings, err := c.ListVNetPeerings(ctx, TenantID(), ProjectID(), v.ID)
		if err != nil {
			return err
		}
		for _, p := range peerings {
			targets = append(targets, target{v.Name + "/" + p.Name, []string{TenantID(), ProjectID(), v.ID, p.ID}})
		}
	}
	return deleteAll(ctx, c, "VNet peering", targets, rawDeleteVNetPeering, VNetPeeringExists, 5*time.Minute)
}

func sweepSubnets(ctx context.Context, c *client.DCAPIClient) error {
	vnets, err := runVNets(ctx, c)
	if err != nil {
		return err
	}
	var targets []target
	for _, v := range vnets {
		subnets, err := c.ListSubnets(ctx, TenantID(), ProjectID(), v.ID)
		if err != nil {
			return err
		}
		for _, s := range subnets {
			targets = append(targets, target{v.Name + "/" + s.Name, []string{TenantID(), ProjectID(), v.ID, s.ID}})
		}
	}
	return deleteAll(ctx, c, "subnet", targets, rawDeleteSubnet, SubnetExists, 15*time.Minute)
}

func sweepVNets(ctx context.Context, c *client.DCAPIClient) error {
	vnets, err := runVNets(ctx, c)
	if err != nil {
		return err
	}
	var targets []target
	for _, v := range vnets {
		targets = append(targets, target{v.Name, []string{TenantID(), ProjectID(), v.ID}})
	}
	return deleteAll(ctx, c, "VNet", targets, rawDeleteVNet, VNetExists, 5*time.Minute)
}

// sweepServiceAccounts needs an owner token. It can only ever match acc-<run>- names, so the
// runner SAs (tf-acc-runner, tf-acc-owner) are never touched.
func sweepServiceAccounts(ctx context.Context, c *client.DCAPIClient) error {
	sas, err := c.ListServiceAccounts(ctx, TenantID(), ProjectID())
	if err != nil {
		return err
	}
	var targets []target
	for _, sa := range sas {
		if shouldSweep(sa.Name) {
			targets = append(targets, target{sa.Name, []string{TenantID(), ProjectID(), sa.ID}})
		}
	}
	return deleteAll(ctx, c, "service account", targets, DeleteServiceAccount, nil, 0)
}

// Raw deletes: send the DELETE only. deleteAll does the waiting after every delete is sent.

func rawDeleteNodePool(ctx context.Context, c *client.DCAPIClient, p []string) error {
	return c.DeleteNodePool(ctx, p[0], p[1], p[2], p[3])
}

func rawDeleteCluster(ctx context.Context, c *client.DCAPIClient, p []string) error {
	return c.DeleteCluster(ctx, p[0], p[1], p[2])
}

func rawDeleteVM(ctx context.Context, c *client.DCAPIClient, p []string) error {
	return c.DeleteVM(ctx, p[0], p[1], p[2])
}

func rawDeleteBastion(ctx context.Context, c *client.DCAPIClient, p []string) error {
	return c.DeleteBastion(ctx, p[0], p[1], p[2])
}

func rawDeletePrivateDNSZone(ctx context.Context, c *client.DCAPIClient, p []string) error {
	return c.DeletePrivateDnsZone(ctx, p[0], p[1], p[2], p[3])
}

func rawDeleteVNetPeering(ctx context.Context, c *client.DCAPIClient, p []string) error {
	return c.DeleteVNetPeering(ctx, p[0], p[1], p[2], p[3])
}

func rawDeleteSubnet(ctx context.Context, c *client.DCAPIClient, p []string) error {
	return c.DeleteSubnet(ctx, p[0], p[1], p[2], p[3])
}

func rawDeleteVNet(ctx context.Context, c *client.DCAPIClient, p []string) error {
	return c.DeleteVNet(ctx, p[0], p[1], p[2])
}
