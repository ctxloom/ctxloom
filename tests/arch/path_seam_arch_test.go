//go:build arch

package arch

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"testing"
)

// isolationDir is the package whose mounts must all be built by its path seam.
const isolationDir = "internal/adapters/isolation"

// pathSeamFile is the one production file in isolationDir allowed to spell a
// mount out field by field: the seam's own bind.
const pathSeamFile = "pathseam.go"

// A MOUNT IS BUILT BY THE PATH SEAM.
//
// Host↔container translation lives in ONE seam (pathSeam): its target rule
// decides where the container sees a host path, its source rule what the
// daemon calls it. A mount literal written anywhere else picks its own target
// and skips the seam — which is how translation had grown three paths plus a
// second seam before it was folded into one. So outside the seam's file, a
// mount carrying fields is refused, whether spelled mount{...}, &mount{...},
// or as an element of a []mount{...}. The empty mount{} is a zero value
// (an error-path return), not an exposure, and passes. _test.go files are
// exempt: a test states the mount it expects.
func TestArch_MountsAreBuiltByThePathSeam(t *testing.T) {
	root := moduleRoot(t)
	dir := filepath.Join(root, isolationDir)
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	fset := token.NewFileSet()
	var findings []string
	var scanned int
	sawSeam := false
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !isNonTestGoFile(name) {
			continue
		}
		if name == pathSeamFile {
			sawSeam = true
			continue
		}
		f, perr := parser.ParseFile(fset, filepath.Join(dir, name), nil, parser.SkipObjectResolution)
		if perr != nil {
			t.Errorf("parse %s: %v", name, perr)
			continue
		}
		scanned++
		for _, pos := range mountLiterals(f) {
			findings = append(findings, isolationDir+"/"+name+":"+strconv.Itoa(fset.Position(pos).Line))
		}
	}
	if !sawSeam {
		t.Fatalf("%s/%s not found — the seam moved; point pathSeamFile at the file that now holds pathSeam.bind", isolationDir, pathSeamFile)
	}
	if scanned < 20 {
		t.Fatalf("only %d production files scanned in %s — the sweep is too small to be believed", scanned, isolationDir)
	}
	sort.Strings(findings)
	for _, f := range findings {
		t.Errorf("mount literal outside the path seam: %s\n"+
			"    build it with the runtime's seam: rt.paths().bind(host, target, readOnly) for a target you "+
			"decided, rt.paths().expose(host, readOnly) to route the host path.", f)
	}
}

// mountLiterals returns the position of every mount composite literal in f
// that carries fields: typed (mount{...}, including under &), or an elided
// element of a []mount / [N]mount literal.
func mountLiterals(f *ast.File) []token.Pos {
	var out []token.Pos
	ast.Inspect(f, func(n ast.Node) bool {
		lit, ok := n.(*ast.CompositeLit)
		if !ok {
			return true
		}
		if isMountIdent(lit.Type) && len(lit.Elts) > 0 {
			out = append(out, lit.Pos())
		}
		if arr, ok := lit.Type.(*ast.ArrayType); ok && isMountIdent(arr.Elt) {
			for _, e := range lit.Elts {
				if el, ok := e.(*ast.CompositeLit); ok && el.Type == nil && len(el.Elts) > 0 {
					out = append(out, el.Pos())
				}
			}
		}
		return true
	})
	return out
}

func isMountIdent(e ast.Expr) bool {
	id, ok := e.(*ast.Ident)
	return ok && id.Name == "mount"
}
