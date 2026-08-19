package archlint

import (
	"go/ast"
	"go/token"
	"regexp"

	"golang.org/x/tools/go/analysis"
)

// lockDisciplineScopes are the packages this rule walks: the engine
// SettingsWriter implementors plus the shared reconcilers they call into. The
// lock and record primitives themselves are not scanned — their callers are
// what must hold the lock.
var lockDisciplineScopes = []string{
	"internal/claude",
	"internal/codex",
	"internal/kiro",
	"internal/opencode",
	"internal/shared/agent",
}

// lockDisciplineExemptFiles are the primitives this rule protects usage OF,
// not usage BY. Scanning them would misattribute their own internal
// read-then-write shapes to a missing lock the caller is responsible for.
var lockDisciplineExemptFiles = map[string]bool{
	"internal/shared/agent/settings_io.go": true,
	"internal/shared/agent/rmw_lock.go":    true,
}

var lockReadPattern = regexp.MustCompile(`(?i)^(read|load)`)

var lockSavePattern = regexp.MustCompile(`(?i)^save`)

var lockWritePrimitives = map[string]bool{
	"AtomicWriteFile":          true,
	"WriteFileAtomicFs":        true,
	"WriteManagedContext":      true,
	"WriteManagedPackageFiles": true,
	"WriteManagedCommandFiles": true,
	"WriteServers":             true,
	"RemoveServers":            true,
}

// LockDisciplineAnalyzer enforces that a read-then-write over an engine's
// settings file happens under a file lock.
//
// Two ctxloom processes reconciling the same engine settings file interleave
// as read-read-write-write, and the second write silently discards the first
// process's change. agent.WithFileLock is the serialization point; a
// read-modify-write that does not take it is a lost-update window.
//
// Detection is per-function and name-based: a body that calls something
// read-shaped AND something write-shaped without calling WithFileLock. A leaf
// helper invoked from inside its caller's lock closure reads as a violation
// here, which is why such helpers are named in lockDisciplineAllowed.
var LockDisciplineAnalyzer = &analysis.Analyzer{
	Name: "archlockdiscipline",
	Doc:  "engine settings read-modify-write must run under agent.WithFileLock",
	Run:  runLockDiscipline,
}

func runLockDiscipline(pass *analysis.Pass) (any, error) {
	if SkipPass(pass) {
		return nil, nil
	}
	dir := PkgDir(pass)
	if dir == "" || !inScopes(dir, lockDisciplineScopes) {
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
			if _, ok := lockDisciplineAllowed[key]; ok {
				continue
			}
			pass.Reportf(at,
				"%s reads and then writes engine settings without calling agent.WithFileLock — two "+
					"processes reconciling the same file interleave and the second write discards the "+
					"first. Wrap the read-modify-write in agent.WithFileLock. If this is a deliberate, "+
					"reviewed exception, add %q to lockDisciplineAllowed in "+
					"internal/archlint/lockdiscipline.go naming why it stands.", sym, key)
		}
	}
	reportStaleAllowlist(pass, lockDisciplineAllowed, analyzedFiles(pass), seen, "lockDisciplineAllowed",
		"internal/archlint/lockdiscipline.go")
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

// lockDisciplineAllowed is the reasoned, symbol-keyed baseline: a durable
// "file.go#Symbol" reference mapped to why the entry stands.
var lockDisciplineAllowed = map[string]string{
	"internal/codex/instanceconfig.go#codexInstanceConfig.WriteInstanceConfig": "REAL GAP, not a heuristic false positive: codexInstanceConfig.WriteInstanceConfig checks afero.Exists(dest) then, on a separate path, reads the HOST config (afero.ReadFile) and writes dest via writer.save — the identical seed-once TOCTOU shape unit 1 (claude.claudeInstanceConfig.WriteInstanceConfig, B5) fixed, but for codex's instance config. Not in the R6 bypass batch's named units (B1-B9) — a NEW finding this gate surfaced. Deferred: needs its own agent.WithFileLock wrap, same reasoning as unit 1's fix (verify it does not collide with isolation.lockInstanceHome's caller-side project lock the same way unit 1's doc proves for claude).",
	"internal/codex/settings.go#CodexHookWriter.save":                          "false positive (leaf helper under the caller's lock): save's own body reads the existing file (afero.ReadFile, the zero-byte-over-existing-content guard) and writes it (agent.AtomicWriteFile), but save is ALWAYS called from inside writeSettingsIn's or removeSettingsIn's agent.WithFileLock closure — this gate's per-function heuristic cannot see a lock held by the CALLER two frames up. See this file's header, blind spot 4.",
	"internal/shared/agent/managedcontext.go#writeManagedContextLocked":        "false positive (leaf helper under the caller's lock): writeManagedContextLocked is WriteManagedContext's body, split out for readability and invoked BY NAME from inside WriteManagedContext's own agent.WithFileLock closure (see its doc: \"run under its caller's lock\") — same shape as CodexHookWriter.save above. See this file's header, blind spot 4.",
}
