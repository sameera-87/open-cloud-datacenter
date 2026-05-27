---
title: "Recovering dcapi-controlplane-rke2 from rke2-server crashloop"
audience: operator
last_verified: 2026-05-18
---

# Recovering `dcapi-controlplane-rke2` from rke2-server crashloop

The dc-api control plane runs on a single-node RKE2 cluster
(`dcapi-controlplane-rke2`) inside Harvester. It hosts every cluster-side
component the project depends on: dc-api, postgres, cloud-ui, the ARC runner
controller, and the Rancher cluster agent. Because it's a single node, a
broken `rke2-server` service takes everything offline at once — there's no
HA failover.

This runbook documents the recovery for the most common failure mode we've
seen: `rke2-server` crashlooping with `context deadline exceeded` talking to
its local etcd, leaving zombie etcd / kube-apiserver processes from a
previous lifecycle squatting on `:2379` / `:6443`.

## Symptoms that point here

- `kubectl --context=dcapi-controlplane-rke2 ...` times out (whether you go
  through Rancher's proxy or hit the apiserver directly).
- CI runs queued indefinitely with `total_count: 0` from
  `gh api repos/<org>/<repo>/actions/runners`.
- `kubectl apply` against the cluster returns `Error from server (Timeout)`
  for read-after-write operations like `apply -f configmap.yaml`.
- TCP to `192.168.10.38:80` returns 200 (ingress nginx is alive) but
  `:6443` times out (apiserver is dead).
- Harvester reports the VM as `Running` — the VM itself is fine; the
  Kubernetes control plane inside it is not.

## Confirm before acting

SSH to the node:

```bash
ssh ubuntu@192.168.10.38   # password is in lk-dev operator notes
```

Then check:

```bash
sudo systemctl is-active rke2-server
# Expect:  active  (healthy) | activating (mid-restart) | failed (gave up)

# Restart counter — high numbers mean systemd has been crashloop-restarting
sudo systemctl show rke2-server --property=NRestarts,ActiveEnterTimestamp,InactiveEnterTimestamp

# What's been failing? Look for "level=fatal" lines:
sudo journalctl -u rke2-server -n 100 --no-pager | grep -E "level=fatal|preparing server"

# Are zombie etcd + apiserver still bound to control-plane ports from a
# previous lifecycle?
sudo ss -tlnp | grep -E ":2379|:2380|:6443"
ps -eo pid,etime,cmd --sort=-etime | grep -E "^\s*\d+\s+\d+-" | grep -E "etcd|kube-apiserver" | grep -v grep
```

A zombie shows up as a process owning the port whose `etime` is hours/days
old and which is NOT a child of the current `rke2-server`. The smoking gun:
the `rke2-server` journal says it can't talk to `127.0.0.1:2379` but
`ss -tlnp` shows something is listening there.

## Recovery — destructive but safe

These steps tear down ALL pods on the node (including dc-api, postgres,
cloud-ui, ARC runners) and bring them back from manifests. Tenant CRUD
will be unavailable for ~3–5 minutes. PVC-backed state survives.

```bash
# 1. Stop the systemd unit so it stops spawning new rke2 processes that
#    immediately fatal. NRestarts will stop incrementing.
sudo systemctl stop rke2-server

# 2. Kill the zombie processes by PID (from the ps output above).
#    Example PIDs from the 2026-05-18 incident:
sudo kill -9 <etcd-pid> <kube-apiserver-pid>

# 3. Belt-and-suspenders cleanup of dangling containerd shims, network
#    mounts, and pod cgroups. RKE2 ships this script for exactly this
#    purpose.
sudo /usr/local/bin/rke2-killall.sh

# 4. Confirm the control-plane ports are released — empty output is good.
sudo ss -tlnp | grep -E ":2379|:2380|:6443"

# 5. Bring rke2 back up. systemd will start kubelet, kubelet will read
#    static pod manifests from /var/lib/rancher/rke2/agent/pod-manifests/
#    and recreate etcd, kube-apiserver, kube-scheduler,
#    kube-controller-manager as containers under containerd.
sudo systemctl start rke2-server
```

## Verifying recovery

Wait ~60s after the start, then verify in this order:

```bash
# A. systemd should be active, not activating
sudo systemctl is-active rke2-server   # expect: active

# B. All four control-plane containers should be Running with 0–1 restarts
sudo /var/lib/rancher/rke2/bin/crictl \
  --runtime-endpoint unix:///run/k3s/containerd/containerd.sock ps \
  | grep -E "etcd|kube-apiserver|kube-scheduler|kube-controller-manager"

# C. Apiserver answers locally (401 here is fine — means it's alive and
#    asking for auth)
curl -ksS -o /dev/null -w "%{http_code}\n" https://127.0.0.1:6443/healthz

# D. Use the embedded kubelet kubeconfig to inspect the cluster
sudo KUBECONFIG=/etc/rancher/rke2/rke2.yaml \
  /var/lib/rancher/rke2/bin/kubectl get nodes

# E. Workload pods need to come back — postgres has to do WAL recovery
#    before becoming Ready, ~30–90s. dc-api will crashloop while postgres
#    is unready (the dc-postgres headless Service returns NXDOMAIN
#    because k8s only puts Ready pods into headless Endpoints).
sudo KUBECONFIG=/etc/rancher/rke2/rke2.yaml \
  /var/lib/rancher/rke2/bin/kubectl -n dc-system get pods

# F. If dc-api is still crashlooping after postgres is Ready, force a
#    restart so it picks up the now-valid DNS resolution:
sudo KUBECONFIG=/etc/rancher/rke2/rke2.yaml \
  /var/lib/rancher/rke2/bin/kubectl -n dc-system delete pod -l app=dc-api --now
```

Then from your laptop (on VPN), verify external paths:

```bash
# Rancher's reverse-tunnel agent (cattle-cluster-agent) takes ~1–2 min to
# reconnect after the killall. Once it does, the standard kubectl context
# works again:
kubectl --context=dcapi-controlplane-rke2 get nodes

# GitHub runners come back automatically — the ARC controller spawns a
# listener pod, which registers with GitHub.
gh api repos/<org>/<repo>/actions/runners | jq '.total_count'

# In-flight queued jobs from BEFORE the listener restart will NOT be
# picked up (they're stuck at GitHub's broker on a message ID the listener
# no longer knows about). Cancel them and trigger fresh ones:
for run_id in $(gh run list --branch main --status queued --json databaseId -q '.[].databaseId'); do
  gh run cancel $run_id
done
# Then push any innocuous commit, or re-push with --force, to retrigger.
```

## Snapshot restore (if step 5 doesn't recover)

If after `systemctl start rke2-server` the journal keeps fataling with
`context deadline exceeded` to etcd AND no etcd container ever appears
in `crictl ps`, the etcd data on disk is corrupted. Restore from the most
recent snapshot:

```bash
# 1. Identify the snapshot — RKE2 takes one every 12h by default
sudo ls -lrth /var/lib/rancher/rke2/server/db/snapshots/

# 2. Stop rke2-server (should already be stopped at this point)
sudo systemctl stop rke2-server

# 3. Restore. This wipes the current etcd member directory and
#    bootstraps a new etcd from the snapshot file. Note: any cluster
#    state created since the snapshot is lost. For a snapshot taken
#    at 12:00 UTC and recovery at 17:00 UTC, that's 5h of CRUD.
sudo /usr/local/bin/rke2 server \
  --cluster-reset \
  --cluster-reset-restore-path=/var/lib/rancher/rke2/server/db/snapshots/etcd-snapshot-<NAME>

# 4. Wait for "etcd is now running" then Ctrl-C
# 5. Start the systemd unit normally
sudo systemctl start rke2-server
```

See the RKE2 docs for the full disaster-recovery flow:
<https://docs.rke2.io/backup_restore>

## Root-cause notes from the 2026-05-18 incident

- `NRestarts=526` accumulated before recovery — rke2-server had been
  crashing every ~15s for several hours.
- Earliest fatal in journal was a `leaderelection lost for rke2-etcd` on
  2026-05-09. These were transient and recoverable.
- The shift to permanent crashloop happened on 2026-05-18 at 16:11 UTC:
  the fatal message changed from `leaderelection lost` to
  `failed to bootstrap cluster data: failed to reconcile with local
  datastore: context deadline exceeded`.
- The zombie etcd (PID 115870) and apiserver (PID 119400) were 9 days
  old — they had survived earlier rke2 restarts because systemd's
  cgroup cleanup doesn't reliably reach into the containerd shim's
  process tree. Once etcd became unhealthy internally (probably from
  prolonged disk-IO pressure or a memory-mapped file going stale), every
  subsequent rke2 startup tried to talk to it, timed out, and exited
  before it could spawn a replacement.

## Hardening to consider

1. **Bump postgres readiness probe tolerance** — current
   `pg_isready` with `timeoutSeconds: 1` regularly flaps. Recommend
   `timeoutSeconds: 3, periodSeconds: 5, failureThreshold: 5`.
2. **Etcd defrag job** — schedule a weekly etcd defrag cronjob to keep
   the DB compact and reduce per-write fsync latency.
3. **Resource requests for the VM** — the harvester VM hosting this
   cluster has no defined resource floor; etcd is sensitive to fsync
   latency, which jumps when the VM is starved. Pin a baseline.
4. **HA control plane** — long term, this should be 3 nodes, not 1.
   Single-node RKE2 is a development convenience that has become a
   demo-day risk.
