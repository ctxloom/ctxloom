//go:build arch

package arch

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// parsePackageDir parses every .go file directly under rel (tests included)
// and returns the files keyed by module-relative path. An unreadable or
// unparseable directory fails the test: a walk that found nothing would let
// every absence assertion pass vacuously.
func parsePackageDir(t *testing.T, rel string) map[string]*ast.File {
	t.Helper()
	dir := filepath.Join(moduleRoot(t), rel)
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read %s: %v", rel, err)
	}
	fset := token.NewFileSet()
	files := map[string]*ast.File{}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".go") {
			continue
		}
		f, perr := parser.ParseFile(fset, filepath.Join(dir, e.Name()), nil, 0)
		if perr != nil {
			t.Fatalf("parse %s/%s: %v", rel, e.Name(), perr)
		}
		files[filepath.ToSlash(filepath.Join(rel, e.Name()))] = f
	}
	if len(files) == 0 {
		t.Fatalf("%s holds no .go files — the walk is broken, not the module", rel)
	}
	return files
}

// TestArch_WriteCommandFilesDoesNotSweep pins that claude.WriteCommandFiles
// writes its commands without RemoveAll-ing the commands directory: a sweep
// there would delete whatever else lives beside the files it writes.
func TestArch_WriteCommandFilesDoesNotSweep(t *testing.T) {
	files := parsePackageDir(t, "internal/engines/claude")
	var found bool
	for rel, f := range files {
		for _, decl := range f.Decls {
			fd, ok := decl.(*ast.FuncDecl)
			if !ok || fd.Name.Name != "WriteCommandFiles" || fd.Recv != nil {
				continue
			}
			found = true
			ast.Inspect(fd.Body, func(n ast.Node) bool {
				call, ok := n.(*ast.CallExpr)
				if !ok {
					return true
				}
				if sel, ok := call.Fun.(*ast.SelectorExpr); ok && sel.Sel.Name == "RemoveAll" {
					t.Errorf("%s: WriteCommandFiles still calls RemoveAll — it must not sweep the commands directory", rel)
				}
				return true
			})
		}
	}
	if !found {
		t.Fatal("claude.WriteCommandFiles not found — this gate pins a call inside it, so the function moving means the gate must move too")
	}
}
