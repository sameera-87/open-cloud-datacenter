terraform {
  required_providers {
    dcapi = {
      source  = "registry.terraform.io/wso2/dcapi"
      version = "~> 0.1.0"
    }
  }

  # State contains the AppRole secret_id in plaintext — never use the local
  # backend for this config. Encryption (below) protects data at rest but
  # does not provide per-deployment isolation or access control on its own;
  # each deployment MUST supply its own bucket (with a restrictive bucket
  # policy) and a unique key, e.g. via:
  #   terraform init \
  #     -backend-config="bucket=<your-tfstate-bucket>" \
  #     -backend-config="key=key_vault/<deployment>/terraform.tfstate" \
  #     -backend-config="region=<your-region>"
  backend "s3" {
    encrypt = true
  }
}

provider "dcapi" {}

resource "dcapi_project" "example" {
  tenant_id  = "tenant-s87"
  project_id = "kv-project-s87"

  name        = "Infrastructure Team"
  description = "Core infrastructure resources: VNets, clusters, and shared VMs."
}

resource "dcapi_key_vault" "prod_secrets" {
  tenant_id  = dcapi_project.example.tenant_id
  project_id = dcapi_project.example.project_id

  name             = "prod-secrets-s87"
  soft_delete_days = 30 # 7-90; immutable — changing it replaces the vault

  # Bump this value (e.g. to a new date) to rotate the AppRole secret_id in
  # place, without destroying and recreating the Key Vault.
  credentials_rotation = "initial"
}

output "kv_id" {
  # dcapi_key_vault.id is the composite state id ("tenant_id/project_id/kv_id").
  value       = element(split("/", dcapi_key_vault.prod_secrets.id), 2)
  description = "UUID of the Key Vault — pass this as key_vault_id to dcapi_key_vault_secret."
}

output "kv_status" {
  value       = dcapi_key_vault.prod_secrets.status
  description = "Provisioning status — ACTIVE after a successful apply."
}

output "kv_mount_path" {
  value       = dcapi_key_vault.prod_secrets.mount_path
  description = "OpenBao mount path for this vault."
}

output "kv_endpoint" {
  value       = "${dcapi_key_vault.prod_secrets.endpoint_address}:${dcapi_key_vault.prod_secrets.endpoint_port}"
  description = "In-cluster OpenBao address and port."
}

output "kv_role_id" {
  value       = dcapi_key_vault.prod_secrets.role_id
  description = "Stable AppRole role_id for authenticating against this vault."
}

# secret_id is shown once: it is fetched at create time, stored only in state and
# never returned by later API reads. To get a new one, change credentials_rotation.
output "kv_secret_id" {
  value       = dcapi_key_vault.prod_secrets.secret_id
  sensitive   = true
  description = "AppRole secret_id. Retrieve with: terraform output -raw kv_secret_id"
}
