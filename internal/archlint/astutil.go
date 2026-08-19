package archlint

import (
	"go/ast"
	"go/token"
	"strconv"
)

// FuncSymbol names a declaration the way an allowlist key does: "Method" for a
// plain function, "Recv.Method" for one with a receiver.
//
// Allowlists are keyed by symbol rather than by line, so an edit above a
// violation cannot silently move the exemption onto different code.
func FuncSymbol(d *ast.FuncDecl) string {
	if d.Recv == nil || len(d.Recv.List) == 0 {
		return d.Name.Name
	}
	recvType := d.Recv.List[0].Type
	if star, ok := recvType.(*ast.StarExpr); ok {
		recvType = star.X
	}
	if ident, ok := recvType.(*ast.Ident); ok {
		return ident.Name + "." + d.Name.Name
	}
	return d.Name.Name
}

// StringLit decodes e as a string literal. ok is false for anything that is
// not one.
func StringLit(e ast.Expr) (string, bool) {
	bl, ok := e.(*ast.BasicLit)
	if !ok || bl.Kind != token.STRING {
		return "", false
	}
	v, err := strconv.Unquote(bl.Value)
	if err != nil {
		return "", false
	}
	return v, true
}

// SelectorOn reports whether e is a selector rooted at an identifier with the
// given package name, and returns the selected name.
func SelectorOn(e ast.Expr, pkg string) (string, bool) {
	sel, ok := e.(*ast.SelectorExpr)
	if !ok {
		return "", false
	}
	ident, ok := sel.X.(*ast.Ident)
	if !ok || ident.Name != pkg {
		return "", false
	}
	return sel.Sel.Name, true
}

// PackageLevelConsts is the set of package-level const names declared across
// the package's production files.
//
// A package-level const is Go's own way of saying "this is a fixed,
// compile-time name" — the shape a path segment takes. A runtime-built path
// variable is never a const, so it is never mistaken for one.
func PackageLevelConsts(files []*ast.File) map[string]bool {
	consts := map[string]bool{}
	for _, f := range files {
		for _, decl := range f.Decls {
			gd, ok := decl.(*ast.GenDecl)
			if !ok || gd.Tok != token.CONST {
				continue
			}
			for _, spec := range gd.Specs {
				vs, ok := spec.(*ast.ValueSpec)
				if !ok {
					continue
				}
				for _, n := range vs.Names {
					if n.Name != "_" {
						consts[n.Name] = true
					}
				}
			}
		}
	}
	return consts
}

// EachFuncDecl calls fn for every function declaration with a body in the
// package's production files, with the symbol name a violation is keyed on.
func EachFuncDecl(files []*ast.File, fn func(fd *ast.FuncDecl, sym string)) {
	for _, f := range files {
		for _, decl := range f.Decls {
			fd, ok := decl.(*ast.FuncDecl)
			if !ok || fd.Body == nil {
				continue
			}
			fn(fd, FuncSymbol(fd))
		}
	}
}
