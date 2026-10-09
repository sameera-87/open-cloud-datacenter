# `dcapi_key_vault_secret`

| | |
|---|---|
| Go file | `internal/acctest/key_vault_secret_test.go`, prefix `TestAccKeyVaultSecret_` |
| Argo template | `dcapi-acc-key-vault-secret` |
| Credentials | member |
| Creates | Its own key vault |
| Go timeout | 40m |
| Estimated duration | 15–25 min |

## Facts from the code ([key_vault_secret.go](../../../internal/resources/key_vault_secret.go))

| Property | Value | Test consequence |
|---|---|---|
| Update | `value` (sensitive) and `metadata` (map), through `PUT …/secrets/{key}` | Update step. `version` increments |
| ForceNew | `key_vault_id`, `key` (must match `^[a-z0-9._-]{1,256}$`), `tenant_id`, `project_id` | Replacement on `key`; plan-only validation on `key` |
| Computed | `version`, `created_at` | |
| Read | Returns `value`, so the state value is refreshed from the API | Out-of-band value changes are detectable |
| Delete | **Soft-delete**. `GET` afterwards returns 410, which the client treats as "gone" | CheckDestroy treats 404 **or** 410 as deleted |
| Feature flag | Secret routes return **501** if the platform's KVI provisioner is disabled | If the first secret call returns 501, the test calls `t.Skip` with a clear reason instead of failing |
| Importer | **Yes** (passthrough) | Import step in the basic test. `value` is verified too, since Read returns it from the API |
| State ID | `tenant_id/project_id/key_vault_id/key` | |

## Dependencies

Each test creates its own key vault (`soft_delete_days = 7`). Every test also uses a **random key**
(`acc-<run>-<rand>`, lowercase, so it matches the key pattern), so keys can't collide with
soft-deleted keys from earlier runs.

## Test cases

| Test | Steps | Proves |
|---|---|---|
| `TestAccKeyVaultSecret_basic` | 1. Create with value + metadata `{env = "acc"}`. 2. Import. 3. Disappears. | Round-trip of value and metadata, `version = 1`, import restores the value from the API, and 410 after delete is treated as drift (ID cleared), not as an error |
| `TestAccKeyVaultSecret_update` | 1. value v1. 2. value v2 → `Update`, same ID, `version = 2`. 3. metadata `{env = "acc", owner = "tf"}` → `Update`. 4. Remove metadata. | In-place updates, version bumping, metadata removal |
| `TestAccKeyVaultSecret_forceNew` | 1. key K1. 2. key K2 → `DestroyBeforeCreate` | ForceNew on `key`. K1 ends soft-deleted (410) |
| `TestAccKeyVaultSecret_recreateAfterDelete` | 1. Create K. 2. Config without the secret (destroy). 3. Create K again with the same key. | What users will do. If DC-API refuses to write a soft-deleted key, step 3 fails. That becomes a documented provider gap (the resource would need a restore or purge path) |
| `TestAccKeyVaultSecret_validation` | Plan-only: `key = "Bad Key!"` → `ExpectError` | The key-pattern ValidateFunc |

## Config sketch

```hcl
resource "dcapi_key_vault" "parent" {
  tenant_id        = local.tenant_id
  project_id       = local.project_id
  name             = "<name>-kv"
  soft_delete_days = 7
}

resource "dcapi_key_vault_secret" "test" {
  tenant_id    = local.tenant_id
  project_id   = local.project_id
  key_vault_id = element(split("/", dcapi_key_vault.parent.id), 2)   # G7
  key          = "<name>"
  value        = "acc-value-1"
  metadata     = { env = "acc" }
}
```

## Checks

- State: `version = 1`; `value = acc-value-1`. `TestCheckResourceAttr` reads sensitive values
  from state fine, and they're never printed by the framework. Also `metadata.env = acc`.
- API: `GetKeyVaultSecret` returns the same value and version.
- Update: `version` goes 1 → 2, asserted with state and API.

## Import verification

The import ID is the state ID, `tenant_id/project_id/key_vault_id/key`. With only `ResourceName` set, the framework imports
using the ID from the previous step's state, so no `ImportStateIdFunc` is needed:

```go
{
	ResourceName:      "dcapi_key_vault_secret.test",
	ImportState:       true,
	ImportStateVerify: true,
},
```

`ImportStateVerify` compares every attribute of the imported state with the state from the
create step. Any field Read doesn't set shows up as a mismatch. Read fetches `value` from the API, so the sensitive value is verified too. Nothing needs to be ignored.

## Destroy verification

- `CheckDestroy`: `GetKeyVaultSecret` returns `(nil, nil)` for both 404 and 410. Both mean
  "not live".
- `Disappears`: `DeleteKeyVaultSecret`. The next Read gets 410 and clears the ID.

## Risks targeted

- 410 handling in Read (soft-deleted). If treated as an error, every plan after an out-of-band delete would fail.
- Recreating a soft-deleted key (see the test above).
- Metadata removal sending `{}` vs omitting the field. The API may keep the old metadata if the field is omitted.
- The secret value leaking into logs. `value` is `Sensitive`; step output is reviewed once during rollout.
