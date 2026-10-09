# Terraform Provider: Resource Implementation Reference

This page lists every resource and data source in the `dcapi` Terraform provider. For each one, it shows what dc-api actually creates on the platform (Harvester, Rancher and Kube-OVN).

It is based on the `controlplane` branch, mainly:

- the dc-api handlers in `dc-api/internal/api/handlers/`
- the backend drivers in `dc-api/internal/providers/` (`kubeovn`, `harvester`, `rancher`, `kvi`, `endpoints`)

Every resource is also stored as a row in the dc-api Postgres database. The tables below list only what gets created on top of that row.

For more detail, with sequence diagrams and code line references, see:

- [resource-provisioning-reference.md](resource-provisioning-reference.md)
- [vm-cluster-creation-flow.md](vm-cluster-creation-flow.md)

## Resources

| Terraform resource | Implementation on the platform | Kubernetes objects / CRDs | Scope / namespace | Provisioning |
|---|---|---|---|---|
| `dcapi_tenant` | A tenant is a Postgres record plus a tenant namespace that holds tenant-wide objects | `v1/Namespace` `dc-tenant-<slug>` | Cluster | Sync; namespace creation is best-effort and doesn't roll back the tenant |
| `dcapi_project` | A project is a namespace with a ResourceQuota that enforces the project's CPU, memory, storage and volume limits. All project resources (VMs, NADs, DBs, vaults) live in it | `v1/Namespace` `dc-<tenant>-<project>` + `v1/ResourceQuota` | Cluster | Returns immediately; namespace is created in the background (2 min timeout, best-effort) |
| `dcapi_tenant_member` | A role assignment (RBAC grant) to a user. Postgres only; nothing is created in Kubernetes | — | — | Sync |
| `dcapi_service_account` | A machine identity: dc-api generates a `dcapi_sa_<id>_<secret>` token and stores only its bcrypt hash. Postgres only | — | Project | Sync; the token is shown once |
| `dcapi_vnet` | A Kube-OVN VPC, which is an OVN logical router. The CIDR (address space) is kept and checked only by dc-api, because the Vpc CRD has no CIDR field | `kubeovn.io/v1 Vpc` (`spec.namespaces` = project ns) | Cluster-scoped | Async; a background job polls until ready |
| `dcapi_subnet` | A Kube-OVN Subnet attached to the VPC, plus a Multus NAD so VMs can get a NIC on it. The NAD is created first, and the Subnet only after Harvester marks the NAD ready | `k8s.cni.cncf.io/v1 NetworkAttachmentDefinition` + `kubeovn.io/v1 Subnet` (`spec.vpc`, `cidrBlock`, `provider=<nad>.<ns>.ovn`) | NAD in project ns; Subnet cluster-scoped | Async; a background job polls until ready |
| `dcapi_route_table` | No CRD of its own. Routes are patched into the parent VPC's `spec.staticRoutes` (server-side apply), each tagged `routetable-<uuid>` | Patches `Vpc.spec.staticRoutes` | Cluster-scoped (Vpc) | Sync |
| `dcapi_route_table_association` | Postgres only. Routes already apply to the whole VPC, so linking a table to a subnet changes nothing in Kubernetes | — | — | Sync |
| `dcapi_network_security_group` | Postgres only when created. The rules don't do anything until the NSG is attached | — | — | Sync |
| `dcapi_nsg_attachment` | Turns the NSG rules into OVN ACLs and patches them into the target subnet's `spec.acls` (server-side apply), each tagged `nsg-<uuid>/<rule>` | Patches `kubeovn.io/v1 Subnet.spec.acls` | Cluster-scoped (Subnet) | Sync |
| `dcapi_vnet_peering` | No peering CRD. Patches both VPCs: adds `spec.vpcPeerings` and matching `spec.staticRoutes` entries over a transit /24 | Patches 2× `Vpc.spec.vpcPeerings` + `Vpc.spec.staticRoutes` | Cluster-scoped | Runs in the background but finishes almost at once |
| `dcapi_private_dns_zone` | A ConfigMap holding the zone's records. The `VpcDns` CRD code exists but is skipped, because it has no per-record API | `v1/ConfigMap` | Project ns | Runs in the background but finishes almost at once |
| `dcapi_dns_record` | One key added to or removed from the zone's ConfigMap | Patches the zone `ConfigMap` | Project ns | Sync |
| `dcapi_private_endpoint` | An nginx stream-proxy pod with two NICs: eth0 on the cluster network and net1 on the tenant subnet NAD, with a pinned IP. It also adds an A-record `<name>.<class>.dc.internal` to the VPC's CoreDNS | `v1/ConfigMap` (nginx config) + `apps/v1 Deployment`; patches `vpc-dns-corefile-<vpc>` ConfigMap | `dc-api-endpoints` + `kube-system` | Async; ready when the proxy pod is Ready |
| `dcapi_virtual_machine` | A KubeVirt VM whose disk is cloned from a Harvester image (`dataVolumeTemplates`). Cloud-init injects the SSH key and password. Its NIC is on the subnet NAD | `kubevirt.io/v1 VirtualMachine` (+ DataVolume/PVC created by Harvester) | Project ns | Async; a reconciler checks every 60 s until Active or Failed |
| `dcapi_bastion` | Same as a VM, but with two NICs (management network + tenant subnet) so it can serve as a jump host | `kubevirt.io/v1 VirtualMachine` (dual-NIC) | Project ns | Async; the same 60 s reconciler |
| `dcapi_cluster` | An RKE2 cluster provisioned by Rancher on Harvester. dc-api creates the Harvester cloud-provider credentials (SA, RoleBinding, token Secret), a `HarvesterConfig` per pool, and the Rancher `Cluster` CR with all `machinePools` and Cilium CNI. Rancher then creates the node VMs | `provisioning.cattle.io/v1 Cluster` + `rke-machine-config.cattle.io HarvesterConfig` + `Secret harvesterconfig-<cluster>` + `ServiceAccount`/`RoleBinding`/`Secret`; node VMs are `kubevirt.io/v1 VirtualMachine` | `fleet-default` (Rancher) + project ns (node VMs) | Async; the same 60 s reconciler |
| `dcapi_node_pool` | Adds, scales or removes an entry in the Rancher Cluster's `spec.rkeConfig.machinePools[]`. Adding a pool creates a new `HarvesterConfig` first | `HarvesterConfig` + patches `Cluster.spec.rkeConfig.machinePools` | `fleet-default` | Async |
| `dcapi_key_vault` | Two levels of keyvault-operator CRDs: one `KeyVaultBackend` per tenant (created automatically if missing) and one `KeyVaultInstance` per vault. The operator then deploys an OpenBao StatefulSet with AppRole auth | `keyvault.opencloud.wso2.com/v1alpha1 KeyVaultBackend` (`kvb-<tenant>`) + `KeyVaultInstance` (`kv-<uuid>`) | Tenant ns + project ns | Status is read straight from the CR on every GET; the operator does the rest |
| `dcapi_key_vault_secret` | Nothing is created in Kubernetes. dc-api forwards the read or write to the KV-v2 engine on the OpenBao leader pod, using a scoped token from a Secret the operator created | — (data in OpenBao) | — | Sync |

## Data sources

Data sources only read existing objects. They never create, update or delete anything.

| Data source | Reads from |
|---|---|
| `dcapi_tenant`, `dcapi_project`, `dcapi_vnet`, `dcapi_subnet`, `dcapi_vnet_peering`, `dcapi_route_table`, `dcapi_network_security_group`, `dcapi_key_vault`, `dcapi_private_dns_zone`, `dcapi_dns_record` | dc-api Postgres records (with live status from the backing CRD where one exists) |
| `dcapi_region` | dc-api region/zone registry (Postgres) |
| `dcapi_image` | Harvester `harvesterhci.io/v1beta1 VirtualMachineImage` |

## Namespace conventions

| Scope | Namespace | Holds |
|---|---|---|
| Tenant | `dc-tenant-<tenantSlug>` | `KeyVaultBackend` |
| Project | `dc-<tenant>-<project>` | Subnet NADs, VMs, bastions, cluster node VMs, DNS zone ConfigMaps, `KeyVaultInstance` |
| System | `dc-api-endpoints` | Private endpoint proxy Deployments |
| System | `kube-system` | Per-VPC CoreDNS Corefile ConfigMaps |
| Rancher | `fleet-default` | Rancher `Cluster` and `HarvesterConfig` CRs |

## Known issue

`dcapi_tenant_member` calls `/v1/tenants/{id}/members` (see `internal/client/tenant_member.go`). That route was not found in the `controlplane` branch router. On `controlplane`, tenant membership goes through `/v1/tenants/{tenant_id}/role-assignments`. Run this resource's acceptance test against a `controlplane` build to confirm whether it works.
