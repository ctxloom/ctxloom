//go:build arch

package arch

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/ctxloom/ctxloom/internal/adapters/operations"
	"github.com/ctxloom/ctxloom/internal/core/agent"
	"github.com/ctxloom/ctxloom/internal/engines"
)

// The approach vocabulary is an OPEN SET declared per engine. The failure
// this gate exists to catch is the enum growing back: a name only ONE engine
// declares appearing as a literal in shared code, which is the first engine's
// shape becoming the contract every later engine must map onto. The
// well-known names shared code legitimately refers to are the ones MORE THAN
// ONE engine declares (or the ones shared code itself implements).
//
// Derived from the registry rather than listed: an engine that adds a name
// of its own is covered the moment it registers, and a name that becomes
// shared by a second engine stops being flagged on its own.

// sharedAgentDir is the shared delivery seam this gate sweeps.
const sharedAgentDir = "internal/core/agent"

func TestArch_SharedAgent_NamesNoEngineOnlyApproach(t *testing.T) {
	names := operations.EngineNames(engines.Registry())
	if len(names) == 0 {
		t.Fatal("operations.EngineNames() returned nothing — the registry did not populate")
	}
	// Names shared code owns outright: it implements them, so it may name them.
	sharedOwned := map[string]bool{agent.ApproachUnsafeFile: true, agent.ApproachHook: true}
	engineOnly := engineOnlyApproaches(approachDeclarers(t, names), sharedOwned)
	if len(engineOnly) == 0 {
		t.Skip("no registered engine declares an approach of its own; nothing for this gate to sweep")
	}

	root := moduleRoot(t)
	dir := filepath.Join(root, filepath.FromSlash(sharedAgentDir))
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	fset := token.NewFileSet()
	for _, e := range entries {
		if !isProductionGoFile(e) {
			continue
		}
		f, err := parser.ParseFile(fset, filepath.Join(dir, e.Name()), nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		reportEngineOnlyLiterals(t, fset, f, engineOnly)
	}
}

// approachDeclarers maps each approach name to the engines that declare it.
func approachDeclarers(t *testing.T, names []string) map[string]map[string]bool {
	t.Helper()
	declaredBy := map[string]map[string]bool{}
	for _, engine := range names {
		h, ok := engines.Hosted(engine)
		if !ok {
			t.Fatalf("%s is composed but not agent.Hosted", engine)
		}
		for _, n := range h.Declaration().AllNames() {
			if declaredBy[n] == nil {
				declaredBy[n] = map[string]bool{}
			}
			declaredBy[n][engine] = true
		}
	}
	return declaredBy
}

// engineOnlyApproaches is the names exactly one engine declares, less the
// ones shared code owns.
func engineOnlyApproaches(declaredBy map[string]map[string]bool, sharedOwned map[string]bool) []string {
	var engineOnly []string
	for n, engines := range declaredBy {
		if len(engines) == 1 && !sharedOwned[n] {
			engineOnly = append(engineOnly, n)
		}
	}
	return engineOnly
}

// isProductionGoFile reports whether e is a non-test Go source file.
func isProductionGoFile(e os.DirEntry) bool {
	name := e.Name()
	return !e.IsDir() && strings.HasSuffix(name, ".go") && !strings.HasSuffix(name, "_test.go")
}

// reportEngineOnlyLiterals fails for every string literal in f that names
// an engine-only approach.
func reportEngineOnlyLiterals(t *testing.T, fset *token.FileSet, f *ast.File, engineOnly []string) {
	t.Helper()
	ast.Inspect(f, func(n ast.Node) bool {
		lit, ok := n.(*ast.BasicLit)
		if !ok || lit.Kind != token.STRING {
			return true
		}
		s, err := strconv.Unquote(lit.Value)
		if err != nil {
			return true
		}
		for _, only := range engineOnly {
			if s == only {
				t.Errorf("%s: names %q, an approach only one engine declares — shared code must not name an engine's own approach; the enum is growing back",
					fset.Position(lit.Pos()), only)
			}
		}
		return true
	})
}
