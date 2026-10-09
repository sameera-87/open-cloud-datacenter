# DC-API Terraform Provider: Quick Start

This guide takes you from zero to a running virtual machine on the Open Cloud Datacenter platform using the `dcapi` Terraform provider. It takes about 10 minutes.

You will create:

```text
project ── vnet ── subnet ── virtual machine
```

## Prerequisites

- **Terraform** 1.5 or later
- **Go** 1.25 or later (to build the provider from source)
- **A DC-API endpoint URL**, for example `https://dc-api.example.com`
- **A service account token** in the form `dcapi_sa_<id>_<secret>`. Ask a tenant admin to create one for you, or create one with `dcapi_service_account`.
- **An existing tenant.** Creating tenants (`dcapi_tenant`) needs platform-admin rights, so most users start from a tenant that already exists.

## 1. Build and install the provider

The provider isn't published to the Terraform Registry yet. Build it and install it into your local plugin directory:

```bash
git clone <repo-url> open-cloud-datacenter
cd open-cloud-datacenter
make install
```

`make install` builds `terraform-provider-dcapi_v0.1.0` and copies it to:

```text
~/.terraform.d/plugins/registry.terraform.io/wso2/dcapi/0.1.0/<os>_<arch>/
```

Terraform picks it up from there automatically.

## 2. Set your credentials

The provider reads its settings from environment variables, so you don't need to put secrets in `.tf` files:

```bash
export DCAPI_ENDPOINT="https://dc-api.example.com"
export DCAPI_TOKEN="dcapi_sa_<id>_<secret>"
```

You can also set them in the provider block:

```hcl
provider "dcapi" {
  endpoint = "https://dc-api.example.com"
  token    = var.dcapi_token   # don't hard-code the token
}
```

| Argument | Env var | Description |
|---|---|---|
| `endpoint` | `DCAPI_ENDPOINT` | DC-API base URL |
| `token` | `DCAPI_TOKEN` | Service account token (sensitive) |

## 3. Write the configuration

Create a new directory with a `main.tf` file. Replace `my-tenant` with your tenant slug, and `lk` with a region that exists on your platform.

```hcl
terraform {
  required_providers {
    dcapi = {
      source  = "registry.terraform.io/wso2/dcapi"
      version = "~> 0.1.0"
    }
  }
}

provider "dcapi" {}

# Look up an OS image by its display name
data "dcapi_image" "ubuntu" {
  tenant_id    = "my-tenant"
  display_name = "Ubuntu 22.04"
}

# Project: a namespace with CPU, memory and storage quotas
resource "dcapi_project" "demo" {
  tenant_id  = "my-tenant"
  project_id = "quickstart"
  name       = "Quick start"

  cpu_cores  = 8
  memory_gb  = 32
  storage_gb = 200
}

# VNet: a private network (Kube-OVN VPC)
resource "dcapi_vnet" "demo" {
  tenant_id  = dcapi_project.demo.tenant_id
  project_id = dcapi_project.demo.project_id

  name          = "demo-vnet"
  address_space = ["10.10.0.0/16"]
  region        = "lk"
}

# Subnet: must fall inside the VNet's address space
resource "dcapi_subnet" "demo" {
  tenant_id  = dcapi_vnet.demo.tenant_id
  project_id = dcapi_vnet.demo.project_id
  vnet_id    = dcapi_vnet.demo.vnet_uuid

  name = "demo-subnet"
  cidr = "10.10.1.0/24"
}

# Virtual machine attached to the subnet
resource "dcapi_virtual_machine" "demo" {
  tenant_id  = dcapi_subnet.demo.tenant_id
  project_id = dcapi_subnet.demo.project_id

  name       = "demo-vm"
  size       = "small"   # small | medium | large | xlarge
  image_name = data.dcapi_image.ubuntu.image_id

  vnet_id   = dcapi_subnet.demo.vnet_id
  subnet_id = dcapi_subnet.demo.subnet_uuid
}

output "vm_ip" {
  value = dcapi_virtual_machine.demo.ip_address
}

output "vm_private_key" {
  value     = dcapi_virtual_machine.demo.private_key
  sensitive = true
}
```

When you pass an ID to a child resource, use `vnet_uuid` and `subnet_uuid`, not `id`. The `id` attribute is Terraform's internal state ID, which is a composite path.

## 4. Apply

```bash
terraform init
terraform plan
terraform apply
```

The provider waits until each resource is `ACTIVE` before moving on to the next. VNets and subnets usually take seconds. A VM can take a few minutes while its disk is cloned from the image; the default create timeout is 15 minutes.

## 5. Connect to the VM

The SSH private key is returned only once, when the VM is created. Save it straight away:

```bash
terraform output -raw vm_private_key > demo-vm.pem
chmod 600 demo-vm.pem
ssh -i demo-vm.pem ubuntu@$(terraform output -raw vm_ip)
```

The login user depends on the image. Ubuntu cloud images use `ubuntu`.

The VM is on a private subnet. If you can't reach it directly, add a `dcapi_bastion` in the same subnet and connect through that (see [examples/bastion](../examples/bastion)).

## 6. Clean up

```bash
terraform destroy
```

Terraform deletes the resources in reverse order: VM, then subnet, VNet and project.

## VM sizes

| Size | vCPU | Memory |
|---|---|---|
| `small` | 2 | 8 GB |
| `medium` | 4 | 16 GB |
| `large` | 8 | 32 GB |
| `xlarge` | 16 | 64 GB |

## Importing existing resources

These resources support `terraform import`. The import ID is the same slash-separated path the provider stores as the state ID:

| Resource | Import ID |
|---|---|
| `dcapi_service_account` | `<tenant>/<project>/<sa-id>` |
| `dcapi_network_security_group` | `<tenant>/<project>/<nsg-id>` |
| `dcapi_nsg_attachment` | `<tenant>/<project>/<nsg-id>/<attachment-id>` |
| `dcapi_route_table` | `<tenant>/<project>/<vnet-uuid>/<route-table-id>` |
| `dcapi_route_table_association` | `<tenant>/<project>/<vnet-uuid>/<route-table-id>/<association-id>` |
| `dcapi_node_pool` | `<tenant>/<project>/<cluster-id>/<pool-name>` |

For example:

```bash
terraform import dcapi_route_table.main my-tenant/quickstart/<vnet-uuid>/<route-table-id>
```

The other resources, including projects, VNets, subnets and VMs, can't be imported yet.

## Troubleshooting

| Symptom | Likely cause |
|---|---|
| `Missing required provider configuration: endpoint` or `token` | `DCAPI_ENDPOINT` or `DCAPI_TOKEN` isn't exported in the shell running Terraform |
| `Failed to query available provider packages` on `terraform init` | The provider isn't installed locally. Run `make install` again and check the version in `required_providers` is `~> 0.1.0` |
| 401 / 403 errors | The token is wrong or expired, or the service account lacks permission in that tenant or project |
| Subnet create fails with a CIDR error | The subnet `cidr` isn't inside the VNet's `address_space`, or it overlaps another subnet |
| A resource ends in `FAILED` | Check its `message` attribute (`terraform state show <resource>`) for the reason from the platform |
| `private_key` is empty | It's only returned on create. If you lost it, recreate the VM with `terraform apply -replace=dcapi_virtual_machine.demo` |

## Next steps

- [examples/](../examples): one working example per resource, including clusters, node pools, key vaults, NSGs, route tables, peering and private DNS
- [terraform-resource-implementation.md](terraform-resource-implementation.md): every resource and data source, and what each creates on the platform
