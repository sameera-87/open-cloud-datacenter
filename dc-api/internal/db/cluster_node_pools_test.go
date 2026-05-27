//go:build integration

package db

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/wso2/dc-api/internal/models"
)

// setupNodePoolDB spins up a fresh Postgres container, runs Migrate, and
// returns a connected Repository plus a cluster resource UUID that tests can
// attach pools to. The returned cancel func terminates the container.
//
// This follows the same one-container-per-test-file pattern used by
// migrate_test.go and transit_allocator_test.go.
func setupNodePoolDB(t *testing.T) (repo *Repository, clusterID uuid.UUID, teardown func()) {
	t.Helper()

	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)

	pgc, err := tcpostgres.Run(ctx,
		"postgres:16-alpine",
		tcpostgres.WithDatabase("dc_api_np_test"),
		tcpostgres.WithUsername("dc_api"),
		tcpostgres.WithPassword("testpass"),
		tcpostgres.BasicWaitStrategies(),
	)
	if err != nil {
		cancel()
		t.Fatalf("start postgres: %v", err)
	}

	connStr, err := pgc.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		cancel()
		_ = pgc.Terminate(ctx)
		t.Fatalf("conn string: %v", err)
	}

	pool, err := Connect(ctx, connStr)
	if err != nil {
		cancel()
		_ = pgc.Terminate(ctx)
		t.Fatalf("connect: %v", err)
	}

	if err := Migrate(ctx, pool); err != nil {
		cancel()
		pool.Close()
		_ = pgc.Terminate(ctx)
		t.Fatalf("migrate: %v", err)
	}

	repo = NewRepository(pool)

	// Seed the minimal rows required by the cluster_node_pools FK chain:
	// regions → tenants → resources (with type=CLUSTER).
	_, err = pool.Exec(ctx,
		`INSERT INTO regions (name) VALUES ('lk') ON CONFLICT (name) DO NOTHING`)
	if err != nil {
		t.Fatalf("seed region: %v", err)
	}

	tenantID := "np-test-tenant"
	_, err = pool.Exec(ctx,
		`INSERT INTO tenants (id, name, created_by) VALUES ($1, $1, 'test') ON CONFLICT (id) DO NOTHING`,
		tenantID)
	if err != nil {
		t.Fatalf("seed tenant: %v", err)
	}

	var tenantUUID uuid.UUID
	if err := pool.QueryRow(ctx,
		`SELECT tenant_uuid FROM tenants WHERE id = $1`, tenantID).Scan(&tenantUUID); err != nil {
		t.Fatalf("read tenant_uuid: %v", err)
	}

	// Insert a minimal CLUSTER row (type=CLUSTER, status=PENDING).
	clusterID = uuid.New()
	_, err = pool.Exec(ctx,
		`INSERT INTO resources
			(id, tenant_id, tenant_uuid, owner_id, name, type, status, provider_type)
		 VALUES ($1, $2, $3, 'owner-1', 'test-cluster', 'CLUSTER', 'PENDING', 'rancher')`,
		clusterID, tenantID, tenantUUID)
	if err != nil {
		t.Fatalf("seed cluster resource: %v", err)
	}

	teardown = func() {
		pool.Close()
		cancel()
		_ = pgc.Terminate(context.Background())
	}
	return repo, clusterID, teardown
}

// ── TC1: Create + Get + List + Delete a system pool ──────────────────────────

func TestNodePool_SystemPool_CRUD(t *testing.T) {
	repo, clusterID, teardown := setupNodePoolDB(t)
	defer teardown()
	ctx := context.Background()

	pool := &models.NodePool{
		ClusterID:           clusterID,
		Name:                "system",
		Role:                models.NodePoolRoleSystem,
		Size:                "medium",
		Count:               3,
		HarvesterConfigName: "nc-test-cluster-system-abc123",
		Status:              models.NodePoolStatusProvisioning,
	}

	// Create
	if err := repo.CreateNodePool(ctx, pool); err != nil {
		t.Fatalf("CreateNodePool: %v", err)
	}
	if pool.ID == uuid.Nil {
		t.Fatal("expected pool.ID to be populated after create")
	}
	if pool.CreatedAt.IsZero() {
		t.Fatal("expected pool.CreatedAt to be populated after create")
	}

	// Get
	got, err := repo.GetNodePool(ctx, clusterID, "system")
	if err != nil {
		t.Fatalf("GetNodePool: %v", err)
	}
	if got.ID != pool.ID {
		t.Errorf("ID mismatch: got %v, want %v", got.ID, pool.ID)
	}
	if got.Role != models.NodePoolRoleSystem {
		t.Errorf("Role: got %q, want %q", got.Role, models.NodePoolRoleSystem)
	}
	if got.Size != "medium" {
		t.Errorf("Size: got %q, want medium", got.Size)
	}
	if got.Count != 3 {
		t.Errorf("Count: got %d, want 3", got.Count)
	}
	if got.HarvesterConfigName != "nc-test-cluster-system-abc123" {
		t.Errorf("HarvesterConfigName: got %q", got.HarvesterConfigName)
	}
	if got.Taints != nil {
		t.Errorf("Taints should be nil (empty DB default), got %v", got.Taints)
	}
	if got.Labels != nil {
		t.Errorf("Labels should be nil (empty DB default), got %v", got.Labels)
	}

	// List — should contain exactly one pool
	pools, err := repo.ListNodePools(ctx, clusterID)
	if err != nil {
		t.Fatalf("ListNodePools: %v", err)
	}
	if len(pools) != 1 {
		t.Fatalf("expected 1 pool, got %d", len(pools))
	}
	if pools[0].Name != "system" {
		t.Errorf("pool[0].Name = %q, want system", pools[0].Name)
	}

	// Delete
	if err := repo.DeleteNodePool(ctx, clusterID, "system"); err != nil {
		t.Fatalf("DeleteNodePool: %v", err)
	}

	// Confirm gone
	_, err = repo.GetNodePool(ctx, clusterID, "system")
	if err == nil {
		t.Fatal("expected ErrNodePoolNotFound after delete, got nil")
	}

	// List now empty
	pools, err = repo.ListNodePools(ctx, clusterID)
	if err != nil {
		t.Fatalf("ListNodePools after delete: %v", err)
	}
	if len(pools) != 0 {
		t.Errorf("expected 0 pools after delete, got %d", len(pools))
	}
}

// ── TC2: Create + Get a worker pool with taints + labels ─────────────────────

func TestNodePool_WorkerPool_TaintsAndLabels(t *testing.T) {
	repo, clusterID, teardown := setupNodePoolDB(t)
	defer teardown()
	ctx := context.Background()

	// Seed system pool first (not required by FK, but realistic).
	sysPool := &models.NodePool{
		ClusterID:           clusterID,
		Name:                "system",
		Role:                models.NodePoolRoleSystem,
		Size:                "medium",
		Count:               1,
		HarvesterConfigName: "nc-cluster-system-aaa",
		Status:              models.NodePoolStatusProvisioning,
	}
	if err := repo.CreateNodePool(ctx, sysPool); err != nil {
		t.Fatalf("create system pool: %v", err)
	}

	diskGB := 120
	workerPool := &models.NodePool{
		ClusterID:           clusterID,
		Name:                "gpu-workers",
		Role:                models.NodePoolRoleWorker,
		Size:                "xlarge",
		Count:               2,
		DiskGB:              &diskGB,
		HarvesterConfigName: "nc-cluster-gpu-workers-bbb",
		Status:              models.NodePoolStatusProvisioning,
		Taints: []models.NodePoolTaint{
			{Key: "nvidia.com/gpu", Value: "present", Effect: "NoSchedule"},
			{Key: "dedicated", Effect: "NoExecute"},
		},
		Labels: map[string]string{
			"workload-type": "gpu",
			"region":        "lk",
		},
	}

	if err := repo.CreateNodePool(ctx, workerPool); err != nil {
		t.Fatalf("CreateNodePool worker: %v", err)
	}

	got, err := repo.GetNodePool(ctx, clusterID, "gpu-workers")
	if err != nil {
		t.Fatalf("GetNodePool: %v", err)
	}

	if got.Role != models.NodePoolRoleWorker {
		t.Errorf("Role: got %q, want worker", got.Role)
	}
	if got.DiskGB == nil || *got.DiskGB != 120 {
		t.Errorf("DiskGB: got %v, want 120", got.DiskGB)
	}
	if len(got.Taints) != 2 {
		t.Fatalf("Taints: got %d, want 2", len(got.Taints))
	}
	if got.Taints[0].Key != "nvidia.com/gpu" || got.Taints[0].Effect != "NoSchedule" {
		t.Errorf("Taints[0]: got %+v", got.Taints[0])
	}
	if got.Taints[1].Key != "dedicated" || got.Taints[1].Effect != "NoExecute" {
		t.Errorf("Taints[1]: got %+v", got.Taints[1])
	}
	if len(got.Labels) != 2 {
		t.Fatalf("Labels: got %d keys, want 2", len(got.Labels))
	}
	if got.Labels["workload-type"] != "gpu" {
		t.Errorf("Labels[workload-type]: got %q, want gpu", got.Labels["workload-type"])
	}
}

// ── TC3: Reject duplicate (cluster_id, name) ─────────────────────────────────

func TestNodePool_DuplicateName_Returns409(t *testing.T) {
	repo, clusterID, teardown := setupNodePoolDB(t)
	defer teardown()
	ctx := context.Background()

	p := &models.NodePool{
		ClusterID:           clusterID,
		Name:                "system",
		Role:                models.NodePoolRoleSystem,
		Size:                "small",
		Count:               1,
		HarvesterConfigName: "nc-cluster-system-ccc",
		Status:              models.NodePoolStatusProvisioning,
	}
	if err := repo.CreateNodePool(ctx, p); err != nil {
		t.Fatalf("first create: %v", err)
	}

	// Reset ID so the struct looks like a fresh insert attempt.
	p.ID = uuid.Nil
	err := repo.CreateNodePool(ctx, p)
	if err == nil {
		t.Fatal("expected ErrNodePoolAlreadyExists, got nil")
	}
	if err.Error() != ErrNodePoolAlreadyExists.Error() {
		t.Errorf("expected ErrNodePoolAlreadyExists, got %v", err)
	}
}

// ── TC4: Update count, taints, labels — fetch and assert ─────────────────────

func TestNodePool_Update_MutableFields(t *testing.T) {
	repo, clusterID, teardown := setupNodePoolDB(t)
	defer teardown()
	ctx := context.Background()

	p := &models.NodePool{
		ClusterID:           clusterID,
		Name:                "workers",
		Role:                models.NodePoolRoleWorker,
		Size:                "large",
		Count:               1,
		HarvesterConfigName: "nc-cluster-workers-ddd",
		Status:              models.NodePoolStatusProvisioning,
	}
	if err := repo.CreateNodePool(ctx, p); err != nil {
		t.Fatalf("create: %v", err)
	}

	// Mutate: scale up, add a taint, add a label, set status ready.
	p.Count = 5
	p.Taints = []models.NodePoolTaint{{Key: "batch", Effect: "NoSchedule"}}
	p.Labels = map[string]string{"tier": "batch"}
	p.Status = models.NodePoolStatusReady
	p.Message = "all nodes joined"

	if err := repo.UpdateNodePool(ctx, p); err != nil {
		t.Fatalf("UpdateNodePool: %v", err)
	}

	got, err := repo.GetNodePool(ctx, clusterID, "workers")
	if err != nil {
		t.Fatalf("GetNodePool after update: %v", err)
	}
	if got.Count != 5 {
		t.Errorf("Count after update: got %d, want 5", got.Count)
	}
	if got.Status != models.NodePoolStatusReady {
		t.Errorf("Status after update: got %q, want ready", got.Status)
	}
	if got.Message != "all nodes joined" {
		t.Errorf("Message after update: got %q", got.Message)
	}
	if len(got.Taints) != 1 || got.Taints[0].Key != "batch" {
		t.Errorf("Taints after update: got %v", got.Taints)
	}
	if got.Labels["tier"] != "batch" {
		t.Errorf("Labels after update: got %v", got.Labels)
	}
}

// ── TC5: Update does NOT change immutable columns ─────────────────────────────

func TestNodePool_Update_DoesNotChangeImmutableColumns(t *testing.T) {
	repo, clusterID, teardown := setupNodePoolDB(t)
	defer teardown()
	ctx := context.Background()

	diskGB := 80
	p := &models.NodePool{
		ClusterID:           clusterID,
		Name:                "system",
		Role:                models.NodePoolRoleSystem,
		Size:                "medium",
		Count:               1,
		DiskGB:              &diskGB,
		HarvesterConfigName: "nc-cluster-system-eee",
		Status:              models.NodePoolStatusProvisioning,
	}
	if err := repo.CreateNodePool(ctx, p); err != nil {
		t.Fatalf("create: %v", err)
	}

	// Attempt to change immutable fields on the struct, then call UpdateNodePool.
	// The SQL only touches count/taints/labels/status/message — the DB row must
	// not reflect the tampering.
	p.Name = "tampered-name"      // not in SET clause
	p.Size = "xlarge"             // not in SET clause
	bigDisk := 999
	p.DiskGB = &bigDisk           // not in SET clause
	p.HarvesterConfigName = "xxx" // not in SET clause
	// We must restore the real name for the WHERE clause to match the row.
	p.Name = "system"
	p.Count = 3

	if err := repo.UpdateNodePool(ctx, p); err != nil {
		t.Fatalf("UpdateNodePool: %v", err)
	}

	got, err := repo.GetNodePool(ctx, clusterID, "system")
	if err != nil {
		t.Fatalf("GetNodePool: %v", err)
	}

	// Immutable fields must be unchanged.
	if got.Size != "medium" {
		t.Errorf("Size was changed to %q (should stay medium)", got.Size)
	}
	if got.DiskGB == nil || *got.DiskGB != 80 {
		t.Errorf("DiskGB was changed to %v (should stay 80)", got.DiskGB)
	}
	if got.HarvesterConfigName != "nc-cluster-system-eee" {
		t.Errorf("HarvesterConfigName was changed to %q", got.HarvesterConfigName)
	}
	if got.Role != models.NodePoolRoleSystem {
		t.Errorf("Role was changed to %q", got.Role)
	}
	// Mutable field was updated.
	if got.Count != 3 {
		t.Errorf("Count not updated: got %d, want 3", got.Count)
	}
}

// ── TC6: Delete cascades when parent Cluster resource is deleted ──────────────

func TestNodePool_Cascade_OnClusterDelete(t *testing.T) {
	repo, clusterID, teardown := setupNodePoolDB(t)
	defer teardown()
	ctx := context.Background()

	// Create two pools on the cluster.
	p1 := &models.NodePool{
		ClusterID:           clusterID,
		Name:                "system",
		Role:                models.NodePoolRoleSystem,
		Size:                "small",
		Count:               1,
		HarvesterConfigName: "nc-c-system-fff",
		Status:              models.NodePoolStatusProvisioning,
	}
	p2 := &models.NodePool{
		ClusterID:           clusterID,
		Name:                "workers",
		Role:                models.NodePoolRoleWorker,
		Size:                "small",
		Count:               2,
		HarvesterConfigName: "nc-c-workers-fff",
		Status:              models.NodePoolStatusProvisioning,
	}
	if err := repo.CreateNodePool(ctx, p1); err != nil {
		t.Fatalf("create p1: %v", err)
	}
	if err := repo.CreateNodePool(ctx, p2); err != nil {
		t.Fatalf("create p2: %v", err)
	}

	// Delete the parent cluster row directly (simulates what the reconciler
	// does when Rancher confirms the cluster is gone).
	if _, err := repo.pool.Exec(ctx,
		`DELETE FROM resources WHERE id = $1`, clusterID); err != nil {
		t.Fatalf("delete cluster resource: %v", err)
	}

	// Both pool rows must have cascaded away.
	pools, err := repo.ListNodePools(ctx, clusterID)
	if err != nil {
		t.Fatalf("ListNodePools after cascade delete: %v", err)
	}
	if len(pools) != 0 {
		t.Errorf("expected 0 pools after cluster cascade delete, got %d", len(pools))
	}
}

// ── TC7: CountNodePools ───────────────────────────────────────────────────────

func TestNodePool_CountNodePools(t *testing.T) {
	repo, clusterID, teardown := setupNodePoolDB(t)
	defer teardown()
	ctx := context.Background()

	// Case A: 1 system pool (count=3), no worker pools.
	sysPool := &models.NodePool{
		ClusterID:           clusterID,
		Name:                "system",
		Role:                models.NodePoolRoleSystem,
		Size:                "medium",
		Count:               3,
		HarvesterConfigName: "nc-c-system-ggg",
		Status:              models.NodePoolStatusReady,
	}
	if err := repo.CreateNodePool(ctx, sysPool); err != nil {
		t.Fatalf("create system pool: %v", err)
	}

	workerCount, totalNodes, err := repo.CountNodePools(ctx, clusterID)
	if err != nil {
		t.Fatalf("CountNodePools (A): %v", err)
	}
	if workerCount != 0 {
		t.Errorf("(A) workerCount: got %d, want 0", workerCount)
	}
	if totalNodes != 3 {
		t.Errorf("(A) totalNodes: got %d, want 3 (system.count)", totalNodes)
	}

	// Case B: add 2 worker pools (count=2, count=4).
	w1 := &models.NodePool{
		ClusterID:           clusterID,
		Name:                "worker-a",
		Role:                models.NodePoolRoleWorker,
		Size:                "medium",
		Count:               2,
		HarvesterConfigName: "nc-c-worker-a-ggg",
		Status:              models.NodePoolStatusReady,
	}
	w2 := &models.NodePool{
		ClusterID:           clusterID,
		Name:                "worker-b",
		Role:                models.NodePoolRoleWorker,
		Size:                "large",
		Count:               4,
		HarvesterConfigName: "nc-c-worker-b-ggg",
		Status:              models.NodePoolStatusReady,
	}
	if err := repo.CreateNodePool(ctx, w1); err != nil {
		t.Fatalf("create w1: %v", err)
	}
	if err := repo.CreateNodePool(ctx, w2); err != nil {
		t.Fatalf("create w2: %v", err)
	}

	workerCount, totalNodes, err = repo.CountNodePools(ctx, clusterID)
	if err != nil {
		t.Fatalf("CountNodePools (B): %v", err)
	}
	if workerCount != 2 {
		t.Errorf("(B) workerCount: got %d, want 2", workerCount)
	}
	// totalNodes = 3 (system) + 2 (w1) + 4 (w2) = 9
	if totalNodes != 9 {
		t.Errorf("(B) totalNodes: got %d, want 9", totalNodes)
	}

	// Case C: cluster that has no pools at all.
	emptyClusterID := uuid.New()
	// We do NOT seed a resources row for emptyClusterID — CountNodePools
	// queries cluster_node_pools directly with no FK check. The WHERE
	// simply returns 0 rows and COALESCE returns 0.
	workerCount, totalNodes, err = repo.CountNodePools(ctx, emptyClusterID)
	if err != nil {
		t.Fatalf("CountNodePools (C): %v", err)
	}
	if workerCount != 0 || totalNodes != 0 {
		t.Errorf("(C) expected (0,0) for empty cluster, got (%d,%d)", workerCount, totalNodes)
	}
}

// ── TC8: GetNodePool returns ErrNodePoolNotFound for missing pool ─────────────

func TestNodePool_GetMissing_ReturnsNotFound(t *testing.T) {
	repo, clusterID, teardown := setupNodePoolDB(t)
	defer teardown()
	ctx := context.Background()

	_, err := repo.GetNodePool(ctx, clusterID, "nonexistent")
	if err == nil {
		t.Fatal("expected ErrNodePoolNotFound, got nil")
	}
	if !errors.Is(err, ErrNodePoolNotFound) {
		t.Errorf("expected ErrNodePoolNotFound, got %v", err)
	}
}

// ── TC9: DeleteNodePool returns ErrNodePoolNotFound for missing pool ──────────

func TestNodePool_DeleteMissing_ReturnsNotFound(t *testing.T) {
	repo, clusterID, teardown := setupNodePoolDB(t)
	defer teardown()
	ctx := context.Background()

	err := repo.DeleteNodePool(ctx, clusterID, "ghost-pool")
	if err == nil {
		t.Fatal("expected ErrNodePoolNotFound, got nil")
	}
	if !errors.Is(err, ErrNodePoolNotFound) {
		t.Errorf("expected ErrNodePoolNotFound, got %v", err)
	}
}
