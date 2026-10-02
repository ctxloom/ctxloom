package archlint

import (
	"go/ast"
	"go/token"
	"go/types"

	"golang.org/x/tools/go/analysis"

	"github.com/ctxloom/ctxloom/internal/shared/archrules"
)

// writeDisciplineScopes are the subtrees this rule governs: both the library
// and the binaries that drive it, with their tests.
var writeDisciplineScopes = []string{"internal", "cmd"}

// writeDisciplineExemptDirs are the packages that ARE the write library, and
// so are structurally exempt: they hold the decorators and the atomic writer
// every other package is sent to. The lock primitive is github.com/gofrs/flock,
// a third-party module; internal/shared/filelock wraps it (creating only the
// lock file and its directory) and is not a write library, so safefs is the
// only in-tree write library to name here.
var writeDisciplineExemptDirs = []string{
	"internal/shared/safefs",
}

// forbiddenOSCalls are the raw-fs-write entry points forbidden outside the
// exempt set: each creates, replaces or empties a file's content or name.
// os.OpenFile is handled separately: only its write-mode calls count.
var forbiddenOSCalls = map[string]bool{
	"WriteFile":  true,
	"Create":     true,
	"CreateTemp": true,
	"Rename":     true,
	"Symlink":    true,
	"Link":       true,
	"Truncate":   true,
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

// forbiddenAferoMethodCalls are the write-shaped afero methods forbidden on
// any receiver whose method resolves into package afero — an afero.Fs, a
// concrete afero filesystem, or a struct embedding one. OpenFile is handled
// separately because only its write-mode calls count.
var forbiddenAferoMethodCalls = map[string]bool{
	"Create": true,
	"Rename": true,
}

// WriteDisciplineAnalyzer enforces that raw filesystem writes route through
// internal/shared/safefs.
//
// A raw write bypasses the two things safefs carries as afero decorators: the
// empty-write guard (safefs.NewGuardFs), which refuses zero bytes over an
// existing file, and durability (safefs.NewDurableFs). It also bypasses the
// atomic write-temp-then-rename (safefs.WriteFile), so a crash or short write
// leaves a half-written file. A guard on the fs cannot see a write that never
// went through the fs, so a raw call is a hole in it, not a style choice.
//
// Calls are classified through type information, never by spelling: an os or
// afero qualifier is resolved through its import (a renamed import is still
// caught, and a package that merely ends in "fs" is not mistaken for one),
// and a method counts when it resolves into package afero.
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
	Doc:  "raw filesystem writes must route through internal/shared/safefs",
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
		remedy: "route through internal/shared/safefs (safefs.WriteFile, or safefs.NewAtomicFile for a " +
			"stream), whose writes pass the empty-write guard (safefs.NewGuardFs) and, with safefs.Durable(), " +
			"the durability decorator (safefs.NewDurableFs)",
	}
	testWriteArm = writeArm{
		aferoOnly: true,
		allowed:   archrules.TestWriteDisciplineAllowed,
		allowName: "archrules.TestWriteDisciplineAllowed",
		remedy: "route through testsupport.WriteFile/WriteFileString/SeedTree (or a sanctioned writer " +
			"such as safefs.WriteFile), so a fixture never disagrees with production about what " +
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
		collectRawWrites(pass.TypesInfo, node, arm.aferoOnly, func(pos token.Pos, call string) {
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
func collectRawWrites(info *types.Info, node ast.Node, aferoOnly bool, report func(pos token.Pos, call string)) {
	ast.Inspect(node, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		if name := rawWriteCallee(info, sel, call, aferoOnly); name != "" {
			report(call.Pos(), name)
		}
		return true
	})
}

// rawWriteCallee names the forbidden write a call makes, or "" for none.
//
// A package-qualified call is judged against that package's forbidden-name
// set and never falls through to the method check, so the two stay visibly
// disjoint.
func rawWriteCallee(info *types.Info, sel *ast.SelectorExpr, call *ast.CallExpr, aferoOnly bool) string {
	if path, ok := qualifierPath(info, sel.X); ok {
		return packageWriteCallee(path, sel, call, aferoOnly)
	}
	name := sel.Sel.Name
	if aferoMethod(info, sel) && (forbiddenAferoMethodCalls[name] || isWriteModeOpen(sel, call)) {
		return "(afero.Fs)." + name
	}
	return ""
}

// qualifierPath is the import path x names when x is a package qualifier.
func qualifierPath(info *types.Info, x ast.Expr) (string, bool) {
	id, ok := x.(*ast.Ident)
	if !ok {
		return "", false
	}
	pkg, ok := info.Uses[id].(*types.PkgName)
	if !ok {
		return "", false
	}
	return pkg.Imported().Path(), true
}

// aferoMethod reports whether sel selects a method that resolves into package
// afero: a method of afero.Fs or a concrete afero filesystem, including one
// promoted through an embedded field, and a method expression such as
// (*afero.MemMapFs).Create.
func aferoMethod(info *types.Info, sel *ast.SelectorExpr) bool {
	s, ok := info.Selections[sel]
	if !ok {
		return false
	}
	pkg := s.Obj().Pkg()
	return pkg != nil && pkg.Path() == aferoImportPath
}

// packageWriteCallee is rawWriteCallee's arm for a call qualified by an
// imported package, or "" when that package's call is not a forbidden write.
func packageWriteCallee(path string, sel *ast.SelectorExpr, call *ast.CallExpr, aferoOnly bool) string {
	name := sel.Sel.Name
	switch {
	case path == aferoImportPath && forbiddenAferoPackageCalls[name]:
		return "afero." + name
	case path == "os" && !aferoOnly && (forbiddenOSCalls[name] || isWriteModeOpen(sel, call)):
		return "os." + name
	}
	return ""
}

// isWriteModeOpen reports whether call is an OpenFile whose flags argument
// asks for a write.
func isWriteModeOpen(sel *ast.SelectorExpr, call *ast.CallExpr) bool {
	return sel.Sel.Name == "OpenFile" && len(call.Args) >= 2 && exprMentionsWriteFlag(call.Args[1])
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
