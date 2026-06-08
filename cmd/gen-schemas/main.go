//go:build schemagen

// Command gen-schemas reflects ctxloom's JSON output structs into published JSON
// Schema files under resources/schema/. Run via `just gen-schemas`
// (go run -tags schemagen ./cmd/gen-schemas). The schemas are checked in and a
// CI step (`just gen-schemas-check`) regenerates them and fails on any git diff,
// so a struct change without a matching schema update is caught.
package main

import (
	"fmt"
	"os"

	"github.com/ctxloom/ctxloom/cmd"
	"github.com/ctxloom/ctxloom/internal/operations"
	"github.com/ctxloom/ctxloom/internal/schemagen"
)

const schemaDir = "resources/schema"

func main() {
	var targets []schemagen.Target
	targets = append(targets, operations.SchemaTargets()...)
	targets = append(targets, cmd.SchemaTargets()...)

	if err := schemagen.Generate(schemaDir, targets); err != nil {
		fmt.Fprintf(os.Stderr, "gen-schemas: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("gen-schemas: wrote %d schemas to %s\n", len(targets), schemaDir)
}
