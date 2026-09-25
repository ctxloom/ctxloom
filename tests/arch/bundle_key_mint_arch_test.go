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

// trustImportPath is the package that owns trust.BundleKey, and the one
// package allowed to convert to it.
const trustImportPath = "github.com/ctxloom/ctxloom/internal/core/trust"

// A LOCK KEY IS MINTED, NEVER CAST.
//
// trust.BundleKey is the key a lockfile entry is stored under and the key a
// publisher's retraction is looked up by. The two are equal only because both
// come from BundleRef.BundleIdentity (remote.Reference.LockKey is that, over a
// parsed reference). A conversion from an arbitrary string produces a value of
// the right TYPE in whatever spelling the string happened to have, and a
// retraction keyed on the canonical spelling then misses it — a retracted
// release admitted. That is the class stony-overtime closed; this gate keeps a
// new cast from reopening it. The trust package itself defines the type and is
// exempt; _test.go files are exempt because a test pins a literal key.
func TestArch_BundleKeyOnlyMintedInTrust(t *testing.T) {
	root := moduleRoot(t)
	exempt := filepath.Join(root, filepath.FromSlash("internal/core/trust"))
	fset := token.NewFileSet()
	var findings []string
	var scanned int
	for _, scope := range goosScopes {
		err := filepath.WalkDir(filepath.Join(root, scope), func(p string, d fs.DirEntry, err error) error {
			switch {
			case err != nil:
				return err
			case d.IsDir() && (skippedDir(d.Name()) || p == exempt):
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
			for _, pos := range bundleKeyCasts(f) {
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
		t.Errorf("trust.BundleKey conversion in production code: %s\n"+
			"    take the key from Reference.LockKey or BundleRef.BundleIdentity; "+
			"a cast keys the string in whatever spelling it arrived in.", f)
	}
}

// bundleKeyCasts returns the position of every conversion to trust.BundleKey in
// f, under whatever name f imports the trust package as.
func bundleKeyCasts(f *ast.File) []token.Pos {
	name := ""
	for _, imp := range f.Imports {
		if path, _ := strconv.Unquote(imp.Path.Value); path == trustImportPath {
			name = "trust"
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
		call, ok := n.(*ast.CallExpr)
		if !ok || len(call.Args) != 1 {
			return true
		}
		fun := call.Fun
		for {
			paren, ok := fun.(*ast.ParenExpr)
			if !ok {
				break
			}
			fun = paren.X
		}
		sel, ok := fun.(*ast.SelectorExpr)
		if !ok || sel.Sel.Name != "BundleKey" {
			return true
		}
		if id, ok := sel.X.(*ast.Ident); ok && id.Name == name {
			out = append(out, call.Pos())
		}
		return true
	})
	return out
}
