//go:build tools

// Package tools tracks build-time dependencies that are NOT imported by
// the dcctl runtime. Listing them here keeps `go mod tidy` from pruning
// them and makes `go run <module>/cmd/...` work for tooling.
//
// Currently the only entry is oapi-codegen, used to regenerate the
// DC-API client from openapi.yaml. See internal/client/generated/.
package tools

import (
	_ "github.com/oapi-codegen/oapi-codegen/v2/cmd/oapi-codegen"
)
