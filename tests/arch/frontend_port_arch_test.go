//go:build arch

package arch

import (
	"go/ast"
	"path"
	"slices"
	"strconv"
	"testing"

	"github.com/ctxloom/ctxloom/internal/shared/archrules"
)

// frontendTrees are the frontends' own packages, and frontendFiles the CLI
// files that compose a frontend's presenters: every one of them consumes the
// frontend port (internal/core/frontend) and none holds the coordinator.
// A frontend that names *coord.Coordinator reaches past the port to the
// in-process core — the coupling the port exists to remove, and what would
// keep an out-of-process frontend from being the same code.
var (
	frontendTrees = []string{"internal/adapters/termui", "internal/adapters/cli/tui"}
	frontendFiles = []string{
		"internal/adapters/cli/run_terminal_ui.go",
		"internal/adapters/cli/approval_presenter.go",
		"internal/adapters/cli/credential_presenter.go",
	}
)

const coordImportPath = "github.com/ctxloom/ctxloom/internal/core/coord"

// TestArch_FrontendsHoldNoCoordinator fails when a frontend's production
// file names coord.Coordinator: a frontend observes and acts through the
// port, never the coordinator itself.
func TestArch_FrontendsHoldNoCoordinator(t *testing.T) {
	seen := map[string]int{}
	walkRingFiles(t, func(rf ringFile) {
		inTree := archrules.UnderAny(rf.dir, frontendTrees)
		if !inTree && !slices.Contains(frontendFiles, rf.rel) {
			return
		}
		if inTree {
			for _, tree := range frontendTrees {
				if archrules.UnderAny(rf.dir, []string{tree}) {
					seen[tree]++
				}
			}
		} else {
			seen[rf.rel]++
		}
		name := coordImportName(rf.f)
		if name == "" {
			return
		}
		ast.Inspect(rf.f, func(n ast.Node) bool {
			sel, ok := n.(*ast.SelectorExpr)
			if !ok || sel.Sel.Name != "Coordinator" {
				return true
			}
			if id, ok := sel.X.(*ast.Ident); ok && id.Name == name {
				t.Errorf("%s:%d names %s.Coordinator — a frontend consumes the frontend port (internal/core/frontend), never the coordinator", rf.rel, rf.fset.Position(sel.Pos()).Line, name)
			}
			return true
		})
	})
	for _, want := range append(slices.Clone(frontendTrees), frontendFiles...) {
		if seen[want] == 0 {
			t.Errorf("the walk saw no production file of %s — the frontend moved and this gate guards nothing; re-point it", want)
		}
	}
}

// coordImportName is the name f refers to the coord package by, "" when f
// does not import it.
func coordImportName(f *ast.File) string {
	for _, imp := range f.Imports {
		p, err := strconv.Unquote(imp.Path.Value)
		if err != nil || p != coordImportPath {
			continue
		}
		if imp.Name != nil {
			return imp.Name.Name
		}
		return path.Base(p)
	}
	return ""
}
