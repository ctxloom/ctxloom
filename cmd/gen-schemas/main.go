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
	"github.com/ctxloom/ctxloom/internal/mcp"
	"github.com/ctxloom/ctxloom/internal/operations"
	"github.com/ctxloom/ctxloom/internal/schemagen"
)

// schemaDir is a gitignored, generated artifact directory (like generated
// protobuf): produced by `just gen-schemas` as a build dependency and in CI,
// not checked in. NOTE (damp-motor, deletion register): resources/embed.go's
// //go:embed now deliberately excludes schema/gen — nothing in the repo ever
// read these files back out of the embedded FS (no accessor used a
// "schema/gen/" path), so ~70 generated files (~284 KB) were dead weight in
// every shipped binary. This directory is still generated to disk as a build
// artifact; whether `gen-schemas` is worth keeping at all, now that its output
// is unreachable at runtime, is a separate call for a human — see damp-motor.
const schemaDir = "resources/schema/gen"

func main() {
	var targets []schemagen.Target
	targets = append(targets, operations.SchemaTargets()...)
	targets = append(targets, cli.SchemaTargets()...)
	targets = append(targets, mcp.SchemaTargets()...)

	written, err := schemagen.Generate(schemaDir, targets)
	if err != nil {
		fmt.Fprintf(os.Stderr, "gen-schemas: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("gen-schemas: wrote %d schemas to %s\n", written, schemaDir)
}
