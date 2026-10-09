# dcapi-acc Argo WorkflowTemplates

Argo manifests for the provider's acceptance test suite. The design is in
[docs/testsuite/05-argo-workflows.md](../../docs/testsuite/05-argo-workflows.md).

| File | What it is |
|---|---|
| `dcapi-acc-suite.yaml` | The master. Runs every resource's `test` template in parallel, then the `sweep` step on exit. |
| `resources/dcapi-acc-<resource>.yaml` | One per resource (18). Submit one on its own to test just that resource. |
| `namespace.yaml`, `rbac.yaml` | Namespace `dcapi-tf-acc` and the pod ServiceAccount `dcapi-acc-runner`. |
| `secrets.example.yaml` | Placeholders for the `dcapi-acc-member` and `dcapi-acc-owner` Secrets. Not applied by kustomize. |

## Install

```bash
kubectl apply -k argo/dcapi-acc
# then create the two Secrets from your secret manager (docs/testsuite/02 §3)
```

## Run

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

All templates share the mutex `dcapi-acc-project`, so a second submission waits for the first.
The run ID passed to the tests (`DCAPI_ACC_RUN_ID`) is the last 5 characters of the workflow name.

## Adding a resource

1. Copy a file in `resources/` and change the name, `-test.run` regex, `-test.timeout` and
   `activeDeadlineSeconds` (Go timeout + 10 min).
2. Add it to `kustomization.yaml`.
3. Add one task line to `all-resources` in `dcapi-acc-suite.yaml`.

Keep parameter defaults (`region`, `vm-image`, `cluster-image`, `k8s-version`) the same here
and in the master.

## Validate

```bash
argo lint --offline --kinds=workflowtemplates dcapi-acc-suite.yaml resources/*.yaml
kubectl kustomize argo/dcapi-acc > /dev/null
```
