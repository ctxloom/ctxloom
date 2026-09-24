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

// runtimeFiles are the only production files that may name a container
// runtime: each one IS that runtime's implementation of isolation.Runtime.
var runtimeFiles = map[string]bool{
	"internal/adapters/isolation/runtime_docker.go": true,
	"internal/adapters/isolation/runtime_podman.go": true,
}

// runtimeNames are the spellings that make a string a runtime dispatch: the
// runtime's name or CLI on its own, or a command line starting with it.
var runtimeNames = []string{"docker", "podman"}

// runtimeTypes are the concrete runtimes a type assertion must not select.
var runtimeTypes = map[string]bool{"Docker": true, "Podman": true}

// runtimeLiteralAllowed is the whole allowlist, keyed by file and exact value.
var runtimeLiteralAllowed = map[string]map[string]string{
	// A cgroup-path marker: /proc/1/cgroup names the container MANAGER when
	// THIS process runs inside a container. It detects where ctxloom is, and
	// selects no runtime to launch with.
	"internal/shared/containerprobe/containerprobe.go": {"docker": "in-container cgroup marker"},
}

// runtimeSeamSkipped are production-tree directories that are test
// infrastructure: they drive the real CLIs to stand test cells up, which is
// the one place naming a runtime directly is the point.
var runtimeSeamSkipped = map[string]bool{"internal/testsupport": true}

// A RUNTIME DIFFERENCE GOES THROUGH isolation.Runtime.
//
// isolation.Runtime is polymorphism, not an if/else over runtime names: each
// runtime's CLI grammar, teardown report, identity mode and host alias is a
// method, overridden only by the runtime that differs. Nothing else enforced
// that, and it leaked as a Name()=="podman" branch, a type assertion to
// Docker, a substring match on docker's stderr, inline `docker ...` argv, and
// a second PATH-based runtime detector outside the package. A runtime named
// in a string, or selected by a type assertion, outside its own file is how
// the next leak starts. _test.go files are exempt: a test names the runtime it
// drives.
func TestArch_RuntimeDifferencesGoThroughTheSeam(t *testing.T) {
	root := moduleRoot(t)
	fset := token.NewFileSet()
	var findings []string
	var scanned int
	for _, scope := range goosScopes {
		err := filepath.WalkDir(filepath.Join(root, scope), func(p string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			rel, _ := filepath.Rel(root, p)
			rel = filepath.ToSlash(rel)
			switch {
			case d.IsDir() && (skippedDir(d.Name()) || runtimeSeamSkipped[rel]):
				return filepath.SkipDir
			case d.IsDir() || !strings.HasSuffix(d.Name(), ".go") || strings.HasSuffix(d.Name(), "_test.go"):
				return nil
			case runtimeFiles[rel]:
				scanned++
				return nil
			}
			f, perr := parser.ParseFile(fset, p, nil, parser.SkipObjectResolution)
			if perr != nil {
				t.Errorf("parse %s: %v", p, perr)
				return nil
			}
			scanned++
			at := func(pos token.Pos, what string) {
				findings = append(findings, rel+":"+strconv.Itoa(fset.Position(pos).Line)+": "+what)
			}
			ast.Inspect(f, func(n ast.Node) bool {
				switch n := n.(type) {
				case *ast.BasicLit:
					if v, ok := runtimeLiteral(n); ok && runtimeLiteralAllowed[rel][v] == "" {
						at(n.Pos(), "string literal "+strconv.Quote(v))
					}
				case *ast.TypeAssertExpr:
					if name := runtimeTypeName(n.Type); name != "" {
						at(n.Pos(), "type assertion to "+name)
					}
				case *ast.TypeSwitchStmt:
					for _, stmt := range n.Body.List {
						for _, e := range stmt.(*ast.CaseClause).List {
							if name := runtimeTypeName(e); name != "" {
								at(e.Pos(), "type switch case "+name)
							}
						}
					}
				}
				return true
			})
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
		t.Errorf("container runtime named outside its isolation.Runtime implementation: %s\n"+
			"    make it a Runtime method (one default on ociRuntime, overridden in runtime_docker.go / "+
			"runtime_podman.go only where the runtime differs), or ask the runtime for its Name()/Binary().", f)
	}
}

// runtimeLiteral reports a string literal's value when it is a runtime
// dispatch: exactly a runtime name, or a command line beginning with one.
// Prose that mentions a runtime ("docker/podman", "rootless docker daemon")
// is neither, and passes.
func runtimeLiteral(lit *ast.BasicLit) (string, bool) {
	if lit.Kind != token.STRING {
		return "", false
	}
	v, err := strconv.Unquote(lit.Value)
	if err != nil {
		return "", false
	}
	for _, name := range runtimeNames {
		if v == name || strings.HasPrefix(v, name+" ") {
			return v, true
		}
	}
	return "", false
}

// runtimeTypeName names the concrete runtime a type expression denotes
// (Docker, or isolation.Docker from outside the package), or "".
func runtimeTypeName(e ast.Expr) string {
	switch e := e.(type) {
	case *ast.Ident:
		if runtimeTypes[e.Name] {
			return e.Name
		}
	case *ast.SelectorExpr:
		if runtimeTypes[e.Sel.Name] {
			return e.Sel.Name
		}
	}
	return ""
}
