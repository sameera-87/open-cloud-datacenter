package acctest

import (
	"fmt"
)

// CIDR plan (docs/testsuite/04 §2). Each resource owns one range; tests inside a resource run
// one after another and reuse it, while resources run in parallel and never overlap.
// A new resource gets a new range here, so a clash shows up in this one file.
const (
	CIDRSubnetVNet           = "10.200.0.0/16"
	CIDRSubnetBasic          = "10.200.10.0/24" // basic and ForceNew start
	CIDRSubnetForceNewTarget = "10.200.11.0/24"
	CIDRSubnetGateway        = "10.200.12.0/24"

	CIDRNSGAttachmentVNet   = "10.201.0.0/16"
	CIDRNSGAttachmentSubnet = "10.201.1.0/24"

	CIDRRouteTableVNet = "10.202.0.0/16"

	CIDRRouteTableAssociationVNet   = "10.203.0.0/16"
	CIDRRouteTableAssociationSubnet = "10.203.1.0/24"

	CIDRVNetPeeringA       = "10.204.0.0/16"
	CIDRVNetPeeringB       = "10.220.0.0/16"
	CIDRVNetPeeringOverlap = "10.204.128.0/17" // overlaps side A on purpose

	CIDRPrivateDNSZoneVNet = "10.205.0.0/16"

	CIDRDNSRecordVNet = "10.206.0.0/16"

	CIDRPrivateEndpointVNet   = "10.207.0.0/16"
	CIDRPrivateEndpointSubnet = "10.207.1.0/24"

	CIDRVirtualMachineVNet   = "10.208.0.0/16"
	CIDRVirtualMachineSubnet = "10.208.1.0/24"

	CIDRBastionVNet   = "10.209.0.0/16"
	CIDRBastionSubnet = "10.209.1.0/28"

	CIDRVNetBasic          = "10.210.0.0/16"
	CIDRVNetForceNewTarget = "10.211.0.0/16"
	CIDRVNetMultiA         = "10.212.0.0/16"
	CIDRVNetMultiB         = "10.213.0.0/16"

	CIDRClusterVNet   = "10.215.0.0/16"
	CIDRClusterSubnet = "10.215.1.0/24"

	CIDRServiceAccountVNet = "10.216.0.0/16" // only for the tokenWorks fallback read
)

// ConfigBase returns the locals every test config starts with, so tenant and project IDs
// and the region are never hard-coded in a test.
func ConfigBase() string {
	return fmt.Sprintf(`
locals {
  tenant_id  = %q
  project_id = %q
  region     = %q
}
`, TenantID(), ProjectID(), Region())
}

// ConfigNetwork returns a dcapi_vnet "parent" and, if subnetCIDR != "", a dcapi_subnet "parent".
// Tests refer to dcapi_vnet.parent.vnet_uuid and dcapi_subnet.parent.subnet_uuid, and the
// framework destroys both with everything else in the test.
func ConfigNetwork(name, vnetCIDR, subnetCIDR string) string {
	cfg := fmt.Sprintf(`
resource "dcapi_vnet" "parent" {
  tenant_id     = local.tenant_id
  project_id    = local.project_id
  name          = "%s-vnet"
  address_space = [%q]
  region        = local.region
  description   = "acceptance test; safe to delete"
}
`, name, vnetCIDR)

	if subnetCIDR != "" {
		cfg += fmt.Sprintf(`
resource "dcapi_subnet" "parent" {
  tenant_id  = local.tenant_id
  project_id = local.project_id
  vnet_id    = dcapi_vnet.parent.vnet_uuid
  name       = "%s-sn"
  cidr       = %q
}
`, name, subnetCIDR)
	}
	return cfg
}

// ConfigKeyVault returns a dcapi_key_vault "parent" and local.kv_uuid, its UUID taken from the
// state ID the same way users do today (G7: the resource exposes no UUID attribute).
func ConfigKeyVault(name string) string {
	return fmt.Sprintf(`
resource "dcapi_key_vault" "parent" {
  tenant_id        = local.tenant_id
  project_id       = local.project_id
  name             = "%s-kv"
  soft_delete_days = 7
}

locals {
  kv_uuid = element(split("/", dcapi_key_vault.parent.id), 2)
}
`, name)
}
