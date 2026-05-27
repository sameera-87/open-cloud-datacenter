# Operations Bootstrap — DC-API on a New Datacenter

This runbook documents the manual steps required to bootstrap DC-API onto a new datacenter.
Everything here is currently done manually for the LK dev environment.
Eventually these steps will move into Terraform (see [MILESTONES.md](../MILESTONES.md) "DC-API Bootstrap").

Audience: IaaS team members setting up a new datacenter region from scratch.

---

## Prerequisites

Before starting:

- A **Harvester cluster** is already running and stable
- **Rancher** is managing Harvester (Rancher sees Harvester as a local cluster)
- VPN access to the datacenter management network (e.g., `192.168.x.x`)
- Local tools installed: `kubectl`, `helm`, `gh` (GitHub CLI)
- GitHub repo access: `HiranAdikari/sovereign-cloud` (for deployment manifests)
- Docker installed locally (to pull and inspect images)

Estimate time: 45–60 minutes for a dev environment. Production with HA will take longer.

---

## Step 1: Create the DC-API RKE2 Cluster in Rancher

DC-API runs in its own RKE2 cluster (separate from Harvester).
This cluster hosts PostgreSQL, DC-API, and the nginx ingress controller.

### Via Rancher UI

1. Open Rancher: `https://rancher.internal.wso2.com`
2. Go to **Cluster Management** → **Create**
3. Select **RKE2**
4. Select **Harvester** as the infrastructure provider
5. Fill in cluster details:
   - **Cluster Name**: `dc-api-control-plane` (or region-specific name like `dc-api-eu`)
   - **Description**: "DC-API control plane for region"
6. Under **Machine Pools**:
   - **Harvester Image**: Use an RKE2-compatible image (e.g., Ubuntu 22.04 with cloud-init)
   - **CPU Count**: 4 (dev: 2 minimum)
   - **Memory**: 8 GiB (dev: 4 GiB minimum)
   - **Disk Size**: 50 GiB
   - **Network**: Must be the **management network** (e.g., `vm-net-mgmt`)
   - **Node Count**: 1 for dev; 3 for HA production
7. Under **Networking**:
   - **Project Network**: Select the project where Harvester is running
8. Under **Add-ons**:
   - **Nginx Ingress**: **Enabled** (required; DC-API ingress depends on this)
   - **Harvester Cloud Provider**: **Enabled** (allows RKE2 VMs to get storage/networking from Harvester)
9. Click **Create**

Wait 5–10 minutes for the cluster to transition from `provisioning` → `active`.
Check Rancher UI for any stuck provisioning steps (usually in node provisioning or waiting for API).

### Merge kubeconfig locally

Once active, download the kubeconfig:

```bash
# Via Rancher UI: Cluster → Kubeconfig File → Copy to clipboard
# or download directly
CLUSTER_ID="c-xxxxx"  # from Rancher URL
curl -s -H "Authorization: Bearer $RANCHER_TOKEN" \
  "https://rancher.internal.wso2.com/v3/clusters/$CLUSTER_ID?action=generateKubeconfig" \
  | jq -r '.config' > /tmp/dcapi-rke2.yaml

# Merge into ~/.kube/config
KUBECONFIG=~/.kube/config:/tmp/dcapi-rke2.yaml kubectl config view --flatten > /tmp/merged && mv /tmp/merged ~/.kube/config

# Rename the context for clarity
kubectl config rename-context "$CLUSTER_ID" "dcapi-controlplane-rke2"

# Verify
kubectl --context dcapi-controlplane-rke2 get nodes
```

Expected output: one or three nodes in `Ready` status.

---

## Step 2: Create Harvester IP Pool for Ingress LoadBalancer

The RKE2 nginx ingress controller needs a static IP from the Harvester management network.
This IP will be assigned to the nginx LoadBalancer service.

### Create the IP pool on Harvester

First, switch to the **Harvester** cluster context:

```bash
kubectl config use-context harvester-dev  # or your Harvester context name
```

Create the IP pool. Edit this manifest with your network details, then apply:

```yaml
# harvester-ippool.yaml
apiVersion: network.harvesterhci.io/v1beta1
kind: IPPool
metadata:
  name: dc-api-ingress-pool
  namespace: harvester-public  # or your Harvester network namespace
spec:
  ipv4Config:
    pools:
      - start: 192.168.10.37
        end: 192.168.10.37
    serverIP: 192.168.10.1      # Your management network gateway
    cidr: 192.168.10.0/24
    excludedAddresses: []
  description: "DC-API ingress LB IP"
  # The 'scope' field ties this pool to the RKE2 cluster's namespace
  scope:
    project: "c-xxxxx"  # Replace with Rancher project ID of your dc-api cluster
```

**Where to find the Rancher project ID:**

In the Rancher UI, go to your dc-api cluster → Namespaces.
Click into any namespace and check the URL: `https://rancher.../p/c-xxxxx/explorer`.
The `c-xxxxx` is the project ID.

Or fetch it via kubectl:

```bash
kubectl --context dcapi-controlplane-rke2 get namespace dc-system -o jsonpath='{.metadata.annotations.project\.cattle\.io/projectId}' 2>/dev/null || \
  echo "Project ID not yet set; check Rancher UI"
```

Apply the IP pool to Harvester:

```bash
kubectl --context harvester-dev apply -f harvester-ippool.yaml
```

Verify:

```bash
kubectl --context harvester-dev get ippool
# Should show: dc-api-ingress-pool with status Ready
```

---

## Step 3: Create nginx LoadBalancer Service

Switch back to the **DC-API RKE2 cluster**:

```bash
kubectl config use-context dcapi-controlplane-rke2
```

Create a LoadBalancer service that ties the nginx ingress to the Harvester IP pool.
This is a **one-time setup** — nginx ingress is usually auto-installed, but it may not have the LoadBalancer type.

```yaml
# ingress-lb.yaml
apiVersion: v1
kind: Service
metadata:
  name: ingress-expose
  namespace: kube-system
  annotations:
    network.harvesterhci.io/ipPool: "dc-api-ingress-pool"
spec:
  type: LoadBalancer
  selector:
    app.kubernetes.io/instance: nginx-ingress
    app.kubernetes.io/name: ingress-nginx
  ports:
    - name: http
      port: 80
      protocol: TCP
      targetPort: 80
    - name: https
      port: 443
      protocol: TCP
      targetPort: 443
```

Apply:

```bash
kubectl apply -f ingress-lb.yaml
```

Wait for an external IP to be assigned:

```bash
kubectl get svc -n kube-system ingress-expose --watch
# Wait for EXTERNAL-IP column to show 192.168.10.37
```

Expected output:
```
NAME             TYPE           CLUSTER-IP      EXTERNAL-IP     PORT(S)
ingress-expose   LoadBalancer   10.43.x.x       192.168.10.37   80:xxxxx/TCP,443:xxxxx/TCP
```

---

## Step 4: Create Namespaces and ConfigMap

All DC-API components live in the `dc-system` namespace.

### Create namespace

```yaml
# namespace.yaml
apiVersion: v1
kind: Namespace
metadata:
  name: dc-system
  labels:
    app: dc-api
```

Apply:

```bash
kubectl apply -f namespace.yaml
```

### Create ConfigMap

The ConfigMap holds non-secret DC-API environment variables.
See [CLAUDE.md](../CLAUDE.md) "Environment Variables (DC-API)" for the full list.

```yaml
# configmap.yaml
apiVersion: v1
kind: ConfigMap
metadata:
  name: dc-api-config
  namespace: dc-system
data:
  DCAPI_LOG_LEVEL: "info"
  DCAPI_LISTEN_ADDR: ":8080"
  DCAPI_HARVESTER_NAMESPACE: "harvester-vms"  # or your namespace
  DCAPI_RANCHER_INSECURE: "false"  # set to "true" if using self-signed certs in dev
  DCAPI_VM_PROVIDER: "harvester"
  DCAPI_CLUSTER_PROVIDER: "rancher"
  DCAPI_TENANT_GROUP_PREFIX: "dc-tenant-"     # Asgardeo groups like "dc-tenant-acme"
  DCAPI_ADMIN_GROUP: "dc-admin"               # Asgardeo group for platform admins
  # Add any other non-secret DCAPI_* variables here
```

Apply:

```bash
kubectl apply -f configmap.yaml
```

---

## Step 5: Create Secrets

Secrets hold sensitive values (database password, API tokens, kubeconfigs).
**Never commit these to git.**

Create them interactively using `create-secrets.sh` or manually via `kubectl`:

### Option A: Interactive script (recommended)

```bash
cd dc-api/deploy && ./create-secrets.sh --context dcapi-controlplane-rke2
```

Provide these values when prompted:

1. **PostgreSQL password** (anything; used to build `DCAPI_DB_URL`)
2. **Asgardeo OIDC client ID** (from Asgardeo UI → Applications → DC-API → Client ID)
3. **Asgardeo OIDC audience** (same as client ID)
4. **Path to Harvester kubeconfig** (e.g., `~/.kube/harvester-dev.yaml`)
5. **Rancher API token** (Rancher UI → User Settings → API Keys → Create; no scope, no expiry)
6. **Rancher base URL** (e.g., `https://rancher.internal.wso2.com`)
7. **Operator SSH public key** (optional; for break-glass access)
8. **Operator console password** (optional; for VNC access)

The script will create a `Secret` resource in `dc-system` with all values base64-encoded.

### Option B: Manual kubectl

```bash
# Encode the Harvester kubeconfig (base64)
base64 < ~/.kube/harvester-dev.yaml > /tmp/hc.b64

# Create the secret
kubectl --context dcapi-controlplane-rke2 create secret generic dc-api-secrets \
  -n dc-system \
  --from-literal=DCAPI_DB_URL="postgres://dc_api:PASSWORD@postgres:5432/dc_api?sslmode=disable" \
  --from-literal=DCAPI_OIDC_ISSUER="https://api.asgardeo.io/t/wso2" \
  --from-literal=DCAPI_OIDC_AUDIENCE="YOUR_CLIENT_ID" \
  --from-literal=DCAPI_HARVESTER_KUBECONFIG="$(base64 < ~/.kube/harvester-dev.yaml)" \
  --from-literal=DCAPI_RANCHER_URL="https://rancher.internal.wso2.com" \
  --from-literal=DCAPI_RANCHER_TOKEN="token-xxxxx:yyyyy"
```

Verify:

```bash
kubectl --context dcapi-controlplane-rke2 get secrets -n dc-system
# Should show: dc-api-secrets
```

---

## Step 6: Create GitHub Container Registry (GHCR) Image Pull Secret

DC-API is built and pushed to `ghcr.io/hiranadikari/dc-api`.
The image is private; Kubernetes needs credentials to pull it.

### Create GitHub Personal Access Token (PAT)

1. Go to [github.com/settings/tokens](https://github.com/settings/tokens)
2. Click **Generate new token** → **Generate new token (classic)**
3. Scopes: select **`read:packages`** only
4. No expiry (or set long expiry)
5. Generate and copy the token (you'll use it once)

### Create the image pull secret

```bash
kubectl --context dcapi-controlplane-rke2 create secret docker-registry ghcr-pull-secret \
  -n dc-system \
  --docker-server=ghcr.io \
  --docker-username=HiranAdikari \
  --docker-password=<GITHUB_PAT> \
  --docker-email=hiranadikari993@gmail.com
```

Verify:

```bash
kubectl --context dcapi-controlplane-rke2 get secrets -n dc-system ghcr-pull-secret
```

---

## Step 7: Deploy PostgreSQL

DC-API needs a PostgreSQL 16+ database.
We run it as a StatefulSet in the same cluster for simplicity.
(Production would use a managed PostgreSQL service.)

```yaml
# postgres.yaml
apiVersion: v1
kind: Service
metadata:
  name: postgres
  namespace: dc-system
  labels:
    app: postgres
spec:
  clusterIP: None  # Headless service for StatefulSet
  selector:
    app: postgres
  ports:
    - port: 5432
      targetPort: 5432
---
apiVersion: apps/v1
kind: StatefulSet
metadata:
  name: postgres
  namespace: dc-system
spec:
  serviceName: postgres
  replicas: 1
  selector:
    matchLabels:
      app: postgres
  template:
    metadata:
      labels:
        app: postgres
    spec:
      containers:
        - name: postgres
          image: postgres:16-alpine
          ports:
            - containerPort: 5432
          env:
            - name: POSTGRES_USER
              value: "dc_api"
            - name: POSTGRES_PASSWORD
              valueFrom:
                secretKeyRef:
                  name: dc-api-secrets
                  key: DCAPI_DB_PASSWORD
            - name: POSTGRES_DB
              value: "dc_api"
          volumeMounts:
            - name: pgdata
              mountPath: /var/lib/postgresql/data
          livenessProbe:
            exec:
              command: ["pg_isready", "-U", "dc_api"]
            initialDelaySeconds: 30
            periodSeconds: 10
  volumeClaimTemplates:
    - metadata:
        name: pgdata
      spec:
        accessModes: ["ReadWriteOnce"]
        storageClassName: "harvester"  # Harvester storage class
        resources:
          requests:
            storage: 20Gi
```

Apply:

```bash
kubectl apply -f postgres.yaml
```

Wait for the StatefulSet to be ready:

```bash
kubectl get statefulset -n dc-system postgres --watch
# Wait for: postgres   1/1   1            1           ~1m
```

Verify database connectivity from inside the cluster:

```bash
kubectl run -it --rm debug --image=postgres:16-alpine --restart=Never -n dc-system -- \
  psql -h postgres -U dc_api -d dc_api -c "SELECT version();"
```

---

## Step 8: Deploy DC-API Deployment

The Deployment runs the DC-API binary, pulls config from ConfigMap and secrets, and exposes port 8080 internally.

```yaml
# deployment.yaml
apiVersion: apps/v1
kind: Deployment
metadata:
  name: dc-api
  namespace: dc-system
  labels:
    app: dc-api
spec:
  replicas: 1
  strategy:
    type: RollingUpdate
    rollingUpdate:
      maxSurge: 1
      maxUnavailable: 0
  selector:
    matchLabels:
      app: dc-api
  template:
    metadata:
      labels:
        app: dc-api
    spec:
      imagePullSecrets:
        - name: ghcr-pull-secret
      containers:
        - name: dc-api
          image: ghcr.io/hiranadikari/dc-api:latest  # or a specific SHA tag
          imagePullPolicy: Always
          ports:
            - containerPort: 8080
              name: http
          envFrom:
            - configMapRef:
                name: dc-api-config
            - secretRef:
                name: dc-api-secrets
          livenessProbe:
            httpGet:
              path: /health
              port: 8080
            initialDelaySeconds: 10
            periodSeconds: 10
            timeoutSeconds: 5
            failureThreshold: 3
          readinessProbe:
            httpGet:
              path: /health
              port: 8080
            initialDelaySeconds: 5
            periodSeconds: 5
            timeoutSeconds: 3
            failureThreshold: 2
          resources:
            requests:
              cpu: 250m
              memory: 512Mi
            limits:
              cpu: 1000m
              memory: 1Gi
          securityContext:
            readOnlyRootFilesystem: true
            runAsNonRoot: true
            runAsUser: 65534
            allowPrivilegeEscalation: false
---
apiVersion: v1
kind: Service
metadata:
  name: dc-api
  namespace: dc-system
  labels:
    app: dc-api
spec:
  type: ClusterIP
  selector:
    app: dc-api
  ports:
    - port: 8080
      targetPort: 8080
      protocol: TCP
```

Apply:

```bash
kubectl apply -f deployment.yaml
```

Wait for the deployment to roll out:

```bash
kubectl rollout status deployment/dc-api -n dc-system
```

Check logs:

```bash
kubectl logs -f deployment/dc-api -n dc-system
# Should show: "DC-API starting", "PostgreSQL migration successful", etc.
```

---

## Step 9: Create nginx Ingress

Expose DC-API over HTTPS via the nginx ingress controller and the LoadBalancer service from Step 3.

```yaml
# ingress.yaml
apiVersion: networking.k8s.io/v1
kind: Ingress
metadata:
  name: dc-api
  namespace: dc-system
  annotations:
    cert-manager.io/cluster-issuer: "letsencrypt-prod"  # if using cert-manager
    nginx.ingress.kubernetes.io/rewrite-target: /
spec:
  ingressClassName: nginx
  rules:
    - host: dcapi.lk.internal.wso2.com
      http:
        paths:
          - path: /
            pathType: Prefix
            backend:
              service:
                name: dc-api
                port:
                  number: 8080
  tls:
    - hosts:
        - dcapi.lk.internal.wso2.com
      secretName: dcapi-tls
```

Apply:

```bash
kubectl apply -f ingress.yaml
```

Verify:

```bash
kubectl get ingress -n dc-system
# Should show: dc-api with HOSTS and ADDRESS
```

---

## Step 10: Set DNS / Hosts Entry

Add the ingress IP to your local `/etc/hosts` or your DNS server:

```bash
# /etc/hosts (local machine, VPN connected)
192.168.10.37  dcapi.lk.internal.wso2.com

# Or via DNS (if you have a DNS server managing the domain)
A record: dcapi.lk.internal.wso2.com → 192.168.10.37
```

Test connectivity:

```bash
curl -i https://dcapi.lk.internal.wso2.com/health
# Expected: 200 OK
```

---

## Step 11 (Optional): Set Up GitHub Actions Self-Hosted Runner

The GitHub Actions workflow in `.github/workflows/deploy.yaml` builds and deploys DC-API automatically on every push to `main` touching `dc-api/`.

To avoid exposing Kubernetes APIs publicly, run the runner **inside the cluster** using GitHub Actions Runner Controller (ARC).

### Install ARC Helm controller

```bash
helm repo add gha-runner-scale-set https://actions-runner-controller.github.io/actions-runner-controller
helm repo update

helm install arc \
  --kube-context dcapi-controlplane-rke2 \
  --namespace arc-systems --create-namespace \
  gha-runner-scale-set/gha-runner-scale-set-controller
```

### Create a GitHub PAT for the runner

1. Go to [github.com/settings/tokens](https://github.com/settings/tokens)
2. **Generate new token (classic)**
3. Scopes: **`repo`** (full control) + **`workflow`** (if needed)
4. Generate and copy

### Install the runner scale set

```bash
helm install dc-runner \
  --kube-context dcapi-controlplane-rke2 \
  --namespace arc-runners --create-namespace \
  --set githubConfigUrl="https://github.com/HiranAdikari/sovereign-cloud" \
  --set githubConfigSecret.github_token="<PAT>" \
  --set runnerScaleSetName="dc-runner" \
  --set containerMode.type="dind" \
  gha-runner-scale-set/gha-runner-scale-set
```

Verify:

```bash
kubectl get runner -n arc-runners --watch
# Should auto-scale up when jobs are queued
```

Check runner status in GitHub UI: **Settings** → **Actions** → **Runners** → You should see `dc-runner` as an "available" self-hosted runner.

---

## Step 12: Trigger the First Deploy

Push a change to `dc-api/` on the `main` branch. The GitHub Actions workflow will:

1. Build the Docker image: `ghcr.io/hiranadikari/dc-api:<commit-sha>`
2. Push to GHCR
3. Deploy to the cluster via `kubectl apply`

Monitor the deployment:

```bash
# Via GitHub Actions UI
# or
kubectl rollout status deployment/dc-api -n dc-system --watch
```

---

## Verification Checklist

Once everything is deployed, verify each component:

```bash
# Contexts and clusters
kubectl config get-contexts | grep dcapi

# Namespaces and resources
kubectl --context dcapi-controlplane-rke2 get all -n dc-system

# PostgreSQL is running and migrated
kubectl logs -n dc-system statefulset/postgres

# DC-API is running and healthy
kubectl logs -n dc-system deployment/dc-api | head -50

# Ingress is configured
kubectl get ingress -n dc-system
curl -i https://dcapi.lk.internal.wso2.com/health

# LoadBalancer service has an external IP
kubectl get svc -n kube-system ingress-expose
```

---

## Troubleshooting

### DC-API pod is crashing

Check logs:

```bash
kubectl logs -n dc-system deployment/dc-api --tail=100
```

Common issues:

- **"failed to load configuration"** → Missing or wrong DCAPI_* environment variables. Check `configmap.yaml` and `secret` exist.
- **"failed to connect to PostgreSQL"** → Database pod not ready or DCAPI_DB_URL is wrong. Verify `postgres` StatefulSet is running.
- **"failed to initialise OIDC"** → DCAPI_OIDC_ISSUER unreachable or invalid token. Check network access and Asgardeo configuration.

### Ingress shows no IP address

```bash
kubectl describe ingress dc-api -n dc-system
```

If EXTERNAL-IP is pending:

1. Check LoadBalancer service has an external IP: `kubectl get svc -n kube-system ingress-expose`
2. If no external IP, Harvester IP pool may not be properly scoped to the cluster. Verify the project ID in `harvester-ippool.yaml`.

### Cannot reach `dcapi.lk.internal.wso2.com`

1. Verify `/etc/hosts` entry or DNS resolution: `nslookup dcapi.lk.internal.wso2.com`
2. Verify VPN is connected to the management network
3. Ping the IP: `ping 192.168.10.37`
4. Curl the service: `curl -v https://dcapi.lk.internal.wso2.com/health` (may show TLS errors if cert-manager not configured)

### GitHub Actions runner doesn't appear

Check ARC controller logs:

```bash
kubectl logs -n arc-systems deployment/gha-runner-scale-set-controller
```

Ensure the GitHub PAT has the correct scopes and the repository URL is correct.

---

## Future: Terraform Bootstrap

All steps above will eventually be codified as Terraform in `wso2-datacenter-project` bootstrap layer.
Target: New datacenter regions (EU, US) require zero manual steps—just `terraform apply`.

See [MILESTONES.md](../MILESTONES.md) "DC-API Bootstrap" for tracking.

---

## Next Steps After Bootstrap

1. Create Asgardeo groups and users (see [asgardeo-setup.md](./asgardeo-setup.md))
2. Test `dcctl login` from your local machine
3. Create your first VM: `dcctl create vm --name test-01 --size small`
4. Run the E2E test suite (see [MILESTONES.md](../MILESTONES.md))

---

## References

- [DC-API Architecture](../CLAUDE.md)
- [Asgardeo Setup Guide](./asgardeo-setup.md)
- [Milestones & Current Status](../MILESTONES.md)
