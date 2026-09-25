//go:build tools

// Package tools pins code generators so `go generate` uses the module-locked version.
package tools

import _ "github.com/oapi-codegen/oapi-codegen/v2/cmd/oapi-codegen"
