# ── Stage 1: Builder ──────────────────────────────────────────────────────────
# We use the full Go image to compile. The output is a single static binary.
FROM golang:1.26-alpine AS builder

WORKDIR /src

# Copy dependency files first. Docker caches this layer separately —
# if only code changes (not go.mod/go.sum), the `go mod download` step
# is skipped on the next build, saving significant time.
COPY go.mod go.sum ./
RUN go mod download

# Copy the rest of the source and build.
# CGO_ENABLED=0: produce a fully static binary (no libc dependency).
# This is required for distroless/scratch base images.
COPY . .
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 \
    go build -ldflags="-s -w" -o /dc-api ./cmd/dc-api/

# ── Stage 2: Runtime ──────────────────────────────────────────────────────────
# distroless/static contains only CA certificates and timezone data — nothing else.
# No shell, no package manager, no attack surface. The binary runs as uid 65532 (nonroot).
FROM gcr.io/distroless/static:nonroot

# Copy only the compiled binary from the builder stage.
COPY --from=builder /dc-api /dc-api

# DC-API listens on 8080 by default (DCAPI_LISTEN_ADDR).
EXPOSE 8080

ENTRYPOINT ["/dc-api"]
