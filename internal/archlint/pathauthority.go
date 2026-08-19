package archlint

import (
	"go/ast"
	"regexp"
	"strconv"

	"golang.org/x/tools/go/analysis"
)

// pathAuthorityExemptDir is the vocabulary itself, not a consumer of it.
const pathAuthorityExemptDir = "internal/paths"

// segmentLiteralPattern is what "looks like a path segment" means here:
// letters, digits, dot, dash, underscore. It keeps glob wildcards and other
// non-segment literals that happen to sit in a Join argument list out of the
// baseline — they are not a duplicated NAME.
var segmentLiteralPattern = regexp.MustCompile(`^[A-Za-z0-9._-]+$`)

// pathAuthorityAllowed is this rule's shrinking allowlist: a durable symbol
// reference ("file.go#Symbol") mapped to the fix required to remove it.
var pathAuthorityAllowed = map[string]string{}

// PathAuthorityAnalyzer enforces that ctxloom's on-disk path segments are
// named in internal/paths and nowhere else.
//
// internal/paths is the single declarative source of truth for the on-disk
// layout. A segment spelled as a literal elsewhere is a SECOND spelling of a
// fact that paths.Layout cannot see and doctor cannot walk.
//
// The detection signal is co-occurrence, not a literal blacklist: a Join call
// outside internal/paths that ALREADY references the paths package — proving
// it builds somewhere under the ctxloom-managed tree — and ALSO carries a bare
// literal or a package-level const as another segment. The paths reference
// shows the author reached for the vocabulary package and then, for this one
// segment, did not. A Join call with no paths reference is out of scope: this
// rule governs ctxloom's own tree, not every path built anywhere.
var PathAuthorityAnalyzer = &analysis.Analyzer{
	Name: "archpathauthority",
	Doc:  "ctxloom path segments must be named constants in internal/paths, not literals at the call site",
	Run:  runPathAuthority,
}

func runPathAuthority(pass *analysis.Pass) (any, error) {
	if SkipPass(pass) {
		return nil, nil
	}
	dir := PkgDir(pass)
	if dir == "" || UnderSubtree(dir, pathAuthorityExemptDir) {
		return nil, nil
	}
	files := ProdFiles(pass)
	consts := PackageLevelConsts(files)

	EachFuncDecl(files, func(fd *ast.FuncDecl, sym string) {
		ast.Inspect(fd.Body, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok || !isJoinCall(call) || !callReferencesPaths(call) {
				return true
			}
			var segments []string
			for _, arg := range call.Args {
				if seg, suspect := suspectSegment(arg, consts); suspect {
					segments = append(segments, seg)
				}
			}
			if len(segments) == 0 {
				return true
			}
			key := dir + "#" + sym
			if _, ok := pathAuthorityAllowed[key]; ok {
				return true
			}
			pass.Reportf(call.Pos(),
				"%s builds a path alongside a paths.* reference using segment(s) %v that internal/paths "+
					"does not name — every ctxloom path segment must be a named constant there. If this is "+
					"a deliberate, reviewed exception, add %q to pathAuthorityAllowed in "+
					"internal/archlint/pathauthority.go naming the fix required to remove it.",
				sym, segments, key)
			return true
		})
	})
	return nil, nil
}

// isJoinCall reports whether call is filepath.Join or path.Join.
func isJoinCall(call *ast.CallExpr) bool {
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok || sel.Sel.Name != "Join" {
		return false
	}
	pkg, ok := sel.X.(*ast.Ident)
	return ok && (pkg.Name == "filepath" || pkg.Name == "path")
}

// callReferencesPaths reports whether any argument selects off an identifier
// named "paths", the signal that this Join builds under the managed tree.
func callReferencesPaths(call *ast.CallExpr) bool {
	found := false
	for _, arg := range call.Args {
		ast.Inspect(arg, func(n ast.Node) bool {
			if found {
				return false
			}
			if sel, ok := n.(*ast.SelectorExpr); ok {
				if ident, ok := sel.X.(*ast.Ident); ok && ident.Name == "paths" {
					found = true
					return false
				}
			}
			return true
		})
		if found {
			break
		}
	}
	return found
}

// suspectSegment reports whether arg is a bare literal that looks like a path
// segment, or a bare identifier naming a package-level const — the two shapes
// a hand-rolled segment takes beside a paths.* reference.
func suspectSegment(arg ast.Expr, consts map[string]bool) (string, bool) {
	switch a := arg.(type) {
	case *ast.BasicLit:
		val, ok := StringLit(a)
		if !ok || val == "" || val == "." || val == ".." {
			return "", false
		}
		if !segmentLiteralPattern.MatchString(val) {
			return "", false
		}
		return strconv.Quote(val), true
	case *ast.Ident:
		if consts[a.Name] {
			return a.Name + " (local const)", true
		}
	}
	return "", false
}
