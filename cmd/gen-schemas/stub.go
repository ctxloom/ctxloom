//go:build !schemagen

// This stub gives cmd/gen-schemas a buildable package for ordinary
// `go build ./...` runs; the real generator is build-tagged `schemagen` (see
// main.go) so its reflection dependency never enters the production toolchain.
//
// The stub must not just exit 0 having done nothing: a bare
// `go run ./cmd/gen-schemas` (no tag) used to silently "succeed" without
// generating a single schema. It now fails loud and names the real command.
package main

import (
	"fmt"
	"os"
)

func main() {
	fmt.Fprintln(os.Stderr, "gen-schemas: this build omits the schemagen tag and cannot generate schemas; run `just gen-schemas` (go run -tags schemagen ./cmd/gen-schemas) instead")
	os.Exit(1)
}
