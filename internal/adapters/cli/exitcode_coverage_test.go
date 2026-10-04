// This gate enforces that a management command must not exit 0 on a real
// failure ("exit-0-on-failure", the project's signature bug family — see the
// silent-no-op standing note: exit 0, success message, nothing actually
// done). `strictness` is deliberately LAUNCH-ONLY (it records fatal startup
// findings that the agent spawner turns into an error via
// strictness.FindingsError), so it gives ordinary management commands no
// policy at all. The actual mechanism management commands need already
// exists one layer up: cli.Run (root.go) turns any non-nil RunE error
// into os.Exit(1) (or an *ExitError's own code) — that seam works fine
// whenever a RunE actually RETURNS its error.
//
// The bug is narrower than "no policy exists": a RunE body collects per-item
// failures into a result.Errors slice, prints each one, and then `return nil`
// anyway — discarding a genuine failure right before the one seam that would
// have turned it into a non-zero exit. clidiag.WarnErrors is the shared
// replacement: `return clidiag.WarnErrors(prog, result.Errors)` prints
// identically but returns non-nil when errs is non-empty.
//
// The gate is STRUCTURAL: a function that ranges over an `.Errors` slice must
// have an error return conditioned on that slice. It never looks at how the
// loop prints — a detector keyed on print-call spellings went blind to a
// warn-only loop that had merely switched writers, and then reported the site
// as debt paid down.
package cli

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// silentFailureSites returns "file.go:Func" for every top-level function in
// files that ranges over an `<expr>.Errors` slice — whatever the loop body
// does — without an error-propagating path for that same slice in its own
// body. A path propagates when it returns a non-nil error that:
//
//   - is returned from inside the loop over the slice, or
//   - is returned under an `if` whose condition mentions the slice, or
//   - mentions the slice itself (`return clidiag.WarnErrors(p, r.Errors)`), or
//   - is a call of a package method on the slice's owner whose own body
//     propagates `<receiver>.Errors` by these same rules (`return
//     result.exitErr(n)`).
//
// Returns inside a closure do not count: only the function's own return value
// reaches cli.Run. A non-nil error is a return whose LAST result is not the
// identifier nil.
func silentFailureSites(files map[string]*ast.File) []string {
	methods := map[string][]*ast.FuncDecl{}
	for _, f := range files {
		for _, d := range f.Decls {
			if fd, ok := d.(*ast.FuncDecl); ok && fd.Recv != nil && fd.Body != nil {
				methods[fd.Name.Name] = append(methods[fd.Name.Name], fd)
			}
		}
	}
	var sites []string
	for name, f := range files {
		for _, d := range f.Decls {
			fd, ok := d.(*ast.FuncDecl)
			if !ok || fd.Body == nil {
				continue
			}
			for _, errs := range rangedErrorSlices(fd.Body) {
				if !propagates(fd.Body, errs, methods, map[*ast.FuncDecl]bool{}) {
					sites = append(sites, name+":"+fd.Name.Name)
					break
				}
			}
		}
	}
	sort.Strings(sites)
	return sites
}

// rangedErrorSlices returns every `<expr>.Errors` selector body ranges over,
// closures included — a loop inside an output closure is still this
// function's loop.
func rangedErrorSlices(body *ast.BlockStmt) []*ast.SelectorExpr {
	var out []*ast.SelectorExpr
	ast.Inspect(body, func(n ast.Node) bool {
		if rs, ok := n.(*ast.RangeStmt); ok {
			if sel, ok := rs.X.(*ast.SelectorExpr); ok && sel.Sel.Name == "Errors" {
				out = append(out, sel)
			}
		}
		return true
	})
	return out
}

// propagates reports whether body has an error-propagating path for errs
// (see silentFailureSites). seen stops method-following cycles.
func propagates(body *ast.BlockStmt, errs *ast.SelectorExpr, methods map[string][]*ast.FuncDecl, seen map[*ast.FuncDecl]bool) bool {
	found := false
	inspectOwnBody(body, func(n ast.Node) {
		if found {
			return
		}
		switch s := n.(type) {
		case *ast.RangeStmt:
			found = mentions(s.X, errs) && returnsError(s.Body)
		case *ast.IfStmt:
			found = mentions(s.Cond, errs) && returnsError(s.Body)
		case *ast.ReturnStmt:
			if !isErrorReturn(s) {
				return
			}
			last := s.Results[len(s.Results)-1]
			found = mentions(last, errs) || ownerMethodPropagates(last, errs, methods, seen)
		}
	})
	return found
}

// ownerMethodPropagates reports whether ret is `<owner>.M(...)`, owner being
// the expression errs selects Errors from, and every package method named M
// propagates its receiver's Errors.
func ownerMethodPropagates(ret ast.Expr, errs *ast.SelectorExpr, methods map[string][]*ast.FuncDecl, seen map[*ast.FuncDecl]bool) bool {
	call, ok := ret.(*ast.CallExpr)
	if !ok {
		return false
	}
	fun, ok := call.Fun.(*ast.SelectorExpr)
	if !ok || !sameExpr(fun.X, errs.X) {
		return false
	}
	candidates := methods[fun.Sel.Name]
	if len(candidates) == 0 {
		return false
	}
	for _, m := range candidates {
		if seen[m] || len(m.Recv.List) == 0 || len(m.Recv.List[0].Names) == 0 {
			return false
		}
		seen[m] = true
		recvErrs := &ast.SelectorExpr{X: m.Recv.List[0].Names[0], Sel: ast.NewIdent("Errors")}
		if !propagates(m.Body, recvErrs, methods, seen) {
			return false
		}
	}
	return true
}

// inspectOwnBody visits every node of body without descending into closures.
func inspectOwnBody(body ast.Node, visit func(ast.Node)) {
	ast.Inspect(body, func(n ast.Node) bool {
		if _, ok := n.(*ast.FuncLit); ok {
			return false
		}
		if n != nil {
			visit(n)
		}
		return true
	})
}

// returnsError reports whether block, outside closures, returns a non-nil error.
func returnsError(block ast.Node) bool {
	found := false
	inspectOwnBody(block, func(n ast.Node) {
		if r, ok := n.(*ast.ReturnStmt); ok && isErrorReturn(r) {
			found = true
		}
	})
	return found
}

func isErrorReturn(r *ast.ReturnStmt) bool {
	if len(r.Results) == 0 {
		return false
	}
	id, ok := r.Results[len(r.Results)-1].(*ast.Ident)
	return !ok || id.Name != "nil"
}

// mentions reports whether n, outside closures, contains an expression equal
// to target — a closure handed the slice is not the function returning it.
func mentions(n ast.Node, target ast.Expr) bool {
	found := false
	inspectOwnBody(n, func(c ast.Node) {
		if e, ok := c.(ast.Expr); ok && sameExpr(e, target) {
			found = true
		}
	})
	return found
}

// sameExpr compares two identifier/selector chains (`result.Errors`).
func sameExpr(a, b ast.Expr) bool {
	switch x := a.(type) {
	case *ast.Ident:
		y, ok := b.(*ast.Ident)
		return ok && x.Name == y.Name
	case *ast.SelectorExpr:
		y, ok := b.(*ast.SelectorExpr)
		return ok && x.Sel.Name == y.Sel.Name && sameExpr(x.X, y.X)
	}
	return false
}

// parsePackageSources parses every non-test .go file directly in
// internal/adapters/cli (the package this gate scopes: cobra command wiring).
func parsePackageSources(t *testing.T) map[string]*ast.File {
	t.Helper()
	// Absolute, from this package's compiled-in source path — not "." (see
	// pkgSourceDir): TestMain sandboxes the binary into a temp cwd, where a
	// "." scan finds no files and this gate silently reports no sites.
	dir := pkgSourceDir(t)
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read internal/adapters/cli: %v", err)
	}
	fset := token.NewFileSet()
	files := map[string]*ast.File{}
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || filepath.Ext(name) != ".go" || strings.HasSuffix(name, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(fset, filepath.Join(dir, name), nil, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", name, err)
		}
		files[name] = f
	}
	if len(files) == 0 {
		t.Fatalf("no source files found in %s", dir)
	}
	return files
}

func TestExitCodePolicy_NoSilentFailureSites(t *testing.T) {
	for _, s := range silentFailureSites(parsePackageSources(t)) {
		t.Errorf("management command %s ranges over a result's Errors without returning an error conditioned on them, so it can warn a real failure and still exit 0 (T9/R1, the exit-0-on-failure family) — return clidiag.WarnErrors(...) or check len(...Errors) and return an error", s)
	}
}
