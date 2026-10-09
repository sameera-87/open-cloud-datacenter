# `dcapi_service_account`

| | |
|---|---|
| Go file | `internal/acctest/service_account_test.go`, prefix `TestAccServiceAccount_` |
| Argo template | `dcapi-acc-service-account` |
| Credentials | **owner** (`dcapi-acc-owner`). The only resource step that uses it |
| Creates | Nothing else (see `tokenWorks` below) |
| Go timeout | 30m |
| Estimated duration | 3–8 min |

## Facts from the code ([service_account.go](../../../internal/resources/service_account.go))

| Property | Value | Test consequence |
|---|---|---|
| Update | None. `name`, `role` (owner\|member\|viewer), `description` are all ForceNew | Replacement on `role` |
| Permission | Create and delete need an **owner** SA ([dc-api-reference](../../dc-api-reference.md#resource-serviceaccount)) | This step mounts the owner Secret |
| Computed | `sa_id`, `created_at`, `last_used`, `token` (**sensitive, shown once**) | |
| Read | Keeps `token` from state (the API never returns it again). Sets `tenant_id` from the response (G5) | |
| Importer | **Yes** (passthrough) | Import with `ImportStateVerifyIgnore: ["token", "last_used"]`. The token can't be recovered, and `last_used` changes whenever the token is used |
| State ID | `tenant_id/project_id/sa_id` | |

## Why this test is special

It is the only test where the resource under test is a **credential**. So the plan also checks
that the credential **works**, not just that the object exists:

1. The new SA's token configures a **second, aliased provider** in the same config.
2. That provider reads something through a data source. A read succeeds only if DC-API accepts
   the new token.
3. A later refresh shows `last_used` populated, which is DC-API confirming the token was used.

This is possible because Terraform allows a provider block to use a managed resource's
attribute once that value is known. In step 2 the SA already exists from step 1, so the token is
known at plan time.

## Test cases

| Test | Steps | Proves |
|---|---|---|
| `TestAccServiceAccount_basic` | 1. Create a `viewer` SA. 2. Import (ignore `token`, `last_used`). 3. Disappears. | Create, one-time token captured (matches `^dcapi_sa_`), import, 404 handling, no `tenant_id` diff (G5) |
| `TestAccServiceAccount_tokenWorks` | 1. Create a `viewer` SA. 2. Add `provider "dcapi" { alias = "new_sa" token = … }` and `data "dcapi_project" "self" { provider = dcapi.new_sa … }`. 3. `RefreshState: true`. | The minted token authenticates and a `viewer` can read. `last_used` gets set. The token survives refresh unchanged |
| `TestAccServiceAccount_forceNewRole` | 1. `viewer`. 2. `member` → `DestroyBeforeCreate`, new `sa_id`, **new token** (`compare.ValuesDiffer`) | ForceNew. The old SA is deleted, and so is its token |
| `TestAccServiceAccount_viewerCannotWrite` | 1. `viewer` SA. 2. Aliased provider tries to create a `dcapi_network_security_group` → `ExpectError` matching `HTTP 403` | DC-API enforces the role, and the provider shows 403 clearly. Nothing is created, so there's nothing to clean up. The sweep step covers it in case DC-API wrongly allowed it |
| `TestAccServiceAccount_validation` | Plan-only `role = "admin"` → `ExpectError` | ValidateFunc |

If the project-scoped `viewer` role can't read `dcapi_project` (403), `tokenWorks` instead creates
a VNet `10.216.0.0/16` with the default (owner) provider and reads it back through the new SA
with `data "dcapi_vnet"`. Which read works is found on the first real run and then fixed in the
test code.

## Config sketch (tokenWorks, step 2)

```hcl
resource "dcapi_service_account" "test" {
  tenant_id   = local.tenant_id
  project_id  = local.project_id
  name        = "<name>"
  role        = "viewer"
  description = "acc: safe to delete"
}

provider "dcapi" {
  alias = "new_sa"
  token = dcapi_service_account.test.token  # endpoint still comes from DCAPI_ENDPOINT
}

data "dcapi_project" "self" {
  provider   = dcapi.new_sa
  tenant_id  = local.tenant_id
  project_id = local.project_id
}
```

## Checks

- State: `token` matches `^dcapi_sa_[A-Za-z0-9]+_.+`; `role = viewer`; `sa_id` set.
- Step 2: `data.dcapi_project.self.project_id = local.project_id`.
- Step 3: `last_used` is set (`TestCheckResourceAttrSet`).
- API (owner client): `GetServiceAccount` returns the same role and name.

## Destroy verification

- `CheckDestroy`: `GetServiceAccount` is nil for every `sa_id` seen.
- `Disappears`: `DeleteServiceAccount`.

## Safety

- Every created SA is named `acc-<run>-…`. The runner SAs are `tf-acc-runner` and `tf-acc-owner`,
  so the sweep step can never match them ([06 §2](../06-cleanup.md#2-the-sweepers-in-go)).
- Tests only create `viewer` and `member` SAs, **never `owner`**. A leaked owner token from a
  failed test would be the most dangerous leak the suite could cause.
- The minted tokens live only in the framework's temporary state inside the pod, which is
  deleted with the pod.

## Risks targeted

- Token lost on refresh (Read must keep it).
- G5 (`tenant_id` from the response body).
- A replaced SA's old token still working. Covered indirectly: the old SA must 404, so its token has no principal.
