//go:build arch

package arch

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// goosScopes are the production trees: the library and the binaries that drive
// it. tests/ is test code and is not read.
var goosScopes = []string{"internal", "cmd", "pkg"}

// NO PRODUCTION CODE READS runtime.GOOS.
//
// A platform difference is a fact about the build, so it is decided by the
// build: a per-OS file (internal/shared/platform, or a package's own
// _linux/_windows twin) that the compiler selects. An inline runtime.GOOS
// branch compiles every arm on every OS, so a Linux-only call inside the
// "linux" arm breaks the Windows build from code that can never run there,
// and the branches smear one fact across whichever packages happen to need it.
// _test.go files are exempt: a test may legitimately skip on a platform.
func TestArch_NoRuntimeGOOSInProductionCode(t *testing.T) {
	root := moduleRoot(t)
	fset := token.NewFileSet()
	var findings []string
	var scanned int
	for _, scope := range goosScopes {
		err := filepath.WalkDir(filepath.Join(root, scope), func(p string, d fs.DirEntry, err error) error {
			switch {
			case err != nil:
				return err
			case d.IsDir() && skippedDir(d.Name()):
				return filepath.SkipDir
			case d.IsDir() || !strings.HasSuffix(d.Name(), ".go") || strings.HasSuffix(d.Name(), "_test.go"):
				return nil
			}
			f, perr := parser.ParseFile(fset, p, nil, parser.SkipObjectResolution)
			if perr != nil {
				t.Errorf("parse %s: %v", p, perr)
				return nil
			}
			scanned++
			for _, pos := range goosReads(f) {
				rel, _ := filepath.Rel(root, fset.Position(pos).Filename)
				findings = append(findings, filepath.ToSlash(rel)+":"+strconv.Itoa(fset.Position(pos).Line))
			}
			return nil
		})
		if err != nil {
			t.Fatalf("walk %s: %v", scope, err)
		}
	}
	if scanned < 500 {
		t.Fatalf("only %d production files scanned — the sweep is too small to be believed", scanned)
	}
	sort.Strings(findings)
	for _, f := range findings {
		t.Errorf("runtime.GOOS read in production code: %s\n"+
			"    move the platform fact behind a build-tagged twin (internal/shared/platform, "+
			"or a _linux/_other file pair in the package).", f)
	}
}

// goosReads returns the position of every runtime.GOOS selector in f, under
// whatever name f imports the standard runtime package as.
func goosReads(f *ast.File) []token.Pos {
	name := ""
	for _, imp := range f.Imports {
		if path, _ := strconv.Unquote(imp.Path.Value); path == "runtime" {
			name = "runtime"
			if imp.Name != nil {
				name = imp.Name.Name
			}
		}
	}
	if name == "" || name == "_" {
		return nil
	}
	var out []token.Pos
	ast.Inspect(f, func(n ast.Node) bool {
		sel, ok := n.(*ast.SelectorExpr)
		if !ok || sel.Sel.Name != "GOOS" {
			return true
		}
		if id, ok := sel.X.(*ast.Ident); ok && id.Name == name {
			out = append(out, sel.Pos())
		}
		return true
	})
	return out
}
