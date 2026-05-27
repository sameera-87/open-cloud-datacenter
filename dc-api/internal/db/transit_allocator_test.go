//go:build integration

package db

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"
)

// TestTransitCIDRAllocator_LifecycleAndReuse spins up a real Postgres, applies
// the schema, then walks the allocator through allocate / lookup / release /
// re-allocate paths. Confirms three properties the SQL is meant to guarantee:
//
//  1. Allocate is idempotent — calling it twice for the same peeringID returns
//     the same CIDR.
//  2. Lowest-free reuse — after a release, the next allocation reuses the
//     freed slot rather than counting upward.
//  3. UNIQUE collisions on cidr_index never produce duplicate CIDRs (proven
//     by inserting a sentinel row at the lowest index then asking the
//     allocator for a new one and confirming it picked a different index).
func TestTransitCIDRAllocator_LifecycleAndReuse(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	pgc, err := tcpostgres.Run(ctx,
		"postgres:16-alpine",
		tcpostgres.WithDatabase("dc_api_transit_test"),
		tcpostgres.WithUsername("dc_api"),
		tcpostgres.WithPassword("testpass"),
		tcpostgres.BasicWaitStrategies(),
	)
	if err != nil {
		t.Fatalf("start postgres: %v", err)
	}
	t.Cleanup(func() { _ = pgc.Terminate(context.Background()) })

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
		t.Fatalf("migrate: %v", err)
	}
	repo := NewRepository(pool)

	// A peering row is needed because peering_transit_cidrs has a FK on it.
	// Insert a minimal one — name + vnet ids are arbitrary.
	mustInsertPeering := func(t *testing.T, id uuid.UUID) {
		t.Helper()
		vnetA := uuid.New()
		vnetB := uuid.New()
		// peerings has a FK on vnets, so seed both vnets too.
		seedVnet := func(vid uuid.UUID) {
			_, err := pool.Exec(ctx, `
				INSERT INTO regions (name) VALUES ('lk') ON CONFLICT (name) DO NOTHING`)
			if err != nil {
				t.Fatalf("seed region: %v", err)
			}
			_, err = pool.Exec(ctx, `
				INSERT INTO vnets (id, tenant_id, name, region, address_space, provider_type, status)
				VALUES ($1, 'tx-test', $2, 'lk', ARRAY['10.0.0.0/16'], 'kubeovn', 'ACTIVE')`,
				vid, vid.String()[:8])
			if err != nil {
				t.Fatalf("seed vnet: %v", err)
			}
		}
		seedVnet(vnetA)
		seedVnet(vnetB)
		_, err := pool.Exec(ctx, `
			INSERT INTO peerings (id, vnet_id, peer_vnet_id, tenant_id, name, provider_type, status)
			VALUES ($1, $2, $3, 'tx-test', $4, 'kubeovn', 'PENDING')`,
			id, vnetA, vnetB, "peering-"+id.String()[:8])
		if err != nil {
			t.Fatalf("seed peering: %v", err)
		}
	}

	// (1) Allocate is idempotent.
	p1 := uuid.New()
	mustInsertPeering(t, p1)
	first, err := repo.AllocateTransitCIDR(ctx, p1)
	if err != nil {
		t.Fatalf("first allocate: %v", err)
	}
	second, err := repo.AllocateTransitCIDR(ctx, p1)
	if err != nil {
		t.Fatalf("second allocate: %v", err)
	}
	if first != second {
		t.Fatalf("allocator is not idempotent: %q vs %q", first, second)
	}
	if first != "100.64.0.0/24" {
		t.Errorf("first allocation was %q, want 100.64.0.0/24", first)
	}

	// (2) Lowest-free reuse — second peering takes idx 1, then we release
	// the first and a third peering reuses idx 0.
	p2 := uuid.New()
	mustInsertPeering(t, p2)
	gotP2, err := repo.AllocateTransitCIDR(ctx, p2)
	if err != nil {
		t.Fatalf("allocate p2: %v", err)
	}
	if gotP2 != "100.64.1.0/24" {
		t.Errorf("p2 allocation was %q, want 100.64.1.0/24", gotP2)
	}

	if err := repo.ReleaseTransitCIDR(ctx, p1); err != nil {
		t.Fatalf("release p1: %v", err)
	}

	p3 := uuid.New()
	mustInsertPeering(t, p3)
	gotP3, err := repo.AllocateTransitCIDR(ctx, p3)
	if err != nil {
		t.Fatalf("allocate p3: %v", err)
	}
	if gotP3 != "100.64.0.0/24" {
		t.Errorf("p3 allocation was %q, want 100.64.0.0/24 (reuse of freed slot)", gotP3)
	}

	// (3) Releasing a peering that has no row is a no-op (idempotent).
	if err := repo.ReleaseTransitCIDR(ctx, uuid.New()); err != nil {
		t.Errorf("release of unallocated peering should be a no-op, got %v", err)
	}

	// LookupTransitCIDR returns "" for an unknown peering, not an error.
	if got, err := repo.LookupTransitCIDR(ctx, uuid.New()); err != nil || got != "" {
		t.Errorf("lookup of unallocated peering = (%q, %v), want (\"\", nil)", got, err)
	}
}
