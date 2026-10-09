# 02 — Authentication and Test Environment

## 1. How the provider authenticates

[provider.go](../../internal/provider/provider.go) accepts two settings:

| Setting | Env fallback | Notes |
|---|---|---|
| `endpoint` | `DCAPI_ENDPOINT` | DC-API base URL. |
| `token` | `DCAPI_TOKEN` | Service account token, format `dcapi_sa_<lookup_id>_<secret>`. Sent as `Authorization: Bearer <token>` by [client.go](../../internal/client/client.go). |

Service account tokens:
- **Never expire** unless the SA is deleted.
- Are **returned exactly once**, when the SA is created.
- Are **project-scoped**. The SA lives at `/v1/tenants/{t}/projects/{p}/service-accounts`.

That makes them the right credential for unattended CI. OIDC JWTs from Asgardeo are short-lived
and need a browser PKCE flow, so they're unsuitable.

The test configs never contain a `provider` block with credentials. Terraform reads `DCAPI_ENDPOINT` and
`DCAPI_TOKEN` from the pod environment. The one exception is the service-account test, which
configures an aliased provider with the token it just created (see
[resources/service-account.md](resources/service-account.md)).

## 2. Pre-provisioned environment (one-time, done by a human)

The SA can't create its own tenant or project, so these are created once, outside the suite,
by someone with tenant-owner (or admin) rights.

| Object | Suggested value | Why |
|---|---|---|
| Tenant | an existing non-production tenant, e.g. `platform-test` | The suite never touches tenant settings. |
| Project | `tf-acc` (dedicated) | Isolation: the cleanup step deletes `acc-` prefixed objects in this project, so it must never hold real workloads. |
| Project quota | ≥ 20 vCPU, 80 GB RAM, 500 GB storage | Covers the peak concurrent compute (see §5). |
| SA `tf-acc-runner` | role `member` | Used by every test except the service-account tests. |
| SA `tf-acc-owner` | role `owner` | Used only by the service-account tests and the cleanup step. Creating and deleting SAs requires `owner`. |

The runner SA names start with `tf-acc-`, not `acc-`. The cleanup step deletes only objects whose
names start with `acc-<run id>-` ([06](06-cleanup.md)), so it can never delete the credentials the
suite runs on.

### 2.1 Creating the service accounts

With the owner's own credential (UI, `curl`, or a one-off Terraform config outside this repo):

```bash
# Run by a human project owner. The token appears ONCE in the response — capture it immediately.
curl -sS -X POST "$DCAPI_ENDPOINT/v1/tenants/platform-test/projects/tf-acc/service-accounts" \
  -H "Authorization: Bearer $OWNER_TOKEN" -H "Content-Type: application/json" \
  -d '{"name":"tf-acc-runner","role":"member","description":"Terraform provider acceptance tests"}' \
  | jq -r .token
```

Repeat with `name: tf-acc-owner`, `role: owner`.

### 2.2 Why two service accounts instead of one `owner`

| Concern | One `owner` SA | `member` + `owner` |
|---|---|---|
| Blast radius if a token leaks | Can manage SAs and members of the project | Most jobs hold a token that can't mint new credentials |
| Proves `member` is sufficient | No | Yes. If any resource wrongly needs `owner`, its test fails with 403. |
| Complexity | Lower | One extra Secret, and templates pick the secret by parameter |

The extra cost is small and the second row is a real test result, so the plan uses two.

## 3. Secret storage in Kubernetes

Two Secrets in the Argo namespace `dcapi-tf-acc`:

```yaml
apiVersion: v1
kind: Secret
metadata:
  name: dcapi-acc-member
  namespace: dcapi-tf-acc
type: Opaque
stringData:
  DCAPI_ENDPOINT: https://dcapi.example.com
  DCAPI_TOKEN: dcapi_sa_<lookup>_<secret>      # tf-acc-runner
  DCAPI_ACC_TENANT_ID: platform-test
  DCAPI_ACC_PROJECT_ID: tf-acc
---
# dcapi-acc-owner: same keys, DCAPI_TOKEN = tf-acc-owner's token
```

Rules:
- **Never commit tokens.** The repo holds only `secrets.example.yaml` with placeholders. Real
  values come from whatever the cluster already uses (External Secrets Operator, Sealed Secrets,
  or Vault). Prefer that over `kubectl create secret` by hand.
- **Injected with `envFrom.secretRef`.** Tokens never appear on command lines or in Argo parameters.
  Parameters show up in the UI and in workflow archives; `envFrom` values don't.
- **Restrict RBAC.** Only the workflow's pod ServiceAccount (`dcapi-acc-runner`) may `get` these
  Secrets. Humans with `argo submit` rights don't need `get secrets`.
- **Rotation.** SA tokens don't expire, so rotate on a schedule (for example quarterly) or on
  suspected leak:
  1. Create `tf-acc-runner-2`.
  2. Update the Secret.
  3. Submit `dcapi-acc-data-sources` on its own to check the new token.
  4. Delete the old SA.

## 4. Environment-variable contract

Every test pod receives these. The Go helpers read them (see [03 §4](03-acceptance-test-framework.md#4-helpers-internalacctest)),
and `acctest.PreCheck(t)` fails fast with a clear message if a required one is missing.

| Variable | Required | Source | Meaning |
|---|---|---|---|
| `TF_ACC` | yes | template | `1`. Without it `terraform-plugin-testing` skips every acceptance test, which keeps `go test ./...` safe in normal CI. |
| `DCAPI_ENDPOINT` | yes | Secret | DC-API base URL |
| `DCAPI_TOKEN` | yes | Secret | The SA token for this step. `tf-acc-runner` (member) everywhere except the service-account step and the cleanup step, which mount the `dcapi-acc-owner` Secret instead. The provider and the out-of-band client both use it. |
| `DCAPI_ACC_TENANT_ID` | yes | Secret | Tenant slug used in every config's `tenant_id` |
| `DCAPI_ACC_PROJECT_ID` | yes | Secret | Project slug used in every config's `project_id` |
| `DCAPI_ACC_REGION` | yes | workflow param (default `lk`) | Region for VNets. `TestAccDataSource_region` checks that it exists. |
| `DCAPI_ACC_VM_IMAGE` | compute tests | workflow param (default `rancher-infra/ubuntu-22-04`) | `image_name` for VMs |
| `DCAPI_ACC_VM_IMAGE_DISPLAY_NAME` | data-source tests | workflow param | The same image's `display_name`, which is what `dcapi_image` looks up by |
| `DCAPI_ACC_CLUSTER_IMAGE` | cluster tests | workflow param (default `rancher-infra/rke2-ubuntu-22-04`) | `image_name` for clusters and node pools |
| `DCAPI_ACC_K8S_VERSION` | cluster tests | workflow param (e.g. `v1.33.10+rke2r3`) | `k8s_version` |
| `DCAPI_ACC_RUN_ID` | yes | template (last 5 characters of the workflow name) | Put into every resource name, so the cleanup step can find this run's leftovers |
| `DCAPI_ACC_LEGACY_NETWORK` | no | workflow param | If set, the legacy `network_name` VM test runs; otherwise it is skipped |
| `TF_ACC_TERRAFORM_PATH` | yes (in image) | image | Pinned Terraform CLI binary, so the framework never downloads one |
| `CHECKPOINT_DISABLE` | yes (in image) | image | `1`. No call to HashiCorp's version-check service from CI |

`tenant_id` and `project_id` are still required attributes on every resource. The SA is
project-scoped, but the provider builds URL paths from them. The helpers insert them into every
config, so test authors never hard-code them.

## 5. Quota budget

All resource workflows run at the same time, so the VM, bastion and cluster tests hold compute
together. Tests inside one resource run one after another, so each holds at most the following.
Worst-case concurrent footprint with the sizes chosen in the resource plans:

| Item | vCPU | RAM (GB) | Disk (GB) |
|---|---|---|---|
| VM, `medium` during the resize test (+ 20 GB disk) | 4 | 16 | 20 |
| Bastion (platform-sized; assume `small`) | 2 | 8 | 20 |
| Cluster system pool, 1 × `medium`, 40 GB | 4 | 16 | 40 |
| Node pool `np1` scaled to 2 × `small`, 40 GB | 4 | 16 | 80 |
| Node pool `np2`, 1 × `small`, 40 GB | 2 | 8 | 40 |
| **Peak** | **16** | **64** | **200** |

A 20 vCPU / 80 GB / 500 GB project quota covers the peak with room for one leaked VM from an
earlier run. If a test fails with `quota_exceeded`, check for leftovers first ([06 §3](06-cleanup.md#3-cleaning-up-by-hand)).

## 6. Network reachability

The Argo pods need HTTPS egress to `DCAPI_ENDPOINT`. If DC-API sits behind the VPN mentioned in
[dc-api-reference.md](../dc-api-reference.md#base-url--versioning), run the workflow in a cluster that
has that route, or add a NetworkPolicy/egress-gateway exception. If every resource
workflow fails within seconds with the same connection error, check this first.
