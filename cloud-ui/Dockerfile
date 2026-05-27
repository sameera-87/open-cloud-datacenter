# Build context: repo root (sovereign-cloud/).
# CI sets: context: . and file: ./cloud-ui/Dockerfile
# Local: docker build -f cloud-ui/Dockerfile .  (from repo root)
#
# ── Stage 1: Builder ─────────────────────────────────────────────────────────
FROM node:22-alpine AS builder

WORKDIR /app

# Enable corepack so pnpm is available at the lockfile's version.
RUN corepack enable

# Copy manifests first so Docker can cache the install layer separately
# from source changes.
COPY cloud-ui/package.json cloud-ui/pnpm-lock.yaml ./

RUN pnpm install --frozen-lockfile

# Copy the OpenAPI spec that gen:api reads (one directory up from cloud-ui/).
# In-container the script runs in /app, so we place it at ../dc-api/openapi.yaml
# relative to WORKDIR — i.e. /dc-api/openapi.yaml.
COPY dc-api/openapi.yaml /dc-api/openapi.yaml

# Copy cloud-ui source.
COPY cloud-ui/ .

# gen:api writes src/api/generated/types.ts from the spec.
# pnpm build runs tsc + vite, emitting the production bundle to dist/.
RUN pnpm gen:api && pnpm build

# ── Stage 2: Runtime ─────────────────────────────────────────────────────────
# nginx-unprivileged runs as uid 101 (nginx group) and listens on 8080.
# No root required — matches the security posture of distroless/static:nonroot.
FROM nginxinc/nginx-unprivileged:1.27-alpine

COPY --from=builder /app/nginx.conf /etc/nginx/nginx.conf
COPY --from=builder /app/dist /usr/share/nginx/html

EXPOSE 8080
