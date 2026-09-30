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
// second seam before it was folded into one. So outside the seam's file, EVERY
// mount literal is refused, whether spelled mount{...}, &mount{...}, or as an
// element of a []mount{...} — the empty mount{} included, so the rule needs no
// judgment about which literals are harmless. A zero value for an error path
// comes from a named result, not a literal. _test.go files are exempt: a test
// states the mount it expects.
func TestArch_MountsAreBuiltByThePathSeam(t *testing.T) {
	findings, scanned, sawSeam := isolationMountLiterals(t, moduleRoot(t))
	if !sawSeam {
		t.Fatalf("%s/%s not found — the seam moved; point pathSeamFile at the file that now holds pathSeam.bind", isolationDir, pathSeamFile)
	}
	if scanned < 20 {
		t.Fatalf("only %d production files scanned in %s — the sweep is too small to be believed", scanned, isolationDir)
	}
	for _, f := range findings {
		t.Errorf("mount literal outside the path seam: %s\n"+
			"    build it with the runtime's seam: rt.paths().bind(host, target, readOnly) for a target you "+
			"decided, rt.paths().expose(host, readOnly) to route the host path; for an error path's zero "+
			"value, name the result (m mount, err error) and return m.", f)
	}
}

// isolationMountLiterals sweeps isolationDir's production files other than
// the seam's own, returning each mount literal's file:line (sorted), how many
// files it parsed, and whether it saw the seam's file at all.
func isolationMountLiterals(t *testing.T, root string) (findings []string, scanned int, sawSeam bool) {
	t.Helper()
	dir := filepath.Join(root, isolationDir)
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	fset := token.NewFileSet()
	for _, e := range entries {
		name := e.Name()
		switch {
		case e.IsDir() || !isNonTestGoFile(name):
			continue
		case name == pathSeamFile:
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
	sort.Strings(findings)
	return findings, scanned, sawSeam
}

// mountLiterals returns the position of every mount composite literal in f:
// typed (mount{...}, including under &), or an elided element of a []mount /
// [N]mount literal.
func mountLiterals(f *ast.File) []token.Pos {
	var out []token.Pos
	ast.Inspect(f, func(n ast.Node) bool {
		lit, ok := n.(*ast.CompositeLit)
		if !ok {
			return true
		}
		if isMountIdent(lit.Type) {
			out = append(out, lit.Pos())
		}
		if arr, ok := lit.Type.(*ast.ArrayType); ok && isMountIdent(arr.Elt) {
			for _, e := range lit.Elts {
				if el, ok := e.(*ast.CompositeLit); ok && el.Type == nil {
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
