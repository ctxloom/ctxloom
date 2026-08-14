//go:build arch

// FILESYSTEM WRITES MUST ROUTE THROUGH iox.
//
// internal/shared/iox is the one atomic-write implementation this repo owns
// (unique temp + fsync + rename, exact-perm chmod — see its doc comment). A
// direct `os.WriteFile`/`os.Create`/`os.Rename`/`os.Symlink`/write-mode
// `os.OpenFile` call anywhere else is a second, hand-copied writer that
// shares no code with iox and no compiler-enforced link to it: it can drop
// the fsync, keep a fixed (not unique) temp name that a concurrent writer
// can clobber, or leave a half-renamed file behind a crash. The
// fs-consolidation plan (`ugly-icy-squid/fs-consolidation.plan.md`, C1) calls
// this out as the write-discipline half of "one atomic-write implementation,
// one lock idiom, one path-derivation chokepoint."
//
// This gate is a RATCHET, not a fix: every call this scan found at authoring
// time is grandfathered into writeDisciplineAllowed with a reason, and none
// of them is migrated here (that is C3/C10's job, sweep by sweep). What the
// gate buys immediately is that the set cannot grow silently — a new raw
// call anywhere in internal/ or cmd/ outside the exempt packages fails the
// build until it either routes through iox or earns its own reviewed entry.
//
// SCOPE EXTENDED TO cmd/ (home-lock-dir fix, closing N4): the original scan
// covered internal/ only, so cmd/ltk and cmd/taskloom — the two companion
// binaries this fix also brought under agent.WithFileLock — were invisible
// to the ratchet even though they read-modify-write the identical engine
// settings files the internal/ side is governed for. A raw write there is
// exactly as ungoverned as one in internal/.
//
// Detection is purely syntactic (go/ast, no go/types), the same technique
// doc_comment_test.go uses for a full-body sweep rather than the
// imports-only parse arch_test.go's scan() does: a call is flagged when its
// callee is the two-token selector `os.<Name>` for one of the five forbidden
// names, with `os.OpenFile` narrowed further to calls whose flags argument
// mentions at least one of the write-implying os.O_* constants (a read-only
// os.OpenFile(path, os.O_RDONLY, 0) is not this gate's business). A local
// identifier that happens to be named `os` would be misread as the stdlib
// package; nothing in this module does that.
//
// AFERO COVERAGE (added fs-consolidation C10, 2026-08-13): the original scan
// only matched the `os` package, so countersign.Store.writeIndex's bare
// `s.fs.Rename` evaded it entirely — an afero-mediated raw write is exactly
// as ungoverned as an os.* one, since afero.OsFs's methods bottom out in the
// same os.* calls this gate already forbids at that layer. Two more shapes
// are now flagged, alongside the original five:
//
//   - Package-level `afero.WriteFile` / `afero.TempFile` calls — the
//     two-token selector check is identical to the `os.*` one, just against
//     the `afero` package identifier instead.
//   - Write-shaped METHOD calls on a value that LOOKS like an afero.Fs:
//     `.Create`, `.Rename`, and write-mode `.OpenFile` (same os.O_* flag
//     check as the os.OpenFile case). `.Remove`/`.RemoveAll` are excluded on
//     purpose — deletion is a different gate's subject, not this one's — and
//     so are `.Mkdir`/`.MkdirAll`, which create no file content to protect.
//
// Resolving "looks like an afero.Fs" without go/types is inherently
// heuristic, and the heuristic chosen here is NAME-based, not type-based:
// aferoFsMethodCall treats a method call's receiver as an afero.Fs candidate
// when the receiver is a bare identifier (`fs.Create(...)`) or the final
// selector of a field access (`s.fs.Create(...)`, `w.FS.Create(...)`) whose
// name, lower-cased, either ends in "fs" or starts with "fsys"
// (isAferoFsLikeName). That one rule was checked against every
// afero.Fs-typed struct field, parameter, and named return in this module at
// authoring time (`fs`, `Fs`, `FS`, `fsys`, `vfs`, `bundleFS`, `cfgFS`, …)
// and covers all of them — the codebase's own naming convention makes the
// heuristic cheap and, empirically, complete for what exists today.
//
// KNOWN BLIND SPOTS of the afero method-call heuristic, so a future reader
// does not mistake ratchet coverage for a proof:
//
//   - A receiver typed afero.Fs but named anything the rule does not
//     recognize (e.g. a local `store` holding an afero.Fs, or a value
//     returned inline from an accessor like `cfgFS(cfg).Create(...)`) is
//     invisible. The rule is purely textual — it never looks at a
//     declaration or a type.
//   - A receiver merely NAMED like an afero.Fs but not actually one (a field
//     called `fs` of some unrelated type with a same-named method) would
//     false-positive. None exist in this module today; if one is added, its
//     entry in writeDisciplineAllowed will say so.
//   - The name check does not distinguish a package-qualified expression
//     from a local one beyond the `os`/`afero` package-identifier carve-out
//     above, so an unrelated package literally named `fs` (none exists here)
//     would also match.
//
// Baseline entries are keyed by SYMBOL, not by file:line — a durable
// reference (package.Function or Type.Method) that survives unrelated edits
// above it in the file, rather than a line number that drifts and then
// silently points at the wrong call. TestArch_WriteDiscipline_AllowlistIsLive
// is what makes that key honest: it fails if the named symbol no longer
// exists or no longer contains a forbidden call, so a stale entry cannot
// quietly exempt whatever lands at that symbol next.
package arch

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// writeDisciplineScopes are the subtrees this gate looks at: production code
// only, per the fs-consolidation plan's C1 scope (originally just
// "internal"; "cmd" joined it closing N4 — see this file's header doc). Test
// files never enter the scan (fsWalkNonTest below skips _test.go the same
// way scan() does).
var writeDisciplineScopes = []string{"internal", "cmd"}

// writeDisciplineExemptDirs are the packages that ARE the write library (or
// the lock primitive beside it) and so are structurally, not provisionally,
// exempt: they are not a second copy of iox, they are the thing this gate
// protects. Anything else that needs an exception earns a reasoned entry in
// writeDisciplineAllowed instead of a free pass here.
var writeDisciplineExemptDirs = []string{
	"internal/shared/iox",
	"internal/shared/filelock",
}

// forbiddenOSCalls are the five raw-fs-write entry points this gate forbids
// outside the exempt set. os.OpenFile is handled separately (writeDisciplineViolation
// below) because only its write-mode calls count.
var forbiddenOSCalls = map[string]bool{
	"WriteFile": true,
	"Create":    true,
	"Rename":    true,
	"Symlink":   true,
}

// writeFlagConstants are the os.O_* names whose presence in an os.OpenFile
// flags argument makes the call write-mode. os.O_RDONLY is deliberately
// absent: a plain read-only open is not this gate's business, and
// os.O_RDONLY is 0 in every Go platform's syscall package, so it never
// appears as a named identifier in a flags expression that means anything
// else.
var writeFlagConstants = map[string]bool{
	"O_WRONLY": true,
	"O_RDWR":   true,
	"O_TRUNC":  true,
	"O_CREATE": true,
	"O_APPEND": true,
}

// forbiddenAferoPackageCalls are the package-level afero.* functions this
// gate forbids outside the exempt set — the afero-package twin of
// forbiddenOSCalls, restricted to the two write entry points that shadow
// os.WriteFile/os.CreateTemp (afero.Rename does not exist as a package-level
// function; only as a method, covered by forbiddenAferoMethodCalls).
var forbiddenAferoPackageCalls = map[string]bool{
	"WriteFile": true,
	"TempFile":  true,
}

// forbiddenAferoMethodCalls are the write-shaped afero.Fs interface methods
// this gate forbids on a receiver aferoFsMethodCall accepts as an afero.Fs
// candidate. OpenFile is handled separately (like os.OpenFile) because only
// its write-mode calls count. Remove/RemoveAll and Mkdir/MkdirAll are
// deliberately absent — see the file header.
var forbiddenAferoMethodCalls = map[string]bool{
	"Create": true,
	"Rename": true,
}

// isAferoFsLikeName reports whether name looks like it holds an afero.Fs, by
// the codebase's own naming convention rather than any type information —
// see the file header's AFERO COVERAGE section for what this does and does
// not catch.
func isAferoFsLikeName(name string) bool {
	lower := strings.ToLower(name)
	return strings.HasSuffix(lower, "fs") || strings.HasPrefix(lower, "fsys")
}

// aferoFsMethodCall reports whether sel's receiver is a name
// isAferoFsLikeName accepts: a bare identifier (`fs.Create(...)`) or the
// final selector of a field access (`s.fs.Create(...)`).
func aferoFsMethodCall(sel *ast.SelectorExpr) bool {
	switch x := sel.X.(type) {
	case *ast.Ident:
		return isAferoFsLikeName(x.Name)
	case *ast.SelectorExpr:
		return isAferoFsLikeName(x.Sel.Name)
	default:
		return false
	}
}

// writeDisciplineAllowed is this gate's shrinking allowlist, in the same
// shape as layering_test.go's layeringRule.allowed and arch_test.go's
// testSupportImporters: a durable symbol reference (module-relative
// "file.go#Symbol", where Symbol is "Type.Method" for a method or the bare
// function name otherwise) mapped to the fix required to remove the entry.
//
// Generated MECHANICALLY by running this gate with an empty map and
// transcribing every reported violation. Two generations so far:
//
//   - 2026-08-13, base bd6b3baf, os.*-only scan: 45 entries, nothing migrated
//     as part of that slice (C1) — that was C3's job (the three named
//     strays: countersign.writeIndex, gitignore, operations.ConvertVendorTranscript,
//     since landed) and C10's (per-area sweeps).
//   - 2026-08-13, base db2f36cc, RE-generated after the afero coverage
//     extension (fs-consolidation plan C10, this slice): 34 new entries
//     surfaced (afero.WriteFile/TempFile and afero.Fs-method call sites the
//     os-only scan could never see), for 79 total. This IS the ratchet
//     growing honestly — the gate got stricter and the baseline records
//     exactly what it now sees. C10 migrates the two named stragglers
//     (bundles.fsStore.Save, countersign.Store.write/writeUnsigned) in the
//     same slice, so those three entries are already gone again by the time
//     this lands; every other new entry carries a reason naming whether it
//     was swept (classified, and migrated or left with a specific reason) or
//     is out of this slice's five swept areas and deferred whole.
//
// Most entries carry the generic baseline reason. A handful carry a more
// specific one where the fix is already obvious from the call site (a fixed
// temp name, a lock's own file, a test fixture that is itself the exempt
// case, or a C10 sweep's classification).
var writeDisciplineAllowed = map[string]string{
	"internal/acp/session.go#chatSession.handleFsWrite":                       "C10 acp/acpagent sweep: unsafe os.WriteFile serving fs/write_text_file (workspace-confined). NOT mechanical — iox.WriteFileAtomicFs always chmods to the passed perm, but os.WriteFile only sets perm on CREATE and preserves an existing file's mode, so migrating would silently strip an executable bit off any script edited through this ACP handler; iox's default empty-over-existing refusal would also break a legitimate 'clear this file' edit. Needs a mode-preserving write primitive (agent.AtomicWriteFile already has one, built for exactly this) before this can migrate — escalated, not fixed here.",
	"internal/agentcoord/coord/artifactstore.go#artifactStore.publish":        "pre-ratchet baseline — migrate to iox (fs-consolidation plan C3/C10)",
	"internal/agentcoord/coord/homeartifacts.go#Home.DownloadArtifact":        "pre-ratchet baseline — migrate to iox (fs-consolidation plan C3/C10)",
	"internal/agentcoord/coord/httpserver.go#coordServing.saveEndpointLocked": "pre-ratchet baseline — migrate to iox (fs-consolidation plan C3/C10)",
	"internal/agentcoord/coord/journal.go#openStoreFromOffset":                "pre-ratchet baseline — migrate to iox (fs-consolidation plan C3/C10)",
	"internal/agentcoord/coord/statedir.go#claimOwner":                        "advisory lock file's own O_EXCL create — mechanically parallel to filelock's exemption but not itself filelock (fs-consolidation plan C10 to decide: fold into filelock or exempt structurally)",
	"internal/agentcoord/mcpschema/gen/main.go#generateXmlLike":               "pre-ratchet baseline, codegen tool — migrate to iox (fs-consolidation plan C3/C10)",
	"internal/agentcoord/mcpschema/gen/main.go#writeSpec":                     "pre-ratchet baseline, codegen tool — migrate to iox (fs-consolidation plan C3/C10)",
	"internal/agentcoord/spool/ops.go#moveTo":                                 "pre-ratchet baseline — migrate to iox (fs-consolidation plan C3/C10)",
	"internal/agentcoord/spool/writer.go#Writer.Write":                        "pre-ratchet baseline — migrate to iox (fs-consolidation plan C3/C10)",
	"internal/agentcoord/spool/writer.go#writeAndSync":                        "pre-ratchet baseline — migrate to iox (fs-consolidation plan C3/C10)",
	"internal/bundles/skill_archive.go#ImportSkillArchive":                    "C10 content/skill_archive sweep: fsys.Rename here is a WHOLE-DIRECTORY swap (staged tree -> final, and final -> aside on replace), not a single-file content write — outside iox's WriteFileAtomicFs API, which has no directory-rename surface. This is a deliberate, already-safe swap-never-clear-then-hope idiom (see the function's own doc) with its own aside/restore recovery; exempt, not a violation to migrate.",
	"internal/claude/contextdelivery.go#appendFlagDelivery.DeliverContext":    "survey D15: the one claude writer not routed through agent.AtomicWriteFile — pre-existing, out of C10's five swept areas, left for a future slice",
	"internal/cli/bundle_items.go#editInEditor":                               "pre-ratchet baseline — migrate to iox (fs-consolidation plan C3/C10)",
	"internal/cli/llm_turn.go#writeRunStartHandoff":                           "pre-ratchet baseline — migrate to iox (fs-consolidation plan C3/C10)",
	"internal/cli/run_terminal_ui.go#redirectDiagnosticsForTUI":               "pre-ratchet baseline — migrate to iox (fs-consolidation plan C3/C10)",
	"internal/cli/tui/export.go#exportTranscript":                             "pre-ratchet baseline — migrate to iox (fs-consolidation plan C3/C10)",
	"internal/contextmetrics/contextmetrics.go#Append":                        "pre-ratchet baseline — migrate to iox (fs-consolidation plan C3/C10)",
	"internal/docsgen/config.go#GenConfig":                                    "pre-ratchet baseline, doc generator — migrate to iox (fs-consolidation plan C3/C10)",
	"internal/docsgen/mcp.go#GenMCPTools":                                     "pre-ratchet baseline, doc generator — migrate to iox (fs-consolidation plan C3/C10)",
	"internal/gitignore/gitignore.go#appendBlock":                             "pre-ratchet baseline — migrate to iox (fs-consolidation plan C3/C10)",
	"internal/lm/backends/mock.go#writeMockRecord":                            "pre-ratchet baseline, test/mock backend — migrate to iox (fs-consolidation plan C3/C10)",
	"internal/lm/isolation/auth.go#copyCredentialFile":                        "C10 isolation sweep: writes into configHome/destSubdir, a per-session ephemeral worktree config-home dir (Worktree.provisionConfigHome) — single-writer-scoped like a temp dir, verified. NOT migrated: an explicit os.Lstat symlink-destination refusal sits directly beside this write as a security defense (a repo-tracked symlink pointing at the real ~/.codex/auth.json would otherwise become an arbitrary-file overwrite via O_TRUNC); iox's rename-based write wouldn't traverse that symlink either, arguably strictly safer, but changing this write path wants a dedicated security-reviewed change, not a mechanical sweep migration.",
	"internal/lm/isolation/imagebuild.go#buildBaseImage":                      "C10 isolation sweep: writes inside os.MkdirTemp(\"\", \"ctxloom-imgbase-\"), reaped by the same function's RemoveAll — temp-dir-scoped, verified. Migration deferred to a future slice (mechanical, low priority — no concurrent-writer risk).",
	"internal/lm/isolation/imagebuild.go#buildImage":                          "C10 isolation sweep: writes inside os.MkdirTemp(\"\", \"ctxloom-imgbuild-\"), reaped by the same function's RemoveAll — temp-dir-scoped, verified. Migration deferred to a future slice (mechanical, low priority — no concurrent-writer risk).",
	"internal/lm/isolation/imagebuild.go#copyExecutable":                      "C10 isolation sweep: os.OpenFile(O_WRONLY|O_CREATE|O_TRUNC, 0o755) writing the ctxloom binary copy into the same MkdirTemp(\"ctxloom-imgbuild-\") tree as buildImage — temp-dir-scoped, verified. iox has no streaming-copy entry point (see C3's NewAtomicFile design note, still unbuilt), so this one is a genuine gap, not a one-line migration; left for that surface to land.",
	"internal/lm/isolation/sharedfs.go#probeOneRoot":                          "C10 isolation sweep: writes a marker file inside os.MkdirTemp(root, probeScratchPrefix), deferred RemoveAll in the same function — temp-dir-scoped, verified. Migration deferred to a future slice (mechanical, low priority).",
	"internal/lm/isolation/statemounts.go#ensureFile":                         "C10 isolation sweep: the plan's 'all ~26 seams are temp-dir-scoped' claim is WRONG for this one — ensureFile's caller passes the LIVE ~/.ctxloom/tasks/<project>.jsonl path (taskpaths.HomeTasksLogPath) and its filelock sidecar, to stand up the container bind-mount SOURCE before `run`. Not a write-discipline risk in practice: O_CREATE|O_WRONLY with no O_TRUNC only creates-if-absent and immediately Closes, matching the doc's 'never truncating a log that already has tasks in it' — but it is not temp-scoped, and iox's whole-file-replace API is the wrong shape for a create-if-absent primitive anyway. Reported, not migrated.",
	"internal/lm/isolation/traceprobe.go#traceProbeFromEnv":                   "C10 isolation sweep: writes into the probe's own trace dir, reaped by RemoveAll per the adjacent comment — temp-dir-scoped, verified. Migration deferred to a future slice (mechanical, low priority).",
	"internal/lm/isolation/worktree_reap.go#recordWorktreeOwner":              "C10 isolation sweep: writes the owner-pid marker beside one ephemeral git worktree's own scratch path (isolation.worktreeScratchPath) — single-writer, session-scoped like a temp dir, verified (not literally MkdirTemp, but no concurrent-writer exposure). Migration deferred to a future slice (mechanical, low priority).",
	"internal/ltk/tools/extract-defaults/main.go#main":                        "pre-ratchet baseline, standalone codegen tool under internal/ltk — migrate to iox (fs-consolidation plan C3/C10)",
	"internal/mockengine/runtime.go#Runtime.emitReport":                       "pre-ratchet baseline, mock engine test double — migrate to iox (fs-consolidation plan C3/C10)",
	"internal/operations/bundles.go#reserveNewBundlePath":                     "pre-ratchet baseline — migrate to iox (fs-consolidation plan C3/C10)",
	"internal/operations/delegate.go#copyUntrackedFile":                       "pre-ratchet baseline — migrate to iox (fs-consolidation plan C3/C10)",
	"internal/operations/review_snapshots.go#moveTrustObjects":                "C10 operations sweep: fs.Rename(src, dst) here is a whole-DIRECTORY rename attempt (EXDEV-fallback pattern; falls back to copyTrustObjects, now migrated, + RemoveAll on cross-device failure) — not a single-file content write, outside iox's WriteFileAtomicFs API. Exempt, not a violation to migrate.",
	"internal/operations/task_triggers_cache.go#saveTriggerCache":             "pre-ratchet baseline — migrate to iox (fs-consolidation plan C3/C10)",
	"internal/profiles/profiles.go#Loader.CommitUpgrade":                      "pre-ratchet baseline — internal/profiles is outside C10's five swept areas, left for a future slice (fs-consolidation plan C10)",
	"internal/profiles/profiles.go#Loader.Save":                               "pre-ratchet baseline — internal/profiles is outside C10's five swept areas, left for a future slice (fs-consolidation plan C10)",
	"internal/schemagen/schemagen.go#Generate":                                "pre-ratchet baseline, codegen tool — migrate to iox (fs-consolidation plan C3/C10)",
	"internal/sessions/index.go#linkEngineTranscript":                         "per-vendor-log symlink create (fs-consolidation plan C12, Q2 RULED) — iox writes byte CONTENT and has no symlink primitive; the first-sighting os.Symlink here is the create-once path",
	"internal/sessions/index.go#atomicSymlink":                                "per-vendor-log symlink ATOMIC replace, the session-id-reuse anomaly path only (fs-consolidation plan C12, Q2 RULED) — unique-temp-name+rename mirrors iox's own algorithm, hand-applied because iox's primitives write byte content and have no symlink surface to delegate to",
	"internal/shared/agent/contextfile.go#WriteContextFile":                   "pre-ratchet baseline — internal/shared/agent is outside C10's five swept areas, left for a future slice (fs-consolidation plan C10)",
	"internal/shared/agent/packagefiles.go#WriteManagedPackageFiles":          "C11's DELIBERATE render-to-temp-then-swap design (fs-consolidation plan D8), not un-swept legacy: afero.WriteFile renders each file into a sibling afero.TempDir tree, then fs.Rename swaps each into place as a single atomic per-file replace — this IS the fix humorless-factor/dutiful-water required, and the flagged calls are its two working parts, not a queued migration (fs-consolidation closing verification, stale-reason finding: the original baseline predates C11's rewrite of this function)",
	"internal/shared/agent/rendezvous.go#writeMarker":                         "pre-ratchet baseline — migrate to iox (fs-consolidation plan C3/C10)",
	"internal/shared/agent/settings_io.go#RefuseCorrupt":                      "pre-ratchet baseline — internal/shared/agent is outside C10's five swept areas, left for a future slice (fs-consolidation plan C10)",
	"internal/shared/logsink/logsink.go#Open":                                 "pre-ratchet baseline — migrate to iox (fs-consolidation plan C3/C10)",
	"internal/shared/logsink/logsink.go#rollIfOversized":                      "pre-ratchet baseline — migrate to iox (fs-consolidation plan C3/C10)",
	"internal/shared/tasks/log.go#eventLog.append":                            "O_APPEND write of one event line onto a log that must never lose prior entries — already runs under filelock.Lock (eventLog.lock, callers hold it across append) with its own f.Sync() (appendLine). NOT a 'migrate to iox' candidate: iox's whole family is REPLACE-file-contents (unique temp + rename over the target), and has no append primitive to migrate an O_APPEND writer onto — this entry is a legitimate, permanent exemption, not a queued fix (fs-consolidation closing verification, stale-reason finding)",
	"internal/shared/tasks/taskstest/gitfixture.go#RealGitWorktreeFixture":    "test fixture package, not shipped production code — pre-ratchet baseline (fs-consolidation plan C10)",
	"internal/testsupport/containercell/containercell.go#Runtime.buildImage":  "test harness, never linked into a binary — pre-ratchet baseline (fs-consolidation plan C10)",
	"internal/testsupport/containercell/containercell.go#buildBinary":         "test harness, never linked into a binary — pre-ratchet baseline (fs-consolidation plan C10)",
	"internal/testsupport/containercell/containercell.go#buildProbeCat":       "test harness, never linked into a binary — pre-ratchet baseline (fs-consolidation plan C10)",
	"internal/transcript/recorder.go#openAppendFile":                          "deliberate streaming O_APPEND recorder, live fd held open across writes (task easeful-dial, C7 design) — not a one-shot write iox's whole-file API covers",
}

// writeDisciplineViolation is one raw-fs-write call site the scanner found.
type writeDisciplineViolation struct {
	file   string // module-relative path
	symbol string // "Type.Method", bare func name, or "<package-level>"
	call   string // "os.WriteFile" etc, for the error message
	line   int
}

// key is this violation's writeDisciplineAllowed lookup key.
func (v writeDisciplineViolation) key() string {
	return v.file + "#" + v.symbol
}

// scanWriteDiscipline walks every non-test .go file under each of
// writeDisciplineScopes (skipping writeDisciplineExemptDirs) and returns
// every raw-fs-write call site it finds, full-body parsed rather than
// imports-only — the same technique doc_comment_test.go uses — because the
// subject here is call expressions, not the import graph.
func scanWriteDiscipline(t *testing.T) []writeDisciplineViolation {
	t.Helper()
	root := moduleRoot(t)
	fset := token.NewFileSet()
	var out []writeDisciplineViolation
	var filesScanned int

	for _, scope := range writeDisciplineScopes {
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
			dir := filepath.ToSlash(filepath.Dir(rel))
			if writeDisciplineDirExempt(dir) {
				return nil
			}

			f, perr := parser.ParseFile(fset, p, nil, 0)
			if perr != nil {
				t.Errorf("parse %s: %v", rel, perr)
				return nil
			}
			filesScanned++
			out = append(out, scanFileForRawWrites(fset, f, rel)...)
			return nil
		})
		if err != nil {
			t.Fatalf("walk %s: %v", scope, err)
		}
	}
	// Anti-vacuity: a walk that silently stopped matching files would make
	// every assertion below pass for the wrong reason.
	if filesScanned < 200 {
		t.Fatalf("scanned only %d non-test files under %v — the walk is broken, not the tree", filesScanned, writeDisciplineScopes)
	}
	return out
}

// writeDisciplineDirExempt reports whether dir is (or is under) one of
// writeDisciplineExemptDirs.
func writeDisciplineDirExempt(dir string) bool {
	for _, ex := range writeDisciplineExemptDirs {
		if dir == ex || strings.HasPrefix(dir, ex+"/") {
			return true
		}
	}
	return false
}

// scanFileForRawWrites finds every forbidden os.* call in one parsed file,
// walking each top-level declaration so every call site can be attributed to
// the symbol that contains it.
func scanFileForRawWrites(fset *token.FileSet, f *ast.File, rel string) []writeDisciplineViolation {
	var out []writeDisciplineViolation
	for _, decl := range f.Decls {
		switch d := decl.(type) {
		case *ast.FuncDecl:
			if d.Body == nil {
				continue
			}
			sym := funcSymbol(d)
			out = append(out, collectRawWrites(fset, d.Body, rel, sym)...)
		case *ast.GenDecl:
			if d.Tok != token.VAR {
				continue
			}
			for _, spec := range d.Specs {
				vs, ok := spec.(*ast.ValueSpec)
				if !ok {
					continue
				}
				for _, val := range vs.Values {
					out = append(out, collectRawWrites(fset, val, rel, "<package-level>")...)
				}
			}
		}
	}
	return out
}

// funcSymbol renders a FuncDecl as "Type.Method" (pointer receivers drop the
// "*") or the bare function name for a non-method, so allowlist keys read as
// durable symbol references rather than line numbers.
func funcSymbol(d *ast.FuncDecl) string {
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

// collectRawWrites walks node for CallExprs matching a forbidden os.* callee,
// attributing every hit (including inside a nested closure) to sym — a
// closure inside writeMarker is still writeMarker's violation to fix.
func collectRawWrites(fset *token.FileSet, node ast.Node, rel, sym string) []writeDisciplineViolation {
	var out []writeDisciplineViolation
	ast.Inspect(node, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		// Package-qualified calls: os.* and afero.* are checked against their
		// own forbidden-name sets and never fall through to the afero.Fs
		// method-call heuristic below (an `os` or `afero` package identifier
		// never itself "looks like" an afero.Fs receiver, but skipping the
		// fallthrough here keeps the two checks visibly disjoint).
		if pkgIdent, ok := sel.X.(*ast.Ident); ok {
			switch pkgIdent.Name {
			case "os":
				switch {
				case forbiddenOSCalls[sel.Sel.Name]:
					out = append(out, writeDisciplineViolation{
						file: rel, symbol: sym, call: "os." + sel.Sel.Name,
						line: fset.Position(call.Pos()).Line,
					})
				case sel.Sel.Name == "OpenFile" && len(call.Args) >= 2 && exprMentionsWriteFlag(call.Args[1]):
					out = append(out, writeDisciplineViolation{
						file: rel, symbol: sym, call: "os.OpenFile",
						line: fset.Position(call.Pos()).Line,
					})
				}
				return true
			case "afero":
				if forbiddenAferoPackageCalls[sel.Sel.Name] {
					out = append(out, writeDisciplineViolation{
						file: rel, symbol: sym, call: "afero." + sel.Sel.Name,
						line: fset.Position(call.Pos()).Line,
					})
				}
				return true
			}
		}
		// Not a package-qualified call: check the name-based afero.Fs
		// method-call heuristic (aferoFsMethodCall's doc explains what
		// "looks like an afero.Fs" means here, and its blind spots).
		if aferoFsMethodCall(sel) {
			switch {
			case forbiddenAferoMethodCalls[sel.Sel.Name]:
				out = append(out, writeDisciplineViolation{
					file: rel, symbol: sym, call: "(afero.Fs)." + sel.Sel.Name,
					line: fset.Position(call.Pos()).Line,
				})
			case sel.Sel.Name == "OpenFile" && len(call.Args) >= 2 && exprMentionsWriteFlag(call.Args[1]):
				out = append(out, writeDisciplineViolation{
					file: rel, symbol: sym, call: "(afero.Fs).OpenFile",
					line: fset.Position(call.Pos()).Line,
				})
			}
		}
		return true
	})
	return out
}

// exprMentionsWriteFlag reports whether expr (an os.OpenFile flags argument)
// contains any identifier named after a write-implying os.O_* constant,
// however it is combined (bitwise-or chain, parenthesised, etc). Purely
// syntactic — it does not evaluate the expression, only asks whether one of
// the write-flag names appears anywhere in it.
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
		if sel, ok := n.(*ast.SelectorExpr); ok && writeFlagConstants[sel.Sel.Name] {
			found = true
			return false
		}
		return true
	})
	return found
}

// TestArch_WriteDiscipline_RawFsWritesRouteThroughIox is the gate: every raw
// os.WriteFile/os.Create/os.Rename/os.Symlink/write-mode-os.OpenFile call
// under internal/ outside writeDisciplineExemptDirs must either not exist, or
// be named (with a reason) in writeDisciplineAllowed.
func TestArch_WriteDiscipline_RawFsWritesRouteThroughIox(t *testing.T) {
	violations := scanWriteDiscipline(t)
	sort.Slice(violations, func(i, j int) bool {
		if violations[i].file != violations[j].file {
			return violations[i].file < violations[j].file
		}
		return violations[i].line < violations[j].line
	})

	for _, v := range violations {
		if why, ok := writeDisciplineAllowed[v.key()]; ok {
			t.Logf("allowed: %s:%d %s in %s (%s)", v.file, v.line, v.call, v.symbol, why)
			continue
		}
		t.Errorf("%s:%d calls %s directly in %s — raw filesystem writes must route through "+
			"internal/shared/iox (see its doc comment: unique temp + fsync + rename, exact-perm chmod). "+
			"If this is a deliberate, reviewed exception, add %q to writeDisciplineAllowed in "+
			"tests/arch/write_discipline_test.go naming the fix required to remove it.",
			v.file, v.line, v.call, v.symbol, v.key())
	}
}

// TestArch_WriteDiscipline_AllowlistIsLive fails when a writeDisciplineAllowed
// entry names a symbol that either does not exist (file gone, function
// renamed) or no longer contains a forbidden call — the same staleness check
// TestArch_TestSupportAllowlist_IsLive and TestArch_LayeringAllowlist_IsLive
// run for their own allowlists. A stale exception is worse than none: left in
// place, it would silently cover whatever raw write lands at that symbol
// next, and the baseline could never shrink.
func TestArch_WriteDiscipline_AllowlistIsLive(t *testing.T) {
	violations := scanWriteDiscipline(t)
	live := make(map[string]bool, len(violations))
	for _, v := range violations {
		live[v.key()] = true
	}

	keys := make([]string, 0, len(writeDisciplineAllowed))
	for k := range writeDisciplineAllowed {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	for _, k := range keys {
		if !live[k] {
			t.Errorf("writeDisciplineAllowed allows %q (%s) but the scan found no forbidden os.* call "+
				"there anymore — delete the entry, or it will silently exempt whatever raw write lands "+
				"at that symbol next", k, writeDisciplineAllowed[k])
		}
	}
}
