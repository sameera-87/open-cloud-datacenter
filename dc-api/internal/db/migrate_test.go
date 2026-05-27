//go:build integration

package db

import (
	"context"
	"testing"
	"time"

	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"
)

// TestMigrate_Idempotent runs schema.sql twice against a fresh postgres
// container and confirms both calls succeed. This proves that every
// CREATE TYPE / CREATE TABLE / CREATE TRIGGER / INSERT seed in
// schema.sql is correctly guarded for re-run.
func TestMigrate_Idempotent(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	pgc, err := tcpostgres.Run(ctx,
		"postgres:16-alpine",
		tcpostgres.WithDatabase("dc_api_test"),
		tcpostgres.WithUsername("dc_api"),
		tcpostgres.WithPassword("testpass"),
		tcpostgres.BasicWaitStrategies(),
	)
	if err != nil {
		t.Fatalf("start postgres: %v", err)
	}
	t.Cleanup(func() { _ = pgc.Terminate(ctx) })

	connStr, err := pgc.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		t.Fatalf("conn string: %v", err)
	}
	pool, err := Connect(ctx, connStr)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(pool.Close)

	if err := Migrate(ctx, pool); err != nil {
		t.Fatalf("first Migrate (fresh DB) failed: %v", err)
	}
	if err := Migrate(ctx, pool); err != nil {
		t.Fatalf("second Migrate (existing DB) failed: %v", err)
	}

	// Spot-check a few invariants the second run must not break.
	var resourceCount int
	if err := pool.QueryRow(ctx, `SELECT COUNT(*) FROM resources`).Scan(&resourceCount); err != nil {
		t.Fatalf("query resources: %v", err)
	}
	if resourceCount != 0 {
		t.Fatalf("expected empty resources table, got %d rows", resourceCount)
	}

	// lk region must have the refreshed reserved_cidrs (ON CONFLICT DO UPDATE).
	var cidrs []string
	if err := pool.QueryRow(ctx,
		`SELECT reserved_cidrs FROM regions WHERE name = 'lk'`).Scan(&cidrs); err != nil {
		t.Fatalf("query regions: %v", err)
	}
	want := "10.16.0.0/16:harvester-ovn-default"
	found := false
	for _, c := range cidrs {
		if c == want {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("expected %q in lk reserved_cidrs, got %v", want, cidrs)
	}

	// BASTION enum value must be present.
	var hasBastion bool
	if err := pool.QueryRow(ctx,
		`SELECT EXISTS (SELECT 1 FROM pg_enum WHERE enumlabel = 'BASTION')`).Scan(&hasBastion); err != nil {
		t.Fatalf("query pg_enum: %v", err)
	}
	if !hasBastion {
		t.Fatalf("expected BASTION enum value to exist")
	}
}
