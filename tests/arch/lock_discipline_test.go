//go:build arch

// LOCK DISCIPLINE: every read-modify-write of an engine-settings or
// R6-exclusive-owned config file must run inside agent.WithFileLock.
//
// R6: a file ctxloom EXCLUSIVELY owns but which lives inside a
// foreign engine's directory is locked and ledgered like a shared file — one
// rule, no per-site judgment. Before R6, three functions answered "does
// exclusive ownership excuse the lock" three different ways: B5
// (claude.claudeInstanceConfig.WriteInstanceConfig) relied on the CALLER's
// project filelock — a second, invisible lock idiom over the SAME class of
// file the SettingsWriter family already self-locks. This gate is the
// write-discipline gate's idiom (tests/arch/write_discipline_test.go)
// applied to that ratchet: a reasoned, symbol-keyed allowlist that stops the
// unlocked-RMW class from growing silently, rather than a one-time fix.
//
// THE HEURISTIC (deliberately coarse, matching the review's own framing:
// "read-then-AtomicWriteFile shapes in engine packages"). For every
// non-test, non-generic top-level function in the scoped packages below,
// this gate asks three purely NAME-based questions of every call expression
// in the function's body (any depth, closures included — same technique
// write_discipline_test.go's collectRawWrites uses):
//
//   - READ SIGNAL: a call whose callee name (the bare identifier for a
//     plain call, or the selector's method/function name for `x.Foo(...)`)
//     matches /^(?i)(read|load)/ — covers afero.ReadFile, os.ReadFile,
//     loadSettings, loadJSONObject, loadOpencodeConfig, ledger.Ledger.Read,
//     Ledger.readAll, MCPFileConfig.load, ...
//   - WRITE SIGNAL: a call whose callee name matches /^(?i)save/, or is
//     exactly one of a short known-primitive list (AtomicWriteFile,
//     WriteFileAtomicFs, WriteManagedContext, WriteManagedPackageFiles,
//     WriteManagedCommandFiles, WriteServers, RemoveServers) — covers every
//     settings-family persist path in this module (see settings_io.go,
//     mcpfile.go, managedcontext.go, packagefiles.go and every engine's own
//     save/saveSettings/saveMCPConfig/saveOpencodeConfig wrapper).
//   - LOCK SIGNAL: a call whose callee name is exactly "WithFileLock"
//     (agent.WithFileLock, the SettingsWriter family's one lock idiom —
//     config.Manager.Update, M7's OWN transactional lock for ctxloom's own
//     config.yaml, is a different mechanism by design and out of this
//     gate's scope; see its doc).
//
// A function with BOTH a read signal and a write signal, and NO lock
// signal ANYWHERE in its own body, is a violation, attributed to the
// enclosing top-level FuncDecl exactly like write_discipline_test.go's
// funcSymbol.
//
// WHY THIS IS A FUNCTION-BODY-LEVEL CHECK, NOT A TRUE NESTING CHECK. Proving
// "the read and the write are INSIDE the WithFileLock closure" would need a
// real scope-aware walk (track depth while descending into the FuncLit
// argument of a WithFileLock call). This gate instead asks the coarser
// question "do all three signals occur ANYWHERE in the same top-level
// function" — which is exactly what write_discipline_test.go's own
// afero.Fs-method heuristic does (name-based, not type-based) for the same
// reason: cheap, no go/types dependency, and empirically sufficient for
// this module's idiom, where every locked RMW wraps its ENTIRE
// read-modify-write cycle in one WithFileLock closure per the SettingsWriter
// family's own convention (see WithFileLock's doc: "fn is the WHOLE cycle").
//
// KNOWN BLIND SPOTS, so a future reader does not mistake ratchet coverage
// for a proof:
//
//   - A function that calls WithFileLock but performs its read or write
//     OUTSIDE the locked closure (in the same function, before or after)
//     reads as compliant to this gate. It proves co-occurrence, not correct
//     nesting.
//   - A PURE whole-file overwrite with no prior read at all — kiro's
//     writeAgentConfig/writeSteering (B8: defensible today, ctxloom owns the
//     whole file, nothing to preserve) and opencode's
//     materializeContextSurface, same shape — is invisible to this gate: it
//     has a write signal but no read signal, so it is not flagged even
//     though R6's text calls for locking every R6-exclusive-owned file
//     regardless of RMW shape. This gate proves READ-MODIFY-WRITE discipline
//     specifically, which is what the review's heuristic asked for; it does
//     not by itself prove the full R6 policy for a no-read overwrite.
//   - A read or write reached only through a HELPER not named read*/load*/
//     save*/one of the known primitives (e.g. a locally invented verb) is
//     invisible — this is a naming-convention gate, exactly like
//     write_discipline_test.go's isAferoFsLikeName.
//   - config.Manager.Update (M7, internal/config) is a DIFFERENT, already-
//     transactional lock idiom (its own filelock.ProjectPathFor-keyed lock,
//     not agent.WithFileLock) and internal/config is out of scope entirely —
//     this gate is specifically about the SettingsWriter/R6 class of
//     foreign-engine-directory files, not ctxloom's own config.yaml.
//   - A LEAF HELPER split out of an already-locked closure for readability
//     (agent.writeManagedContextLocked, called BY NAME from inside
//     WriteManagedContext's own WithFileLock closure; codex's
//     CodexHookWriter.save, always called from within writeSettingsIn's or
//     removeSettingsIn's WithFileLock closure) reads+writes with no lock
//     call of ITS OWN and is flagged — a real false positive this gate
//     cannot resolve without call-graph analysis. Both are in
//     lockDisciplineAllowed naming exactly this.
//   - THE WRITE SIGNAL MISSES THE RENDER-TO-TEMP-THEN-SWAP IDIOM, and this
//     is a REAL FALSE NEGATIVE, not just a false positive risk: B3
//     (agent.WriteManagedPackageFiles, config-patching-review.md — ledgered
//     but NOT under agent.WithFileLock, a known deferred gap) writes via
//     raw afero.WriteFile-into-a-temp-dir + fs.Rename swaps (see
//     write_discipline_test.go's own baseline entry for this exact symbol,
//     "C11's DELIBERATE render-to-temp-then-swap design"), never
//     AtomicWriteFile or a `save*`-named call. This gate's write signal
//     does not recognize that shape, so B3 is INVISIBLE here — it neither
//     appears as a violation nor is it in lockDisciplineAllowed, because the
//     scan never nominates it as a candidate at all. Confirmed empirically:
//     running this gate finds 3 violations, none of them
//     WriteManagedPackageFiles. B3 remains tracked only in the review and in
//     write_discipline_test.go's own baseline comment, not by this gate.
package arch

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// lockDisciplineScopes are the packages this gate walks: the four engine
// SettingsWriter implementors plus the shared reconcilers they and R6's
// exclusively-owned-file writers call into. internal/shared/ledger and
// internal/shared/filelock are the lock/record PRIMITIVES themselves (like
// write_discipline_test.go's iox/filelock exemption) and are excluded below,
// not scanned here — their own callers are what must hold the lock.
var lockDisciplineScopes = []string{
	"internal/claude",
	"internal/codex",
	"internal/kiro",
	"internal/opencode",
	"internal/shared/agent",
}

// lockDisciplineExemptDirs are the primitives this gate protects usage of,
// not usage BY — calling AtomicWriteFile from inside AtomicWriteFile is not
// a thing, but scanning these files themselves would misattribute their
// internal read/write shapes (e.g. AtomicWriteFile's own Stat-then-rename)
// to a "missing lock" the caller is always the one responsible for.
var lockDisciplineExemptDirs = []string{
	"internal/shared/agent/settings_io.go",
	"internal/shared/agent/rmw_lock.go",
}

var lockReadPattern = regexp.MustCompile(`(?i)^(read|load)`)

var lockWritePrimitives = map[string]bool{
	"AtomicWriteFile":          true,
	"WriteFileAtomicFs":        true,
	"WriteManagedContext":      true,
	"WriteManagedPackageFiles": true,
	"WriteManagedCommandFiles": true,
	"WriteServers":             true,
	"RemoveServers":            true,
}

var lockSavePattern = regexp.MustCompile(`(?i)^save`)

// lockDisciplineAllowed is this gate's reasoned, symbol-keyed allowlist —
// same shape and same generation discipline as write_discipline_test.go's
// writeDisciplineAllowed: a durable "file.go#Symbol" reference mapped to why
// the entry stands. Generated mechanically by running this gate with an
// empty map and transcribing every reported violation, then classifying
// each as either a heuristic false-positive (blind spot above) or a real,
// deferred gap.
//
// Generated by running this gate against an empty map: 3 violations found,
// none migrated as part of this slice.
var lockDisciplineAllowed = map[string]string{
	"internal/codex/instanceconfig.go#codexInstanceConfig.WriteInstanceConfig": "REAL GAP, not a heuristic false positive: codexInstanceConfig.WriteInstanceConfig checks afero.Exists(dest) then, on a separate path, reads the HOST config (afero.ReadFile) and writes dest via writer.save — the identical seed-once TOCTOU shape unit 1 (claude.claudeInstanceConfig.WriteInstanceConfig, B5) fixed, but for codex's instance config. Not in the R6 bypass batch's named units (B1-B9) — a NEW finding this gate surfaced. Deferred: needs its own agent.WithFileLock wrap, same reasoning as unit 1's fix (verify it does not collide with isolation.lockInstanceHome's caller-side project lock the same way unit 1's doc proves for claude).",
	"internal/codex/settings.go#CodexHookWriter.save":                          "false positive (leaf helper under the caller's lock): save's own body reads the existing file (afero.ReadFile, the zero-byte-over-existing-content guard) and writes it (agent.AtomicWriteFile), but save is ALWAYS called from inside writeSettingsIn's or removeSettingsIn's agent.WithFileLock closure — this gate's per-function heuristic cannot see a lock held by the CALLER two frames up. See this file's header, blind spot 4.",
	"internal/shared/agent/managedcontext.go#writeManagedContextLocked":        "false positive (leaf helper under the caller's lock): writeManagedContextLocked is WriteManagedContext's body, split out for readability and invoked BY NAME from inside WriteManagedContext's own agent.WithFileLock closure (see its doc: \"run under its caller's lock\") — same shape as CodexHookWriter.save above. See this file's header, blind spot 4.",
}

// lockDisciplineViolation mirrors writeDisciplineViolation's shape.
type lockDisciplineViolation struct {
	file   string
	symbol string
	line   int
}

func (v lockDisciplineViolation) key() string { return v.file + "#" + v.symbol }

// scanLockDiscipline walks lockDisciplineScopes for functions with both a
// read and a write signal but no WithFileLock call anywhere in their body.
func scanLockDiscipline(t *testing.T) []lockDisciplineViolation {
	t.Helper()
	root := moduleRoot(t)
	fset := token.NewFileSet()
	var out []lockDisciplineViolation
	var filesScanned int

	for _, scope := range lockDisciplineScopes {
		scopeRoot := filepath.Join(root, scope)
		err := filepath.WalkDir(scopeRoot, func(p string, d fs.DirEntry, err error) error {
			switch {
			case err != nil:
				return err
			case d.IsDir() && skippedDir(d.Name()):
				return filepath.SkipDir
			case d.IsDir():
				return nil
			case !strings.HasSuffix(d.Name(), ".go") || strings.HasSuffix(d.Name(), "_test.go"):
				return nil
			}
			rel, rerr := filepath.Rel(root, p)
			if rerr != nil {
				return rerr
			}
			rel = filepath.ToSlash(rel)
			if lockDisciplineFileExempt(rel) {
				return nil
			}

			f, perr := parser.ParseFile(fset, p, nil, 0)
			if perr != nil {
				t.Errorf("parse %s: %v", rel, perr)
				return nil
			}
			filesScanned++
			out = append(out, scanFileForLockDiscipline(fset, f, rel)...)
			return nil
		})
		if err != nil {
			t.Fatalf("walk %s: %v", scope, err)
		}
	}
	// Anti-vacuity: a walk that silently stopped matching files would make
	// every assertion below pass for the wrong reason (write_discipline_test.go
	// uses the same guard, scaled to this narrower scope's real file count).
	if filesScanned < 20 {
		t.Fatalf("scanned only %d non-test files under %v — the walk is broken, not the tree", filesScanned, lockDisciplineScopes)
	}
	return out
}

func lockDisciplineFileExempt(rel string) bool {
	for _, ex := range lockDisciplineExemptDirs {
		if rel == ex {
			return true
		}
	}
	return false
}

// scanFileForLockDiscipline inspects every top-level function in f for the
// read/write/lock signals, attributing each to its enclosing FuncDecl.
func scanFileForLockDiscipline(fset *token.FileSet, f *ast.File, rel string) []lockDisciplineViolation {
	var out []lockDisciplineViolation
	for _, decl := range f.Decls {
		d, ok := decl.(*ast.FuncDecl)
		if !ok || d.Body == nil {
			continue
		}
		hasRead, hasWrite, hasLock := false, false, false
		var line int
		ast.Inspect(d.Body, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			name := calleeName(call)
			if name == "" {
				return true
			}
			switch {
			case name == "WithFileLock":
				hasLock = true
			case lockReadPattern.MatchString(name):
				hasRead = true
				if line == 0 {
					line = fset.Position(call.Pos()).Line
				}
			case lockSavePattern.MatchString(name) || lockWritePrimitives[name]:
				hasWrite = true
				if line == 0 {
					line = fset.Position(call.Pos()).Line
				}
			}
			return true
		})
		if hasRead && hasWrite && !hasLock {
			out = append(out, lockDisciplineViolation{file: rel, symbol: funcSymbol(d), line: line})
		}
	}
	return out
}

// calleeName returns the bare function name for a plain call (`load(...)`)
// or the selector's method/function name for `x.Foo(...)` — the same
// package-agnostic, receiver-agnostic name extraction write_discipline_test.go's
// isAferoFsLikeName family uses, deliberately not resolving types.
func calleeName(call *ast.CallExpr) string {
	switch fn := call.Fun.(type) {
	case *ast.Ident:
		return fn.Name
	case *ast.SelectorExpr:
		return fn.Sel.Name
	default:
		return ""
	}
}

// TestArch_LockDiscipline_EngineRMWIsLocked is the gate: every read-then-
// write shaped function in the scoped packages must call agent.WithFileLock
// somewhere in its own body, or be named (with a reason) in
// lockDisciplineAllowed.
func TestArch_LockDiscipline_EngineRMWIsLocked(t *testing.T) {
	violations := scanLockDiscipline(t)
	sort.Slice(violations, func(i, j int) bool {
		if violations[i].file != violations[j].file {
			return violations[i].file < violations[j].file
		}
		return violations[i].line < violations[j].line
	})

	for _, v := range violations {
		if why, ok := lockDisciplineAllowed[v.key()]; ok {
			t.Logf("allowed: %s:%d %s (%s)", v.file, v.line, v.symbol, why)
			continue
		}
		t.Errorf("%s:%d %s reads AND writes the same class of file but never calls agent.WithFileLock — "+
			"R6 (ruled 2026-08-14): a file ctxloom exclusively owns inside a foreign engine's directory is "+
			"locked and ledgered like a shared file, no per-site judgment. If this is a reviewed exception "+
			"(or a heuristic false-positive — see this file's blind-spot list), add %q to "+
			"lockDisciplineAllowed in tests/arch/lock_discipline_test.go naming why.",
			v.file, v.line, v.symbol, v.key())
	}
}

// TestArch_LockDisciplineAllowlist_IsLive fails when a lockDisciplineAllowed
// entry names a symbol the scan no longer finds violating — the same
// staleness check write_discipline_test.go's AllowlistIsLive runs. A stale
// exception would silently cover whatever unlocked RMW lands at that symbol
// next.
func TestArch_LockDisciplineAllowlist_IsLive(t *testing.T) {
	violations := scanLockDiscipline(t)
	live := make(map[string]bool, len(violations))
	for _, v := range violations {
		live[v.key()] = true
	}

	keys := make([]string, 0, len(lockDisciplineAllowed))
	for k := range lockDisciplineAllowed {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	for _, k := range keys {
		if !live[k] {
			t.Errorf("lockDisciplineAllowed allows %q (%s) but the scan found no unlocked read+write there "+
				"anymore — delete the entry, or it will silently exempt whatever regresses at that symbol "+
				"next", k, lockDisciplineAllowed[k])
		}
	}
}
