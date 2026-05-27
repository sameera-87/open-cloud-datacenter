# dc-runner — fat self-hosted GitHub Actions runner image.
#
# Why this exists:
#   ghcr.io/actions/actions-runner:latest is intentionally minimal — git,
#   tar, curl, node. No make, no build tools, no kubectl/helm. Workflows
#   that need anything else have to install per-run, which (a) costs time
#   on every job and (b) silently breaks when a workflow forgets the
#   install step (see the KVI operator build broken by `make test` —
#   nothing in the runner image provided make).
#
# What this adds, on top of actions-runner:latest:
#   * make + build-essential + ca-certificates  — for any Makefile-driven
#       Go project (kubebuilder operators, controller-gen / envtest setups)
#   * kubectl                                   — every dc-* workflow uses
#       it for deploy steps; was being curl'd inline by deploy.yaml
#   * helm                                      — used by future operator
#       chart releases and by ad-hoc workflows
#   * jq, yq, unzip, wget                       — JSON/YAML manipulation
#       and the basic fetch tooling expected on a hosted runner
#   * python3 + pip                             — schemathesis (contract
#       tests), boto3 (any AWS-touching glue)
#
# Tool versions are pinned. Bump deliberately — the runner image is what
# every workflow in this repo runs against, so a bump is a fleet-wide
# change. Image is published to ghcr.io/hiranadikari/dc-runner:latest
# (eventually ghcr.io/wso2/dc-runner once promoted).

FROM ghcr.io/actions/actions-runner:latest

# Tool versions — bump here, image rebuilds, ARC pulls on next runner pod
# spin-up (driven by the HelmRelease's :latest tag).
ARG KUBECTL_VERSION=v1.34.3
ARG HELM_VERSION=v3.16.3
ARG YQ_VERSION=v4.44.5

USER root

# Disable interactive apt prompts (tzdata in particular) — the runner
# image's base is Ubuntu and apt will block on tzdata otherwise.
ENV DEBIAN_FRONTEND=noninteractive

# Build + scripting essentials. --no-install-recommends keeps the image
# tighter; we add only what we actually need.
RUN apt-get update \
 && apt-get install -y --no-install-recommends \
        ca-certificates \
        curl \
        wget \
        git \
        jq \
        unzip \
        make \
        build-essential \
        python3 \
        python3-pip \
 && rm -rf /var/lib/apt/lists/*

# kubectl — matches the cluster version (harvester-dev RKE2 runs v1.34.3).
# Bump in lockstep with the cluster upgrade so client and server stay
# within Kubernetes' supported skew window.
RUN curl -fsSL "https://dl.k8s.io/release/${KUBECTL_VERSION}/bin/linux/amd64/kubectl" \
        -o /usr/local/bin/kubectl \
 && chmod +x /usr/local/bin/kubectl \
 && kubectl version --client

# helm — chart pull + template + lint. No reason to pin to a cluster
# version; chart APIs are version-stable.
RUN curl -fsSL "https://get.helm.sh/helm-${HELM_VERSION}-linux-amd64.tar.gz" \
        | tar -xz -C /tmp \
 && mv /tmp/linux-amd64/helm /usr/local/bin/helm \
 && rm -rf /tmp/linux-amd64 \
 && helm version --short

# yq — kustomize-aware YAML editing. mikefarah/yq is the Go reimplementation
# (NOT the Python yq, which is jq-style); standardising on the Go one to
# match what local devs use.
RUN curl -fsSL "https://github.com/mikefarah/yq/releases/download/${YQ_VERSION}/yq_linux_amd64" \
        -o /usr/local/bin/yq \
 && chmod +x /usr/local/bin/yq \
 && yq --version

# Drop back to the runner user — the ARC scale-set entrypoint runs as
# 'runner' (uid 1001). Anything left as root would fail on first job.
USER runner
