# DC-API Terraform Provider — Test Suite Plan

This folder is the implementation plan for the end-to-end test suite of
`terraform-provider-dcapi`. It replaces the manual `terraform init/plan/apply/destroy`
cycle over [examples/](../../examples/) with acceptance tests written in Go
(`terraform-plugin-testing`). Argo Workflows runs them against a real DC-API.

> Supersedes the resource-provisioning parts of [docs/argo-test-suite-plan.md](../argo-test-suite-plan.md).
> That draft assumed the suite creates tenants and projects. It can't: Terraform
> authenticates with a **project-scoped service account**, so the tenant and project
> are pre-provisioned and every test runs *inside* them.

## The design in four sentences

1. **Go tests contain the test logic.** Each resource has `TestAcc*` tests that create, check,
   update, import and destroy it against the real DC-API.
2. **One Argo WorkflowTemplate per resource.** It runs that resource's Go tests in one pod, and
   you can submit it on its own to test just that resource.
3. **One master WorkflowTemplate runs all of them in a single run.** Every resource workflow
   starts at the same time. A failure in one doesn't stop the others. The run takes as long as
   the slowest resource (cluster + node pool, about 1.5 hours).
4. **Every test creates and destroys everything it needs.** No test depends on another test or
   on shared infrastructure. A cleanup step at the end of the run deletes anything a crashed
   test left behind.

```
argo submit --from workflowtemplate/dcapi-acc-suite -p image=<test image>
        │
        ▼
  dcapi-acc-suite ──┬─► dcapi-acc-vnet            ─► pod: go tests ^TestAccVNet_
  (master)          ├─► dcapi-acc-subnet          ─► pod: go tests ^TestAccSubnet_
                    ├─► …  (one per resource, all in parallel)
                    └─► dcapi-acc-cluster         ─► pod: go tests ^TestAccCluster_
        │
        └─ on exit: sweep (delete anything left over from this run)
```

The suite runs when someone starts it. There are no schedules and no test tiers.

## Reading order

| # | Document | What it answers |
|---|---|---|
| 00 | [Test suite at a glance](00-overview.md) | The whole suite in diagrams: architecture, templates, test flow, timeline, credentials, cleanup. |
| 01 | [Scope and strategy](01-scope-and-strategy.md) | What is tested, what is not, and why. The coverage matrix. |
| 02 | [Authentication and test environment](02-authentication-and-test-environment.md) | The service accounts, secrets, quota and environment variables. |
| 03 | [Acceptance test framework](03-acceptance-test-framework.md) | Go package layout, helpers, the standard test anatomy, running locally. |
| 04 | [Test isolation](04-test-isolation.md) | Naming, the CIDR plan, and why tests share nothing. |
| 05 | [Argo Workflows](05-argo-workflows.md) | The image, the per-resource templates, the master template, running and reading results. |
| 06 | [Cleanup](06-cleanup.md) | How resources are deleted, even when a test crashes. |
| 07 | [Provider gaps found during planning](07-provider-gaps-found.md) | Bugs and missing features found while reading the code that the suite will surface. |
| 08 | [Rollout plan](08-rollout-plan.md) | The build order in three phases. |
| — | [Per-resource plans](resources/README.md) | One document per resource, plus the data sources. |

## Resources in scope

| Group | Resources |
|---|---|
| Networking | [vnet](resources/vnet.md), [subnet](resources/subnet.md), [network_security_group](resources/network-security-group.md), [nsg_attachment](resources/nsg-attachment.md), [route_table](resources/route-table.md), [route_table_association](resources/route-table-association.md), [vnet_peering](resources/vnet-peering.md) |
| DNS | [private_dns_zone](resources/private-dns-zone.md), [dns_record](resources/dns-record.md) |
| Secrets | [key_vault](resources/key-vault.md), [key_vault_secret](resources/key-vault-secret.md), [private_endpoint](resources/private-endpoint.md) |
| Compute | [virtual_machine](resources/virtual-machine.md), [bastion](resources/bastion.md), [cluster](resources/cluster.md), [node_pool](resources/node-pool.md) |
| Identity | [service_account](resources/service-account.md) |
| Read-only | [data sources](resources/data-sources.md) (all 12) |

Out of scope: `dcapi_tenant`, `dcapi_project` and `dcapi_tenant_member`. See [01 §2](01-scope-and-strategy.md#2-out-of-scope-and-why).
