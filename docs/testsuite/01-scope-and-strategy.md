# 01 — Scope and Strategy

## 1. In scope

### 1.1 Resources

Every resource a **project-scoped service account** can create and delete:

| # | Resource | Fields updatable in place | Import | Create waits for `ACTIVE` | Delete waits for 404 | SA role |
|---|---|---|---|---|---|---|
| 1 | `dcapi_vnet` | — | Yes | Yes, 5m | Yes, 5m | member |
| 2 | `dcapi_subnet` | — | Yes | Yes, 5m | Yes, 10m | member |
| 3 | `dcapi_network_security_group` | `rules` (full replace) | Yes | No | No | member |
| 4 | `dcapi_nsg_attachment` | — | Yes | No | No | member |
| 5 | `dcapi_route_table` | `routes` (full replace) | Yes | No | No | member |
| 6 | `dcapi_route_table_association` | — | Yes | No | No | member |
| 7 | `dcapi_vnet_peering` | — | Yes | Yes, 5m | Yes, 5m | member |
| 8 | `dcapi_private_dns_zone` | — | Yes | Yes, 5m | Yes, 5m | member |
| 9 | `dcapi_dns_record` | `values`, `ttl` | Yes | No | No | member |
| 10 | `dcapi_key_vault` | `credentials_rotation` (rotates `secret_id`) | Yes | Yes, 5m | No | member |
| 11 | `dcapi_key_vault_secret` | `value`, `metadata` | Yes | No | No | member |
| 12 | `dcapi_private_endpoint` | — | Yes | No ([G11](07-provider-gaps-found.md#g11--private_endpoint-has-a-status-but-no-waiter-medium-verify)) | No | member |
| 13 | `dcapi_virtual_machine` | — | No ([G2](07-provider-gaps-found.md#g2--read-cant-refresh-every-configured-field-medium)) | Yes, 15m | Yes, 10m | member |
| 14 | `dcapi_bastion` | — | Yes | Yes, 15m | Yes, 10m | member |
| 15 | `dcapi_cluster` | — | No ([G2](07-provider-gaps-found.md#g2--read-cant-refresh-every-configured-field-medium)) | Yes, 30m | Yes, 20m | member |
| 16 | `dcapi_node_pool` | `node_count`, `taints`, `labels` | Yes | Yes (`ready`), 15m | Yes, 10m | member |
| 17 | `dcapi_service_account` | — | Yes | No | No | **owner** |

How to read the columns:
- **Fields updatable in place:** "—" means there is no `UpdateContext`. Every argument is
  ForceNew, so any change replaces the resource.
- **Import:** whether the resource has an `Importer`.
- **Create waits / Delete waits:** whether the provider polls until the resource is
  `ACTIVE` (node pool: `ready`) or gone (HTTP 404), with the default timeout. "No" means the
  call returns as soon as DC-API responds. Node pool updates also wait for `ready` (15m).
- **SA role:** the minimum service-account role the test step needs
  ([02 §2.2](02-authentication-and-test-environment.md#22-why-two-service-accounts-instead-of-one-owner)).

All four behaviour columns come straight from [internal/resources](../../internal/resources).
They decide which test steps each resource gets (see
[03 §5](03-acceptance-test-framework.md#5-the-standard-test-anatomy)).

### 1.2 Data sources

These are read-only, and all tested with the member SA:

| Data source | Kind | Tested |
|---|---|---|
| `dcapi_vnet`, `dcapi_subnet`, `dcapi_network_security_group`, `dcapi_route_table`, `dcapi_vnet_peering`, `dcapi_private_dns_zone`, `dcapi_dns_record`, `dcapi_key_vault` | Read an object the suite creates | Inside the matching resource's basic test |
| `dcapi_region`, `dcapi_image` | Read platform objects that already exist | Standalone, in `dcapi-acc-data-sources` |
| `dcapi_project`, `dcapi_tenant` | Read the SA's own scope | Standalone, in `dcapi-acc-data-sources`; skipped with a reason if the SA gets 403 |

Details: [resources/data-sources.md](resources/data-sources.md).

## 2. Out of scope, and why

| Resource | Reason |
|---|---|
| `dcapi_tenant` | Tenant create and update are admin-only (`is_admin` claim). A service account can't do it, whatever its role. |
| `dcapi_project` | The service account lives *inside* a project. A project-scoped credential can't create or delete its own parent, and deleting the project would delete the SA running the suite. |
| `dcapi_tenant_member` | Tenant-scoped (`/v1/tenants/{id}/members`). A project-scoped SA has no tenant-level authority. |

These three keep their unit tests (`httptest`, e.g.
[project_test.go](../../internal/resources/project_test.go)). If an admin-credential suite is ever
needed, it can reuse the same framework with a separate secret. It is deliberately not part of
this plan.

## 3. Test layers and where this suite fits

```
┌──────────────────────────────────────────────────────────────────────┐
│ Layer 3 — Acceptance / E2E  (THIS PLAN)                              │
│   terraform-plugin-testing + real Terraform CLI + real DC-API        │
│   one Argo run on demand: the whole suite, or one resource           │
├──────────────────────────────────────────────────────────────────────┤
│ Layer 2 — Provider unit tests with a fake API                        │
│   schema.TestResourceDataRaw + httptest (project_test.go pattern)    │
│   every PR, seconds, no credentials                                  │
├──────────────────────────────────────────────────────────────────────┤
│ Layer 1 — Client unit tests                                          │
│   internal/client/*_test.go against httptest                         │
│   every PR                                                           │
└──────────────────────────────────────────────────────────────────────┘
```

The acceptance suite catches the bugs the lower layers can't:
- **Perpetual diffs.** The API returns a value in a different shape or order from the config.
- **Wrong ForceNew and update wiring.** A field changes but nothing happens, or the resource gets replaced when it should update in place.
- **Broken async polling.** Polling gives up too early, targets the wrong status string, or ignores `FAILED`.
- **Destroy that doesn't clean up,** and the resource still exists in DC-API.
- **Import that loses fields.**
- **Real DC-API behaviour changes,** such as new validation or renamed fields.

## 4. Design principles

### 4.1 Go tests assert; Argo runs them
The test logic lives in Go, not in Argo YAML. `terraform-plugin-testing` already gives us:
- the empty-plan check after every apply
- import verification
- `CheckDestroy`, which runs even when a step fails
- plan checks that assert "update, not replace"

Rebuilding those as shell scripts in Argo would be more work and less reliable. Argo's only
job is to start one pod per resource, run them in parallel, and clean up at the end.

It also means the same test runs identically on a laptop (`make testacc`) and in Argo.

### 4.2 Every test is self-contained
Each test creates the parents it needs (VNet, subnet, key vault), tests its own resource, and
destroys everything. No test reads anything another test created, and there is no shared
infrastructure. So:
- a failure in `subnet` can't show up as failures in `vm`, `bastion` or `private_endpoint`
- any resource can be run on its own, in any order
- the per-resource Argo templates need no inputs from each other, and the master needs no
  dependency graph

The cost is that several tests create their own VNet and subnet, which adds a few minutes to
each of them. They run in parallel, so it barely changes the length of the whole run.

**One deliberate chain:** `cluster` → `node_pool` runs as a single multi-step test. A node pool
can't exist without a cluster, and a cluster takes up to 30 minutes to create, so two clusters
per run isn't worth it. See [resources/cluster.md](resources/cluster.md).

### 4.3 Resources in parallel, tests inside a resource in sequence
The master starts every resource workflow at once. Inside one resource workflow, its tests run
one after another. That keeps the CIDR plan simple (one address range per resource, see
[04](04-test-isolation.md)) and keeps the number of concurrent VMs and clusters predictable.

### 4.4 Cleanup can't depend on the test passing
The framework destroys every test's resources, even after a failed step. If a pod is killed
before that happens, the master's exit step sweeps this run's leftovers. See [06](06-cleanup.md).

### 4.5 Least privilege
Almost every test runs with a `member` SA. Only the `service_account` tests and the sweep step
(which deletes leaked SAs) use an `owner` SA. This also proves the API reference's claim that
`member` is enough for resource management. See [02](02-authentication-and-test-environment.md).

## 5. One run, everything in it

There is one kind of run: all resources, all tests. There are no schedules and no smoke, core
or extended tiers. Someone starts the run (see [05 §6](05-argo-workflows.md#6-running-the-suite)),
for example before a release or after a provider change.

Plan-only validation tests (bad input that must be rejected at plan time) create nothing and
take seconds. They run in their resource's workflow like every other test.

## 6. Coverage matrix (what every resource gets)

✓ planned now · ✗ not possible until DC-API returns the missing fields ([07 G2](07-provider-gaps-found.md#g2--read-cant-refresh-every-configured-field-medium)) · — not applicable

| Resource | Create + attrs | Empty plan | Update in place | ForceNew replace | Import | Disappears | Data source | Validation (plan-only) |
|---|---|---|---|---|---|---|---|---|
| vnet | ✓ | ✓ | — | ✓ | ✓ | ✓ | ✓ | — |
| subnet | ✓ | ✓ | — | ✓ | ✓ | ✓ | ✓ | — |
| network_security_group | ✓ | ✓ | ✓ | ✓ | ✓ | ✓ | ✓ | ✓ |
| nsg_attachment | ✓ | ✓ | — | — | ✓ | ✓ | ✓ (via NSG `attachments`) | ✓ |
| route_table | ✓ | ✓ | ✓ | ✓ | ✓ | ✓ | ✓ | ✓ |
| route_table_association | ✓ | ✓ | — | — | ✓ | ✓ | — | — |
| vnet_peering | ✓ | ✓ | — | — | ✓ | ✓ | ✓ | — |
| private_dns_zone | ✓ | ✓ | — | — | ✓ | ✓ | ✓ | — |
| dns_record | ✓ | ✓ | ✓ | ✓ | ✓ | ✓ | ✓ | — |
| key_vault | ✓ | ✓ | ✓ (rotation) | — | ✓ | ✓ | ✓ | — |
| key_vault_secret | ✓ | ✓ | ✓ | ✓ | ✓ | ✓ | — | ✓ |
| private_endpoint | ✓ | ✓ | — | — | ✓ | ✓ | — | — |
| virtual_machine | ✓ | ✓ | — | ✓ | ✗ | ✓ | — | ✓ |
| bastion | ✓ | ✓ | — | ✓ | ✓ | ✓ | — | — |
| cluster | ✓ | ✓ | — | — | ✗ | — | — | — |
| node_pool | ✓ | ✓ | ✓ | — | ✓ | — | — | ✓ |
| service_account | ✓ | ✓ | — | ✓ | ✓ | ✓ | — | ✓ |

Explanation of the columns:
- **Create + attrs:** apply the config, then assert user-set and computed attributes (`status = ACTIVE`, IDs set, sensitive one-time fields non-empty).
- **Empty plan:** automatic in every `terraform-plugin-testing` step. After apply the framework
  re-plans, and any diff fails the test. This is the most valuable check in the suite.
- **Update in place:** change an updatable field. `plancheck.ExpectResourceAction(..., Update)`
  asserts it wasn't a replacement, and the resource ID stays the same.
- **ForceNew replace:** change an immutable field and assert `DestroyBeforeCreate`, a new ID, and
  that the old object is gone from DC-API.
- **Import:** `ImportState: true, ImportStateVerify: true`. Every resource except `virtual_machine` and `cluster`.
- **Disappears:** delete the object out-of-band through the Go client, then assert the next plan
  wants to recreate it. This proves Read handles a 404 by clearing the ID.
- **Data source:** read the created object back through its data source by name and compare
  attributes with `TestCheckResourceAttrPair`.
- **Validation:** configs that must fail at plan time (`PlanOnly` + `ExpectError`). They create
  no infrastructure and finish in seconds.
