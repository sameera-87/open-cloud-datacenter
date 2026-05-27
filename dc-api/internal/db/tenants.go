// Package db — tenants.go
//
// Repository methods for the `tenants` registry. The registry exists so
// platform admins can enumerate every tenant via GET /v1/tenants, including
// empty ones (Asgardeo groups that have been created but have no members
// yet, so no role_assignments rows). Before this table, the admin path
// derived the list from DISTINCT role_assignments.scope_id, which made
// empty tenants structurally invisible.
//
// Phase 6a: every row also carries an immutable tenant_uuid. Per-tenant
// tables reference this UUID; the slug (`id`) is just a renameable handle.
//
// Patterns mirror rbac.go: SQL lives here; handlers and middleware call
// these methods, never pool directly.
package db

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/wso2/dc-api/internal/models"
)

const tenantCols = `id, tenant_uuid, name, asgardeo_group, description, cpu_cores_cap, memory_gb_cap, storage_gb_cap, created_at, created_by`

// scanTenant reads one tenant row (in tenantCols order) into a models.Tenant.
// Centralises the nullable AsgardeoGroup/Description handling so the
// per-method scans stay short.
func scanTenant(row pgx.Row, t *models.Tenant) error {
	var asgGroup, descr *string
	if err := row.Scan(
		&t.ID, &t.TenantUUID, &t.Name, &asgGroup, &descr,
		&t.CPUCoresCap, &t.MemoryGBCap, &t.StorageGBCap,
		&t.CreatedAt, &t.CreatedBy,
	); err != nil {
		return err
	}
	if asgGroup != nil {
		t.AsgardeoGroup = *asgGroup
	}
	if descr != nil {
		t.Description = *descr
	}
	return nil
}

// UpsertTenant inserts a tenant row, or no-ops when one already exists.
// Used by handlers (members invite) to ensure the tenants registry has a
// row before granting access.
//
// Returns (nil, nil) when the row already existed (no change made). Returns
// the freshly-created Tenant when a new row was inserted.
func (r *Repository) UpsertTenant(ctx context.Context, id, name, asgardeoGroup, createdBy string) (*models.Tenant, error) {
	q := `
		INSERT INTO tenants (id, name, asgardeo_group, created_by)
		VALUES ($1, $2, $3, $4)
		ON CONFLICT (id) DO NOTHING
		RETURNING ` + tenantCols

	row := r.pool.QueryRow(ctx, q, id, name, asgardeoGroup, createdBy)
	var t models.Tenant
	if err := scanTenant(row, &t); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			// Row already existed — ON CONFLICT swallowed the insert.
			return nil, nil
		}
		return nil, fmt.Errorf("db upsert tenant: %w", err)
	}
	return &t, nil
}

// CreateTenant inserts a tenant row and returns ErrTenantAlreadyExists
// when the id collides. Used by POST /v1/admin/tenants so admins see a
// 409 rather than a silent no-out.
//
// Caps (CPUCoresCap, MemoryGBCap, StorageGBCap): pass 0 to keep the schema
// default (80/256/2000); pass any positive integer to override at creation.
// The handler is the right place for "cap ≥ 1" validation — this layer just
// passes through.
func (r *Repository) CreateTenant(ctx context.Context, t models.Tenant) (*models.Tenant, error) {
	q := `
		INSERT INTO tenants
			(id, name, asgardeo_group, description, created_by,
			 cpu_cores_cap, memory_gb_cap, storage_gb_cap)
		VALUES
			($1, $2, NULLIF($3, ''), NULLIF($4, ''), $5,
			 COALESCE(NULLIF($6, 0), 80),
			 COALESCE(NULLIF($7, 0), 256),
			 COALESCE(NULLIF($8, 0), 2000))
		RETURNING ` + tenantCols

	row := r.pool.QueryRow(ctx, q,
		t.ID, t.Name, t.AsgardeoGroup, t.Description, t.CreatedBy,
		t.CPUCoresCap, t.MemoryGBCap, t.StorageGBCap,
	)
	var out models.Tenant
	if err := scanTenant(row, &out); err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" {
			return nil, ErrTenantAlreadyExists
		}
		return nil, fmt.Errorf("db create tenant: %w", err)
	}
	return &out, nil
}

// ─────────────────────────── Tenant capacity caps ───────────────────────────
//
// The hybrid quota model: platform admin sets the tenant ceiling; tenant
// owner distributes across projects. The functions below enforce both halves
// of the invariant inside transactions with row-level locks on the tenants
// row, so two concurrent operations can't race past the budget.

// GetTenantCapAndAllocation returns the per-tenant cap, the current
// per-project allocation sum, and the remaining headroom — all in one round
// trip. Used by the admin tenants page, the RegisterProjectDialog (to show
// "X CPU available"), and the project create/PATCH cap check.
func (r *Repository) GetTenantCapAndAllocation(ctx context.Context, tenantUUID uuid.UUID) (*models.TenantCapUsage, error) {
	// Aggregate query: outer SELECT must list every non-aggregated column in
	// GROUP BY (Postgres SQLSTATE 42803). tenant_uuid alone isn't enough —
	// add the three cap columns explicitly.
	const q = `
		SELECT
			t.cpu_cores_cap, t.memory_gb_cap, t.storage_gb_cap,
			COALESCE(SUM(p.cpu_cores),  0)::INTEGER,
			COALESCE(SUM(p.memory_gb),  0)::INTEGER,
			COALESCE(SUM(p.storage_gb), 0)::INTEGER
		FROM       tenants t
		LEFT JOIN  projects p ON p.tenant_uuid = t.tenant_uuid
		WHERE      t.tenant_uuid = $1
		GROUP BY   t.tenant_uuid, t.cpu_cores_cap, t.memory_gb_cap, t.storage_gb_cap`

	var u models.TenantCapUsage
	err := r.pool.QueryRow(ctx, q, tenantUUID).Scan(
		&u.Cap.CPUCores, &u.Cap.MemoryGB, &u.Cap.StorageGB,
		&u.Allocated.CPUCores, &u.Allocated.MemoryGB, &u.Allocated.StorageGB,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		return nil, fmt.Errorf("db get tenant cap+allocation: %w", err)
	}
	u.Available.CPUCores = u.Cap.CPUCores - u.Allocated.CPUCores
	u.Available.MemoryGB = u.Cap.MemoryGB - u.Allocated.MemoryGB
	u.Available.StorageGB = u.Cap.StorageGB - u.Allocated.StorageGB
	return &u, nil
}

// UpdateTenantCap atomically changes the cap with the shrink-guard applied
// inside a single transaction. Returns the updated Tenant (with the new
// cap) on success. If the new cap would shrink any dimension below the
// already-allocated sum, returns ErrCapBelowAllocated wrapping a
// TenantCapUsage so the handler can format a helpful 400 body.
//
// The `SELECT FOR UPDATE` on the tenants row prevents a concurrent project
// create/PATCH from sliding past the check after we've validated.
func (r *Repository) UpdateTenantCap(ctx context.Context, tenantUUID uuid.UUID, newCap models.TenantCap) (*models.Tenant, *models.TenantCapUsage, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return nil, nil, fmt.Errorf("db begin tx (update tenant cap): %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	// Row-lock the tenants row. Concurrent project creates also lock this
	// row before doing the cap check, so they queue behind us.
	if _, err := tx.Exec(ctx, `SELECT 1 FROM tenants WHERE tenant_uuid = $1 FOR UPDATE`, tenantUUID); err != nil {
		return nil, nil, fmt.Errorf("db lock tenant row: %w", err)
	}

	// Compute current allocation while the row is locked.
	var alloc models.TenantCap
	const allocQ = `
		SELECT COALESCE(SUM(cpu_cores), 0)::INTEGER,
		       COALESCE(SUM(memory_gb), 0)::INTEGER,
		       COALESCE(SUM(storage_gb), 0)::INTEGER
		FROM   projects
		WHERE  tenant_uuid = $1`
	if err := tx.QueryRow(ctx, allocQ, tenantUUID).Scan(&alloc.CPUCores, &alloc.MemoryGB, &alloc.StorageGB); err != nil {
		return nil, nil, fmt.Errorf("db sum project allocation: %w", err)
	}

	// Shrink-guard: cap must be ≥ what's already allocated.
	if newCap.CPUCores < alloc.CPUCores || newCap.MemoryGB < alloc.MemoryGB || newCap.StorageGB < alloc.StorageGB {
		usage := &models.TenantCapUsage{
			Cap:       newCap,
			Allocated: alloc,
			Available: models.TenantCap{
				CPUCores:  newCap.CPUCores - alloc.CPUCores,
				MemoryGB:  newCap.MemoryGB - alloc.MemoryGB,
				StorageGB: newCap.StorageGB - alloc.StorageGB,
			},
		}
		return nil, usage, ErrCapBelowAllocated
	}

	// Apply the cap.
	const updQ = `
		UPDATE tenants
		SET    cpu_cores_cap = $2, memory_gb_cap = $3, storage_gb_cap = $4
		WHERE  tenant_uuid = $1
		RETURNING ` + tenantCols
	row := tx.QueryRow(ctx, updQ, tenantUUID, newCap.CPUCores, newCap.MemoryGB, newCap.StorageGB)
	var out models.Tenant
	if err := scanTenant(row, &out); err != nil {
		return nil, nil, fmt.Errorf("db update tenant cap: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, nil, fmt.Errorf("db commit tenant cap update: %w", err)
	}
	return &out, nil, nil
}

// ListTenants returns every row in the `tenants` table, ordered by id.
// Used by GET /v1/tenants (admin path).
func (r *Repository) ListTenants(ctx context.Context) ([]models.Tenant, error) {
	q := `SELECT ` + tenantCols + ` FROM tenants ORDER BY id`

	rows, err := r.pool.Query(ctx, q)
	if err != nil {
		return nil, fmt.Errorf("db list tenants: %w", err)
	}
	defer rows.Close()

	var out []models.Tenant
	for rows.Next() {
		var t models.Tenant
		if err := scanTenant(rows, &t); err != nil {
			return nil, fmt.Errorf("db list tenants scan: %w", err)
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// GetTenant returns a tenant by id (slug), or (nil, nil) when not found.
func (r *Repository) GetTenant(ctx context.Context, id string) (*models.Tenant, error) {
	q := `SELECT ` + tenantCols + ` FROM tenants WHERE id = $1`

	row := r.pool.QueryRow(ctx, q, id)
	var t models.Tenant
	if err := scanTenant(row, &t); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		return nil, fmt.Errorf("db get tenant: %w", err)
	}
	return &t, nil
}

// GetTenantUUIDBySlug is the hot-path lookup called by TenantContext middleware
// on every /v1/tenants/{tenant_id}/... request. Returns (uuid.Nil, nil) when
// no row exists for the slug — matching the project convention used by
// GetTenant — so callers gate on `u == uuid.Nil` without importing this
// package's error symbols. Indexed by primary key (id); one PK lookup per
// request.
func (r *Repository) GetTenantUUIDBySlug(ctx context.Context, slug string) (uuid.UUID, error) {
	var u uuid.UUID
	err := r.pool.QueryRow(ctx, `SELECT tenant_uuid FROM tenants WHERE id = $1`, slug).Scan(&u)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return uuid.Nil, nil
		}
		return uuid.Nil, fmt.Errorf("db get tenant uuid by slug: %w", err)
	}
	return u, nil
}

// ErrTenantAlreadyExists is returned by CreateTenant when the id is taken.
// Distinct from a generic DB error so the handler can map it to 409.
var ErrTenantAlreadyExists = errors.New("tenant already exists")

// ErrCapBelowAllocated is returned by UpdateTenantCap when the requested
// new cap is lower than what's already distributed across projects.
// Handler maps it to 400 with a quota_exceeded body.
var ErrCapBelowAllocated = errors.New("tenant cap would shrink below allocated project quotas")

// ErrProjectQuotaExceedsTenantCap is returned by project create/PATCH when
// the requested project quotas plus other-projects' quotas would exceed
// the tenant cap. Handler maps it to 400 with a quota_exceeded body.
var ErrProjectQuotaExceedsTenantCap = errors.New("project quotas would exceed tenant cap")

// ErrProjectQuotaBelowUsage is returned by project PATCH when the requested
// new quota is below the project's current in-use sum (active resources).
// Handler maps it to 400.
var ErrProjectQuotaBelowUsage = errors.New("project quota would shrink below current resource usage")
