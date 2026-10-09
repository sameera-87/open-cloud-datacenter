# `dcapi_virtual_machine`

| | |
|---|---|
| Go file | `internal/acctest/virtual_machine_test.go`, prefix `TestAccVirtualMachine_` |
| Argo template | `dcapi-acc-virtual-machine` |
| Credentials | member |
| Creates | Its own VNet `10.208.0.0/16` and subnet |
| Go timeout | 90m |
| Estimated duration | 40–70 min |

## Facts from the code ([vm.go](../../../internal/resources/vm.go))

| Property | Value | Test consequence |
|---|---|---|
| Update | None. Every argument is ForceNew | Replacement test on `size` |
| Arguments | `name`, `size` (small\|medium\|large\|xlarge), `disk_gb` (optional), `image_name`, `network_name` **or** `vnet_id` + `subnet_id`, `tenant_id`, `project_id` | |
| Network validation | In **Create**, not at plan time ([G6](../07-provider-gaps-found.md#g6--network-mode-validation-happens-at-apply-time-not-plan-time-low)) | The negative test runs an apply, but no API call is made |
| Computed | `status`, `provider_type`, `ip_address`, `message`, `created_at`, `private_key` + `console_password` (**sensitive, shown once**) | |
| Read | Keeps `private_key` / `console_password` from state. Doesn't refresh `disk_gb`, `image_name` or the network fields ([G2](../07-provider-gaps-found.md#g2--read-cant-refresh-every-configured-field-medium)) | A refresh step asserts the secrets survive |
| Async | Create `PENDING → ACTIVE` (15m). Delete polls until 404 (10m) | |
| Importer | **None, deliberately** | `GET` doesn't return `image_name`, `disk_gb` or the network fields. An imported VM would plan a replacement ([G2](../07-provider-gaps-found.md#g2--read-cant-refresh-every-configured-field-medium)). Blocked on DC-API |
| State ID | `tenant_id/project_id/vm_id` | |

## Dependencies

Each test creates its own VNet `10.208.0.0/16` and subnet `10.208.1.0/24`
([04 §2](../04-test-isolation.md#2-cidr-plan)). Size `small` + `disk_gb = 20` keeps the quota
footprint at 2 vCPU / 8 GB / 20 GB, and `medium` during the resize test
([02 §5](../02-authentication-and-test-environment.md#5-quota-budget)).

## Test cases

| Test | Steps | Proves |
|---|---|---|
| `TestAccVirtualMachine_vpc` | 1. Create a VPC-mode VM. 2. `RefreshState: true`. 3. Disappears. | Create polling to `ACTIVE`, IP inside the subnet, one-time secrets stored **and preserved across refresh**, 404 handling |
| `TestAccVirtualMachine_invalidNetwork` | Apply configs that (a) set both `network_name` and `vnet_id`, (b) set neither, (c) set `vnet_id` without `subnet_id` → `ExpectError` on each message | The Create-time validation runs **before** any API call, so nothing is created. The configs use dummy network IDs and declare no VNet. Becomes `PlanOnly` once G6 is fixed |
| `TestAccVirtualMachine_validation` | Plan-only `size = "tiny"` → `ExpectError` | ValidateFunc |
| `TestAccVirtualMachine_forceNewSize` | 1. `small`. 2. `medium` → `DestroyBeforeCreate`, new ID, new secrets (`compare.ValuesDiffer` on `private_key`) | Replacement deletes the old VM (CheckDestroy on both IDs), and new one-time secrets are captured |
| `TestAccVirtualMachine_legacyNetwork` | 1. VM with `network_name = env DCAPI_ACC_LEGACY_NETWORK`. **Skipped** when the env var is unset | Legacy bridge mode still works where it's still offered |

## Config sketch

```hcl
resource "dcapi_virtual_machine" "test" {
  tenant_id  = local.tenant_id
  project_id = local.project_id
  name       = "<name>"
  size       = "small"
  disk_gb    = 20
  image_name = "<DCAPI_ACC_VM_IMAGE>"     # injected, default rancher-infra/ubuntu-22-04
  vnet_id    = dcapi_vnet.parent.vnet_uuid        # from acctest.ConfigNetwork
  subnet_id  = dcapi_subnet.parent.subnet_uuid
}
```

## Checks

- State: `status = ACTIVE`; `ip_address` matches `^10\.208\.1\.\d+$`; `private_key` matches
  `BEGIN .*PRIVATE KEY`; `console_password` is set; `provider_type` is set.
- After the refresh step: `private_key` and `console_password` are still set and unchanged
  (`compare.ValuesSame`).
- API: `GetVM` returns `ACTIVE` and the same IP.

## Destroy verification

- `CheckDestroy`: `GetVM` is nil for every VM ID seen (both IDs in the replacement test).
- `Disappears`: `DeleteVM`, then poll until 404 (10m), then `ExpectNonEmptyPlan`.

## Risks targeted

- Losing the one-time secrets on refresh. Read deliberately re-sets them from state, and this pins it.
- Create waiter mishandling `FAILED`. It must surface `message`. Hard to trigger on purpose; watch for it in failure output.
- Quota leakage from half-created VMs. `quota_exceeded` in the next run is the symptom, and the sweep step is the remedy.
