//go:build schemagen

// Command gen-schemas reflects ctxloom's JSON output structs into published JSON
// Schema files under resources/schema/. Run via `just gen-schemas`
// (go run -tags schemagen ./cmd/gen-schemas). The schemas are NOT checked in —
// schemaDir below is gitignored — and are regenerated as a build dependency
// (`just build`) and in CI (see .github/workflows/ci.yml) each run, like
// generated protobuf.
package main

import (
	"fmt"
	"os"

	"github.com/ctxloom/ctxloom/internal/cli"
	"github.com/ctxloom/ctxloom/internal/operations"
	"github.com/ctxloom/ctxloom/internal/schemagen"
)

// schemaDir is a gitignored, generated artifact directory (like generated
// protobuf): produced by `just gen-schemas` as a build dependency and in CI, not
// checked in. It sits under schema/ so the existing //go:embed all:schema picks
// it up into the binary.
const schemaDir = "resources/schema/gen"

func main() {
	var targets []schemagen.Target
	targets = append(targets, operations.SchemaTargets()...)
	targets = append(targets, cli.SchemaTargets()...)

	if err := schemagen.Generate(schemaDir, targets); err != nil {
		fmt.Fprintf(os.Stderr, "gen-schemas: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("gen-schemas: wrote %d schemas to %s\n", len(targets), schemaDir)
}
