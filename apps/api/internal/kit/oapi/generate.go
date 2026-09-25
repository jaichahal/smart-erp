// Package oapi holds the wire types generated from contracts/openapi/openapi.yaml.
// Modules import these for request and response bodies so the Go code cannot drift
// from the contract; hand-written types live in kit/httpx.
package oapi

//go:generate go run github.com/oapi-codegen/oapi-codegen/v2/cmd/oapi-codegen -config oapi.yaml ../../../../../contracts/openapi/openapi.yaml
