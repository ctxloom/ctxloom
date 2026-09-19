package archlint

import (
	"go/ast"
	"go/token"
	"regexp"

	"golang.org/x/tools/go/analysis"

	"github.com/ctxloom/ctxloom/internal/shared/archrules"
)

// lockDisciplineScopes are the packages this rule walks: the engine
// SettingsWriter implementors plus the shared reconcilers they call into. The
// lock and record primitives themselves are not scanned — their callers are
// what must hold the lock.
var lockDisciplineScopes = []string{
	"internal/engines/claude",
	"internal/core/agent",
}

// lockDisciplineExemptFiles are the primitives this rule protects usage OF,
// not usage BY. Scanning them would misattribute their own internal
// read-then-write shapes to a missing lock the caller is responsible for.
var lockDisciplineExemptFiles = map[string]bool{
	"internal/core/agent/settings_io.go":   true,
	"internal/core/sessions/filelock.go":   true,
	"internal/shared/filelock/filelock.go": true,
	"internal/shared/iox/atomicwrite.go":   true,
}

var lockReadPattern = regexp.MustCompile(`(?i)^(read|load)`)

var lockSavePattern = regexp.MustCompile(`(?i)^save`)

var lockWritePrimitives = map[string]bool{
	"AtomicWriteFile":          true,
	"WriteFileAtomicFs":        true,
	"WriteManagedContext":      true,
	"WriteManagedPackageFiles": true,
	"WriteManagedCommandFiles": true,
}

// LockDisciplineAnalyzer enforces that a read-then-write over an engine's
// settings file happens under a file lock.
//
// Two ctxloom processes reconciling the same engine settings file interleave
// as read-read-write-write, and the second write silently discards the first
// process's change. sessions.WithFileLock is the serialization point; a
// read-modify-write that does not take it is a lost-update window.
//
// Detection is per-function and name-based: a body that calls something
// read-shaped AND something write-shaped without calling WithFileLock. A leaf
// helper invoked from inside its caller's lock closure reads as a violation
// here, which is why such helpers are named in archrules.LockDisciplineAllowed.
var LockDisciplineAnalyzer = &analysis.Analyzer{
	Name: "archlockdiscipline",
	Doc:  "engine settings read-modify-write must run under sessions.WithFileLock",
	Run:  runLockDiscipline,
}

func runLockDiscipline(pass *analysis.Pass) (any, error) {
	if SkipPass(pass) {
		return nil, nil
	}
	dir := PkgDir(pass)
	if dir == "" || !archrules.UnderAny(dir, lockDisciplineScopes) {
		return nil, nil
	}
	seen := map[string]bool{}
	for _, f := range ProdFiles(pass) {
		rel := FileRel(pass, f)
		if lockDisciplineExemptFiles[rel] {
			continue
		}
		for _, decl := range f.Decls {
			d, ok := decl.(*ast.FuncDecl)
			if !ok || d.Body == nil {
				continue
			}
			hasRead, hasWrite, hasLock := false, false, false
			var at token.Pos
			ast.Inspect(d.Body, func(n ast.Node) bool {
				call, ok := n.(*ast.CallExpr)
				if !ok {
					return true
				}
				name := CalleeName(call)
				switch {
				case name == "":
					return true
				case name == "WithFileLock":
					hasLock = true
				case lockReadPattern.MatchString(name):
					hasRead = true
					if at == token.NoPos {
						at = call.Pos()
					}
				case lockSavePattern.MatchString(name) || lockWritePrimitives[name]:
					hasWrite = true
					if at == token.NoPos {
						at = call.Pos()
					}
				}
				return true
			})
			if !hasRead || !hasWrite || hasLock {
				continue
			}
			sym := FuncSymbol(d)
			key := rel + "#" + sym
			seen[key] = true
			if _, ok := archrules.LockDisciplineAllowed[key]; ok {
				continue
			}
			pass.Reportf(at,
				"%s reads and then writes engine settings without calling sessions.WithFileLock — two "+
					"processes reconciling the same file interleave and the second write discards the "+
					"first. Wrap the read-modify-write in sessions.WithFileLock. If this is a deliberate, "+
					"reviewed exception, add %q to archrules.LockDisciplineAllowed "+
					" naming why it stands.", sym, key)
		}
	}
	reportStaleAllowlist(pass, archrules.LockDisciplineAllowed, analyzedFiles(pass), seen, "archrules.LockDisciplineAllowed")
	return nil, nil
}

// CalleeName returns the bare function name for a plain call, or the
// selector's final name for a method call. Deliberately package-agnostic and
// receiver-agnostic: it does not resolve types.
func CalleeName(call *ast.CallExpr) string {
	switch fn := call.Fun.(type) {
	case *ast.Ident:
		return fn.Name
	case *ast.SelectorExpr:
		return fn.Sel.Name
	default:
		return ""
	}
}
