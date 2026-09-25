package coord

import (
	"go/ast"
	"go/constant"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/testsupport/sourcedir"
)

// packageDir is this package's source directory, located from the test file
// itself rather than the working directory: SandboxedMain moves the cwd to a
// throwaway root before any test runs, so "." is no longer the package.
// Untagged so both the arch walkers and the plain source-pinning tests share
// the one locator.
func packageDir(t *testing.T) string {
	t.Helper()
	dir, err := sourcedir.Dir()
	require.NoError(t, err, "locate this package's source directory")
	return dir
}

// referencingFiles is sourcedir.ReferencingFiles.
func referencingFiles(t *testing.T, dir, sym string, includeTests bool) []string {
	return sourcedir.ReferencingFiles(t, dir, sym, includeTests)
}

// nonTestGoFiles lists dir's non-test Go sources, absolute, sorted.
func nonTestGoFiles(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	require.NoError(t, err)
	var out []string
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		out = append(out, filepath.Join(dir, name))
	}
	sort.Strings(out)
	return out
}

// durationLiteralsEqualTo returns, for every duration expression in file that
// folds to want, the name of the constant or variable it initialises (or
// "<expr>" for one used inline). It understands the ordinary spellings —
// `N * time.Unit`, `time.Unit * N`, `time.Duration(N)`, sums and products of
// those — by constant-folding with go/constant; a spelling it cannot fold is
// not a duration literal in the sense this walker guards.
func durationLiteralsEqualTo(t *testing.T, file string, want time.Duration) []string {
	t.Helper()
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, file, nil, 0)
	require.NoError(t, err, "parsing %s", file)

	var hits []string
	var walk func(n ast.Node, owner string)
	walk = func(n ast.Node, owner string) {
		ast.Inspect(n, func(n ast.Node) bool {
			switch v := n.(type) {
			case *ast.ValueSpec:
				for i, val := range v.Values {
					name := "<expr>"
					if i < len(v.Names) {
						name = v.Names[i].Name
					}
					walk(val, name)
				}
				return false
			case *ast.BinaryExpr, *ast.CallExpr:
				if d, ok := foldDuration(v.(ast.Expr)); ok {
					if d == want {
						hits = append(hits, owner)
					}
					return false
				}
			}
			return true
		})
	}
	walk(f, "<expr>")
	return hits
}

// foldDuration constant-folds e as a time.Duration built from integer
// literals and time.<Unit> selectors. ok is false for anything else.
func foldDuration(e ast.Expr) (time.Duration, bool) {
	v, ok := foldDurationValue(e)
	if !ok {
		return 0, false
	}
	n, exact := constant.Int64Val(v)
	if !exact {
		return 0, false
	}
	return time.Duration(n), true
}

func foldDurationValue(e ast.Expr) (constant.Value, bool) {
	switch v := e.(type) {
	case *ast.ParenExpr:
		return foldDurationValue(v.X)
	case *ast.BasicLit:
		return foldIntLit(v)
	case *ast.SelectorExpr:
		return foldTimeUnit(v)
	case *ast.CallExpr:
		return foldDurationConversion(v)
	case *ast.BinaryExpr:
		return foldDurationArith(v)
	}
	return nil, false
}

// foldIntLit folds an integer literal.
func foldIntLit(v *ast.BasicLit) (constant.Value, bool) {
	if v.Kind != token.INT {
		return nil, false
	}
	n, err := strconv.ParseInt(v.Value, 0, 64)
	if err != nil {
		return nil, false
	}
	return constant.MakeInt64(n), true
}

// foldTimeUnit folds a time.<Unit> selector.
func foldTimeUnit(v *ast.SelectorExpr) (constant.Value, bool) {
	if id, ok := v.X.(*ast.Ident); ok && id.Name == "time" {
		if unit, ok := timeUnits[v.Sel.Name]; ok {
			return constant.MakeInt64(int64(unit)), true
		}
	}
	return nil, false
}

// foldDurationConversion folds time.Duration(N): a conversion, not a scale.
func foldDurationConversion(v *ast.CallExpr) (constant.Value, bool) {
	if sel, ok := v.Fun.(*ast.SelectorExpr); ok && len(v.Args) == 1 {
		if id, ok := sel.X.(*ast.Ident); ok && id.Name == "time" && sel.Sel.Name == "Duration" {
			return foldDurationValue(v.Args[0])
		}
	}
	return nil, false
}

// foldDurationArith folds a product, sum or difference of foldable operands.
func foldDurationArith(v *ast.BinaryExpr) (constant.Value, bool) {
	x, okX := foldDurationValue(v.X)
	y, okY := foldDurationValue(v.Y)
	if !okX || !okY {
		return nil, false
	}
	switch v.Op {
	case token.MUL, token.ADD, token.SUB:
		return constant.BinaryOp(x, v.Op, y), true
	}
	return nil, false
}

// timeUnits are the time package's duration units by name.
var timeUnits = map[string]time.Duration{
	"Nanosecond":  time.Nanosecond,
	"Microsecond": time.Microsecond,
	"Millisecond": time.Millisecond,
	"Second":      time.Second,
	"Minute":      time.Minute,
	"Hour":        time.Hour,
}
