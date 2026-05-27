---
title: "M1.5 RBAC — Membership and Roles"
author: WSO2 IaaS Team
date: 2026-05-09
---

# M1.5 RBAC — Membership and Roles

**Related design doc:** [MILESTONES.md § M1.5](../MILESTONES.md#m15--full-rbac-before-m2-after-m1-e2e-test-passes)

## What This Is

DC-API has its own membership and role system. **Identity still comes from Asgardeo** (via OIDC JWT), but **access control lives inside DC-API**. This lets you:

- Assign different roles to different users within the same tenant (owner vs member vs viewer)
- Issue long-lived tokens to CI/CD systems without giving them Asgardeo credentials
- Audit who granted access to whom, and when
- Extend to finer-grained scopes in M5 (subscriptions, resource groups, individual resources) without a schema rewrite

The mental model: Asgardeo answers *"who are you?"*. DC-API answers *"what can you do?"*.

## The Model in One Diagram

```
    ┌─────────────────────────────────────┐
    │     Asgardeo                        │
    │  (OIDC JWT with groups)             │
    └────────────────┬────────────────────┘
                     │
                     ▼
         ┌───────────────────────┐
         │    DC-API Auth        │
         │  (JWT validation)     │
         └────────────┬──────────┘
                      │
          ┌───────────┴───────────┐
          ▼                       ▼
    ┌──────────────┐      ┌──────────────┐
    │  role_       │      │ is_admin     │
    │  assignments │      │ (dc-admin    │
    │  table       │      │ group check) │
    └────────┬─────┘      └──────┬───────┘
             │                   │
             └───────────┬───────┘
                         ▼
              ┌────────────────────┐
              │  Access decision:  │
              │  allowed / 403     │
              └────────────────────┘
```

When a user logs in via `dcctl login`, Asgardeo issues a JWT. DC-API validates the signature and extracts the user's email and groups. Then:

1. **If the user is in the `dc-admin` group** → they're a platform admin, can do anything
2. **If the user is NOT a member of any tenant in DC-API** → depends on `DCAPI_RBAC_AUTOPROVISION`:
   - `true` (dev default): auto-grant `member` role if the JWT contains a `dc-tenant-<name>` group
   - `false` (prod recommended): reject with 403; they must be invited explicitly
3. **If the user IS a member** → look up their role(s) and enforce per-operation

This design is **forward-compatible with M5**: when the hierarchy lands, the table schema stays the same, just with more scope types.

## Roles

| Role | Can create | Can read | Can update | Can delete | Can invite/remove members | Notes |
|---|---|---|---|---|---|---|
| `owner` | Yes | Yes | Yes | Yes | Yes | Full control within scope; can manage team membership |
| `member` | Yes | Yes | Yes | No | No | Day-to-day developer; can't delete or manage people |
| `viewer` | No | Yes | No | No | No | Read-only; auditors, CI dashboards, approval flows |
| `platform-admin` | Yes | Yes | Yes | Yes | Yes | Via Asgardeo `dc-admin` group; never stored in `role_assignments` |

**Effective role** is the most permissive role a principal holds across the scope chain. In M1.5 the chain is always one element (the tenant). In M5 it grows to resource → resource group → subscription → tenant, and you take the max permission across all four.

## Scopes

Today: `tenant` only. Example:

```sql
INSERT INTO role_assignments (principal_type, principal_id, scope_type, scope_id, role, granted_by)
VALUES ('user', 'user-123@acme.com', 'tenant', 'acme', 'member', 'admin@acme.com');
```

This user can now act on all resources in the `acme` tenant.

**In M5** these will be valid scope types:
- `subscription` — a team's quota boundary
- `resource_group` — logical grouping within a subscription (dev/staging/prod)
- `resource` — individual VM, cluster, volume (fine-grained)

**Your M1.5 membership rows are compatible with M5** — they stay exactly as-is. New rows will simply have different `scope_type` values. No migration, no data loss.

## Adding a Human to a Tenant

### Step 1: Create the Asgardeo group

The user must be in an Asgardeo group named `dc-tenant-<tenant-slug>`. For tenant `acme`:

1. Open Asgardeo console → Groups
2. Create group `dc-tenant-acme`
3. Add the user to the group

See [asgardeo-setup.md](asgardeo-setup.md) for detailed steps.

### Step 2: First login

When the user runs `dcctl login` for the first time:

```bash
dcctl login
# Browser opens, user authenticates in Asgardeo
# JWT contains groups: ["dc-tenant-acme"]
```

**If autoprovision is ON** (`DCAPI_RBAC_AUTOPROVISION=true`, dev default):
- DC-API checks: does this user have a `dc-tenant-acme` group?
- If yes: inserts a `member` role assignment row automatically
- User is now a member of the `acme` tenant

**If autoprovision is OFF** (`DCAPI_RBAC_AUTOPROVISION=false`, prod recommended):
- DC-API checks: does this user exist in the `role_assignments` table?
- If no: returns 403, tells them to ask an owner to invite them
- No auto-insert happens; an existing `owner` must run the next step

### Step 3: Manual invite (optional, prod only)

If autoprovision is off, an existing tenant owner invites the user:

```bash
dcctl tenant add-member alice@acme.com --role member --tenant acme
```

This inserts a `role_assignments` row even if the user has never logged in. When they do log in later, they'll have access.

## Autoprovision Explained

### Dev mode (autoprovision ON)

**Environment:** `DCAPI_RBAC_AUTOPROVISION=true` (default)

**Behavior:** First login with a valid `dc-tenant-<x>` group → instant `member` role.

**Pros:**
- Low friction for local dev and testing
- Developers can self-onboard into shared dev/test tenants
- Matches M1 behavior (backward compatible)

**Cons:**
- Not suitable for production (no explicit approval)
- Creates membership rows even for typos in group names

**When to use:** Local dev, CI integration tests, pre-prod staging.

### Prod mode (autoprovision OFF)

**Environment:** `DCAPI_RBAC_AUTOPROVISION=false`

**Behavior:** First login without an existing `role_assignments` row → 403. Owner must explicitly invite.

**Pros:**
- Explicit audit trail for every access grant
- Prevents accidental over-provisioning
- Complies with zero-trust onboarding policies

**Cons:**
- Higher overhead (owner must run `dcctl tenant add-member` for each hire)
- Requires coordination between teams

**When to use:** Production, regulated industries, multi-tenant platforms.

### Migrating from dev to prod

When you flip `DCAPI_RBAC_AUTOPROVISION=false` for the first time:

1. All existing `role_assignments` rows stay in the database — no data loss
2. New first-time users will be rejected with 403 instead of auto-provisioned
3. Existing users who already have rows are unaffected
4. Owners must now explicitly invite new hires

**No migration script needed** — the row format is unchanged.

## Roles in Practice

### Add a member

```bash
dcctl tenant add-member bob@acme.com --role member --tenant acme
```

Output:
```
Added bob@acme.com as member in tenant acme
Role assignments ID: 550e8400-e29b-41d4-a716-446655440000
```

What it does:
- Inserts a `role_assignments` row with `scope_type=tenant`, `scope_id=acme`, `role=member`
- If a row with the same (principal_id, scope_type, scope_id, role) already exists: returns 200 (idempotent, no error)
- Appends an audit event: actor=you, action=MEMBER_ADDED
- Prints the new role assignment ID

### Add a viewer

```bash
dcctl tenant add-member audit@acme.com --role viewer --tenant acme
```

The viewer can read everything but can't create, update, or delete:
- `dcctl get vm <id>` ✓
- `dcctl list vms` ✓
- `dcctl create vm ...` ✗ (403)
- `dcctl delete vm <id>` ✗ (403)

### Add an owner

```bash
dcctl tenant add-member cto@acme.com --role owner --tenant acme
```

Owners can invite/remove members, delete resources, etc. Only other owners can invite a new owner (one owner can always do what other owners can do).

### List members

```bash
dcctl tenant list-members --tenant acme
```

Output:
```
PRINCIPAL_ID              ROLE      GRANTED_AT            GRANTED_BY
alice@acme.com            member    2026-05-09T10:00:00Z  admin@wso2.com
bob@acme.com              member    2026-05-09T10:02:00Z  alice@acme.com
cto@acme.com              owner     2026-05-09T10:05:00Z  admin@wso2.com
```

Service accounts also appear here if they're assigned roles:

```
SERVICE_ACCOUNT_ID                        ROLE      GRANTED_AT            GRANTED_BY
550e8400-e29b-41d4-a716-446655440000      member    2026-05-09T11:00:00Z  cto@acme.com
```

## Removing Members

```bash
dcctl tenant remove-member alice@acme.com --tenant acme
```

This deletes **all** `role_assignments` rows for that principal in that tenant scope (she might have had multiple roles; now they're all gone). Rows are deleted, audit event is appended.

### The "last owner" guard

You cannot remove the last owner of a tenant. If only `cto@acme.com` is an owner:

```bash
dcctl tenant remove-member cto@acme.com --tenant acme
# Error: cannot remove the last owner of tenant acme
```

**Rationale:** prevents locking out the entire tenant. If you need to transfer ownership, add a new owner first, then remove the old one.

## Service Accounts

### What they are

DC-API-issued long-lived tokens for non-human callers: CI/CD pipelines, webhooks, automation scripts. They authenticate with a token instead of an OIDC JWT.

They are **not** Kubernetes service accounts or Asgardeo service accounts. They live entirely in DC-API and are scoped to a tenant.

### Token format

```
dcapi_sa_<lookup_id>_<secret>
         ↑↑↑↑↑↑↑↑↑↑↑↑↑ ↑↑↑↑↑↑↑↑↑↑↑↑↑↑↑↑
         12 chars       32 chars (random)
         plaintext      secret (bcrypt-hashed in DB)
```

Example:
```
dcapi_sa_a7f3b2c1d9e8_gX9kL2mN5pQ8rT1uV4wX7yZ0aB3cD6eF9
```

The lookup ID is stored plaintext for efficient indexed lookups. Only the secret portion is bcrypt-hashed. This avoids O(N) bcrypt comparisons on every request while keeping the security of cryptographic hashing.

### Token shown once

When you create a service account:

```bash
dcctl tenant create-service-account ci-deployer --role member --tenant acme
```

Output:
```
Created service account: ci-deployer

Token (shown once, save it now):
dcapi_sa_a7f3b2c1d9e8_gX9kL2mN5pQ8rT1uV4wX7yZ0aB3cD6eF9

Expires: never (tokens are long-lived; revoke via delete)
```

**This is the only time the raw token is printed.** It is NOT stored in the database — only the bcrypt hash is saved. If you lose it, delete the SA and create a new one.

### Using the token

In a GitHub Actions workflow:

```yaml
- name: Deploy to DC
  env:
    DCAPI_TOKEN: ${{ secrets.DCAPI_TOKEN }}
  run: |
    dcctl create vm --name web-01 --size medium --image default/img \
      --network default/net --token "$DCAPI_TOKEN"
```

Or as an HTTP header:

```bash
curl -H "Authorization: Bearer dcapi_sa_a7f3b2c1d9e8_gX9kL2mN5pQ8rT1uV4wX7yZ0aB3cD6eF9" \
  https://dcapi.lk.internal.wso2.com/v1/virtual-machines
```

### Token rotation

Service account tokens are long-lived by design (no expiry). To rotate:

1. Create a new SA
2. Update your automation to use the new token
3. Delete the old SA

```bash
dcctl tenant create-service-account ci-deployer-v2 --role member --tenant acme
# Update your CI secrets...
dcctl tenant delete-service-account ci-deployer --tenant acme
```

### List service accounts

```bash
dcctl tenant list-service-accounts --tenant acme
```

Output:
```
NAME               ID                                    ROLE     CREATED_AT            LAST_USED
ci-deployer        550e8400-e29b-41d4-a716-446655440000 member   2026-05-08T15:00:00Z  2026-05-09T10:32:14Z
slack-webhook      550e8400-e29b-41d4-a716-446655440001 viewer   2026-05-01T08:00:00Z  2026-05-09T09:45:00Z
```

The `last_used` timestamp is updated fire-and-forget on the auth path (non-blocking). It's operational information only, not security-critical.

### Delete service account

```bash
dcctl tenant delete-service-account ci-deployer --tenant acme
```

The token is immediately revoked. Any request with the deleted SA's token will get a 401.

## Platform Admin

Members of the Asgardeo group `dc-admin` are **platform admins**. They bypass all role assignment checks:

```bash
# In Asgardeo:
dcctl tenant add-member admin@wso2.com --role owner --tenant acme
# But if admin@wso2.com is in dc-admin group, the role row is optional.
# They have access to all tenants regardless.
```

**Key point:** Admin access is determined by **group membership in Asgardeo, not by DC-API roles**. DC-API checks the JWT's `groups` claim for the configured admin group (default: `dc-admin`).

**Do not make this implicit.** The `dc-admin` group must be managed in Asgardeo, and membership is auditable there. The only place DC-API surfaces this is in context (the `is_admin` flag).

**Admins can still appear in `role_assignments`** — there's no harm in it. But the rows are not consulted if `dc-admin` is present in the JWT.

## Forward Compatibility (M5 Preview)

Your M1.5 membership rows will work unchanged in M5. Here's why:

**Today's schema:**
```sql
INSERT INTO role_assignments
  (principal_type, principal_id, scope_type, scope_id, role, granted_by)
VALUES
  ('user', 'alice@acme.com', 'tenant', 'acme', 'member', 'admin@wso2.com');
```

**In M5, when subscriptions exist:**
```sql
INSERT INTO role_assignments
  (principal_type, principal_id, scope_type, scope_id, role, granted_by)
VALUES
  ('user', 'alice@acme.com', 'subscription', 'sub-123', 'member', 'admin@wso2.com');
```

Both rows are valid in the same database. The middleware walks the scope chain (narrow to broad) and picks the most permissive role. A user with a `member` role on a subscription and a `viewer` role on the parent tenant gets `member` access.

**No migration needed.** The schema design anticipated this from the start. New rows just have new scope types.

## Gotchas

### Removed from Asgardeo group

If an employee leaves and you remove them from the `dc-tenant-acme` group in Asgardeo, their JWT will no longer contain that group. On their next login, DC-API sees a JWT without the group — but **the `role_assignments` row still exists** (deliberately, for audit history).

**What happens:**
- If the JWT doesn't contain the `dc-tenant-acme` group, the user's effective role is empty
- They get 403 on any `dcctl` command
- An owner can explicitly remove them with `dcctl tenant remove-member`

This is by design: the removal audit trail is permanent.

### Token rotation with same secret

Don't try to reuse a deleted SA's name and expect the old token to work. Once deleted, the old token is gone. The bcrypt hash is deleted from the database. Even if you create a new SA with the same name, it gets a different secret and a different lookup_id.

### Removing a user from Asgardeo doesn't auto-remove role assignments

Once you remove someone from the IdP, revoke their DC-API membership explicitly:

```bash
dcctl tenant remove-member alice@acme.com --tenant acme
```

If you don't, the row sits in the audit trail forever (which is fine — it documents they once had access). It just doesn't grant access anymore because the JWT won't authenticate.

### Existing `tenant_id` column on resources is unchanged

All your VMs, clusters, and other resources still have a `tenant_id` column. RBAC is a **layer on top**, not a replacement:

- Every resource checks `tenant_id` first (one VM can't cross tenants)
- Then it checks RBAC roles on that tenant (can this user act on resources in this tenant?)

Existing tenant isolation guarantees remain. RBAC is purely about controlling *who* can do *what* within their tenant.

## Audit Trail

Every role-assignment mutation is logged to `audit_events`:

```sql
SELECT action, actor_id, created_at FROM audit_events
  WHERE action IN ('MEMBER_ADDED', 'MEMBER_REMOVED')
  ORDER BY created_at DESC;
```

Output:
```
action         actor_id              created_at
MEMBER_REMOVED alice@acme.com        2026-05-09T11:02:00Z
MEMBER_ADDED   cto@acme.com          2026-05-09T11:00:00Z
```

Service account creation, deletion, and token usage are also audited. The `last_used` timestamp on service accounts is a convenience; the authoritative audit trail is `audit_events`.

## Environment Variables (DC-API)

| Variable | Default | Notes |
|---|---|---|
| `DCAPI_RBAC_AUTOPROVISION` | `true` | `true` = auto-grant member on first login; `false` = require explicit invite |
| `DCAPI_TENANT_GROUP_PREFIX` | `dc-tenant-` | Asgardeo group prefix for tenant mapping |
| `DCAPI_ADMIN_GROUP` | `dc-admin` | Asgardeo group name for platform admins |

Change these if your IdP uses different naming conventions.

## See Also

- [MILESTONES.md § M1.5](../MILESTONES.md#m15--full-rbac-before-m2-after-m1-e2e-test-passes) — design rationale and scope-polymorphic schema
- [asgardeo-setup.md](asgardeo-setup.md) — step-by-step Asgardeo group creation
- [dc-api-architecture.md](dc-api-architecture.md) — how auth fits into the bigger picture
