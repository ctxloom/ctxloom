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

	"github.com/ctxloom/ctxloom/internal/lm/backends"
	"github.com/ctxloom/ctxloom/internal/shared/agent"
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
const sharedAgentDir = "internal/shared/agent"

func TestArch_SharedAgent_NamesNoEngineOnlyApproach(t *testing.T) {
	names := backends.List()
	if len(names) == 0 {
		t.Fatal("backends.List() returned nothing — the registry did not populate")
	}

	// Which engines declare each approach name.
	declaredBy := map[string]map[string]bool{}
	for _, engine := range names {
		for _, n := range backends.Declared(engine).AllNames() {
			if declaredBy[n] == nil {
				declaredBy[n] = map[string]bool{}
			}
			declaredBy[n][engine] = true
		}
	}
	// Names shared code owns outright: it implements them, so it may name them.
	sharedOwned := map[string]bool{agent.ApproachUnsafeFile: true, agent.ApproachHook: true}

	var engineOnly []string
	for n, engines := range declaredBy {
		if len(engines) == 1 && !sharedOwned[n] {
			engineOnly = append(engineOnly, n)
		}
	}
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
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".go") || strings.HasSuffix(e.Name(), "_test.go") {
			continue
		}
		path := filepath.Join(dir, e.Name())
		f, err := parser.ParseFile(fset, path, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
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
}
