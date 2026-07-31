// Package schemagen reflects ctxloom's JSON output structs into published JSON
// Schema documents (resources/schema/gen/*-schema.json). It is build-tagged
// `schemagen`, so this package's own code is not compiled into the production
// binary; it is reached only by `go run -tags schemagen ./cmd/gen-schemas`
// (see `just gen-schemas`). This file carries the package declaration for
// untagged builds so `go build ./...` sees a valid package.
//
// The tag does NOT keep the reflection library out. github.com/google/
// jsonschema-go is a direct module requirement (this package imports it), and
// independently of that it is already linked into every shipped binary:
// github.com/modelcontextprotocol/go-sdk/mcp imports it and ctxloom imports
// that SDK unconditionally. The tag therefore buys code size in this package,
// not a smaller dependency or supply-chain surface — deleting this package
// would leave the reflector in the binary and in go.mod either way.
// doc_test.go pins both halves of that statement.
package schemagen
