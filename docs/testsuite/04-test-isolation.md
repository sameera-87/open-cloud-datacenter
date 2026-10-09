# 04 — Test Isolation

Every test creates everything it needs and destroys it at the end. No test uses an object that
another test, or a separate setup step, created. This page covers the three things that make
that work: names, address ranges, and a few rules for test authors.

## 1. Names

Every object a test creates is named with `acctest.RandomName(<short>)`
([03 §4.2](03-acceptance-test-framework.md#42-names--namesgo)):

```
acc-<run>-<short>-<rand5>        e.g. acc-k3f9q-nsg-x7a2m
```

| Part | Why |
|---|---|
| `acc-` | Marks the object as created by the suite. The cleanup step only ever deletes `acc-` objects |
| `<run>` | The run ID (last 5 characters of the Argo workflow name). The cleanup step deletes exactly this run's leftovers and nothing from other runs |
| `<short>` | Resource abbreviation, for people reading DC-API listings |
| `<rand5>` | Unique across tests and retries. NSG names are unique across the whole tenant, and key vault and secret names can stay reserved after a soft delete |

## 2. CIDR plan

Each resource gets its own address range. Tests inside one resource run one after another
([03 §5](03-acceptance-test-framework.md#5-the-standard-test-anatomy)), so they can reuse their
resource's range. Resources run in parallel, so their ranges never overlap. That avoids any
question of whether DC-API allows overlapping VNets in one project, and peering needs
non-overlapping VNets anyway.

| Resource | VNet range | Subnets |
|---|---|---|
| subnet | `10.200.0.0/16` | `10.200.10.0/24` (basic and ForceNew start), `10.200.11.0/24` (ForceNew target), `10.200.12.0/24` (explicit gateway) |
| nsg_attachment | `10.201.0.0/16` | `10.201.1.0/24` |
| route_table | `10.202.0.0/16` | none |
| route_table_association | `10.203.0.0/16` | `10.203.1.0/24` |
| vnet_peering | `10.204.0.0/16` (side A), `10.220.0.0/16` (side B) | none. The overlap test's peer is `10.204.128.0/17`, which overlaps side A on purpose |
| private_dns_zone | `10.205.0.0/16` | none |
| dns_record | `10.206.0.0/16` | none |
| private_endpoint | `10.207.0.0/16` | `10.207.1.0/24` |
| virtual_machine | `10.208.0.0/16` | `10.208.1.0/24` |
| bastion | `10.209.0.0/16` | `10.209.1.0/28` |
| vnet | `10.210.0.0/16` (basic), `10.211.0.0/16` (ForceNew target), `10.212.0.0/16` + `10.213.0.0/16` (multi address space) | none |
| cluster + node_pool | `10.215.0.0/16` | `10.215.1.0/24` |
| service_account | `10.216.0.0/16`, only if the fallback read is needed ([resources/service-account.md](resources/service-account.md)) | none |

Put the table in `internal/acctest/config.go` as named constants (`CIDRSubnetVNet = "10.200.0.0/16"`, …).
A clash then shows up as a duplicate in one file rather than a `409` at run time.

The ranges are static, so **only one run may use the test project at a time**. The master and
every resource template take the same Argo mutex, so a second run waits for the first to finish
([05 §3.1](05-argo-workflows.md#31-example-dcapi-acc-nsg)).

## 3. The last-subnet delete

When a test deletes the only subnet in its VNet, DC-API also tears down that VNet's NAT gateway
and CoreDNS, which can take up to 15 minutes
([dc-api-reference.md §9](../dc-api-reference.md#9-subnet-delete-ordering)). Almost every test
with a subnet hits this path, because each test has its own VNet with one subnet.

The provider's default subnet delete timeout is 10 minutes ([G10](07-provider-gaps-found.md#g10--subnet-delete-timeout-is-shorter-than-the-documented-last-subnet-teardown-medium-fixed)).
Raise it to 15 minutes before running the suite ([08 Phase 1](08-rollout-plan.md#phase-1--framework-and-networking-local)),
or subnet deletes will fail at random across many resources. The subnet tests keep the default
timeout, so they will catch it if the default ever drops again.

## 4. Rules for test authors

- **Create your own parents.** Use `acctest.ConfigNetwork(...)` for a VNet and subnet, and declare
  your own key vault if you need one. Never look up an object you didn't create.
- **Use your resource's CIDR range** from the table above. A new resource gets a new row.
- **Name everything with `RandomName`.** Hard-coded names collide with soft-deleted objects and
  escape the cleanup step.
- **Let the framework destroy.** Don't delete objects at the end of a test by hand. The only
  out-of-band deletes are `Disappears` steps, and they target the resource under test.
