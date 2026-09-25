package archlint

import (
	"go/ast"
	"go/token"
	"strings"

	"golang.org/x/tools/go/analysis"

	"github.com/ctxloom/ctxloom/internal/shared/archrules"
)

// writeDisciplineScopes are the subtrees this rule governs: both the library
// and the binaries that drive it, with their tests.
var writeDisciplineScopes = []string{"internal", "cmd"}

// writeDisciplineExemptDirs are the packages that ARE the write library, and
// so are structurally exempt: they are not a second copy of iox, they are
// the thing this rule protects. The lock primitive is github.com/gofrs/flock,
// a third-party module; internal/shared/filelock wraps it (creating only the
// lock file and its directory) and is not a write library, so iox is the
// only in-tree write library to name here.
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
// Test code is judged too, by a narrower arm: raw AFERO writes in a _test.go
// file that imports afero must route through the test-support writers, so a
// fixture never disagrees with production about what "write a file" means.
// archrules.TestWriteDisciplineAllowed's doc carries why the arm is that
// narrow.
//
// This rule is a RATCHET: every site found at authoring time is grandfathered
// into an allowlist with the fix required to remove it. What it buys
// immediately is that the set cannot grow silently, and an entry that has
// stopped being a violation is reported so the baseline can only shrink.
var WriteDisciplineAnalyzer = &analysis.Analyzer{
	Name: "archwritediscipline",
	Doc:  "raw filesystem writes must route through internal/shared/iox",
	Run:  runWriteDiscipline,
}

// packageLevelSymbol keys a write in a package-level var initialiser, which
// runs at program start inside no function that could be named instead.
const packageLevelSymbol = "<package-level>"

// aferoImportPath is the import a test file must name to be in the test arm's
// scope.
const aferoImportPath = "github.com/spf13/afero"

// writeArm is how one file is judged: which writes count, which allowlist
// excuses one, and what a violation must do instead.
type writeArm struct {
	aferoOnly bool
	allowed   map[string]string
	allowName string
	remedy    string
}

var (
	prodWriteArm = writeArm{
		allowed:   archrules.WriteDisciplineAllowed,
		allowName: "archrules.WriteDisciplineAllowed",
		remedy: "route through internal/shared/iox, which is where the atomic write-then-rename " +
			"sequence and the ownership ledger live",
	}
	testWriteArm = writeArm{
		aferoOnly: true,
		allowed:   archrules.TestWriteDisciplineAllowed,
		allowName: "archrules.TestWriteDisciplineAllowed",
		remedy: "route through testsupport.WriteFile/WriteFileString/SeedTree (or a sanctioned writer " +
			"such as iox.WriteFileAtomicFs), so a fixture never disagrees with production about what " +
			"\"write a file\" means",
	}
)

func runWriteDiscipline(pass *analysis.Pass) (any, error) {
	dir := PkgDir(pass)
	if dir == "" || !archrules.UnderAny(dir, writeDisciplineScopes) || archrules.UnderAny(dir, writeDisciplineExemptDirs) {
		return nil, nil
	}
	seen := map[string]bool{}
	for _, f := range OwnedFiles(pass) {
		checkRawWrites(pass, f, seen)
	}
	analyzed := analyzedFiles(pass)
	reportStaleAllowlist(pass, archrules.WriteDisciplineAllowed, analyzed, seen, "archrules.WriteDisciplineAllowed")
	reportStaleAllowlist(pass, archrules.TestWriteDisciplineAllowed, analyzed, seen, "archrules.TestWriteDisciplineAllowed")
	return nil, nil
}

// writeArmFor picks the arm that judges f; ok is false for a test file the
// test arm does not cover.
func writeArmFor(pass *analysis.Pass, f *ast.File) (arm writeArm, ok bool) {
	if !IsTestFile(pass, f) {
		return prodWriteArm, true
	}
	return testWriteArm, fileImports(f, aferoImportPath)
}

// checkRawWrites reports every forbidden write in f that its arm does not
// excuse, recording in seen every key that had one.
func checkRawWrites(pass *analysis.Pass, f *ast.File, seen map[string]bool) {
	arm, ok := writeArmFor(pass, f)
	if !ok {
		return
	}
	rel := FileRel(pass, f)
	eachWriteSubject(f, func(node ast.Node, sym string) {
		key := rel + "#" + sym
		collectRawWrites(node, arm.aferoOnly, func(pos token.Pos, call string) {
			seen[key] = true
			if _, ok := arm.allowed[key]; ok {
				return
			}
			pass.Reportf(pos,
				"%s calls %s directly — raw filesystem writes must %s. If this is a deliberate, reviewed "+
					"exception, add %q to %s naming the fix required to remove it.",
				sym, call, arm.remedy, key, arm.allowName)
		})
	})
}

// eachWriteSubject calls fn for every place in f a write can be written: each
// function body, keyed by its symbol, and each package-level var initialiser,
// keyed by packageLevelSymbol.
func eachWriteSubject(f *ast.File, fn func(node ast.Node, sym string)) {
	for _, decl := range f.Decls {
		switch d := decl.(type) {
		case *ast.FuncDecl:
			if d.Body != nil {
				fn(d.Body, FuncSymbol(d))
			}
		case *ast.GenDecl:
			if d.Tok != token.VAR {
				continue
			}
			for _, spec := range d.Specs {
				for _, val := range spec.(*ast.ValueSpec).Values {
					fn(val, packageLevelSymbol)
				}
			}
		}
	}
}

// fileImports reports whether f's import declarations name importPath.
func fileImports(f *ast.File, importPath string) bool {
	for _, spec := range f.Imports {
		if p, err := ImportPathOf(spec); err == nil && p == importPath {
			return true
		}
	}
	return false
}

// collectRawWrites walks node for calls matching a forbidden write callee,
// attributing every hit — including inside a nested closure — to the
// enclosing subject, whose job it is to fix them. aferoOnly drops the os.*
// set, for the test arm.
func collectRawWrites(node ast.Node, aferoOnly bool, report func(pos token.Pos, call string)) {
	ast.Inspect(node, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		if name := rawWriteCallee(sel, call, aferoOnly); name != "" {
			report(call.Pos(), name)
		}
		return true
	})
}

// rawWriteCallee names the forbidden write a call makes, or "" for none.
//
// Package-qualified calls are checked against their own forbidden-name sets
// and never fall through to the afero.Fs receiver heuristic, so the two checks
// stay visibly disjoint.
func rawWriteCallee(sel *ast.SelectorExpr, call *ast.CallExpr, aferoOnly bool) string {
	name := sel.Sel.Name
	if pkgIdent, ok := sel.X.(*ast.Ident); ok {
		switch pkgIdent.Name {
		case "os":
			if !aferoOnly && (forbiddenOSCalls[name] || isWriteModeOpen(sel, call)) {
				return "os." + name
			}
			return ""
		case "afero":
			if forbiddenAferoPackageCalls[name] {
				return "afero." + name
			}
			return ""
		}
	}
	if aferoFsMethodCall(sel) && (forbiddenAferoMethodCalls[name] || isWriteModeOpen(sel, call)) {
		return "(afero.Fs)." + name
	}
	return ""
}

// isWriteModeOpen reports whether call is an OpenFile whose flags argument
// asks for a write.
func isWriteModeOpen(sel *ast.SelectorExpr, call *ast.CallExpr) bool {
	return sel.Sel.Name == "OpenFile" && len(call.Args) >= 2 && exprMentionsWriteFlag(call.Args[1])
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
