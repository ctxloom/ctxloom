// Command archlint runs ctxloom's architectural rules as a go/analysis
// multichecker.
//
// The binary is PREBUILT and invoked as a compiled artifact (`just lint-arch`,
// the pre-commit hook). Compiling an analyzer suite on every commit is slow
// enough that the hook gets bypassed, and a gate nobody tolerates is not a
// gate.
package main

import (
	"golang.org/x/tools/go/analysis/multichecker"

	"github.com/ctxloom/ctxloom/internal/archlint"
)

func main() {
	multichecker.Main(archlint.Analyzers()...)
}
