package archlint

import (
	"go/ast"
	"go/token"
	"strings"

	"golang.org/x/tools/go/analysis"

	"github.com/ctxloom/ctxloom/internal/shared/archrules"
)

// writeDisciplineScopes are the subtrees this rule governs: production code
// only, both the library and the binaries that drive it.
var writeDisciplineScopes = []string{"internal", "cmd"}

// writeDisciplineExemptDirs are the packages that ARE the write library, and
// so are structurally exempt: they are not a second copy of iox, they are
// the thing this rule protects. The lock primitive is github.com/gofrs/flock,
// a third-party module rather than an in-tree package, so unlike its
// predecessor (internal/shared/filelock, deleted — every lock call site
// calls flock.New directly per internal/core/agent/rendezvous.go's idiom)
// there is nothing beside iox left to name here.
var writeDisciplineExemptDirs = []string{
	"internal/shared/iox",
}

// forbiddenOSCalls are the raw-fs-write entry points forbidden outside the
// exempt set. os.OpenFile is handled separately: only its write-mode calls
// count.
var forbiddenOSCalls = map[string]bool{
	"WriteFile": true,
	"Create":    true,
	"Rename":    true,
	"Symlink":   true,
}

// writeFlagConstants are the os.O_* names whose presence in an os.OpenFile
// flags argument makes the call write-mode. os.O_RDONLY is deliberately
// absent: a plain read-only open is not this rule's business, and it is 0 on
// every platform, so it never appears as a named identifier that means
// anything else.
var writeFlagConstants = map[string]bool{
	"O_WRONLY": true,
	"O_RDWR":   true,
	"O_TRUNC":  true,
	"O_CREATE": true,
	"O_APPEND": true,
}

// forbiddenAferoPackageCalls are the package-level afero.* write entry points
// that shadow os.WriteFile/os.CreateTemp. afero.Rename exists only as a
// method, covered by forbiddenAferoMethodCalls.
var forbiddenAferoPackageCalls = map[string]bool{
	"WriteFile": true,
	"TempFile":  true,
}

// forbiddenAferoMethodCalls are the write-shaped afero.Fs methods forbidden on
// a receiver that looks like an afero.Fs. OpenFile is handled separately
// because only its write-mode calls count.
var forbiddenAferoMethodCalls = map[string]bool{
	"Create": true,
	"Rename": true,
}

// WriteDisciplineAnalyzer enforces that raw filesystem writes route through
// internal/shared/iox.
//
// A raw os.WriteFile leaves a half-written file behind on a crash or a short
// write, and leaves no ownership record. iox is the one place the atomic
// write-temp-then-rename sequence and the ownership ledger live, so a call
// that bypasses it is a durability and provenance hole rather than a style
// preference.
//
// This rule is a RATCHET: every site found at authoring time is grandfathered
// into archrules.WriteDisciplineAllowed with the fix required to remove it. What it buys
// immediately is that the set cannot grow silently, and an entry that has
// stopped being a violation is reported so the baseline can only shrink.
var WriteDisciplineAnalyzer = &analysis.Analyzer{
	Name: "archwritediscipline",
	Doc:  "raw filesystem writes must route through internal/shared/iox",
	Run:  runWriteDiscipline,
}

func runWriteDiscipline(pass *analysis.Pass) (any, error) {
	if SkipPass(pass) {
		return nil, nil
	}
	dir := PkgDir(pass)
	if dir == "" || !archrules.UnderAny(dir, writeDisciplineScopes) || archrules.UnderAny(dir, writeDisciplineExemptDirs) {
		return nil, nil
	}

	seen := map[string]bool{}
	for _, f := range ProdFiles(pass) {
		rel := FileRel(pass, f)
		for _, decl := range f.Decls {
			fd, ok := decl.(*ast.FuncDecl)
			if !ok || fd.Body == nil {
				continue
			}
			sym := FuncSymbol(fd)
			key := rel + "#" + sym
			collectRawWrites(fd.Body, func(pos token.Pos, call string) {
				seen[key] = true
				if _, ok := archrules.WriteDisciplineAllowed[key]; ok {
					return
				}
				pass.Reportf(pos,
					"%s calls %s directly — raw filesystem writes must route through "+
						"internal/shared/iox, which is where the atomic write-then-rename sequence and the "+
						"ownership ledger live. If this is a deliberate, reviewed exception, add %q to "+
						"archrules.WriteDisciplineAllowed naming the fix "+
						"required to remove it.", sym, call, key)
			})
		}
	}
	reportStaleAllowlist(pass, archrules.WriteDisciplineAllowed, analyzedFiles(pass), seen, "archrules.WriteDisciplineAllowed")
	return nil, nil
}

// collectRawWrites walks node for calls matching a forbidden write callee,
// attributing every hit — including inside a nested closure — to the
// enclosing declaration, whose job it is to fix them.
func collectRawWrites(node ast.Node, report func(pos token.Pos, call string)) {
	ast.Inspect(node, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		// Package-qualified calls are checked against their own forbidden-name
		// sets and never fall through to the afero.Fs receiver heuristic, so
		// the two checks stay visibly disjoint.
		if pkgIdent, ok := sel.X.(*ast.Ident); ok {
			switch pkgIdent.Name {
			case "os":
				switch {
				case forbiddenOSCalls[sel.Sel.Name]:
					report(call.Pos(), "os."+sel.Sel.Name)
				case sel.Sel.Name == "OpenFile" && len(call.Args) >= 2 && exprMentionsWriteFlag(call.Args[1]):
					report(call.Pos(), "os.OpenFile")
				}
				return true
			case "afero":
				if forbiddenAferoPackageCalls[sel.Sel.Name] {
					report(call.Pos(), "afero."+sel.Sel.Name)
				}
				return true
			}
		}
		if aferoFsMethodCall(sel) {
			switch {
			case forbiddenAferoMethodCalls[sel.Sel.Name]:
				report(call.Pos(), "(afero.Fs)."+sel.Sel.Name)
			case sel.Sel.Name == "OpenFile" && len(call.Args) >= 2 && exprMentionsWriteFlag(call.Args[1]):
				report(call.Pos(), "(afero.Fs).OpenFile")
			}
		}
		return true
	})
}

// isAferoFsLikeName reports whether name looks like it holds an afero.Fs, by
// the codebase's naming convention rather than by type information.
func isAferoFsLikeName(name string) bool {
	lower := strings.ToLower(name)
	return strings.HasSuffix(lower, "fs") || strings.HasPrefix(lower, "fsys")
}

// aferoFsMethodCall reports whether sel's receiver is a name
// isAferoFsLikeName accepts: a bare identifier, or the final selector of a
// field access.
func aferoFsMethodCall(sel *ast.SelectorExpr) bool {
	switch x := sel.X.(type) {
	case *ast.Ident:
		return isAferoFsLikeName(x.Name)
	case *ast.SelectorExpr:
		return isAferoFsLikeName(x.Sel.Name)
	}
	return false
}

// exprMentionsWriteFlag reports whether an os.OpenFile flags argument contains
// any write-implying os.O_* name, however it is combined. Purely syntactic: it
// asks whether the name appears, never what the expression evaluates to.
func exprMentionsWriteFlag(expr ast.Expr) bool {
	found := false
	ast.Inspect(expr, func(n ast.Node) bool {
		if found {
			return false
		}
		if ident, ok := n.(*ast.Ident); ok && writeFlagConstants[ident.Name] {
			found = true
			return false
		}
		return true
	})
	return found
}
