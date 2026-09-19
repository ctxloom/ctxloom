package archlint

import (
	"go/types"

	"golang.org/x/tools/go/analysis"
)

// importedPkg resolves a direct import path to the type-checked package the
// fact store is keyed on, or nil when the import is not among the analyzed
// package's dependencies.
func importedPkg(pass *analysis.Pass, importPath string) *types.Package {
	for _, p := range pass.Pkg.Imports() {
		if p.Path() == importPath {
			return p
		}
	}
	return nil
}
