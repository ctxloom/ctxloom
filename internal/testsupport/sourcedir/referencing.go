package sourcedir

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// ReferencingFiles returns the files in dir that MENTION sym somewhere other
// than in sym's own declaration. Comments are invisible to it: the files are
// parsed without them, so prose naming a symbol never counts as a use. The
// source-pinning arch tests use it to hold a symbol to the files that may
// reach it.
func ReferencingFiles(t *testing.T, dir, sym string, includeTests bool) []string {
	t.Helper()

	entries, err := os.ReadDir(dir)
	require.NoError(t, err)

	fset := token.NewFileSet()
	var out []string
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") {
			continue
		}
		if !includeTests && strings.HasSuffix(name, "_test.go") {
			continue
		}

		f, err := parser.ParseFile(fset, filepath.Join(dir, name), nil, 0)
		require.NoError(t, err, "parsing %s", name)

		found := false
		ast.Inspect(f, func(n ast.Node) bool {
			if found {
				return false
			}
			switch v := n.(type) {
			case *ast.FuncDecl:
				// The declaration of sym is not a reference to it. Its BODY
				// still is — a method that calls itself is a real call site.
				if isDeclOf(v, sym) {
					found = bodyReferences(v.Body, sym)
					return false
				}
			case *ast.SelectorExpr:
				if v.Sel != nil && v.Sel.Name == sym {
					found = true
					return false
				}
			case *ast.Ident:
				if v.Name == sym {
					found = true
					return false
				}
			}
			return true
		})

		if found {
			out = append(out, name)
		}
	}
	sort.Strings(out)
	return out
}

// isDeclOf reports whether fn declares sym.
func isDeclOf(fn *ast.FuncDecl, sym string) bool {
	return fn.Name != nil && fn.Name.Name == sym
}

// bodyReferences reports whether body names sym; a nil body names nothing.
func bodyReferences(body *ast.BlockStmt, sym string) bool {
	if body == nil {
		return false
	}
	found := false
	ast.Inspect(body, func(b ast.Node) bool {
		if id, ok := b.(*ast.Ident); ok && id.Name == sym {
			found = true
			return false
		}
		return true
	})
	return found
}
