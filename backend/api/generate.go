// Package api holds the OpenAPI spec, the single source of truth for the HTTP API.
// Run `go generate ./...` after editing openapi.yaml to regenerate internal/oas.
package api

//go:generate go tool ogen --target ../internal/oas --package oas --clean openapi.yaml
