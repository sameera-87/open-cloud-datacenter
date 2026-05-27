// Package db — cluster_node_pools.go
//
// Repository methods for the cluster_node_pools table.
// Each Cluster resource (resources.type='CLUSTER') has an associated set of
// node pool rows. Exactly one pool has role='system' (name hardcoded to
// "system"); additional pools have role='worker'.
//
// JSONB columns (taints, labels):
//   - Marshal []NodePoolTaint / map[string]string → []byte with json.Marshal.
//   - Scan into []byte, Unmarshal on read.
//   - Empty slice/map defaults ('[]' / '{}') match the column DEFAULT.
//
// No global state; all methods are on *Repository.
package db

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/wso2/dc-api/internal/models"
)

// ErrNodePoolNotFound is returned when a pool row is absent or not visible
// to the caller. Handlers map this to 404 Not Found.
var ErrNodePoolNotFound = errors.New("node pool not found")

// ErrNodePoolAlreadyExists is returned when the (cluster_id, name) unique
// constraint fires. Handlers map this to 409 Conflict.
var ErrNodePoolAlreadyExists = errors.New("node pool with this name already exists in the cluster")

// ── helpers ───────────────────────────────────────────────────────────────────

// taintsJSON marshals a []NodePoolTaint to a JSONB-safe []byte.
// A nil slice is coerced to an empty JSON array ('[]') to match the column
// DEFAULT and avoid NULL in NOT NULL columns.
func taintsJSON(taints []models.NodePoolTaint) []byte {
	if taints == nil {
		taints = []models.NodePoolTaint{}
	}
	b, _ := json.Marshal(taints)
	return b
}

// labelsJSON marshals a map[string]string to a JSONB-safe []byte.
// A nil map is coerced to an empty JSON object ('{}') to match the column DEFAULT.
func labelsJSON(labels map[string]string) []byte {
	if labels == nil {
		labels = map[string]string{}
	}
	b, _ := json.Marshal(labels)
	return b
}

// scanNodePool scans a single cluster_node_pools row. The caller must provide
// raw []byte accumulators for taints and labels; this function unmarshals them.
// Column order must match the SELECT in every caller.
func scanNodePool(row pgx.Row, p *models.NodePool) error {
	var msg *string
	var diskGB *int
	var taintsRaw, labelsRaw []byte

	if err := row.Scan(
		&p.ID,
		&p.ClusterID,
		&p.Name,
		&p.Role,
		&p.Size,
		&p.Count,
		&diskGB,
		&taintsRaw,
		&labelsRaw,
		&p.HarvesterConfigName,
		&p.Status,
		&msg,
		&p.CreatedAt,
		&p.UpdatedAt,
	); err != nil {
		return err
	}

	if diskGB != nil {
		p.DiskGB = diskGB
	}
	if msg != nil {
		p.Message = *msg
	}

	if err := json.Unmarshal(taintsRaw, &p.Taints); err != nil {
		return fmt.Errorf("unmarshal taints: %w", err)
	}
	if err := json.Unmarshal(labelsRaw, &p.Labels); err != nil {
		return fmt.Errorf("unmarshal labels: %w", err)
	}

	// Normalise empty collections to nil so JSON responses omit them
	// (the struct tags carry `omitempty`).
	if len(p.Taints) == 0 {
		p.Taints = nil
	}
	if len(p.Labels) == 0 {
		p.Labels = nil
	}

	return nil
}

// nodePoolCols is the canonical SELECT column list. Every query that returns
// full pool rows uses this to guarantee scanNodePool's column-order assumption.
const nodePoolCols = `
    id, cluster_id, name, role, size, count, disk_gb,
    taints, labels, harvester_config_name,
    status, message, created_at, updated_at`

// ── CRUD ─────────────────────────────────────────────────────────────────────

// CreateNodePool inserts a new pool row. The caller must populate at least
// ClusterID, Name, Role, Size, Count, HarvesterConfigName, and Status.
// ID, CreatedAt, and UpdatedAt are populated from the RETURNING clause.
//
// ErrNodePoolAlreadyExists is returned when the (cluster_id, name) unique
// constraint fires (maps to 409 Conflict in the handler).
func (r *Repository) CreateNodePool(ctx context.Context, p *models.NodePool) error {
	const q = `
		INSERT INTO cluster_node_pools
			(cluster_id, name, role, size, count, disk_gb,
			 taints, labels, harvester_config_name, status, message)
		VALUES
			($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)
		RETURNING id, created_at, updated_at`

	err := r.pool.QueryRow(ctx, q,
		p.ClusterID,
		p.Name,
		string(p.Role),
		p.Size,
		p.Count,
		p.DiskGB,
		taintsJSON(p.Taints),
		labelsJSON(p.Labels),
		p.HarvesterConfigName,
		string(p.Status),
		nilIfEmpty(p.Message),
	).Scan(&p.ID, &p.CreatedAt, &p.UpdatedAt)
	if err != nil {
		if isUniqueViolation(err) {
			return ErrNodePoolAlreadyExists
		}
		return fmt.Errorf("db create node pool: %w", err)
	}
	return nil
}

// GetNodePool fetches a single pool by cluster ID and pool name.
// Returns ErrNodePoolNotFound when the row is absent.
func (r *Repository) GetNodePool(ctx context.Context, clusterID uuid.UUID, name string) (*models.NodePool, error) {
	q := `SELECT` + nodePoolCols + `
		FROM cluster_node_pools
		WHERE cluster_id = $1 AND name = $2`

	var p models.NodePool
	err := scanNodePool(r.pool.QueryRow(ctx, q, clusterID, name), &p)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNodePoolNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("db get node pool: %w", err)
	}
	return &p, nil
}

// ListNodePools returns all pools for a cluster, ordered by role (system first)
// then by creation time.
func (r *Repository) ListNodePools(ctx context.Context, clusterID uuid.UUID) ([]models.NodePool, error) {
	q := `SELECT` + nodePoolCols + `
		FROM cluster_node_pools
		WHERE cluster_id = $1
		ORDER BY
		    CASE role WHEN 'system' THEN 0 ELSE 1 END,
		    created_at ASC`

	rows, err := r.pool.Query(ctx, q, clusterID)
	if err != nil {
		return nil, fmt.Errorf("db list node pools: %w", err)
	}
	defer rows.Close()

	var out []models.NodePool
	for rows.Next() {
		var p models.NodePool
		if err := scanNodePool(rows, &p); err != nil {
			return nil, fmt.Errorf("db scan node pool: %w", err)
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// UpdateNodePool writes the mutable columns for a pool: count, taints, labels,
// status, and message. Immutable columns (name, role, size, disk_gb,
// harvester_config_name, cluster_id, created_at) are never updated.
// UpdatedAt is refreshed by the touch_updated_at trigger.
//
// The method does NOT return ErrNodePoolNotFound for a zero-row update;
// callers that need to distinguish should call GetNodePool first.
func (r *Repository) UpdateNodePool(ctx context.Context, p *models.NodePool) error {
	const q = `
		UPDATE cluster_node_pools
		SET
		    count   = $3,
		    taints  = $4,
		    labels  = $5,
		    status  = $6,
		    message = $7
		WHERE cluster_id = $1 AND name = $2`

	_, err := r.pool.Exec(ctx, q,
		p.ClusterID,
		p.Name,
		p.Count,
		taintsJSON(p.Taints),
		labelsJSON(p.Labels),
		string(p.Status),
		nilIfEmpty(p.Message),
	)
	if err != nil {
		return fmt.Errorf("db update node pool: %w", err)
	}
	return nil
}

// DeleteNodePool removes a pool row by cluster ID and pool name.
// Returns ErrNodePoolNotFound when no row matched.
func (r *Repository) DeleteNodePool(ctx context.Context, clusterID uuid.UUID, name string) error {
	const q = `DELETE FROM cluster_node_pools WHERE cluster_id = $1 AND name = $2`
	tag, err := r.pool.Exec(ctx, q, clusterID, name)
	if err != nil {
		return fmt.Errorf("db delete node pool: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNodePoolNotFound
	}
	return nil
}

// CountNodePools returns two counts for a cluster:
//   - workerCount: the number of worker pool rows (role='worker').
//   - totalNodes: the sum of count across ALL pools (system + worker).
//
// Used by cluster handlers to populate the worker_pool_count and
// total_node_count response fields.
func (r *Repository) CountNodePools(ctx context.Context, clusterID uuid.UUID) (workerCount int, totalNodes int, err error) {
	const q = `
		SELECT
		    COUNT(*) FILTER (WHERE role = 'worker') AS worker_count,
		    COALESCE(SUM(count), 0)                  AS total_nodes
		FROM cluster_node_pools
		WHERE cluster_id = $1`

	if err = r.pool.QueryRow(ctx, q, clusterID).Scan(&workerCount, &totalNodes); err != nil {
		return 0, 0, fmt.Errorf("db count node pools: %w", err)
	}
	return workerCount, totalNodes, nil
}

// ── internal helpers ──────────────────────────────────────────────────────────

// isUniqueViolation returns true when err is a PostgreSQL unique-constraint
// violation (SQLSTATE 23505). Mirrors the inline pattern used in
// projects.go and tenants.go.
func isUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505"
}
