package textutil

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The package doc now states ONE invariant — shorten a string to fit a byte
// budget without splitting a rune — where it used to describe a grab-bag
// ("small string helpers shared across ctxloom packages"). Prose alone cannot
// keep that true: a grab-bag name is how unrelated helpers accumulate, and the
// accumulation happens one obviously-harmless addition at a time.
//
// So the exported surface is enumerated. A new export fails here, which is the
// point: extending the list is a deliberate act that asks whether the addition
// really is the same invariant, or whether it wants a package whose name says
// what it does. Nothing executable can go red for a documentation fix, so this
// is the pin that makes the corrected prose maintained rather than merely
// written.
func TestPackage_ExportsOnlyByteBudgetedShortening(t *testing.T) {
	want := map[string]string{
		"TruncateBytes": "cut to fit a byte budget on a rune boundary",
		"Ellipsize":     "the same cut, with the marker reserved from the budget",
	}

	entries, err := os.ReadDir(".")
	require.NoError(t, err)

	fset := token.NewFileSet()
	got := map[string]bool{}
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		file, err := parser.ParseFile(fset, name, nil, 0)
		require.NoError(t, err)
		for _, decl := range file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || !fn.Name.IsExported() {
				continue
			}
			got[fn.Name.Name] = true
		}
	}

	for name := range want {
		assert.True(t, got[name], "%s went missing from the package", name)
	}
	for name := range got {
		if _, ok := want[name]; !ok {
			t.Errorf("textutil exported %s, which the package doc does not cover. "+
				"If it shortens a string to a byte budget on a rune boundary, add it here; "+
				"if it does not, it belongs in a package whose name says what it holds", name)
		}
	}
}
