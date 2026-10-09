# 05 — Argo Workflows

There are two kinds of WorkflowTemplate:

| Kind | Count | What it does |
|---|---|---|
| `dcapi-acc-<resource>` | one per resource (18) | Runs that resource's Go tests in one pod. Submit it on its own to test just that resource. |
| `dcapi-acc-suite` | 1 | The master. Runs every resource template in parallel in one run, then cleans up. |

```
                    ┌──────────────────────────────┐
  argo submit  ────►│ dcapi-acc-suite  (master)    │
                    └──────────────┬───────────────┘
                                   │ templateRef (template: test), all at once
       ┌──────────────┬────────────┼──────────────┬────────────────┐
       ▼              ▼            ▼              ▼                ▼
 dcapi-acc-vnet dcapi-acc-subnet dcapi-acc-nsg  …  dcapi-acc-cluster     (one per resource)
       │              │            │              │                │
       ▼              ▼            ▼              ▼                ▼
   pod: Go tests  pod: Go tests  pod: Go tests  …  pod: Go tests
                                   │
                    on exit: sweep (delete this run's leftovers)
```

## 1. The test image

One image per provider commit, tagged with the Git SHA. It contains the precompiled Go test
binary and a pinned Terraform CLI, so test pods start in seconds and download nothing.

```dockerfile
# test/acc/Dockerfile
FROM golang:1.25 AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go test -c -o /out/acctest.test ./internal/acctest

FROM hashicorp/terraform:1.15.8 AS tf

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=tf    /bin/terraform     /usr/local/bin/terraform
COPY --from=build /out/acctest.test  /opt/acc/acctest.test
ENV TF_ACC=1 \
    TF_ACC_TERRAFORM_PATH=/usr/local/bin/terraform \
    TF_IN_AUTOMATION=1 \
    CHECKPOINT_DISABLE=1
```

The provider doesn't need to be built or installed separately. `terraform-plugin-testing`
serves it in-process from the test binary ([03 §1](03-acceptance-test-framework.md#1-why-terraform-plugin-testing)).

Build and push it before a run:

```bash
TAG=$(git rev-parse --short HEAD)
docker build -f test/acc/Dockerfile -t <registry>/dcapi-acc:$TAG .
docker push <registry>/dcapi-acc:$TAG
```

Wrap this in a `make acc-image` target. If CI already builds images on every merge, let it build
this one too.

## 2. Manifest layout

```
argo/dcapi-acc/
  kustomization.yaml
  namespace.yaml                  # dcapi-tf-acc
  rbac.yaml                       # ServiceAccount dcapi-acc-runner + the Argo executor Role
  secrets.example.yaml            # placeholders only (02 §3)
  dcapi-acc-suite.yaml            # the master
  resources/
    dcapi-acc-data-sources.yaml
    dcapi-acc-vnet.yaml            dcapi-acc-subnet.yaml           dcapi-acc-nsg.yaml
    dcapi-acc-nsg-attachment.yaml  dcapi-acc-route-table.yaml      dcapi-acc-route-table-association.yaml
    dcapi-acc-vnet-peering.yaml    dcapi-acc-private-dns-zone.yaml dcapi-acc-dns-record.yaml
    dcapi-acc-key-vault.yaml       dcapi-acc-key-vault-secret.yaml dcapi-acc-private-endpoint.yaml
    dcapi-acc-virtual-machine.yaml dcapi-acc-bastion.yaml          dcapi-acc-cluster.yaml
    dcapi-acc-node-pool.yaml       dcapi-acc-service-account.yaml
```

Apply with `kubectl apply -k argo/dcapi-acc`.

## 3. A per-resource template

### 3.1 Example: `dcapi-acc-nsg`

```yaml
apiVersion: argoproj.io/v1alpha1
kind: WorkflowTemplate
metadata:
  name: dcapi-acc-nsg
spec:
  serviceAccountName: dcapi-acc-runner
  entrypoint: test
  onExit: cleanup                        # used only when this template is submitted on its own
  synchronization:
    mutexes: [{name: dcapi-acc-project}] # one run at a time (04 §2)
  arguments:
    parameters:
      - name: image                      # e.g. registry.example.com/dcapi-acc:14d3bd6
      - {name: region, value: lk}
  templates:
    - name: test
      activeDeadlineSeconds: 1800        # Go timeout + 10 min
      retryStrategy:
        limit: "1"
        retryPolicy: OnError             # pod eviction or node loss only, not test failures
      container:
        image: "{{workflow.parameters.image}}"
        command: [/opt/acc/acctest.test]
        args: ["-test.v", "-test.run=^TestAccNSG_", "-test.timeout=20m"]
        envFrom:
          - secretRef: {name: dcapi-acc-member}
        env:
          - {name: DCAPI_ACC_RUN_ID, value: "{{=sprig.trunc(-5, workflow.name)}}"}
          - {name: DCAPI_ACC_REGION, value: "{{workflow.parameters.region}}"}
        resources:
          requests: {cpu: 250m, memory: 256Mi}
          limits:   {cpu: "1",  memory: 1Gi}

    - name: cleanup
      steps:
        - - name: sweep
            templateRef: {name: dcapi-acc-suite, template: sweep}
```

How it works:
- **Parameters come from the workflow.** The `test` template reads `{{workflow.parameters.*}}`.
  When you submit this template on its own, those are its own `arguments`. When the master
  calls it, they are the master's `arguments`. So the master passes nothing to it.
- **The run ID is the same for every pod in a run.** It comes from the name of the running
  workflow, which is the master's name during a full run.
- **`onExit`, `entrypoint` and `synchronization` only apply when the template is submitted.**
  When the master calls `templateRef: {name: dcapi-acc-nsg, template: test}`, only the `test`
  template is used. The master has its own mutex and cleanup. That's why one file can serve
  both uses.
- **Tokens arrive via `envFrom`,** never as parameters. Parameters are visible in the Argo UI
  ([02 §3](02-authentication-and-test-environment.md#3-secret-storage-in-kubernetes)).
- **Terraform working directories** are created by the framework under `/tmp` and disappear
  with the pod. Nothing needs to persist between pods.

### 3.2 How the other templates differ

Every resource template is a copy of the one above with different values from §3.3. Three
kinds of change:

| Change | Templates | What to add |
|---|---|---|
| Owner credentials | service-account | `secretRef: {name: dcapi-acc-owner}` instead of `dcapi-acc-member` |
| Image and Kubernetes inputs | data-sources, virtual-machine, cluster, node-pool | The parameters from the table below in `arguments`, and matching `env` entries |
| Legacy network | virtual-machine | Optional `legacy-network` parameter (default `""`) → `DCAPI_ACC_LEGACY_NETWORK` |

| Parameter | Env var | Default | Used by |
|---|---|---|---|
| `vm-image` | `DCAPI_ACC_VM_IMAGE` | `rancher-infra/ubuntu-22-04` | data-sources, virtual-machine |
| `vm-image-display-name` | `DCAPI_ACC_VM_IMAGE_DISPLAY_NAME` | none; set it once known for the environment | data-sources |
| `cluster-image` | `DCAPI_ACC_CLUSTER_IMAGE` | `rancher-infra/rke2-ubuntu-22-04` | data-sources, cluster, node-pool |
| `k8s-version` | `DCAPI_ACC_K8S_VERSION` | `v1.33.10+rke2r3` | cluster, node-pool |

Keep the defaults the same in the resource templates and the master.

### 3.3 Values per resource

`activeDeadlineSeconds` is always the Go timeout plus 10 minutes.

| Template | Test regex | Go timeout | Credentials | Est. duration |
|---|---|---|---|---|
| dcapi-acc-data-sources | `^TestAccDataSource_` | 10m | member | 1–2 min |
| dcapi-acc-vnet | `^TestAccVNet_` | 60m | member | 15–25 min |
| dcapi-acc-subnet | `^TestAccSubnet_` | 90m | member | 30–60 min |
| dcapi-acc-nsg | `^TestAccNSG_` | 20m | member | 3–6 min |
| dcapi-acc-nsg-attachment | `^TestAccNSGAttachment_` | 90m | member | 30–60 min |
| dcapi-acc-route-table | `^TestAccRouteTable_` | 45m | member | 15–30 min |
| dcapi-acc-route-table-association | `^TestAccRouteTableAssociation_` | 90m | member | 30–60 min |
| dcapi-acc-vnet-peering | `^TestAccVNetPeering_` | 60m | member | 20–40 min |
| dcapi-acc-private-dns-zone | `^TestAccPrivateDNSZone_` | 40m | member | 10–20 min |
| dcapi-acc-dns-record | `^TestAccDNSRecord_` | 60m | member | 20–35 min |
| dcapi-acc-key-vault | `^TestAccKeyVault_` | 40m | member | 10–20 min |
| dcapi-acc-key-vault-secret | `^TestAccKeyVaultSecret_` | 40m | member | 15–25 min |
| dcapi-acc-private-endpoint | `^TestAccPrivateEndpoint_` | 60m | member | 25–45 min |
| dcapi-acc-virtual-machine | `^TestAccVirtualMachine_` | 90m | member | 40–70 min |
| dcapi-acc-bastion | `^TestAccBastion_` | 90m | member | 45–75 min |
| dcapi-acc-cluster | `^TestAccCluster_` | 120m | member | 60–90 min |
| dcapi-acc-node-pool | `^TestAccCluster_withNodePool$` | 120m | member | 60–90 min. **Not in the master** (see below) |
| dcapi-acc-service-account | `^TestAccServiceAccount_` | 30m | **owner** | 3–8 min |

The durations are estimates until the first real run. Several of them are dominated by the
last-subnet delete ([04 §3](04-test-isolation.md#3-the-last-subnet-delete)).

`dcapi-acc-node-pool` runs the cluster chain test, because node pools are tested inside it
([resources/node-pool.md](resources/node-pool.md)). It exists so someone working on node-pool
code can submit it on its own. The master leaves it out, because `dcapi-acc-cluster` already runs
the same test, and listing both would build two clusters.

## 4. The master: `dcapi-acc-suite`

```yaml
apiVersion: argoproj.io/v1alpha1
kind: WorkflowTemplate
metadata:
  name: dcapi-acc-suite
spec:
  serviceAccountName: dcapi-acc-runner
  entrypoint: all-resources
  onExit: sweep
  activeDeadlineSeconds: 10800            # 3 h hard stop for the whole run
  synchronization:
    mutexes: [{name: dcapi-acc-project}]  # one run at a time (04 §2)
  podGC: {strategy: OnPodSuccess}         # keep failed pods so their logs can be read
  arguments:
    parameters:
      - name: image
      - {name: region, value: lk}
      - {name: vm-image, value: rancher-infra/ubuntu-22-04}
      - name: vm-image-display-name
      - {name: cluster-image, value: rancher-infra/rke2-ubuntu-22-04}
      - {name: k8s-version, value: v1.33.10+rke2r3}
      - {name: legacy-network, value: ""}
  templates:
    - name: all-resources
      dag:
        failFast: false                   # one failing resource must not cancel the rest
        tasks:
          - {name: data-sources,     templateRef: {name: dcapi-acc-data-sources,            template: test}}
          - {name: vnet,             templateRef: {name: dcapi-acc-vnet,                    template: test}}
          - {name: subnet,           templateRef: {name: dcapi-acc-subnet,                  template: test}}
          - {name: nsg,              templateRef: {name: dcapi-acc-nsg,                     template: test}}
          - {name: nsg-attachment,   templateRef: {name: dcapi-acc-nsg-attachment,          template: test}}
          - {name: route-table,      templateRef: {name: dcapi-acc-route-table,             template: test}}
          - {name: rt-association,   templateRef: {name: dcapi-acc-route-table-association, template: test}}
          - {name: vnet-peering,     templateRef: {name: dcapi-acc-vnet-peering,            template: test}}
          - {name: private-dns-zone, templateRef: {name: dcapi-acc-private-dns-zone,        template: test}}
          - {name: dns-record,       templateRef: {name: dcapi-acc-dns-record,              template: test}}
          - {name: key-vault,        templateRef: {name: dcapi-acc-key-vault,               template: test}}
          - {name: kv-secret,        templateRef: {name: dcapi-acc-key-vault-secret,        template: test}}
          - {name: private-endpoint, templateRef: {name: dcapi-acc-private-endpoint,        template: test}}
          - {name: virtual-machine,  templateRef: {name: dcapi-acc-virtual-machine,         template: test}}
          - {name: bastion,          templateRef: {name: dcapi-acc-bastion,                 template: test}}
          - {name: cluster,          templateRef: {name: dcapi-acc-cluster,                 template: test}}
          - {name: service-account,  templateRef: {name: dcapi-acc-service-account,         template: test}}

    # Deletes every object named acc-<this run>-*. Also used by the resource templates' onExit.
    - name: sweep
      container:
        image: "{{workflow.parameters.image}}"
        command: [/opt/acc/acctest.test]
        args:
          - -test.v
          - -test.run=^$
          - -sweep=$(DCAPI_ACC_PROJECT_ID)
          - -sweep-allow-failures
          - -test.timeout=60m
        envFrom:
          - secretRef: {name: dcapi-acc-owner}   # owner: it may have to delete leaked SAs
        env:
          - {name: DCAPI_ACC_RUN_ID, value: "{{=sprig.trunc(-5, workflow.name)}}"}
```

No task has a `depends`. Every test is self-contained ([01 §4.2](01-scope-and-strategy.md#42-every-test-is-self-contained)),
so all 17 tasks start together. The run takes as long as the slowest resource: the cluster,
bastion or VM branch, about 1.5 hours.

**Why `failFast: false`:** by default Argo stops scheduling new tasks after the first failure.
A broken `nsg` must not stop us learning whether `vnet_peering` works. The workflow still ends
`Failed` if any task failed.

**Adding a resource** means adding its template file and one line to the task list.

## 5. Retries and timeouts

| Level | Policy | Reason |
|---|---|---|
| Test failures | **No retry** | A failed assertion or API error is a result. Retrying would hide flaky provider behaviour, such as polling that sometimes gives up too early. |
| Pod infrastructure errors | `retryPolicy: OnError`, `limit: 1` | Eviction or node loss says nothing about the provider. A test that exits non-zero is `Failed`, not `Error`, so it isn't retried. |
| Go `-test.timeout` | Per resource, §3.3 | When it fires, Go stops the binary and the framework's destroy may not run. The sweep step cleans up after it. |
| `activeDeadlineSeconds` | Go timeout + 10 min | Hard stop if the binary hangs outside Go's own timer. |
| Suite `activeDeadlineSeconds` | 3 h | A stuck run can't hold the mutex forever. |

## 6. Running the suite

```bash
# the whole suite
argo submit -n dcapi-tf-acc --from workflowtemplate/dcapi-acc-suite \
  -p image=<registry>/dcapi-acc:<sha> \
  -p vm-image-display-name="<display name>" \
  --watch

# one resource
argo submit -n dcapi-tf-acc --from workflowtemplate/dcapi-acc-nsg \
  -p image=<registry>/dcapi-acc:<sha> --watch
```

Only one run can hold the mutex at a time. A second submission waits until the first finishes.

## 7. Reading the results

The Argo UI shows one node per resource, green or red. For a red one, open its log
(`argo logs -n dcapi-tf-acc <workflow> <node>`). Each test prints `--- PASS` or `--- FAIL`, and a
failure is followed by the error.

Common failures and what they mean:

| Message | Meaning | Where to look |
|---|---|---|
| `After applying this test step, the plan was not empty` | A **perpetual diff**. The output shows the plan diff and the attribute Read maps differently from the config | The resource's Read or schema, often a missing `Computed: true` or a list-ordering issue ([07](07-provider-gaps-found.md)) |
| `Expected a non-empty plan` in a Disappears step | Read doesn't clear the ID on 404 | The resource's Read |
| `DC-API returned HTTP 409` | A leftover from an earlier run, or a name still reserved after a soft delete | Run the sweep for the earlier run ([06 §3](06-cleanup.md#3-cleaning-up-by-hand)) |
| `quota exceeded` | Leaked compute from an earlier run | [06 §3](06-cleanup.md#3-cleaning-up-by-hand) |
| `timeout while waiting for state to become 'ACTIVE'` | DC-API or the platform behind it (Harvester, Rancher, KubeOVN) is slow or stuck | Compare against the resource's timeout, and check DC-API |
| Every resource fails within seconds | Wrong token, wrong endpoint, or no network route to DC-API | [02 §6](02-authentication-and-test-environment.md#6-network-reachability) |

To get provider-level logs for a re-run, add `TF_LOG=DEBUG` to that resource template's `env`.
The client doesn't log HTTP headers, so tokens won't appear. Still, treat DEBUG logs as
sensitive: response bodies contain one-time secrets (VM `private_key`, key vault `secret_id`).

To reproduce one failure locally:

```bash
make testacc TESTARGS='-run ^TestAccNSG_rulesOrdering$ -v'
```
