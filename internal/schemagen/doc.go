// Package schemagen reflects ctxloom's JSON output structs into published JSON
// Schema documents (resources/schema/*-schema.json). It is build-tagged
// `schemagen` so neither it nor its reflection dependency is compiled into the
// production binary; it is reached only by `go run -tags schemagen
// ./cmd/gen-schemas` (see `just gen-schemas`). This file carries the package
// declaration for untagged builds so `go build ./...` sees a valid package.
package schemagen
