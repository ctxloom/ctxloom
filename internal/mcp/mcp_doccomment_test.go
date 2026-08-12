package mcp

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// lowerCamelConstructorRef matches an unexported constructor-shaped identifier
// (`newFoo`, `newCtxServerForIdentity`) as it appears inside a doc comment.
// Exported cross-package forms (`mcp.NewServer`) never match: the leading rune
// is upper-case there, so only THIS package's own unexported constructors are
// in scope — exactly the references a reader of package mcp can be expected to
// resolve locally.
var lowerCamelConstructorRef = regexp.MustCompile(`\bnew[A-Z][A-Za-z0-9_]*\b`)

// TestDocComments_NameOnlyConstructorsThatExist pins a documentation
// invariant: a comment in package mcp that names one of the package's own
// `newXxx` constructors must name one that is actually declared. A comment
// pointing at a function that no longer exists is worse than no comment — it
// sends the reader looking for a symbol they will never find, and (because
// nothing in the toolchain checks comment prose) it survives every rename and
// every deletion silently.
//
// The defect this pins: two comments once directed the reader to
// `newCtxServerForIdentity`, which the runner-terminated MCP rework deleted.
// A sibling of internal/cli's test of the same name — the invariant is
// per-package by construction (it resolves references against the
// declarations of ONE directory), so the package that now holds the MCP
// implementation needs its own or the check silently stops covering it.
func TestDocComments_NameOnlyConstructorsThatExist(t *testing.T) {
	fset := token.NewFileSet()
	// Absolute, from this package's compiled-in source path: TestMain sandboxes
	// the binary into a temp cwd, where a "." scan would find no Go files at
	// all and report a clean sweep (see pkgSourceDir).
	pkgDir := pkgSourceDir(t)
	entries, err := os.ReadDir(pkgDir)
	require.NoError(t, err)

	declared := map[string]bool{}
	type commentRef struct {
		name string
		pos  string
	}
	var refs []commentRef

	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".go") {
			continue
		}
		path := filepath.Join(pkgDir, e.Name())
		file, perr := parser.ParseFile(fset, path, nil, parser.ParseComments)
		require.NoError(t, perr, "parse %s", path)

		// Declarations come from EVERY file in the directory, test files
		// included: a test helper is a legitimate referent for a comment.
		for _, decl := range file.Decls {
			switch d := decl.(type) {
			case *ast.FuncDecl:
				declared[d.Name.Name] = true
			case *ast.GenDecl:
				for _, spec := range d.Specs {
					switch s := spec.(type) {
					case *ast.TypeSpec:
						declared[s.Name.Name] = true
					case *ast.ValueSpec:
						for _, n := range s.Names {
							declared[n.Name] = true
						}
					}
				}
			}
		}

		// References are collected from PRODUCTION files only. A test file's
		// comments describe scaffolding whose churn is not a reader hazard.
		if strings.HasSuffix(e.Name(), "_test.go") {
			continue
		}
		for _, group := range file.Comments {
			for _, c := range group.List {
				for _, m := range lowerCamelConstructorRef.FindAllString(c.Text, -1) {
					refs = append(refs, commentRef{name: m, pos: fset.Position(c.Pos()).String()})
				}
			}
		}
	}

	require.NotEmpty(t, refs, "found no constructor references at all — the scanner is broken, not the package")

	var dangling []string
	for _, r := range refs {
		if !declared[r.name] {
			dangling = append(dangling, r.pos+": "+r.name)
		}
	}
	require.Empty(t, dangling, "doc comments name constructors this package does not declare:\n%s", strings.Join(dangling, "\n"))
}
