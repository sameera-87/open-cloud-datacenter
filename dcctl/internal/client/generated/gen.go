// Package dcapi holds the oapi-codegen-generated DC-API client.
//
// Regenerate after a dc-api/openapi.yaml change:
//
//	go generate ./internal/client/generated/...
//
// (oapi-codegen is pinned via tools.go at the dcctl module root.)
package dcapi

//go:generate go run github.com/oapi-codegen/oapi-codegen/v2/cmd/oapi-codegen -config oapi.cfg.yaml ../../../../dc-api/openapi.yaml
