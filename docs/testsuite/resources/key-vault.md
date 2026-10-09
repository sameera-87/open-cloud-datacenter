# `dcapi_key_vault`

| | |
|---|---|
| Go file | `internal/acctest/key_vault_test.go`, prefix `TestAccKeyVault_` |
| Argo template | `dcapi-acc-key-vault` |
| Credentials | member |
| Creates | Its own vaults only |
| Go timeout | 40m |
| Estimated duration | 10–20 min |

## Facts from the code ([key_vault.go](../../../internal/resources/key_vault.go))

| Property | Value | Test consequence |
|---|---|---|
| Update | `credentials_rotation` only. Changing it calls `POST /credentials/rotate` | Rotation test: `secret_id` changes, `role_id` stays |
| ForceNew | `name`, `soft_delete_days` (default 30; API range 7–90), `tenant_id`, `project_id` | Replacement on `soft_delete_days` |
| Computed | `status`, `message`, `mount_path`, `endpoint_address`, `endpoint_port`, `created_at`, `updated_at`, `role_id`, `secret_id` (**sensitive, shown once**) | |
| Credentials lifecycle | Create polls to `ACTIVE`, then calls `GET /credentials` **exactly once**. Read never re-fetches, because a second call returns 410 | A refresh step proves `secret_id` survives Read |
| Async | Create only (5m). Delete is synchronous | |
| Importer | **Yes** (passthrough) | Import step in the basic test with `ImportStateVerifyIgnore: ["secret_id", "role_id", "credentials_rotation"]`. The one-time credentials can never be recovered, so an imported vault needs `credentials_rotation` set to mint new ones |
| No UUID attribute | Children use `split("/", id)[2]` ([G7](../07-provider-gaps-found.md#g7--dcapi_key_vault-exposes-no-vault-uuid-low)) | The data source's `kv_uuid` is compared against that expression |
| Data source | `dcapi_key_vault` by `name` → `kv_uuid` | |

## Test cases

| Test | Steps | Proves |
|---|---|---|
| `TestAccKeyVault_basic` | 1. Create, `soft_delete_days = 7`. 2. `RefreshState: true` step. 3. Data source. 4. Import (ignore the credentials). 5. Disappears. | Create polling, one-time credentials stored, **refresh doesn't wipe `secret_id`**, data source `kv_uuid` equals the ID segment, import parity for everything except credentials, 404 handling |
| `TestAccKeyVault_rotation` | 1. `credentials_rotation = "r1"`. 2. `"r2"` → `Update`, same ID; `secret_id` **differs** (`compare.ValuesDiffer`), `role_id` **same** (`compare.ValuesSame`). 3. Remove `credentials_rotation` from the config → assert the **decided** behaviour ([G12](../07-provider-gaps-found.md#g12--removing-credentials_rotation-rotates-the-credentials-low-decision-needed)). | The rotation trigger works, and removing the trigger does what the team decided |
| `TestAccKeyVault_forceNew` | 1. `soft_delete_days = 7`. 2. `8` → `DestroyBeforeCreate` | ForceNew, and a **new** vault with the same name can be created while the old one is soft-deleted. If DC-API keeps the name reserved, this step fails, and that is useful to know (documented as an API constraint) |

## Config sketch

```hcl
resource "dcapi_key_vault" "test" {
  tenant_id            = local.tenant_id
  project_id           = local.project_id
  name                 = "<name>"
  soft_delete_days     = 7
  credentials_rotation = "r1"
}

data "dcapi_key_vault" "test" {
  tenant_id  = local.tenant_id
  project_id = local.project_id
  name       = dcapi_key_vault.test.name
}
```

## Checks

- State: `status = ACTIVE`; `mount_path`, `endpoint_address`, `endpoint_port`, `role_id`, `secret_id` all set.
- After the refresh step: `secret_id` is still set. This is the main regression guard for the
  "credentials are shown once" logic.
- Data source: `kv_uuid` equals `element(split("/", dcapi_key_vault.test.id), 2)`. Asserted with
  an `output` in the config and `TestCheckOutput`.
- Step 3 of rotation: today `"r2"` → null counts as `HasChange`, so `resourceKeyVaultUpdate`
  **rotates again**. The step asserts whichever behaviour G12 settles on: `secret_id` differs
  (rotation on removal is intended), or stays the same (removal is a no-op).

## Import verification

The import ID is the state ID, `tenant_id/project_id/keyvault_id`. With only `ResourceName` set, the framework imports
using the ID from the previous step's state, so no `ImportStateIdFunc` is needed:

```go
{
	ResourceName:      "dcapi_key_vault.test",
	ImportState:       true,
	ImportStateVerify: true,
	ImportStateVerifyIgnore: []string{"secret_id", "role_id", "credentials_rotation"},
},
```

`ImportStateVerify` compares every attribute of the imported state with the state from the
create step. Any field Read doesn't set shows up as a mismatch. `role_id` and `secret_id` come from the one-time `GET /credentials` call and can never be fetched again, and `credentials_rotation` exists only in config. After a real import, the user sets `credentials_rotation` to mint new credentials.

## Destroy verification

- `CheckDestroy`: `GetKeyVault` is nil. A soft-deleted vault counts as deleted if the API returns 404 for it.
- `Disappears`: `DeleteKeyVault`.

## Risks targeted

- Read overwriting `secret_id` with `""`. It uses `d.Get("secret_id")` to preserve it, and the refresh step pins that behaviour.
- Rotation firing on unrelated diffs, or on removal of the trigger.
- Soft-delete name reservation blocking same-name replacement.
