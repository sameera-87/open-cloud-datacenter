# 08 — Rollout Plan

Three phases. Each ends in something that runs and is useful on its own.

> **Status (2026-09-29):** the Go side of Phases 1 and 3 is written: helpers, sweepers, the client
> `List*` functions and tests for every resource and data source in [internal/acctest](../../internal/acctest),
> plus `make testacc` / `make sweep` and [test/acc/env.example](../../test/acc/env.example). All 61
> tests pass against an in-memory fake of DC-API. They have **not yet run against a real DC-API**,
> so the next step is Phase 0 followed by `make testacc`. Running them found G13–G15
> ([07](07-provider-gaps-found.md)); G10, G13 and G14 are fixed. The Argo manifests and the image
> (Phase 2) are not written yet.

## Phase 0 — Environment (prerequisite, mostly not code)

| Deliverable | Detail |
|---|---|
| Test project | `tf-acc` in a non-production tenant, with quota ≥ 20 vCPU / 80 GB / 500 GB ([02 §2](02-authentication-and-test-environment.md#2-pre-provisioned-environment-one-time-done-by-a-human)) |
| Service accounts | `tf-acc-runner` (member), `tf-acc-owner` (owner). Tokens stored in the cluster's secret manager |
| Argo namespace | `dcapi-tf-acc`, the runner ServiceAccount, and egress to DC-API confirmed from a pod (`curl $DCAPI_ENDPOINT/v1/regions` with the runner token) |
| Inputs confirmed | Region slug, VM image (and its display name), cluster image and `k8s_version` that are valid in this environment |

**Done when:** a throwaway pod in the namespace, using the member Secret, can list VNets in `tf-acc`.

## Phase 1 — Framework and networking (local)

| Deliverable | Detail |
|---|---|
| Dependencies | `terraform-plugin-testing`, `terraform-plugin-go` ([03 §2](03-acceptance-test-framework.md#2-dependencies-to-add)) |
| `internal/acctest` helpers | Provider factory, PreCheck, `RandomName`, API client, `ConfigBase`, `ConfigNetwork`, CIDR constants, CheckDestroy, Disappears ([03 §4](03-acceptance-test-framework.md#4-helpers-internalacctest)) |
| Provider fix | Subnet delete timeout 10m → 15m ([G10](07-provider-gaps-found.md#g10--subnet-delete-timeout-is-shorter-than-the-documented-last-subnet-teardown-medium-fixed)). Without it, last-subnet deletes fail across many tests ([04 §3](04-test-isolation.md#3-the-last-subnet-delete)) |
| Tests | Data sources, vnet, subnet, network_security_group, nsg_attachment, route_table, route_table_association |
| Sweepers | For those resources ([06 §2](06-cleanup.md#2-the-sweepers-in-go)) |
| Make targets | `testacc`, `sweep`, the `.env.acc` convention |

**Done when:** each of those resources passes locally with `make testacc`, `go test ./...`
without `TF_ACC` still runs no acceptance tests, and interrupting a test mid-apply (Ctrl-C)
followed by `make sweep RUN_ID=…` leaves no `acc-` objects in the project.

## Phase 2 — Argo

| Deliverable | Detail |
|---|---|
| Image | `test/acc/Dockerfile` and `make acc-image` ([05 §1](05-argo-workflows.md#1-the-test-image)) |
| Resource templates | For the Phase 1 resources ([05 §3](05-argo-workflows.md#3-a-per-resource-template)) |
| Master | `dcapi-acc-suite` with those tasks and the sweep step ([05 §4](05-argo-workflows.md#4-the-master-dcapi-acc-suite)) |

**Done when:**
- a full `dcapi-acc-suite` run completes green
- `argo submit --from workflowtemplate/dcapi-acc-nsg` works on its own
- deleting a test pod mid-run still ends with no `acc-<run>-` objects after the sweep step

## Phase 3 — The remaining resources

Add each resource's tests, sweeper, template and master task, in this order:

| Group | Resources | Also needed |
|---|---|---|
| DNS and peering | vnet_peering, private_dns_zone, dns_record | — |
| Secrets and identity | key_vault, key_vault_secret, private_endpoint, service_account | `ListServiceAccounts`, `ListPrivateEndpoints` + unit tests ([06 §2.3](06-cleanup.md#23-client-functions-the-sweepers-need)) |
| Compute | virtual_machine, bastion, cluster (with the node_pool chain), the node-pool template | `ListVMs`, `ListBastions`, `ListClusters`, `ListNodePools` + unit tests |

**Done when:** a full run covers every resource and data source, passes, and project quota usage
returns to where it started. At that point the manual `examples/` apply/destroy process can be
retired.

## Order inside each phase

Go down the dependency tree (vnet before subnet before nsg_attachment). Every new test then
builds on helpers that are already proven, and a failure points at the new code rather than
the foundation.

## Later, if needed

These are deliberately left out to keep the suite simple. Each can be added without changing
the design:
- A schedule (an Argo `CronWorkflow` that submits `dcapi-acc-suite`).
- A trigger from pull requests.
- Chat notifications or merged JUnit reports.
- A `terraform validate` check over `examples/*` ([G8](07-provider-gaps-found.md#g8--examplesnode_poolmaintf-is-invalid-low)).
