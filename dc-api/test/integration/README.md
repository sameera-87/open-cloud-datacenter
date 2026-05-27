# M2 Network Integration Tests (Phase 1)

Tests run against the live lk-dev Harvester cluster and a testcontainers-spawned
Postgres 16 instance. DC-API runs in-process via `httptest.NewServer`.

## Prerequisites

| Requirement | Check |
|---|---|
| Docker running | `docker info` |
| `KUBECONFIG` set | `export KUBECONFIG=$HOME/.kube/config` |
| `KUBE_CONTEXT` set | `export KUBE_CONTEXT=harvester-dev` |
| VPN connected | `kubectl --context=harvester-dev get vpc.kubeovn.io` |
| KubeOVN CRDs registered | `kubectl get crd vpcs.kubeovn.io` |

## Run

```bash
export KUBECONFIG=$HOME/.kube/config
export KUBE_CONTEXT=harvester-dev
cd dc-api
go test -tags integration -v ./test/integration/... -count=1 -timeout 15m
```

Watch resources cycle on the cluster from another terminal:

```bash
watch 'kubectl --context=harvester-dev get vpc.kubeovn.io,subnet.kubeovn.io,vpc-dnses.kubeovn.io 2>&1 | grep "test-"'
```

## Test-mode JWT

`middleware.NewTestModeAuth` accepts JWTs signed with a per-run RSA-2048 key
generated in memory. Never persisted. Production Asgardeo path is unchanged
(this is gated on the test framework only constructing a `TestModeAuth`
instance — production builds construct `*Auth` from `NewAuth(ctx, issuer, ...)`).

## Phase 2

Reconciler-restart-resilience tests are deferred to Phase 2 (requires
extending the reconciler to poll network resources).

## Cleanup

- Each test calls `t.Cleanup(...)` to delete what it created
- Suite startup runs a "scrub" that removes any KubeOVN `Vpc`/`Subnet` matching
  `test-*` older than 1 hour (catches debris from crashed runs)
- testcontainers Postgres is terminated automatically when the test binary exits
- **F24 cluster sweep** — `mustCreateActiveVNet`'s `t.Cleanup` calls
  `SweepKubeOVNVPC` after the API DELETE, force-removing the kubeovn Vpc +
  its Subnets even when the API path times out. Belt-and-braces against
  the slow-teardown leak that built up ~50 zombie VPCs before 2026-05-12.

### Operator-runnable zombie sweep

When a previous run leaked VPCs and the cluster is now degraded
(`kube-ovn-controller` slow to reconcile new resources), purge them with:

```bash
KUBECONFIG=$HOME/.kube/config KUBE_CONTEXT=harvester-dev \
DCAPI_ZOMBIE_SWEEP=1 \
go test -tags integration -timeout 5m \
    -run TestZombieSweep ./test/integration/...
```

This walks every kubeovn `Vpc` with `dc-api/managed=true` whose
`dc-api/tenant` label matches one of the test prefixes (`test-`,
`test-tenant-`, `vnet-test-tenant-`, `vnet-admin-test-`) and force-deletes
it plus its child Subnets. Override the prefix set with
`DCAPI_ZOMBIE_SWEEP_PREFIXES=foo-,bar-`.
