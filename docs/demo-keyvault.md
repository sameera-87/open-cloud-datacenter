# Key Vault demo — chunks 1 + 2

End-to-end demo of what M3 Key Vault delivers today: vault metadata + per-VPC
Private Endpoint, with a tenant VM reaching the vault through the proxy and
reading/writing secrets. Chunk 3 (real OpenBao mounts, per-vault AppRoles,
audit) is not yet implemented — secrets today land in OpenBao's shared
default `secret/` mount, authenticated with the dev-mode root token. That's
fine for proving the architecture; not for real workloads.

## Prerequisites

- **VPN to `harvester-dev`** is up
- Local dc-api running on `:18080` (`/tmp/dc-api-local`)
- Local Postgres port-forward on `:15432` (`/tmp/pgpf-watchdog.sh`)
- Local dcctl built (`/tmp/dcctl-local`) and logged in
- The shared OpenBao spike pod is running in `dc-api-vault` namespace
  (created once; persists between sessions)

Quick check:

```bash
# dc-api
curl -sS -o /dev/null -w "dc-api → %{http_code}\n" http://localhost:18080/healthz
# expected: 200

# OpenBao backend
kubectl --context harvester-dev -n dc-api-vault get pods,svc
# expected: openbao-... 1/1 Running, Service openbao ClusterIP <ip>:8200

# dcctl
/tmp/dcctl-local list vnets 2>&1 | head -3
# expected: tabular listing (or "No VNets found"), NOT "401 unauthorized"
```

If dcctl says 401 → `/tmp/dcctl-local login`.

## Step 0 — pick a stable shell for the bastion later

You'll SSH into a bastion in step 5. Keep two terminals open:

- **Terminal A** — your laptop, where dcctl + kubectl run
- **Terminal B** — for SSH'ing into the bastion when we get there

## Step 1 — Create the tenant VPC, subnet, bastion

The bastion gives you a test client *inside* the tenant VPC, so you can curl
the endpoint IP as a real tenant would.

```bash
# In Terminal A:
/tmp/dcctl-local create vnet demo-vault --address-space 10.40.0.0/16 --region lk
/tmp/dcctl-local create subnet --vnet demo-vault --name app --cidr 10.40.1.0/24
/tmp/dcctl-local create bastion --name demo-vault-bastion \
    --vnet demo-vault --subnet app \
    --save-key /tmp/demo-vault-bastion.pem
chmod 600 /tmp/demo-vault-bastion.pem
```

`dcctl bastion create` will print the bastion's mgmt IP and internal IP. Note
the **mgmt IP** (172.22.x.x) — that's the SSH target.

Verify the kube-ovn side:

```bash
kubectl --context harvester-dev get vpcs.kubeovn.io | grep demo-vault
kubectl --context harvester-dev get subnets.kubeovn.io | grep demo-vault
# expected: VPC + subnet exist, subnet CIDR 10.40.1.0/24
```

## Step 2 — Create the Key Vault

This is **just a DB row** today. No backend storage is provisioned. Status
goes to `ACTIVE` synchronously.

```bash
/tmp/dcctl-local create keyvault billing-secrets --soft-delete-days 14
# expected: prints ID, Status: ACTIVE
```

Save the vault ID for later:

```bash
KV_ID=$(/tmp/dcctl-local list keyvaults -o json 2>/dev/null \
    | python3 -c "import sys,json; print([v['id'] for v in json.load(sys.stdin) if v['name']=='billing-secrets'][0])")
echo "vault: $KV_ID"
```

Sanity-check that nothing has been deployed on the cluster — vault is just metadata:

```bash
kubectl --context harvester-dev get all -A 2>&1 | grep billing-secrets
# expected: NO MATCH — vault is just a DB row at this stage
```

## Step 3 — Create the Private Endpoint

This is where the network plumbing materialises. dc-api creates:

- A KubeOVN `Vip` reservation in the tenant subnet (in spirit — actually pinned via Multus annotation)
- A dual-NIC nginx proxy pod in `dc-api-endpoints` namespace on Harvester
- A DNS record in the tenant's per-VPC CoreDNS

```bash
/tmp/dcctl-local create keyvault-endpoint "$KV_ID" \
    --name billing --vnet demo-vault --subnet app
# expected:
#   Status:    ACTIVE
#   IP:        10.40.1.<x>  (allocated from your subnet)
#   Hostname:  billing.kv.dc.internal
```

Save what you got:

```bash
EP_IP=$(/tmp/dcctl-local list keyvault-endpoints "$KV_ID" -o json \
    | python3 -c "import sys,json; print(json.load(sys.stdin)[0]['ip_address'])")
EP_HOST=$(/tmp/dcctl-local list keyvault-endpoints "$KV_ID" -o json \
    | python3 -c "import sys,json; print(json.load(sys.stdin)[0]['hostname'])")
echo "endpoint: $EP_HOST → $EP_IP"
```

## Step 4 — Inspect the cluster-side resources

Show what `dc-api` actually provisioned:

```bash
# Proxy pod (now in dc-api-endpoints — F46 fix)
kubectl --context harvester-dev -n dc-api-endpoints get pods -l app=private-endpoint -o wide
# expected: 1 Running pod, eth0 IP on Calico pod CIDR (10.52.x.x)

# Both NICs on the proxy pod
POD=$(kubectl --context harvester-dev -n dc-api-endpoints get pod -l app=private-endpoint -o name | head -1)
kubectl --context harvester-dev -n dc-api-endpoints get $POD -o json \
    | jq -r '.metadata.annotations["k8s.v1.cni.cncf.io/network-status"]'
# expected: 2 entries
#   eth0: k8s-pod-network (Calico, 10.52.x.x)
#   net1: <ns>/<subnet-nad> (10.40.1.x, the tenant VPC NIC)

# Inside-pod view of the NICs
kubectl --context harvester-dev -n dc-api-endpoints exec $POD -- ip -4 addr | grep "inet "
# expected:
#   inet 10.52.x.x/32 scope global eth0
#   inet 10.40.1.x/24 scope global net1

# nginx forwarder config
kubectl --context harvester-dev -n dc-api-endpoints get cm $(echo $POD | sed 's|pod/||' | cut -d- -f1-2) -o yaml \
    | grep -A 3 "upstream backend"
# expected: server openbao.dc-api-vault.svc.cluster.local:8200;

# Confirm proxy can reach OpenBao on its eth0 side
kubectl --context harvester-dev -n dc-api-endpoints exec $POD -- \
    wget -qO- --timeout=5 http://openbao.dc-api-vault.svc.cluster.local:8200/v1/sys/health \
    | python3 -m json.tool | head -8
# expected: JSON with "initialized": true, "sealed": false
```

## Step 5 — Talk to the vault FROM INSIDE the tenant VPC

This is the actual demo. The tenant in their VPC has exactly one network path
to the vault — the proxy's `net1` IP. They can't reach OpenBao directly, can't
reach the proxy's cluster-side IP either.

### 5a. SSH into the bastion

```bash
# Terminal B (use the mgmt IP from step 1)
ssh -i /tmp/demo-vault-bastion.pem -o StrictHostKeyChecking=no \
    -o UserKnownHostsFile=/dev/null ubuntu@<bastion-mgmt-ip>
```

All commands below run **inside the bastion**.

### 5b. Confirm you're in the tenant VPC

```bash
ip -4 addr show enp2s0 | grep inet
# expected: inet 10.40.1.4/24 ...   (bastion's internal IP)
```

### 5c. Reach the endpoint IP

Substitute the `<EP_IP>` you saved in step 3.

```bash
curl -sS http://<EP_IP>:443/v1/sys/health | head -1
# expected: full OpenBao health JSON, status 200
```

### 5d. Write a secret

```bash
curl -sS -X POST \
    -H "X-Vault-Token: spike-root-token" \
    -H "Content-Type: application/json" \
    -d '{"data":{"db_password":"hunter2","api_key":"abc123"}}' \
    http://<EP_IP>:443/v1/secret/data/myapp
```

### 5e. Read it back

```bash
curl -sS -H "X-Vault-Token: spike-root-token" \
    http://<EP_IP>:443/v1/secret/data/myapp | python3 -m json.tool
# expected: full secret JSON containing db_password + api_key
```

## Step 6 — Negative tests (prove the isolation)

Still inside the bastion:

```bash
# Get OpenBao's ClusterIP from terminal A first:
#   kubectl --context harvester-dev -n dc-api-vault get svc openbao
# Substitute the IP below.

curl --max-time 5 -sS -o /dev/null -w "OpenBao ClusterIP direct: HTTP %{http_code} (exit=%{exitcode})\n" \
    http://<openbao-clusterip>:8200/v1/sys/health
# expected: exit=28 timeout — no L3 path from tenant VPC to Harvester pod CIDR
```

```bash
# Also try the proxy's eth0 IP directly (kubectl get pod ... -o wide gives this):
curl --max-time 5 -sS -o /dev/null -w "proxy eth0 direct: HTTP %{http_code} (exit=%{exitcode})\n" \
    http://<proxy-eth0-ip>:443/v1/sys/health
# expected: exit=28 timeout
```

```bash
# Verify the routing table — only the tenant subnet is reachable
ip route
# expected: 10.40.1.0/24 dev enp2s0 ; no routes to 10.52.x.x or 10.53.x.x
```

If both negative tests time out, network isolation is real: the tenant can
only touch the proxy's `net1` IP (which lives in their own VPC); they can't
even attempt to reach the OpenBao backend.

## Step 7 — Add a second VPC, prove the same vault is reachable from there

This proves the per-VPC Private Endpoint scaling. Same vault, different VPCs,
each with its own proxy and its own resolved IP.

```bash
# Terminal A (back on your laptop)
/tmp/dcctl-local create vnet demo-vault-2 --address-space 10.41.0.0/16 --region lk
/tmp/dcctl-local create subnet --vnet demo-vault-2 --name app --cidr 10.41.1.0/24
/tmp/dcctl-local create keyvault-endpoint "$KV_ID" --name billing --vnet demo-vault-2 --subnet app
# expected: status ACTIVE, IP 10.41.1.x  (different from the first one)

# Now there are 2 proxy pods, each in its own VPC's address space
kubectl --context harvester-dev -n dc-api-endpoints get pods -l app=private-endpoint -o wide
# expected: 2 Running pods, different IPs

# Both forward to the same OpenBao backend
kubectl --context harvester-dev -n dc-api-endpoints logs -l app=private-endpoint --tail=3
# expected: both proxies forwarding TCP to openbao backend
```

The same `KV_ID` is now reachable from two different VPCs. Same backend mount,
different network paths. Tenant in vpc-A can't reach vpc-B's proxy and vice versa.

## Step 8 — DNS resolution (works when F20 ran for the VPC)

If F20 ran for the VPC (per-VPC CoreDNS), the hostname will also resolve.

Inside the bastion:

```bash
resolvectl status enp2s0 | grep "DNS Server"
# expected: DNS Server: 10.40.1.2  (the per-VPC CoreDNS pod IP)

dig +short billing.kv.dc.internal
# expected: 10.40.1.<x>  (the EP_IP for THIS VPC's endpoint — same hostname,
#                       different IP if you're in a different VPC)

# Via the hostname instead of IP
curl -sS -H "X-Vault-Token: spike-root-token" \
    http://billing.kv.dc.internal:443/v1/secret/data/myapp | python3 -m json.tool
# expected: the secret JSON
```

If `dig +short` returns nothing, F20 didn't run for this VPC. That's an F20
issue (NAT GW image pull timing), not a chunk-2 issue. Using the IP directly
still works.

## Step 9 — Cleanup

```bash
# Terminal A
# Delete endpoints (one per VPC if you did step 7)
for EP_ID in $(/tmp/dcctl-local list keyvault-endpoints "$KV_ID" -o json \
    | python3 -c "import sys,json; [print(e['id']) for e in json.load(sys.stdin)]"); do
  /tmp/dcctl-local delete keyvault-endpoint "$EP_ID" --vault "$KV_ID" -y
done

# Then the vault
/tmp/dcctl-local delete keyvault "$KV_ID" -y

# Bastion + subnet + VNet (LIFO)
/tmp/dcctl-local delete bastion demo-vault-bastion -y
sleep 30   # let the bastion VM tear down

/tmp/dcctl-local delete subnet --vnet demo-vault app -y
sleep 15
/tmp/dcctl-local delete vnet demo-vault -y

# Same for the second VPC if step 7 ran
/tmp/dcctl-local delete subnet --vnet demo-vault-2 app -y
sleep 15
/tmp/dcctl-local delete vnet demo-vault-2 -y
```

Verify the cluster is clean:

```bash
kubectl --context harvester-dev -n dc-api-endpoints get pods -l app=private-endpoint
# expected: No resources found

kubectl --context harvester-dev get vpcs.kubeovn.io | grep demo-vault
# expected: no match

/tmp/dcctl-local list keyvaults 2>&1 | head -3
# expected: No key vaults found.
```

## What this demo proves

- The whole network primitive (Multus + per-VPC NAD + dual-NIC proxy + per-VPC IPAM) works end-to-end
- A tenant has exactly one path to the vault (their proxy's `net1`), zero paths to anyone else's
- One shared OpenBao backend can serve many proxies, each isolated to one VPC
- Same vault from multiple VPCs each gets its own proxy IP under the same hostname
- The kube-ovn 63-char label-cap (F44 fix) and the reserved-CIDR check (F45) prevent collisions

## What this demo *doesn't* prove (because chunk 3 isn't built)

- Per-vault token / AppRole isolation — today every vault shares OpenBao's `secret/` mount, authenticated by the dev-mode root token
- Per-vault mount path (`tenants/<tid>/vaults/<vault_id>`)
- Audit log of secret operations
- Soft-delete recovery window
- Production-grade OpenBao (HA Raft, persistent storage, real init/unseal)
- HTTP-mode nginx with hostname-based path rewriting (Azure Key Vault-style URLs)

Those land in chunk 3.

## Troubleshooting

### Endpoint stuck in PENDING

Most likely the local dc-api binary is stale. Rebuild + restart:

```bash
cd /Users/hiranadikari/Documents/wso2/dc/sovereign-cloud/dc-api
go build -o /tmp/dc-api-local ./cmd/dc-api
pkill -f /tmp/dc-api-local
sleep 2
bash /tmp/dc-api-local-start.sh > /tmp/dc-api-local.log 2>&1 &
sleep 5
curl -sS -o /dev/null -w "dc-api → %{http_code}\n" http://localhost:18080/healthz
```

Then delete the stuck endpoint and recreate.

### Bastion subnet stuck in DELETING

The proxy pod's LSP can pin the subnet. Wait ~30s after deleting the
endpoint before deleting the subnet. If still stuck:

```bash
kubectl --context harvester-dev get subnets.kubeovn.io | grep <stuck-subnet-name>
# If finalizer is set, force-remove (last resort):
kubectl --context harvester-dev patch subnet.kubeovn.io <subnet-name> --type=merge -p '{"metadata":{"finalizers":[]}}'
```

### OpenBao service unreachable from proxy

Verify OpenBao pod is Running:

```bash
kubectl --context harvester-dev -n dc-api-vault get pods,svc
```

If the pod is gone (cluster restart, etc.):

```bash
kubectl --context harvester-dev apply -f /tmp/openbao-spike.yaml
```

The OpenBao spike manifest lives at `/tmp/openbao-spike.yaml`. It's a dev-mode
single pod with root token `spike-root-token`. Chunk 3 replaces this with
proper HA Raft OpenBao via Terraform.
