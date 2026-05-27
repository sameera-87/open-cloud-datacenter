// Package dcapi exposes the embedded OpenAPI spec to the rest of the binary.
//
// The spec file (openapi.yaml) lives at the module root so dcctl's
// oapi-codegen, cloud-ui's pnpm gen:api, and redocly lint can all read it
// from the same canonical path. This package adds a `//go:embed` so the
// running dc-api can serve the same file at `/openapi.yaml` and render it
// as Redoc HTML at `/docs`.
package dcapi

import _ "embed"

// OpenAPISpec is the raw bytes of openapi.yaml at build time.
// Served unauthenticated at /openapi.yaml and consumed by the /docs Redoc
// HTML page. Embedding rather than reading from disk means the spec is
// always in sync with the binary version.
//
//go:embed openapi.yaml
var OpenAPISpec []byte
