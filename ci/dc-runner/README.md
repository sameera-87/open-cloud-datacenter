# dc-runner

Custom self-hosted GitHub Actions runner image used by every workflow in
this repo. Extends `ghcr.io/actions/actions-runner:latest` with the build
+ scripting tooling that minimal image deliberately omits.

## What it adds

| Tool | Source | Notes |
|---|---|---|
| `make`, `build-essential` | Ubuntu apt | unblocks Makefile-driven projects (kubebuilder operators) |
| `kubectl` | dl.k8s.io | pinned to match the cluster's K8s version |
| `helm` | get.helm.sh | unpinned API; pin a stable version for reproducibility |
| `yq` | mikefarah/yq GitHub release | Go reimplementation (not the Python jq-style) |
| `jq`, `git`, `curl`, `wget`, `unzip`, `python3`, `python3-pip` | Ubuntu apt | the basic toolkit a hosted runner ships with |

## Where it runs

ARC (Actions Runner Controller) on the `dcapi-controlplane-rke2` cluster.
The `gha-runner-scale-set` HelmRelease that templates the runner pods is
defined in OCD at
[`flux/platform/arc/base/helmrelease-runner.yaml`](https://github.com/HiranAdikari/open-cloud-datacenter/blob/spike/flux-gitops/flux/platform/arc/base/helmrelease-runner.yaml).
That HelmRelease points `template.spec.containers[0].image` at this image's
published `:latest` tag.

## How it's built

`.github/workflows/dc-runner-image.yaml` builds and pushes on every change
under `ci/dc-runner/**`. The workflow runs on `ubuntu-latest`
(GitHub-hosted, not on the self-hosted dc-runner) — that's deliberate, to
break the chicken-and-egg: this image is what makes the self-hosted runner
useful, so the build can't depend on the self-hosted runner being healthy.

## Bumping tool versions

Edit the `ARG` lines at the top of `Dockerfile`, commit, push. CI rebuilds
`:latest` automatically. To force ARC to pick up the new image without
waiting for natural pod churn:

```bash
kubectl --context=dcapi-controlplane-rke2 \
        -n arc-runners delete pod -l actions.github.com/scale-set-name=dc-runner
```

Pods come back up with the new image (kubelet re-pulls `:latest`).

## When to add a new tool

Add to this image when **two or more workflows** would need it. For
one-off tooling needs, install in the workflow step — keeps the runner
image bounded. If you find yourself copy-pasting a `curl | tar` block
across workflows, that's the signal to graduate it into this Dockerfile.
