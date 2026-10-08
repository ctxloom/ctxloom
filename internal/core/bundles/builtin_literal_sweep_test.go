package bundles

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// TestNoBuiltinSourceRefLiteralSurvives keeps the retired "builtin:" source
// class retired. Every bundle source ref is minted by an ident constructor
// (LocalRef, GitRef, FileRef, CompanionRef), and none of them mints a
// "builtin:" ref, so a producer that hand-spells "builtin:<name>" would key a
// bundle under a second identity no constructor produces — and the compiler
// would never notice, because a string literal is not a constructor call.
//
// Scoped to the three packages that mint or consume a bundle-shipped
// executable's SCM/source ref — internal/core/bundles, internal/core/config,
// operations/managedhooks — which is where every genuine producer lives.
//
// AST-based (go/parser over STRING literals), not a text grep: a grep would
// also flag the doc comments that correctly narrate this history
// (reader_localfs.go, catalog.go) as if they were code.
func TestNoBuiltinSourceRefLiteralSurvives(t *testing.T) {
	root := repoRootForTest(t)
	dirs := []string{"internal/core/bundles", "internal/core/config", "internal/adapters/operations/managedhooks"}

	var offenders []string
	for _, dir := range dirs {
		full := filepath.Join(root, dir)
		entries, err := os.ReadDir(full)
		if err != nil {
			t.Fatalf("read %s: %v", full, err)
		}
		for _, e := range entries {
			if e.IsDir() || !strings.HasSuffix(e.Name(), ".go") || strings.HasSuffix(e.Name(), "_test.go") {
				continue
			}
			path := filepath.Join(full, e.Name())
			fset := token.NewFileSet()
			f, err := parser.ParseFile(fset, path, nil, 0)
			if err != nil {
				t.Fatalf("parse %s: %v", path, err)
			}
			ast.Inspect(f, func(n ast.Node) bool {
				lit, ok := n.(*ast.BasicLit)
				if !ok || lit.Kind != token.STRING {
					return true
				}
				value, err := stringLitValue(lit.Value)
				if err != nil {
					return true
				}
				if strings.HasPrefix(value, "builtin:") {
					offenders = append(offenders, filepath.Join(dir, e.Name())+
						fmt.Sprintf(":%d %q", fset.Position(lit.Pos()).Line, value))
				}
				return true
			})
		}
	}

	if len(offenders) > 0 {
		t.Fatalf("a bundle source ref must be MINTED by an ident constructor, never hand-spelled — "+
			"found %d raw \"builtin:\" string literal(s) in producer code:\n%s",
			len(offenders), strings.Join(offenders, "\n"))
	}
}

// stringLitValue decodes a *ast.BasicLit's raw source text (still carrying its
// quoting) into the string it denotes. Non-double-quoted literals (raw
// backtick strings) are reported as an error rather than guessed at, since a
// backtick literal starting with "builtin:" would need different unescaping.
func stringLitValue(raw string) (string, error) {
	if !strings.HasPrefix(raw, `"`) {
		return "", fmt.Errorf("not a double-quoted string literal: %s", raw)
	}
	return strconv.Unquote(raw)
}

// repoRootForTest locates the module root by walking up from the working
// directory (which `go test` sets to the package directory) until it finds
// go.mod, so this test finds the scanned directories
// regardless of which package it happens to run from.
func repoRootForTest(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("could not locate module root (no go.mod found walking up)")
		}
		dir = parent
	}
}
