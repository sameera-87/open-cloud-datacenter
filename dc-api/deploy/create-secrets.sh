#!/usr/bin/env bash
# Creates DC-API secrets on the cluster interactively.
# Run once manually — secrets are never committed or deployed by CI.
#
# Usage:
#   ./create-secrets.sh --context dcapi-controlplane-rke2

set -euo pipefail

CONTEXT=${1:-dcapi-controlplane-rke2}
if [[ "$1" == "--context" ]]; then CONTEXT=$2; fi

echo "Creating DC-API secrets on context: $CONTEXT"
echo ""

# Postgres password (used to build the DSN)
read -rsp "Postgres password for dc_api user: " PG_PASS
echo ""
DB_URL="postgres://dc_api:${PG_PASS}@dc-postgres.dc-system:5432/dc_api?sslmode=disable"

# Asgardeo OIDC audience (client ID)
read -rp "Asgardeo OIDC client ID (DCAPI_OIDC_AUDIENCE): " OIDC_AUDIENCE

# Harvester kubeconfig path
read -rp "Path to Harvester kubeconfig [~/.kube/harvester-dev.yaml]: " HARV_PATH
HARV_PATH=${HARV_PATH:-~/.kube/harvester-dev.yaml}
HARV_PATH="${HARV_PATH/#\~/$HOME}"
if [[ ! -f "$HARV_PATH" ]]; then
  echo "Error: file not found: $HARV_PATH" >&2
  exit 1
fi

# Rancher token
read -rsp "Rancher API token (token-xxxxx:yyyyyyy): " RANCHER_TOKEN
echo ""

# Rancher Harvester cloud credential ID
echo ""
echo "Rancher cloud credential ID for Harvester:"
echo "  Rancher UI → Cluster Management → Cloud Credentials → Harvester → copy ID"
echo "  Format: cattle-global-data:cc-xxxxx"
read -rp "Rancher Harvester credential ID: " RANCHER_HARVESTER_CREDENTIAL

# Operator break-glass access (optional — press Enter to skip)
read -rp "Operator SSH public key (optional, press Enter to skip): " OPERATOR_SSH_KEY
read -rsp "Operator console password (optional, press Enter to skip): " OPERATOR_PASSWORD
echo ""

# GitHub Container Registry pull credentials — needed because the dc-api image
# at ghcr.io/<owner>/dc-api is private. Skipped automatically if you switch the
# Deployment to a public image.
echo ""
read -rp "GitHub username for GHCR (default: hiranadikari): " GHCR_USER
GHCR_USER=${GHCR_USER:-hiranadikari}
read -rsp "GHCR Personal Access Token (read:packages scope): " GHCR_PAT
echo ""

# GitHub PAT for the ARC runner scale set. Read by the listener pod to
# register/deregister ephemeral runners against the GitHub repo. Stored as a
# k8s secret named "github-runner-pat" in the arc-runners namespace; the
# arc-runner-values.yaml references it by name (githubConfigSecret).
read -rsp "GitHub PAT for ARC runner registration (repo scope, classic PAT): " RUNNER_PAT
echo ""

# Postgres secret (separate from DC-API secrets, used by the StatefulSet)
kubectl --context "$CONTEXT" create secret generic dc-postgres-secret \
  --from-literal=password="$PG_PASS" \
  -n dc-system \
  --dry-run=client -o yaml | kubectl --context "$CONTEXT" apply -f -

# DC-API secrets
kubectl --context "$CONTEXT" create secret generic dc-api-secrets \
  --from-literal=DCAPI_DB_URL="$DB_URL" \
  --from-literal=DCAPI_OIDC_AUDIENCE="$OIDC_AUDIENCE" \
  --from-file=DCAPI_HARVESTER_KUBECONFIG="$HARV_PATH" \
  --from-literal=DCAPI_RANCHER_TOKEN="$RANCHER_TOKEN" \
  --from-literal=DCAPI_RANCHER_HARVESTER_CREDENTIAL="$RANCHER_HARVESTER_CREDENTIAL" \
  --from-literal=DCAPI_OPERATOR_SSH_KEY="$OPERATOR_SSH_KEY" \
  --from-literal=DCAPI_OPERATOR_PASSWORD="$OPERATOR_PASSWORD" \
  -n dc-system \
  --dry-run=client -o yaml | kubectl --context "$CONTEXT" apply -f -

# GHCR pull secret (referenced as imagePullSecrets in deployment.yaml)
kubectl --context "$CONTEXT" create secret docker-registry ghcr-pull-secret \
  --docker-server=ghcr.io \
  --docker-username="$GHCR_USER" \
  --docker-password="$GHCR_PAT" \
  -n dc-system \
  --dry-run=client -o yaml | kubectl --context "$CONTEXT" apply -f -

# GitHub runner PAT secret in arc-runners. Referenced by name from
# arc-runner-values.yaml's githubConfigSecret field. Namespace is created
# here in case the ARC controller hasn't been installed yet.
kubectl --context "$CONTEXT" create namespace arc-runners \
  --dry-run=client -o yaml | kubectl --context "$CONTEXT" apply -f -
kubectl --context "$CONTEXT" create secret generic github-runner-pat \
  --from-literal=github_token="$RUNNER_PAT" \
  -n arc-runners \
  --dry-run=client -o yaml | kubectl --context "$CONTEXT" apply -f -

echo ""
echo "Secrets applied:"
echo "  dc-system/dc-postgres-secret"
echo "  dc-system/dc-api-secrets"
echo "  dc-system/ghcr-pull-secret"
echo "  arc-runners/github-runner-pat"
