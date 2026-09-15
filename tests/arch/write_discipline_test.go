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
// fs-consolidation plan (C1) calls this out as the write-discipline half of
// "one atomic-write implementation, one lock idiom, one path-derivation
// chokepoint."
//
// This gate is a RATCHET, not a fix: every call this scan found at authoring
// time is grandfathered into writeDisciplineAllowed with a reason, and none
// of them is migrated here (that is C3/C10's job, sweep by sweep). What the
// gate buys immediately is that the set cannot grow silently — a new raw
// call anywhere in internal/ or cmd/ outside the exempt packages fails the
// build until it either routes through iox or earns its own reviewed entry.
//
// SCOPE EXTENDED TO cmd/ (home-lock-dir fix): the original scan
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
// AFERO COVERAGE: the original scan only matched the `os` package, so
// countersign.Store.writeIndex's bare
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
// "internal"; "cmd" joined it later — see this file's header doc). Test
// files never enter the scan (fsWalkNonTest below skips _test.go the same
// way scan() does).
var writeDisciplineScopes = []string{"internal", "cmd"}

// writeDisciplineExemptDirs are the packages that ARE the write library, and
// so are structurally, not provisionally, exempt: they are not a second copy
// of iox, they are the thing this gate protects. The lock primitive is
// github.com/gofrs/flock, a third-party module rather than an in-tree
// package, so unlike its predecessor (internal/shared/filelock, deleted —
// every lock call site now calls flock.New directly per
// internal/shared/agent/rendezvous.go's idiom) there is nothing beside iox
// left to name here. Anything else that needs an exception earns a reasoned
// entry in writeDisciplineAllowed instead of a free pass here.
var writeDisciplineExemptDirs = []string{
	"internal/shared/iox",
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
//   - The first, an os.*-only scan: 45 entries, nothing migrated in that
//     generation — three named strays (countersign.writeIndex, gitignore,
//     operations.ConvertVendorTranscript) and the rest were migrated later,
//     by per-area sweeps.
//   - The second, RE-generated after the afero coverage extension: 34 new
//     entries surfaced (afero.WriteFile/TempFile and afero.Fs-method call
//     sites the os-only scan could never see), for 79 total. This IS the
//     ratchet growing honestly — the gate got stricter and the baseline
//     records exactly what it now sees. The two remaining stragglers
//     (bundles.fsStore.Save, countersign.Store.write/writeUnsigned) were
//     migrated in the same generation, so those three entries are already
//     gone again by the time this lands; every other new entry carries a
//     reason naming whether it was swept (classified, and migrated or left
//     with a specific reason) or is out of the swept areas and deferred
//     whole.
//
// Most entries carry the generic baseline reason. A handful carry a more
// specific one where the fix is already obvious from the call site (a fixed
// temp name, a lock's own file, a test fixture that is itself the exempt
// case, or a C10 sweep's classification).
var writeDisciplineAllowed = map[string]string{
	"internal/agentcoord/coord/artifactstore.go#artifactStore.publish":        "pre-ratchet baseline — migrate to iox (fs-consolidation plan C3/C10)",
	"internal/agentcoord/coord/homeartifacts.go#Home.DownloadArtifact":        "pre-ratchet baseline — migrate to iox (fs-consolidation plan C3/C10)",
	"internal/agentcoord/coord/httpserver.go#coordServing.saveEndpointLocked": "pre-ratchet baseline — migrate to iox (fs-consolidation plan C3/C10)",
	"internal/agentcoord/coord/journal.go#openStoreFromOffset":                "pre-ratchet baseline — migrate to iox (fs-consolidation plan C3/C10)",
	"internal/agentcoord/coord/statedir.go#claimOwner":                        "advisory lock file's own O_EXCL create — mechanically parallel to the old filelock package's (deleted) exemption but never itself part of it (fs-consolidation plan C10 to decide: fold into a shared lock-file-create helper or exempt structurally)",
	"internal/agentcoord/mcpschema/gen/main.go#generateXmlLike":               "pre-ratchet baseline, codegen tool — migrate to iox (fs-consolidation plan C3/C10)",
	"internal/agentcoord/mcpschema/gen/main.go#writeSpec":                     "pre-ratchet baseline, codegen tool — migrate to iox (fs-consolidation plan C3/C10)",
	"internal/agentcoord/spool/ops.go#renameInto":                             "pre-ratchet baseline — migrate to iox (fs-consolidation plan C3/C10)",
	"internal/agentcoord/spool/writer.go#Writer.Write":                        "pre-ratchet baseline — migrate to iox (fs-consolidation plan C3/C10)",
	"internal/agentcoord/spool/writer.go#writeAndSync":                        "pre-ratchet baseline — migrate to iox (fs-consolidation plan C3/C10)",
	"internal/bundles/skill_archive.go#ImportSkillArchive":                    "C10 content/skill_archive sweep: fsys.Rename here is a WHOLE-DIRECTORY swap (staged tree -> final, and final -> aside on replace), not a single-file content write — outside iox's WriteFileAtomicFs API, which has no directory-rename surface. This is a deliberate, already-safe swap-never-clear-then-hope idiom (see the function's own doc) with its own aside/restore recovery; exempt, not a violation to migrate.",
	"internal/cli/bundle_items.go#editInEditor":                               "pre-ratchet baseline — migrate to iox (fs-consolidation plan C3/C10)",
	"internal/cli/llm_turn.go#writeRunStartHandoff":                           "pre-ratchet baseline — migrate to iox (fs-consolidation plan C3/C10)",
	"internal/cli/run_terminal_ui.go#redirectDiagnosticsForTUI":               "pre-ratchet baseline — migrate to iox (fs-consolidation plan C3/C10)",
	"internal/contextmetrics/contextmetrics.go#Append":                        "pre-ratchet baseline — migrate to iox (fs-consolidation plan C3/C10)",
	"internal/docsgen/config.go#GenConfig":                                    "pre-ratchet baseline, doc generator — migrate to iox (fs-consolidation plan C3/C10)",
	"internal/docsgen/mcp.go#GenMCPTools":                                     "pre-ratchet baseline, doc generator — migrate to iox (fs-consolidation plan C3/C10)",
	"internal/lm/backends/mock.go#writeMockRecord":                            "pre-ratchet baseline, test/mock backend — migrate to iox (fs-consolidation plan C3/C10)",
	"internal/lm/isolation/imagebuild.go#buildBaseImage":                      "C10 isolation sweep: writes inside os.MkdirTemp(\"\", \"ctxloom-imgbase-\"), reaped by the same function's RemoveAll — temp-dir-scoped, verified. Migration deferred to a future slice (mechanical, low priority — no concurrent-writer risk).",
	"internal/lm/isolation/imagebuild.go#buildImage":                          "C10 isolation sweep: writes inside os.MkdirTemp(\"\", \"ctxloom-imgbuild-\"), reaped by the same function's RemoveAll — temp-dir-scoped, verified. Migration deferred to a future slice (mechanical, low priority — no concurrent-writer risk).",
	"internal/lm/isolation/sharedfs.go#probeOneRoot":                          "C10 isolation sweep: writes a marker file inside os.MkdirTemp(root, probeScratchPrefix), deferred RemoveAll in the same function — temp-dir-scoped, verified. Migration deferred to a future slice (mechanical, low priority).",
	"internal/lm/isolation/statemounts.go#ensureFile":                         "C10 isolation sweep: the plan's 'all ~26 seams are temp-dir-scoped' claim is WRONG for this one — ensureFile's caller passes the LIVE ~/.ctxloom/tasks/<project>.jsonl path (taskpaths.HomeTasksLogPath) and its advisory-lock sidecar, to stand up the container bind-mount SOURCE before `run`. Not a write-discipline risk in practice: O_CREATE|O_WRONLY with no O_TRUNC only creates-if-absent and immediately Closes, matching the doc's 'never truncating a log that already has tasks in it' — but it is not temp-scoped, and iox's whole-file-replace API is the wrong shape for a create-if-absent primitive anyway. Reported, not migrated.",
	"internal/lm/isolation/traceprobe.go#traceProbeFromEnv":                   "C10 isolation sweep: writes into the probe's own trace dir, reaped by RemoveAll per the adjacent comment — temp-dir-scoped, verified. Migration deferred to a future slice (mechanical, low priority).",
	"internal/ltk/tools/extract-defaults/main.go#main":                        "pre-ratchet baseline, standalone codegen tool under internal/ltk — migrate to iox (fs-consolidation plan C3/C10)",
	"internal/mockengine/runtime.go#Runtime.emitReport":                       "pre-ratchet baseline, mock engine test double — migrate to iox (fs-consolidation plan C3/C10)",
	"internal/operations/bundles.go#reserveNewBundlePath":                     "pre-ratchet baseline — migrate to iox (fs-consolidation plan C3/C10)",
	"internal/operations/delegate.go#copyUntrackedFile":                       "pre-ratchet baseline — migrate to iox (fs-consolidation plan C3/C10)",
	"internal/operations/harp_artifacts.go#migrateOneHarp":                    "the flagged call MOVES an existing file — os.Rename of one regular file from a harp's top level into its persist/ dir — and iox has no move primitive to delegate to: WriteFileAtomic, WriteFileAtomicFs and NewAtomicFile all write NEW BYTES to a destination path. Copy-then-delete would satisfy the gate and WEAKEN the guarantee this path exists to keep: rename(2) leaves the user's authored plan file at exactly one of src or dst, while a read-write-unlink pair has a window in which a crash leaves it at neither, and these are documents the user wrote. Removing this entry requires a rename/move primitive on iox (a public API addition), not a rewrite of this call site.",
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
	"internal/shared/tasks/taskstest/gitfixture.go#RealGitWorktreeFixture":    "test fixture package, not shipped production code — pre-ratchet baseline (fs-consolidation plan C10)",
	"internal/testsupport/containercell/containercell.go#Runtime.buildImage":  "test harness, never linked into a binary — pre-ratchet baseline (fs-consolidation plan C10)",
	"internal/testsupport/containercell/containercell.go#buildBinary":         "test harness, never linked into a binary — pre-ratchet baseline (fs-consolidation plan C10)",
	"internal/testsupport/containercell/containercell.go#buildProbeCat":       "test harness, never linked into a binary — pre-ratchet baseline (fs-consolidation plan C10)",
	"internal/transcript/recorder.go#openAppendFile":                          "the recorder holds ONE fd open across every Record call for a session's lifetime (opened lazily on the first captured event, closed by Close) and streams into it. WriteFileInPlace(AppendInPlace) is one-shot — it opens, writes and closes per call — so routing this through it would reopen the transcript per event and, more importantly, dissolve the `open func(path string) (io.WriteCloser, error)` seam the partial-write tests drive, which iox deliberately provides no twin for. The exemption is the HELD DESCRIPTOR; it lasts as long as the recorder streams rather than writes.",
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

// --- test-file arm ------------------------------------------------------
//
// Everything above this point governs PRODUCTION code and explicitly skips
// _test.go files (see scanWriteDiscipline's file filter). This arm is the
// missing other half: test code writes files too, ad hoc and almost never
// through a sanctioned writer (of roughly 1,400 test-side file writes,
// measured on the tree this arm landed against, nine went through
// iox.WriteFileAtomic* and three through testsupport.WriteDirProfiles).
//
// The incident that prompted this: a fixture called
// afero.WriteFile(fs, homePath, ...) directly, which does not create parent
// directories, while the production writer it stood in for
// (operations.appendAllowedSignersLine) calls fs.MkdirAll first. The test
// failed on a path production writes fine — fixture and subject disagreed
// about what "write a file" means, and nothing caught it, because this gate
// did not yet look at test code.
//
// SCOPED TO AFERO, NOT OS.*, DELIBERATELY. Roughly 100 test files legitimately
// need real filesystem semantics (exec.Command, symlinks, flock, fsync,
// umask) that afero cannot express, and a file that mixes afero AND os.* is
// an already-vetted, protected convention (a 2026-08-22 audit read all 67
// such files individually and found zero divergent) — not a violation to
// migrate. The bug class this arm exists to catch is specific to afero:
// a MemMapFs silently auto-creates missing parent directories, which is
// exactly what let the signer_test.go fixture pass for as long as it did.
// os.* raw writes in test files are a different, already-governed
// question (or a deliberate real-fs necessity) and are out of this arm's
// scope entirely.
//
// KEYED ON THE IMPORT, NOT THE WORD "afero": a file is in scope only if its
// import declarations name "github.com/spf13/afero" (fileImportsAfero),
// checked via go/ast, never by searching the file's text for the substring
// "afero" — a prior audit found 9 files where that word appeared only in a
// comment. The import check also bounds aferoFsMethodCall's name-based
// receiver heuristic (isAferoFsLikeName) to files that plausibly hold an
// afero.Fs at all, the same purpose the production arm's identical heuristic
// serves.
//
// testWriteDisciplineAllowed is generated the same mechanical way
// writeDisciplineAllowed was (see that var's doc): run this arm with an
// empty map and transcribe every reported violation. The overwhelming
// majority of the baseline is exactly this — ad hoc test-side afero writes
// nobody has migrated yet — so most entries carry the same generic reason
// rather than a bespoke one; a handful carry a specific reason where the
// call site's own shape makes one worth recording.
var testWriteDisciplineAllowed = map[string]string{
	"internal/bundles/reader_testhelpers_test.go#repoTreeTamperedAfterSigning":                                                   "PERMANENT, NOT A DEFERRED MIGRATION. This fixture signs a tree and then alters an item file, leaving the manifest and signature intact — content the sanctioned writers REFUSE to produce, because production refuses to produce it. That refusal is correct and must not be relaxed; it is why the fixture cannot route through them. A hostile publisher does not use our helpers, and a fixture that can only write well-formed content cannot express the attack it exists to test. Do NOT migrate this to testsupport.WriteFile; doing so would silently weaken the test to something production could have written.",
	"internal/operations/seed_readers_test.go#seedHostileTree":                                                                   "PERMANENT, NOT A DEFERRED MIGRATION. Seeds a tree as a hostile publisher would write it, including item bytes the sanctioned writers reject by design. See repoTreeTamperedAfterSigning's entry for the full reasoning: the gate exists so a fixture never disagrees with production about what 'write a file' means, and this fixture's entire purpose is to disagree.",
	"internal/operations/seed_readers_test.go#seedTampered":                                                                      "PERMANENT, NOT A DEFERRED MIGRATION. The tree-form expression of the spec §10.2 downgrade attempt: sign, then alter one item file so the files no longer match SHA256SUMS. content/convert correctly REFUSES to write malformed content, which makes it the wrong tool for a fixture about content that ARRIVED broken rather than content we are authoring. Routing this through a sanctioned writer would mean it could no longer produce the tamper it asserts is caught.",
	"internal/bundles/bundles_test.go#expandRefsFixture":                                                                         "pre-ratchet baseline — ad hoc test-side afero write, not yet migrated (task august-bonanza, test-fs-helper slice): route through testsupport.WriteFile/WriteFileString/SeedTree when this fixture is next touched",
	"internal/bundles/bundles_test.go#TestNewLoader_ReadsWhatItsReadersReport":                                                   "pre-ratchet baseline — ad hoc test-side afero write, not yet migrated (task august-bonanza, test-fs-helper slice): route through testsupport.WriteFile/WriteFileString/SeedTree when this fixture is next touched",
	"internal/bundles/catalog_fs_test.go#TestReadersFS_PrefersTheProjectTreeRegardlessOfReaderOrder":                             "pre-ratchet baseline — ad hoc test-side afero write, not yet migrated (task august-bonanza, test-fs-helper slice): route through testsupport.WriteFile/WriteFileString/SeedTree when this fixture is next touched",
	"internal/bundles/item_ask_test.go#TestReadCommand_PromptsAliasReachesTheSameItem":                                           "pre-ratchet baseline — ad hoc test-side afero write, not yet migrated (task august-bonanza, test-fs-helper slice): route through testsupport.WriteFile/WriteFileString/SeedTree when this fixture is next touched",
	"internal/bundles/item_ask_test.go#TestReadFragment_CommandSelectorIsRefusedByKindNotByHash":                                 "pre-ratchet baseline — ad hoc test-side afero write, not yet migrated (task august-bonanza, test-fs-helper slice): route through testsupport.WriteFile/WriteFileString/SeedTree when this fixture is next touched",
	"internal/bundles/loader_local_signature_test.go#localSigFixture":                                                            "pre-ratchet baseline — ad hoc test-side afero write, not yet migrated (task august-bonanza, test-fs-helper slice): route through testsupport.WriteFile/WriteFileString/SeedTree when this fixture is next touched",
	"internal/bundles/loader_local_signature_test.go#TestLoader_LoadFile_CorruptLocalSignature_WarnsAndDelivers":                 "pre-ratchet baseline — ad hoc test-side afero write, not yet migrated (task august-bonanza, test-fs-helper slice): route through testsupport.WriteFile/WriteFileString/SeedTree when this fixture is next touched",
	"internal/bundles/loader_local_signature_test.go#TestLoader_LoadFile_StaleLocalSignature_WarnsAndDelivers":                   "pre-ratchet baseline — ad hoc test-side afero write, not yet migrated (task august-bonanza, test-fs-helper slice): route through testsupport.WriteFile/WriteFileString/SeedTree when this fixture is next touched",
	"internal/bundles/loader_local_signature_test.go#TestLoader_LoadFile_StaleLocalSignature_WarnsOncePerBundle":                 "pre-ratchet baseline — ad hoc test-side afero write, not yet migrated (task august-bonanza, test-fs-helper slice): route through testsupport.WriteFile/WriteFileString/SeedTree when this fixture is next touched",
	"internal/bundles/loader_local_signature_test.go#TestLoader_LoadFile_UnreadableLocalSignature_WarnsAndDelivers":              "pre-ratchet baseline — ad hoc test-side afero write, not yet migrated (task august-bonanza, test-fs-helper slice): route through testsupport.WriteFile/WriteFileString/SeedTree when this fixture is next touched",
	"internal/bundles/loader_local_signature_test.go#TestLoader_LoadFile_ValidLocalSignature_Silent":                             "pre-ratchet baseline — ad hoc test-side afero write, not yet migrated (task august-bonanza, test-fs-helper slice): route through testsupport.WriteFile/WriteFileString/SeedTree when this fixture is next touched",
	"internal/bundles/loader_silent_failure_test.go#TestCommandsFromBundleRef_CommandSelectorResolvesNotSilent":                  "pre-ratchet baseline — ad hoc test-side afero write, not yet migrated (task august-bonanza, test-fs-helper slice): route through testsupport.WriteFile/WriteFileString/SeedTree when this fixture is next touched",
	"internal/bundles/loader_silent_failure_test.go#TestCommandsFromBundleRef_ItemScopedRefIsSilentEmpty":                        "pre-ratchet baseline — ad hoc test-side afero write, not yet migrated (task august-bonanza, test-fs-helper slice): route through testsupport.WriteFile/WriteFileString/SeedTree when this fixture is next touched",
	"internal/bundles/loader_silent_failure_test.go#TestSkillContent_RefusesNonFilesystemBundlePath":                             "pre-ratchet baseline — ad hoc test-side afero write, not yet migrated (task august-bonanza, test-fs-helper slice): route through testsupport.WriteFile/WriteFileString/SeedTree when this fixture is next touched",
	"internal/bundles/loader_silent_failure_test.go#TestSkillsFromBundleRef_ItemScopedRefIsSilentEmpty":                          "pre-ratchet baseline — ad hoc test-side afero write, not yet migrated (task august-bonanza, test-fs-helper slice): route through testsupport.WriteFile/WriteFileString/SeedTree when this fixture is next touched",
	"internal/bundles/loader_skills_test.go#TestListAllSkills_WithheldSkillOmittedNotErrored":                                    "pre-ratchet baseline — ad hoc test-side afero write, not yet migrated (task august-bonanza, test-fs-helper slice): route through testsupport.WriteFile/WriteFileString/SeedTree when this fixture is next touched",
	"internal/bundles/loader_skills_test.go#TestLoadFile_ConcurrencyContract":                                                    "pre-ratchet baseline — ad hoc test-side afero write, not yet migrated (task august-bonanza, test-fs-helper slice): route through testsupport.WriteFile/WriteFileString/SeedTree when this fixture is next touched",
	"internal/bundles/loader_skills_test.go#TestSkillContent_MalformedAuthoredModeIsWithheldNotDowngraded":                       "pre-ratchet baseline — ad hoc test-side afero write, not yet migrated (task august-bonanza, test-fs-helper slice): route through testsupport.WriteFile/WriteFileString/SeedTree when this fixture is next touched",
	"internal/bundles/loader_skills_test.go#TestSkillContent_ManifestResolutionFailureWarns":                                     "pre-ratchet baseline — ad hoc test-side afero write, not yet migrated (task august-bonanza, test-fs-helper slice): route through testsupport.WriteFile/WriteFileString/SeedTree when this fixture is next touched",
	"internal/bundles/loader_skills_test.go#TestSkillContent_UmaskCheckoutIsDeliveredNotWithheld":                                "pre-ratchet baseline — ad hoc test-side afero write, not yet migrated (task august-bonanza, test-fs-helper slice): route through testsupport.WriteFile/WriteFileString/SeedTree when this fixture is next touched",
	"internal/bundles/loader_skills_test.go#TestSkillsFromBundleRef_TamperedManifestWithheld":                                    "pre-ratchet baseline — ad hoc test-side afero write, not yet migrated (task august-bonanza, test-fs-helper slice): route through testsupport.WriteFile/WriteFileString/SeedTree when this fixture is next touched",
	"internal/bundles/reader_local_tree_test.go#TestLocalTreeForm_EmptySingleFileDocumentBesideItemDirsStillReads":               "pre-ratchet baseline — ad hoc test-side afero write, not yet migrated (task august-bonanza, test-fs-helper slice): route through testsupport.WriteFile/WriteFileString/SeedTree when this fixture is next touched",
	"internal/bundles/reader_local_tree_test.go#TestLocalTreeForm_InlineDirectoryFormStillReadsAsADocument":                      "pre-ratchet baseline — ad hoc test-side afero write, not yet migrated (task august-bonanza, test-fs-helper slice): route through testsupport.WriteFile/WriteFileString/SeedTree when this fixture is next touched",
	"internal/bundles/reader_local_tree_test.go#TestLocalTreeForm_MetadataOnlyDirectoryBundleStillLoads":                         "pre-ratchet baseline — ad hoc test-side afero write, not yet migrated (task august-bonanza, test-fs-helper slice): route through testsupport.WriteFile/WriteFileString/SeedTree when this fixture is next touched",
	"internal/bundles/reader_local_tree_test.go#TestLocalTreeForm_SingleFileDocumentStillReads":                                  "pre-ratchet baseline — ad hoc test-side afero write, not yet migrated (task august-bonanza, test-fs-helper slice): route through testsupport.WriteFile/WriteFileString/SeedTree when this fixture is next touched",
	"internal/bundles/reader_local_tree_test.go#TestLocalTreeForm_UnreadableTreeIsReportedNotSilentlyEmptied":                    "pre-ratchet baseline — ad hoc test-side afero write, not yet migrated (task august-bonanza, test-fs-helper slice): route through testsupport.WriteFile/WriteFileString/SeedTree when this fixture is next touched",
	"internal/bundles/reader_test.go#TestLoader_LocalInvalidSignatureIsAdmittedAndTheAuthorIsTold":                               "pre-ratchet baseline — ad hoc test-side afero write, not yet migrated (task august-bonanza, test-fs-helper slice): route through testsupport.WriteFile/WriteFileString/SeedTree when this fixture is next touched",
	"internal/bundles/reader_test.go#TestNewProjectReader_ReportsProjectProvenanceAndLocalContext":                               "pre-ratchet baseline — ad hoc test-side afero write, not yet migrated (task august-bonanza, test-fs-helper slice): route through testsupport.WriteFile/WriteFileString/SeedTree when this fixture is next touched",
	"internal/bundles/reader_test.go#TestNewProjectReader_ReportsSignatureFactsAsDiagnostics":                                    "pre-ratchet baseline — ad hoc test-side afero write, not yet migrated (task august-bonanza, test-fs-helper slice): route through testsupport.WriteFile/WriteFileString/SeedTree when this fixture is next touched",
	"internal/bundles/skill_archive_test.go#renameFailFs.Rename":                                                                 "pre-ratchet baseline — ad hoc test-side afero write, not yet migrated (task august-bonanza, test-fs-helper slice): route through testsupport.WriteFile/WriteFileString/SeedTree when this fixture is next touched",
	"internal/bundles/skill_archive_test.go#TestImportSkillArchive_FailedFinalRenameLeavesDestinationIntact":                     "pre-ratchet baseline — ad hoc test-side afero write, not yet migrated (task august-bonanza, test-fs-helper slice): route through testsupport.WriteFile/WriteFileString/SeedTree when this fixture is next touched",
	"internal/bundles/skill_archive_test.go#TestImportSkillArchive_FailedValidationLeavesDestinationIntact":                      "pre-ratchet baseline — ad hoc test-side afero write, not yet migrated (task august-bonanza, test-fs-helper slice): route through testsupport.WriteFile/WriteFileString/SeedTree when this fixture is next touched",
	"internal/bundles/skill_archive_test.go#TestImportSkillArchive_SucceedsOverAnExistingTreeAndLeavesNoBackup":                  "pre-ratchet baseline — ad hoc test-side afero write, not yet migrated (task august-bonanza, test-fs-helper slice): route through testsupport.WriteFile/WriteFileString/SeedTree when this fixture is next touched",
	"internal/bundles/skill_archive_test.go#TestImportSkillArchive_UnrestorableTreeSaysWhereItIs":                                "pre-ratchet baseline — ad hoc test-side afero write, not yet migrated (task august-bonanza, test-fs-helper slice): route through testsupport.WriteFile/WriteFileString/SeedTree when this fixture is next touched",
	"internal/bundles/skill_archive_test.go#TestResolveSymlinkChain":                                                             "pre-ratchet baseline — ad hoc test-side afero write, not yet migrated (task august-bonanza, test-fs-helper slice): route through testsupport.WriteFile/WriteFileString/SeedTree when this fixture is next touched",
	"internal/bundles/skill_archive_test.go#TestVerifyExtractedManifest_LooseModeDoesNotLoosenContent":                           "pre-ratchet baseline — ad hoc test-side afero write, not yet migrated (task august-bonanza, test-fs-helper slice): route through testsupport.WriteFile/WriteFileString/SeedTree when this fixture is next touched",
	"internal/bundles/skill_preimage_test.go#realSkillTree":                                                                      "pre-ratchet baseline — ad hoc test-side afero write, not yet migrated (task august-bonanza, test-fs-helper slice): route through testsupport.WriteFile/WriteFileString/SeedTree when this fixture is next touched",
	"internal/bundles/skill_preimage_test.go#TestSkillsFromBundleRef_ManifestLessTamperIsWithheld":                               "pre-ratchet baseline — ad hoc test-side afero write, not yet migrated (task august-bonanza, test-fs-helper slice): route through testsupport.WriteFile/WriteFileString/SeedTree when this fixture is next touched",
	"internal/bundles/skill_test.go#TestBuildSkillManifest_PackageSizeBoundary":                                                  "pre-ratchet baseline — ad hoc test-side afero write, not yet migrated (task august-bonanza, test-fs-helper slice): route through testsupport.WriteFile/WriteFileString/SeedTree when this fixture is next touched",
	"internal/bundles/skill_test.go#TestParseSkillPackage_DefaultMaxSizeGatesRealParsing":                                        "pre-ratchet baseline — ad hoc test-side afero write, not yet migrated (task august-bonanza, test-fs-helper slice): route through testsupport.WriteFile/WriteFileString/SeedTree when this fixture is next touched",
	"internal/bundles/skill_test.go#TestParseSkillPackage_PackageTooLarge":                                                       "pre-ratchet baseline — ad hoc test-side afero write, not yet migrated (task august-bonanza, test-fs-helper slice): route through testsupport.WriteFile/WriteFileString/SeedTree when this fixture is next touched",
	"internal/bundles/skill_test.go#writeSkillFixture":                                                                           "pre-ratchet baseline — ad hoc test-side afero write, not yet migrated (task august-bonanza, test-fs-helper slice): route through testsupport.WriteFile/WriteFileString/SeedTree when this fixture is next touched",
	"internal/bundles/store_sig_test.go#TestFSStore_Save_UnreadableSignatureIsLoudNotAssumedAbsent":                              "pre-ratchet baseline — ad hoc test-side afero write, not yet migrated (task august-bonanza, test-fs-helper slice): route through testsupport.WriteFile/WriteFileString/SeedTree when this fixture is next touched",
	"internal/bundles/store_test.go#TestFSStore_Delete_DirectoryFormBundleLeavesItsSubtreesOnDisk":                               "pre-ratchet baseline — ad hoc test-side afero write, not yet migrated (task august-bonanza, test-fs-helper slice): route through testsupport.WriteFile/WriteFileString/SeedTree when this fixture is next touched",
	"internal/bundles/trust_ref_kinds_test.go#TestTrustRefKindDirs_MatchTheTrustAuthority":                                       "pre-ratchet baseline — ad hoc test-side afero write, not yet migrated (task august-bonanza, test-fs-helper slice): route through testsupport.WriteFile/WriteFileString/SeedTree when this fixture is next touched",
	"internal/bundles/warn_test.go#TestLoader_WarnWriterReceivesTheWarnerDiagnostics":                                            "pre-ratchet baseline — ad hoc test-side afero write, not yet migrated (task august-bonanza, test-fs-helper slice): route through testsupport.WriteFile/WriteFileString/SeedTree when this fixture is next touched",
	"internal/claude/claude_test.go#TestClaudeCodeHookWriter_DenyTools_PreservesUserAllowAsk":                                    "pre-ratchet baseline — ad hoc test-side afero write, not yet migrated (task august-bonanza, test-fs-helper slice): route through testsupport.WriteFile/WriteFileString/SeedTree when this fixture is next touched",
	"internal/claude/claude_test.go#TestClaudeCodeHookWriter_DenyTools_UserAuthoredDenySurvives":                                 "pre-ratchet baseline — ad hoc test-side afero write, not yet migrated (task august-bonanza, test-fs-helper slice): route through testsupport.WriteFile/WriteFileString/SeedTree when this fixture is next touched",
	"internal/claude/claude_test.go#TestClaudeCodeHookWriter_LegacyMCPServersInSettings_ArePreserved":                            "pre-ratchet baseline — ad hoc test-side afero write, not yet migrated (task august-bonanza, test-fs-helper slice): route through testsupport.WriteFile/WriteFileString/SeedTree when this fixture is next touched",
	"internal/claude/claude_test.go#TestClaudeCodeHookWriter_MalformedHooksJSON_FailsLoudAndBacksUp":                             "pre-ratchet baseline — ad hoc test-side afero write, not yet migrated (task august-bonanza, test-fs-helper slice): route through testsupport.WriteFile/WriteFileString/SeedTree when this fixture is next touched",
	"internal/claude/claude_test.go#TestClaudeCodeHookWriter_MalformedMCPConfig_FailsLoudAndBacksUp":                             "pre-ratchet baseline — ad hoc test-side afero write, not yet migrated (task august-bonanza, test-fs-helper slice): route through testsupport.WriteFile/WriteFileString/SeedTree when this fixture is next touched",
	"internal/claude/claude_test.go#TestClaudeCodeHookWriter_MalformedMCPConfig_IsNotOverwritten":                                "pre-ratchet baseline — ad hoc test-side afero write, not yet migrated (task august-bonanza, test-fs-helper slice): route through testsupport.WriteFile/WriteFileString/SeedTree when this fixture is next touched",
	"internal/claude/claude_test.go#TestClaudeCodeHookWriter_MalformedPermissionsDeny_FailsLoudAndBacksUp":                       "pre-ratchet baseline — ad hoc test-side afero write, not yet migrated (task august-bonanza, test-fs-helper slice): route through testsupport.WriteFile/WriteFileString/SeedTree when this fixture is next touched",
	"internal/claude/claude_test.go#TestClaudeCodeHookWriter_MalformedPermissionsJSON_FailsLoudAndBacksUp":                       "pre-ratchet baseline — ad hoc test-side afero write, not yet migrated (task august-bonanza, test-fs-helper slice): route through testsupport.WriteFile/WriteFileString/SeedTree when this fixture is next touched",
	"internal/claude/claude_test.go#TestClaudeCodeHookWriter_MalformedSettingsJSON_FailsLoudAndBacksUp":                          "pre-ratchet baseline — ad hoc test-side afero write, not yet migrated (task august-bonanza, test-fs-helper slice): route through testsupport.WriteFile/WriteFileString/SeedTree when this fixture is next touched",
	"internal/claude/claude_test.go#TestClaudeCodeHookWriter_ModifiesInPlaceWithoutABackupSibling":                               "pre-ratchet baseline — ad hoc test-side afero write, not yet migrated (task august-bonanza, test-fs-helper slice): route through testsupport.WriteFile/WriteFileString/SeedTree when this fixture is next touched",
	"internal/claude/claude_test.go#TestClaudeCodeHookWriter_WellFormedPermissions_RoundTripUntouched":                           "pre-ratchet baseline — ad hoc test-side afero write, not yet migrated (task august-bonanza, test-fs-helper slice): route through testsupport.WriteFile/WriteFileString/SeedTree when this fixture is next touched",
	"internal/claude/claude_test.go#TestLoadSettings_UnreadableStatusLineIsRefusedNotDropped":                                    "pre-ratchet baseline — ad hoc test-side afero write, not yet migrated (task august-bonanza, test-fs-helper slice): route through testsupport.WriteFile/WriteFileString/SeedTree when this fixture is next touched",
	"internal/claude/claude_test.go#TestRemoveSettings_MCPStatErrorIsLoud":                                                       "pre-ratchet baseline — ad hoc test-side afero write, not yet migrated (task august-bonanza, test-fs-helper slice): route through testsupport.WriteFile/WriteFileString/SeedTree when this fixture is next touched",
	"internal/claude/claude_test.go#TestRemoveSettings_SettingsStatErrorIsLoud":                                                  "pre-ratchet baseline — ad hoc test-side afero write, not yet migrated (task august-bonanza, test-fs-helper slice): route through testsupport.WriteFile/WriteFileString/SeedTree when this fixture is next touched",
	"internal/claude/claude_test.go#TestSaveSettings_PreservesLargeIntegerUserSetting":                                           "pre-ratchet baseline — ad hoc test-side afero write, not yet migrated (task august-bonanza, test-fs-helper slice): route through testsupport.WriteFile/WriteFileString/SeedTree when this fixture is next touched",
	"internal/claude/claude_test.go#TestSaveSettings_UnencodablePermissionsSiblingIsRefusedNotDropped":                           "pre-ratchet baseline — ad hoc test-side afero write, not yet migrated (task august-bonanza, test-fs-helper slice): route through testsupport.WriteFile/WriteFileString/SeedTree when this fixture is next touched",
	"internal/claude/claude_test.go#TestSaveSettings_UnencodableUserFieldIsRefusedNotDropped":                                    "pre-ratchet baseline — ad hoc test-side afero write, not yet migrated (task august-bonanza, test-fs-helper slice): route through testsupport.WriteFile/WriteFileString/SeedTree when this fixture is next touched",
	"internal/claude/claude_test.go#TestStatus_MCPStatErrorIsLoud":                                                               "pre-ratchet baseline — ad hoc test-side afero write, not yet migrated (task august-bonanza, test-fs-helper slice): route through testsupport.WriteFile/WriteFileString/SeedTree when this fixture is next touched",
	"internal/claude/claude_test.go#TestStatus_SettingsStatErrorIsLoud":                                                          "pre-ratchet baseline — ad hoc test-side afero write, not yet migrated (task august-bonanza, test-fs-helper slice): route through testsupport.WriteFile/WriteFileString/SeedTree when this fixture is next touched",
	"internal/claude/claude_test.go#TestWriteSettings_HandAuthoredCtxloomHookSurvives":                                           "pre-ratchet baseline — ad hoc test-side afero write, not yet migrated (task august-bonanza, test-fs-helper slice): route through testsupport.WriteFile/WriteFileString/SeedTree when this fixture is next touched",
	"internal/claude/claude_test.go#TestWriteSettings_UserStatusLineInvokingCtxloomSurvives":                                     "pre-ratchet baseline — ad hoc test-side afero write, not yet migrated (task august-bonanza, test-fs-helper slice): route through testsupport.WriteFile/WriteFileString/SeedTree when this fixture is next touched",
	"internal/claude/commandfiles_test.go#TestWriteCommandFiles_LegacyDirRemovalErrorIsLoud":                                     "pre-ratchet baseline — ad hoc test-side afero write, not yet migrated (task august-bonanza, test-fs-helper slice): route through testsupport.WriteFile/WriteFileString/SeedTree when this fixture is next touched",
	"internal/claude/mcp_preserve_test.go#TestWriteMCPConfig_PreservesForeignTopLevelKeys":                                       "pre-ratchet baseline — ad hoc test-side afero write, not yet migrated (task august-bonanza, test-fs-helper slice): route through testsupport.WriteFile/WriteFileString/SeedTree when this fixture is next touched",
	"internal/claude/mcp_preserve_test.go#TestWriteMCPConfig_PreservesUnmodelledServerFields":                                    "pre-ratchet baseline — ad hoc test-side afero write, not yet migrated (task august-bonanza, test-fs-helper slice): route through testsupport.WriteFile/WriteFileString/SeedTree when this fixture is next touched",
	"internal/claude/surfaces_test.go#armedFailFs.Create":                                                                        "pre-ratchet baseline — ad hoc test-side afero write, not yet migrated (task august-bonanza, test-fs-helper slice): route through testsupport.WriteFile/WriteFileString/SeedTree when this fixture is next touched",
	"internal/claude/surfaces_test.go#armedFailFs.Rename":                                                                        "pre-ratchet baseline — ad hoc test-side afero write, not yet migrated (task august-bonanza, test-fs-helper slice): route through testsupport.WriteFile/WriteFileString/SeedTree when this fixture is next touched",
	"internal/claude/writecontext_test.go#TestClaudeCodeHookWriter_WriteContext_PreservesHandWrittenContent":                     "pre-ratchet baseline — ad hoc test-side afero write, not yet migrated (task august-bonanza, test-fs-helper slice): route through testsupport.WriteFile/WriteFileString/SeedTree when this fixture is next touched",
	"internal/cli/init_test.go#TestCtxloomDefaultTrusted":                                                                        "pre-ratchet baseline — ad hoc test-side afero write, not yet migrated (task august-bonanza, test-fs-helper slice): route through testsupport.WriteFile/WriteFileString/SeedTree when this fixture is next touched",
	"internal/cli/llm_default_test.go#memConfig":                                                                                 "pre-ratchet baseline — ad hoc test-side afero write, not yet migrated (task august-bonanza, test-fs-helper slice): route through testsupport.WriteFile/WriteFileString/SeedTree when this fixture is next touched",
	"internal/cli/util_config_write_test.go#tamperedReadFs.Open":                                                                 "pre-ratchet baseline — ad hoc test-side afero write, not yet migrated (task august-bonanza, test-fs-helper slice): route through testsupport.WriteFile/WriteFileString/SeedTree when this fixture is next touched",
	"internal/cli/util_config_write_test.go#TestApplicationRecord_InverseRestoresTheOriginalBytes":                               "pre-ratchet baseline — ad hoc test-side afero write, not yet migrated (task august-bonanza, test-fs-helper slice): route through testsupport.WriteFile/WriteFileString/SeedTree when this fixture is next touched",
	"internal/cli/util_config_write_test.go#TestFreeRecordPath_NeverOverwritesAnExistingRecord":                                  "pre-ratchet baseline — ad hoc test-side afero write, not yet migrated (task august-bonanza, test-fs-helper slice): route through testsupport.WriteFile/WriteFileString/SeedTree when this fixture is next touched",
	"internal/cli/util_config_write_test.go#TestRunConfigWrite_JSONMerge_PreservesForeignKeysAndAddsNew":                         "pre-ratchet baseline — ad hoc test-side afero write, not yet migrated (task august-bonanza, test-fs-helper slice): route through testsupport.WriteFile/WriteFileString/SeedTree when this fixture is next touched",
	"internal/cli/util_config_write_test.go#TestRunConfigWrite_JSONPatch_ApplicationRecord_WrittenWithContent":                   "pre-ratchet baseline — ad hoc test-side afero write, not yet migrated (task august-bonanza, test-fs-helper slice): route through testsupport.WriteFile/WriteFileString/SeedTree when this fixture is next touched",
	"internal/cli/util_config_write_test.go#TestRunConfigWrite_JSONPatch_BytePreservingFormatting":                               "pre-ratchet baseline — ad hoc test-side afero write, not yet migrated (task august-bonanza, test-fs-helper slice): route through testsupport.WriteFile/WriteFileString/SeedTree when this fixture is next touched",
	"internal/cli/util_config_write_test.go#TestRunConfigWrite_JSONPatch_ModePreserved":                                          "pre-ratchet baseline — ad hoc test-side afero write, not yet migrated (task august-bonanza, test-fs-helper slice): route through testsupport.WriteFile/WriteFileString/SeedTree when this fixture is next touched",
	"internal/cli/util_config_write_test.go#TestRunConfigWrite_JSONPatch_NestedMergePreservesSiblingsBeyondOneLevel":             "pre-ratchet baseline — ad hoc test-side afero write, not yet migrated (task august-bonanza, test-fs-helper slice): route through testsupport.WriteFile/WriteFileString/SeedTree when this fixture is next touched",
	"internal/cli/util_config_write_test.go#TestRunConfigWrite_JSONPatch_NullDeletesKey":                                         "pre-ratchet baseline — ad hoc test-side afero write, not yet migrated (task august-bonanza, test-fs-helper slice): route through testsupport.WriteFile/WriteFileString/SeedTree when this fixture is next touched",
	"internal/cli/util_config_write_test.go#TestRunConfigWrite_JSONPatch_NullOnAbsentKey_NoOp":                                   "pre-ratchet baseline — ad hoc test-side afero write, not yet migrated (task august-bonanza, test-fs-helper slice): route through testsupport.WriteFile/WriteFileString/SeedTree when this fixture is next touched",
	"internal/cli/util_config_write_test.go#TestRunConfigWrite_ReReadVerify_CatchesBadWrite":                                     "pre-ratchet baseline — ad hoc test-side afero write, not yet migrated (task august-bonanza, test-fs-helper slice): route through testsupport.WriteFile/WriteFileString/SeedTree when this fixture is next touched",
	"internal/cli/util_config_write_test.go#TestRunConfigWrite_TOMLMerge_PreservesForeignKeysAndAddsNew":                         "pre-ratchet baseline — ad hoc test-side afero write, not yet migrated (task august-bonanza, test-fs-helper slice): route through testsupport.WriteFile/WriteFileString/SeedTree when this fixture is next touched",
	"internal/cli/util_config_write_test.go#TestRunConfigWrite_TOMLPatch_PreservesUnrelatedBytes":                                "pre-ratchet baseline — ad hoc test-side afero write, not yet migrated (task august-bonanza, test-fs-helper slice): route through testsupport.WriteFile/WriteFileString/SeedTree when this fixture is next touched",
	"internal/cli/util_config_write_test.go#TestRunConfigWrite_TOMLPatch_WritesApplicationRecord":                                "pre-ratchet baseline — ad hoc test-side afero write, not yet migrated (task august-bonanza, test-fs-helper slice): route through testsupport.WriteFile/WriteFileString/SeedTree when this fixture is next touched",
	"internal/cli/util_config_write_test.go#TestRunConfigWrite_UnparseableExisting_RefusesAndPreservesBytes":                     "pre-ratchet baseline — ad hoc test-side afero write, not yet migrated (task august-bonanza, test-fs-helper slice): route through testsupport.WriteFile/WriteFileString/SeedTree when this fixture is next touched",
	"internal/config/agents_test.go#TestLoadAgents_AbsentOrEmptyDirectoryIsSilent":                                               "pre-ratchet baseline — ad hoc test-side afero write, not yet migrated (task august-bonanza, test-fs-helper slice): route through testsupport.WriteFile/WriteFileString/SeedTree when this fixture is next touched",
	"internal/config/agents_test.go#TestLoadAgents_RetiredDirectoryFindingIsRecordedOncePerWindow":                               "pre-ratchet baseline — ad hoc test-side afero write, not yet migrated (task august-bonanza, test-fs-helper slice): route through testsupport.WriteFile/WriteFileString/SeedTree when this fixture is next touched",
	"internal/config/agents_test.go#TestLoadAgents_RetiredDirectoryIsAFatalFinding":                                              "pre-ratchet baseline — ad hoc test-side afero write, not yet migrated (task august-bonanza, test-fs-helper slice): route through testsupport.WriteFile/WriteFileString/SeedTree when this fixture is next touched",
	"internal/config/config_save_preserve_test.go#TestConfig_Save_PreservesCommentsAndKeyOrder":                                  "pre-ratchet baseline — ad hoc test-side afero write, not yet migrated (task august-bonanza, test-fs-helper slice): route through testsupport.WriteFile/WriteFileString/SeedTree when this fixture is next touched",
	"internal/config/config_save_test.go#TestConfig_Save_CorruptConfig_RefusesToTruncate":                                        "pre-ratchet baseline — ad hoc test-side afero write, not yet migrated (task august-bonanza, test-fs-helper slice): route through testsupport.WriteFile/WriteFileString/SeedTree when this fixture is next touched",
	"internal/config/config_save_test.go#TestConfig_Save_ParseableConfig_StillSaves":                                             "pre-ratchet baseline — ad hoc test-side afero write, not yet migrated (task august-bonanza, test-fs-helper slice): route through testsupport.WriteFile/WriteFileString/SeedTree when this fixture is next touched",
	"internal/config/config_save_test.go#TestConfig_Save_PrunesEmptiedEditor":                                                    "pre-ratchet baseline — ad hoc test-side afero write, not yet migrated (task august-bonanza, test-fs-helper slice): route through testsupport.WriteFile/WriteFileString/SeedTree when this fixture is next touched",
	"internal/config/config_test.go#TestLoadConfigFile_Errors":                                                                   "pre-ratchet baseline — ad hoc test-side afero write, not yet migrated (task august-bonanza, test-fs-helper slice): route through testsupport.WriteFile/WriteFileString/SeedTree when this fixture is next touched",
	"internal/config/config_test.go#TestLoad_CurrentConfigHasNoPendingUpgrade":                                                   "pre-ratchet baseline — ad hoc test-side afero write, not yet migrated (task august-bonanza, test-fs-helper slice): route through testsupport.WriteFile/WriteFileString/SeedTree when this fixture is next touched",
	"internal/config/config_test.go#TestLoad_PreservesEnvKeyCase":                                                                "pre-ratchet baseline — ad hoc test-side afero write, not yet migrated (task august-bonanza, test-fs-helper slice): route through testsupport.WriteFile/WriteFileString/SeedTree when this fixture is next touched",
	"internal/config/config_test.go#TestLoad_RetiredAgentTurnCapKeyRefusedNotIgnored":                                            "pre-ratchet baseline — ad hoc test-side afero write, not yet migrated (task august-bonanza, test-fs-helper slice): route through testsupport.WriteFile/WriteFileString/SeedTree when this fixture is next touched",
	"internal/config/config_test.go#TestLoad_SchemaCompileFailureProducesWarning":                                                "pre-ratchet baseline — ad hoc test-side afero write, not yet migrated (task august-bonanza, test-fs-helper slice): route through testsupport.WriteFile/WriteFileString/SeedTree when this fixture is next touched",
	"internal/config/config_test.go#TestLoad_SchemaValidationProducesWarning":                                                    "pre-ratchet baseline — ad hoc test-side afero write, not yet migrated (task august-bonanza, test-fs-helper slice): route through testsupport.WriteFile/WriteFileString/SeedTree when this fixture is next touched",
	"internal/config/config_test.go#TestLoad_WithOptions":                                                                        "pre-ratchet baseline — ad hoc test-side afero write, not yet migrated (task august-bonanza, test-fs-helper slice): route through testsupport.WriteFile/WriteFileString/SeedTree when this fixture is next touched",
	"internal/config/config_test.go#TestResilientStartup_CompletelyInvalidYAML":                                                  "pre-ratchet baseline — ad hoc test-side afero write, not yet migrated (task august-bonanza, test-fs-helper slice): route through testsupport.WriteFile/WriteFileString/SeedTree when this fixture is next touched",
	"internal/config/config_test.go#TestResilientStartup_EmptyConfig":                                                            "pre-ratchet baseline — ad hoc test-side afero write, not yet migrated (task august-bonanza, test-fs-helper slice): route through testsupport.WriteFile/WriteFileString/SeedTree when this fixture is next touched",
	"internal/config/config_test.go#TestResilientStartup_MalformedConfig":                                                        "pre-ratchet baseline — ad hoc test-side afero write, not yet migrated (task august-bonanza, test-fs-helper slice): route through testsupport.WriteFile/WriteFileString/SeedTree when this fixture is next touched",
	"internal/config/config_test.go#TestResilientStartup_NonExistentProfile":                                                     "pre-ratchet baseline — ad hoc test-side afero write, not yet migrated (task august-bonanza, test-fs-helper slice): route through testsupport.WriteFile/WriteFileString/SeedTree when this fixture is next touched",
	"internal/config/config_test.go#TestResilientStartup_PartiallyValidConfig":                                                   "pre-ratchet baseline — ad hoc test-side afero write, not yet migrated (task august-bonanza, test-fs-helper slice): route through testsupport.WriteFile/WriteFileString/SeedTree when this fixture is next touched",
	"internal/config/config_test.go#TestResilientStartup_WarningsAreCollected":                                                   "pre-ratchet baseline — ad hoc test-side afero write, not yet migrated (task august-bonanza, test-fs-helper slice): route through testsupport.WriteFile/WriteFileString/SeedTree when this fixture is next touched",
	"internal/config/config_version_refusal_test.go#loadRefusalFindings":                                                         "pre-ratchet baseline — ad hoc test-side afero write, not yet migrated (task august-bonanza, test-fs-helper slice): route through testsupport.WriteFile/WriteFileString/SeedTree when this fixture is next touched",
	"internal/config/dirty_tree_ack_test.go#TestDirtyTreeCommitAcknowledged_UnreadableStoreWarnsAndDenies":                       "pre-ratchet baseline — ad hoc test-side afero write, not yet migrated (task august-bonanza, test-fs-helper slice): route through testsupport.WriteFile/WriteFileString/SeedTree when this fixture is next touched",
	"internal/config/golden_fixture_test.go#TestGoldenFixture_CurrentEffectiveConfig_D3Characterization":                         "pre-ratchet baseline — ad hoc test-side afero write, not yet migrated (task august-bonanza, test-fs-helper slice): route through testsupport.WriteFile/WriteFileString/SeedTree when this fixture is next touched",
	"internal/config/golden_fixture_test.go#TestGoldenFixture_D3Drift_IsAdditiveOnly":                                            "pre-ratchet baseline — ad hoc test-side afero write, not yet migrated (task august-bonanza, test-fs-helper slice): route through testsupport.WriteFile/WriteFileString/SeedTree when this fixture is next touched",
	"internal/config/home_upgrade_test.go#TestCommitUpgrade_EmptyPayload_RefusesAndLeavesTheFileAlone":                           "pre-ratchet baseline — ad hoc test-side afero write, not yet migrated (task august-bonanza, test-fs-helper slice): route through testsupport.WriteFile/WriteFileString/SeedTree when this fixture is next touched",
	"internal/config/layering_test.go#TestLoad_ExplicitAppDirDifferentFromHome_StaysSourceProject":                               "pre-ratchet baseline — ad hoc test-side afero write, not yet migrated (task august-bonanza, test-fs-helper slice): route through testsupport.WriteFile/WriteFileString/SeedTree when this fixture is next touched",
	"internal/config/layering_test.go#TestLoad_ExplicitAppDirEqualToHome_ResolvesSourceHome":                                     "pre-ratchet baseline — ad hoc test-side afero write, not yet migrated (task august-bonanza, test-fs-helper slice): route through testsupport.WriteFile/WriteFileString/SeedTree when this fixture is next touched",
	"internal/config/layering_test.go#writeLayers":                                                                               "pre-ratchet baseline — ad hoc test-side afero write, not yet migrated (task august-bonanza, test-fs-helper slice): route through testsupport.WriteFile/WriteFileString/SeedTree when this fixture is next touched",
	"internal/config/layer_policy_test.go#TestLoad_ConfigSetCanStillMintAPrivilegedAgent_ByDesign":                               "pre-ratchet baseline — ad hoc test-side afero write, not yet migrated (task august-bonanza, test-fs-helper slice): route through testsupport.WriteFile/WriteFileString/SeedTree when this fixture is next touched",
	"internal/config/layer_policy_test.go#TestLoad_ConfigSetPatchesOneAgentFieldWithoutWipingSiblings":                           "pre-ratchet baseline — ad hoc test-side afero write, not yet migrated (task august-bonanza, test-fs-helper slice): route through testsupport.WriteFile/WriteFileString/SeedTree when this fixture is next touched",
	"internal/config/layer_policy_test.go#TestLoad_EscalationPath1_ConfigSetCannotGrantDirtyTreeCommitAck":                       "pre-ratchet baseline — ad hoc test-side afero write, not yet migrated (task august-bonanza, test-fs-helper slice): route through testsupport.WriteFile/WriteFileString/SeedTree when this fixture is next touched",
	"internal/config/layer_policy_test.go#TestLoad_EscalationPath1_EnvCannotGrantDirtyTreeCommitAck":                             "pre-ratchet baseline — ad hoc test-side afero write, not yet migrated (task august-bonanza, test-fs-helper slice): route through testsupport.WriteFile/WriteFileString/SeedTree when this fixture is next touched",
	"internal/config/layer_policy_test.go#TestLoad_EscalationPath2_EnvCannotMintPrivilegedAgent":                                 "pre-ratchet baseline — ad hoc test-side afero write, not yet migrated (task august-bonanza, test-fs-helper slice): route through testsupport.WriteFile/WriteFileString/SeedTree when this fixture is next touched",
	"internal/config/layer_policy_test.go#TestLoad_EscalationPath3_HomeCannotEscalateProjectAgent":                               "pre-ratchet baseline — ad hoc test-side afero write, not yet migrated (task august-bonanza, test-fs-helper slice): route through testsupport.WriteFile/WriteFileString/SeedTree when this fixture is next touched",
	"internal/config/layer_policy_test.go#TestManagerUpdate_DoesNotPersistHomeInheritedMachineValueIntoProjectFile":              "pre-ratchet baseline — ad hoc test-side afero write, not yet migrated (task august-bonanza, test-fs-helper slice): route through testsupport.WriteFile/WriteFileString/SeedTree when this fixture is next touched",
	"internal/config/llm_authored_test.go#TestIsLLMUserAuthored_EmptyRegistry_DefaultLabelsAreNotUserAuthored":                   "pre-ratchet baseline — ad hoc test-side afero write, not yet migrated (task august-bonanza, test-fs-helper slice): route through testsupport.WriteFile/WriteFileString/SeedTree when this fixture is next touched",
	"internal/config/llm_authored_test.go#TestIsLLMUserAuthored_ExplicitEntry_IsUserAuthored":                                    "pre-ratchet baseline — ad hoc test-side afero write, not yet migrated (task august-bonanza, test-fs-helper slice): route through testsupport.WriteFile/WriteFileString/SeedTree when this fixture is next touched",
	"internal/config/llm_authored_test.go#TestIsLLMUserAuthored_ExplicitOverrideOfADefaultName_IsUserAuthored":                   "pre-ratchet baseline — ad hoc test-side afero write, not yet migrated (task august-bonanza, test-fs-helper slice): route through testsupport.WriteFile/WriteFileString/SeedTree when this fixture is next touched",
	"internal/config/llm_authored_test.go#TestIsLLMUserAuthored_UnknownLabel_IsFalse":                                            "pre-ratchet baseline — ad hoc test-side afero write, not yet migrated (task august-bonanza, test-fs-helper slice): route through testsupport.WriteFile/WriteFileString/SeedTree when this fixture is next touched",
	"internal/config/override_schema_test.go#TestLoad_ConfigSetEnumTypoIsSchemaChecked":                                          "pre-ratchet baseline — ad hoc test-side afero write, not yet migrated (task august-bonanza, test-fs-helper slice): route through testsupport.WriteFile/WriteFileString/SeedTree when this fixture is next touched",
	"internal/config/override_schema_test.go#TestLoad_ConfigSetTypeGuessIsSchemaChecked":                                         "pre-ratchet baseline — ad hoc test-side afero write, not yet migrated (task august-bonanza, test-fs-helper slice): route through testsupport.WriteFile/WriteFileString/SeedTree when this fixture is next touched",
	"internal/config/override_schema_test.go#TestLoad_ValidOverridesWarnNothing":                                                 "pre-ratchet baseline — ad hoc test-side afero write, not yet migrated (task august-bonanza, test-fs-helper slice): route through testsupport.WriteFile/WriteFileString/SeedTree when this fixture is next touched",
	"internal/config/project_permissions_test.go#TestProjectPermissions_EnvCannotGrantIt":                                        "pre-ratchet baseline — ad hoc test-side afero write, not yet migrated (task august-bonanza, test-fs-helper slice): route through testsupport.WriteFile/WriteFileString/SeedTree when this fixture is next touched",
	"internal/config/tree_bundles_test.go#stageInstalledTree":                                                                    "pre-ratchet baseline — ad hoc test-side afero write, not yet migrated (task august-bonanza, test-fs-helper slice): route through testsupport.WriteFile/WriteFileString/SeedTree when this fixture is next touched",
	"internal/config/tree_bundles_test.go#stageLoaderFormTree":                                                                   "pre-ratchet baseline — ad hoc test-side afero write, not yet migrated (task august-bonanza, test-fs-helper slice): route through testsupport.WriteFile/WriteFileString/SeedTree when this fixture is next touched",
	"internal/config/tree_bundles_test.go#TestLoadTreeBundle_EditedAfterSigningIsWithheldNotDegradedToUnsigned":                  "pre-ratchet baseline — ad hoc test-side afero write, not yet migrated (task august-bonanza, test-fs-helper slice): route through testsupport.WriteFile/WriteFileString/SeedTree when this fixture is next touched",
	"internal/config/tree_bundles_test.go#TestLoadTreeBundle_FileAddedAfterSigningIsWithheld":                                    "pre-ratchet baseline — ad hoc test-side afero write, not yet migrated (task august-bonanza, test-fs-helper slice): route through testsupport.WriteFile/WriteFileString/SeedTree when this fixture is next touched",
	"internal/config/trustroot_suppression_test.go#TestTrustRoot_SuppressedEmbeddedPrincipal_NoLongerTrusted":                    "pre-ratchet baseline — ad hoc test-side afero write, not yet migrated (task august-bonanza, test-fs-helper slice): route through testsupport.WriteFile/WriteFileString/SeedTree when this fixture is next touched",
	"internal/config/trustroot_suppression_test.go#TestTrustRoot_UnreadableRevocationListDoesNotResurrectTrust":                  "pre-ratchet baseline — ad hoc test-side afero write, not yet migrated (task august-bonanza, test-fs-helper slice): route through testsupport.WriteFile/WriteFileString/SeedTree when this fixture is next touched",
	"internal/config/trustroot_test.go#TestSuppressedEmbeddedPrincipals_TruncatedFile_IsLoud":                                    "pre-ratchet baseline — ad hoc test-side afero write, not yet migrated (task august-bonanza, test-fs-helper slice): route through testsupport.WriteFile/WriteFileString/SeedTree when this fixture is next touched",
	"internal/config/trustroot_test.go#TestSuppressedEmbeddedPrincipals_UnreadableStore_IsLoud":                                  "pre-ratchet baseline — ad hoc test-side afero write, not yet migrated (task august-bonanza, test-fs-helper slice): route through testsupport.WriteFile/WriteFileString/SeedTree when this fixture is next touched",
	"internal/config/trustroot_test.go#TestTrustRootFilesystemResolution_NilFSFallsBackAndInjectedFSIsHonored":                   "pre-ratchet baseline — ad hoc test-side afero write, not yet migrated (task august-bonanza, test-fs-helper slice): route through testsupport.WriteFile/WriteFileString/SeedTree when this fixture is next touched",
	"internal/config/trustroot_test.go#TestTrustRoot_MalformedLineSkippedRestStillLoads":                                         "pre-ratchet baseline — ad hoc test-side afero write, not yet migrated (task august-bonanza, test-fs-helper slice): route through testsupport.WriteFile/WriteFileString/SeedTree when this fixture is next touched",
	"internal/config/trustroot_test.go#TestTrustRoot_MalformedLine_WarnsButIsNotATrustStoreFinding":                              "pre-ratchet baseline — ad hoc test-side afero write, not yet migrated (task august-bonanza, test-fs-helper slice): route through testsupport.WriteFile/WriteFileString/SeedTree when this fixture is next touched",
	"internal/config/trustroot_test.go#TestTrustRoot_NamespaceScopingIsEnforced":                                                 "pre-ratchet baseline — ad hoc test-side afero write, not yet migrated (task august-bonanza, test-fs-helper slice): route through testsupport.WriteFile/WriteFileString/SeedTree when this fixture is next touched",
	"internal/config/trustroot_test.go#TestTrustRoot_ProjectStoreIsParsedAndTrusted":                                             "pre-ratchet baseline — ad hoc test-side afero write, not yet migrated (task august-bonanza, test-fs-helper slice): route through testsupport.WriteFile/WriteFileString/SeedTree when this fixture is next touched",
	"internal/config/trustroot_test.go#TestTrustRoot_UnreadableStore_EscalatesViaStrictness":                                     "pre-ratchet baseline — ad hoc test-side afero write, not yet migrated (task august-bonanza, test-fs-helper slice): route through testsupport.WriteFile/WriteFileString/SeedTree when this fixture is next touched",
	"internal/config/trustroot_test.go#TestTrustRoot_UnreadableStore_IsRecordedNotErased":                                        "pre-ratchet baseline — ad hoc test-side afero write, not yet migrated (task august-bonanza, test-fs-helper slice): route through testsupport.WriteFile/WriteFileString/SeedTree when this fixture is next touched",
	"internal/config/unknown_keys_test.go#TestLoad_UnknownKeyInHomeLayer_StillWarns":                                             "pre-ratchet baseline — ad hoc test-side afero write, not yet migrated (task august-bonanza, test-fs-helper slice): route through testsupport.WriteFile/WriteFileString/SeedTree when this fixture is next touched",
	"internal/config/unknown_keys_test.go#TestLoad_UnknownKeyInProjectLayer_NotMaskedByValidHome":                                "pre-ratchet baseline — ad hoc test-side afero write, not yet migrated (task august-bonanza, test-fs-helper slice): route through testsupport.WriteFile/WriteFileString/SeedTree when this fixture is next touched",
	"internal/config/warnings_test.go#TestLoad_BrokenYAMLTaggedParse":                                                            "pre-ratchet baseline — ad hoc test-side afero write, not yet migrated (task august-bonanza, test-fs-helper slice): route through testsupport.WriteFile/WriteFileString/SeedTree when this fixture is next touched",
	"internal/config/warnings_test.go#TestLoad_UnreadableConfigTaggedRead":                                                       "pre-ratchet baseline — ad hoc test-side afero write, not yet migrated (task august-bonanza, test-fs-helper slice): route through testsupport.WriteFile/WriteFileString/SeedTree when this fixture is next touched",
	"internal/confpatch/confpatch_test.go#TestApplyPreservesForeignContent":                                                      "pre-ratchet baseline — ad hoc test-side afero write, not yet migrated (task august-bonanza, test-fs-helper slice): route through testsupport.WriteFile/WriteFileString/SeedTree when this fixture is next touched",
	"internal/confpatch/confpatch_test.go#TestApplyRecreatesATargetDeletedSinceTheRecordWasWritten":                              "pre-ratchet baseline — ad hoc test-side afero write, not yet migrated (task august-bonanza, test-fs-helper slice): route through testsupport.WriteFile/WriteFileString/SeedTree when this fixture is next touched",
	"internal/confpatch/confpatch_test.go#TestApplyWritesOneRecordCarryingAParseableReversal":                                    "pre-ratchet baseline — ad hoc test-side afero write, not yet migrated (task august-bonanza, test-fs-helper slice): route through testsupport.WriteFile/WriteFileString/SeedTree when this fixture is next touched",
	"internal/confpatch/confpatch_test.go#TestDriftRefusesAndLeavesTheTargetUntouched":                                           "pre-ratchet baseline — ad hoc test-side afero write, not yet migrated (task august-bonanza, test-fs-helper slice): route through testsupport.WriteFile/WriteFileString/SeedTree when this fixture is next touched",
	"internal/confpatch/confpatch_test.go#TestEmptyDesiredSetRemovesCtxloomAndRestoresTheUser":                                   "pre-ratchet baseline — ad hoc test-side afero write, not yet migrated (task august-bonanza, test-fs-helper slice): route through testsupport.WriteFile/WriteFileString/SeedTree when this fixture is next touched",
	"internal/confpatch/confpatch_test.go#TestForeignEditDoesNotRefuseAndSurvivesTheWrite":                                       "pre-ratchet baseline — ad hoc test-side afero write, not yet migrated (task august-bonanza, test-fs-helper slice): route through testsupport.WriteFile/WriteFileString/SeedTree when this fixture is next touched",
	"internal/confpatch/confpatch_test.go#TestRecordCarriesTheAddPolicy":                                                         "pre-ratchet baseline — ad hoc test-side afero write, not yet migrated (task august-bonanza, test-fs-helper slice): route through testsupport.WriteFile/WriteFileString/SeedTree when this fixture is next touched",
	"internal/confpatch/confpatch_test.go#TestRecordRoundTripsAnArrayValued":                                                     "pre-ratchet baseline — ad hoc test-side afero write, not yet migrated (task august-bonanza, test-fs-helper slice): route through testsupport.WriteFile/WriteFileString/SeedTree when this fixture is next touched",
	"internal/confpatch/confpatch_test.go#TestReversalRestoresTheUsersBytesExactly":                                              "pre-ratchet baseline — ad hoc test-side afero write, not yet migrated (task august-bonanza, test-fs-helper slice): route through testsupport.WriteFile/WriteFileString/SeedTree when this fixture is next touched",
	"internal/confpatch/confpatch_test.go#TestSecondApplyReversesTheFirst":                                                       "pre-ratchet baseline — ad hoc test-side afero write, not yet migrated (task august-bonanza, test-fs-helper slice): route through testsupport.WriteFile/WriteFileString/SeedTree when this fixture is next touched",
	"internal/confpatch/confpatch_test.go#TestTheStoredReversalAssertsOnlyCtxloomsOwnEntry":                                      "pre-ratchet baseline — ad hoc test-side afero write, not yet migrated (task august-bonanza, test-fs-helper slice): route through testsupport.WriteFile/WriteFileString/SeedTree when this fixture is next touched",
	"internal/confpatch/confpatch_test.go#TestUnchangedApplyWritesNoNewRecord":                                                   "pre-ratchet baseline — ad hoc test-side afero write, not yet migrated (task august-bonanza, test-fs-helper slice): route through testsupport.WriteFile/WriteFileString/SeedTree when this fixture is next touched",
	"internal/content/attest/attest_test.go#fixture":                                                                             "pre-ratchet baseline — ad hoc test-side afero write, not yet migrated (task august-bonanza, test-fs-helper slice): route through testsupport.WriteFile/WriteFileString/SeedTree when this fixture is next touched",
	"internal/content/attest/attest_test.go#TestVerifyBundle_CorruptSignatureBlobIsTampered":                                     "pre-ratchet baseline — ad hoc test-side afero write, not yet migrated (task august-bonanza, test-fs-helper slice): route through testsupport.WriteFile/WriteFileString/SeedTree when this fixture is next touched",
	"internal/content/attest/attest_test.go#write":                                                                               "pre-ratchet baseline — ad hoc test-side afero write, not yet migrated (task august-bonanza, test-fs-helper slice): route through testsupport.WriteFile/WriteFileString/SeedTree when this fixture is next touched",
	"internal/content/convert/skillfiles_test.go#authoredSkillBundle":                                                            "pre-ratchet baseline — ad hoc test-side afero write, not yet migrated (task august-bonanza, test-fs-helper slice): route through testsupport.WriteFile/WriteFileString/SeedTree when this fixture is next touched",
	"internal/content/convert/skillfiles_test.go#TestSkillFilesFromDir_HonoursAnExplicitPath":                                    "pre-ratchet baseline — ad hoc test-side afero write, not yet migrated (task august-bonanza, test-fs-helper slice): route through testsupport.WriteFile/WriteFileString/SeedTree when this fixture is next touched",
	"internal/content/helpers_test.go#copyTreeInto":                                                                              "pre-ratchet baseline — ad hoc test-side afero write, not yet migrated (task august-bonanza, test-fs-helper slice): route through testsupport.WriteFile/WriteFileString/SeedTree when this fixture is next touched",
	"internal/content/helpers_test.go#writeFile":                                                                                 "pre-ratchet baseline — ad hoc test-side afero write, not yet migrated (task august-bonanza, test-fs-helper slice): route through testsupport.WriteFile/WriteFileString/SeedTree when this fixture is next touched",
	"internal/content/registry_ext_test.go#TestRegistryExtension_ThirdPartyKindWorksThroughPublicAPI":                            "pre-ratchet baseline — ad hoc test-side afero write, not yet migrated (task august-bonanza, test-fs-helper slice): route through testsupport.WriteFile/WriteFileString/SeedTree when this fixture is next touched",
	"internal/lm/backends/mock_surfaces_full_test.go#TestMockSettingsSurface_PreservesKeysCtxloomDoesNotOwn":                     "pre-ratchet baseline — ad hoc test-side afero write, not yet migrated (task august-bonanza, test-fs-helper slice): route through testsupport.WriteFile/WriteFileString/SeedTree when this fixture is next touched",
	"internal/lm/backends/mock_surfaces_test.go#TestMockContextSurface_Deliver_PreservesUserContentOutsideMarkers":               "pre-ratchet baseline — ad hoc test-side afero write, not yet migrated (task august-bonanza, test-fs-helper slice): route through testsupport.WriteFile/WriteFileString/SeedTree when this fixture is next touched",
	"internal/lm/backends/mock_surfaces_test.go#TestMockContextSurface_State_IgnoresUserContentOutsideMarkersForCurrency":        "pre-ratchet baseline — ad hoc test-side afero write, not yet migrated (task august-bonanza, test-fs-helper slice): route through testsupport.WriteFile/WriteFileString/SeedTree when this fixture is next touched",
	"internal/lm/backends/mock_surfaces_test.go#TestMockContextSurface_State_ReportsMissing_WhenFileExistsWithoutManagedSection": "pre-ratchet baseline — ad hoc test-side afero write, not yet migrated (task august-bonanza, test-fs-helper slice): route through testsupport.WriteFile/WriteFileString/SeedTree when this fixture is next touched",
	"internal/lm/backends/mock_surfaces_test.go#TestMockSkillsSurface_Cleanup_LeavesUserAuthoredFilesAlone":                      "pre-ratchet baseline — ad hoc test-side afero write, not yet migrated (task august-bonanza, test-fs-helper slice): route through testsupport.WriteFile/WriteFileString/SeedTree when this fixture is next touched",
	"internal/lm/backends/mock_surfaces_test.go#TestMockSkillsSurface_Deliver_DeclaredModeBeatsAnExistingFilesMode":              "pre-ratchet baseline — ad hoc test-side afero write, not yet migrated (task august-bonanza, test-fs-helper slice): route through testsupport.WriteFile/WriteFileString/SeedTree when this fixture is next touched",
	"internal/lm/backends/statusline_test.go#TestManagedSettings_StatusLineDisabled_PreservesUserStatusline":                     "pre-ratchet baseline — ad hoc test-side afero write, not yet migrated (task august-bonanza, test-fs-helper slice): route through testsupport.WriteFile/WriteFileString/SeedTree when this fixture is next touched",
	"internal/lm/backends/uninstall_test.go#TestClaudeCodeRemoveSettings_StripsManagedPreservesUser":                             "pre-ratchet baseline — ad hoc test-side afero write, not yet migrated (task august-bonanza, test-fs-helper slice): route through testsupport.WriteFile/WriteFileString/SeedTree when this fixture is next touched",
	"internal/lm/backends/uninstall_test.go#TestRemoveSettings_FailureNamesBackend":                                              "pre-ratchet baseline — ad hoc test-side afero write, not yet migrated (task august-bonanza, test-fs-helper slice): route through testsupport.WriteFile/WriteFileString/SeedTree when this fixture is next touched",
	"internal/lm/conformance/conformance_test.go#recordingFs.Create":                                                             "pre-ratchet baseline — ad hoc test-side afero write, not yet migrated (task august-bonanza, test-fs-helper slice): route through testsupport.WriteFile/WriteFileString/SeedTree when this fixture is next touched",
	"internal/lm/conformance/conformance_test.go#recordingFs.Rename":                                                             "pre-ratchet baseline — ad hoc test-side afero write, not yet migrated (task august-bonanza, test-fs-helper slice): route through testsupport.WriteFile/WriteFileString/SeedTree when this fixture is next touched",
	"internal/lm/conformance/conformance_test.go#TestConformance_AtomicWriteLeavesNoBackup":                                      "pre-ratchet baseline — ad hoc test-side afero write, not yet migrated (task august-bonanza, test-fs-helper slice): route through testsupport.WriteFile/WriteFileString/SeedTree when this fixture is next touched",
	"internal/lm/conformance/conformance_test.go#TestConformance_RefusesToOverwriteUnparseableSettings":                          "pre-ratchet baseline — ad hoc test-side afero write, not yet migrated (task august-bonanza, test-fs-helper slice): route through testsupport.WriteFile/WriteFileString/SeedTree when this fixture is next touched",
	"internal/lm/conformance/conformance_test.go#TestConformance_RemovePreservesUser":                                            "pre-ratchet baseline — ad hoc test-side afero write, not yet migrated (task august-bonanza, test-fs-helper slice): route through testsupport.WriteFile/WriteFileString/SeedTree when this fixture is next touched",
	"internal/ltk/scm/submodules_parse_test.go#TestSubmodulePaths_EmptyStartDirIsAnError":                                        "pre-ratchet baseline — ad hoc test-side afero write, not yet migrated (task august-bonanza, test-fs-helper slice): route through testsupport.WriteFile/WriteFileString/SeedTree when this fixture is next touched",
	"internal/ltk/scm/submodules_parse_test.go#TestSubmodulePaths_StrayGitFileIsNotARepoRoot":                                    "pre-ratchet baseline — ad hoc test-side afero write, not yet migrated (task august-bonanza, test-fs-helper slice): route through testsupport.WriteFile/WriteFileString/SeedTree when this fixture is next touched",
	"internal/ltk/scm/submodules_silentnoop_test.go#TestSubmodulePaths_AbsentIsQuiet":                                            "pre-ratchet baseline — ad hoc test-side afero write, not yet migrated (task august-bonanza, test-fs-helper slice): route through testsupport.WriteFile/WriteFileString/SeedTree when this fixture is next touched",
	"internal/ltk/scm/submodules_silentnoop_test.go#TestSubmodulePaths_UnreadableIsNotAbsent":                                    "pre-ratchet baseline — ad hoc test-side afero write, not yet migrated (task august-bonanza, test-fs-helper slice): route through testsupport.WriteFile/WriteFileString/SeedTree when this fixture is next touched",
	"internal/ltk/scm/submodules_test.go#TestSubmodulePaths":                                                                     "pre-ratchet baseline — ad hoc test-side afero write, not yet migrated (task august-bonanza, test-fs-helper slice): route through testsupport.WriteFile/WriteFileString/SeedTree when this fixture is next touched",
	"internal/ltk/scm/submodules_test.go#TestSubmodulePathsStopAtRepositoryRoot":                                                 "pre-ratchet baseline — ad hoc test-side afero write, not yet migrated (task august-bonanza, test-fs-helper slice): route through testsupport.WriteFile/WriteFileString/SeedTree when this fixture is next touched",
	"internal/ltk/scm/submodules_test.go#TestSubmodulePathsWalksUp":                                                              "pre-ratchet baseline — ad hoc test-side afero write, not yet migrated (task august-bonanza, test-fs-helper slice): route through testsupport.WriteFile/WriteFileString/SeedTree when this fixture is next touched",
	"internal/ltk/state/confirm_test.go#TestConfirmByRepeatReportsAnUndecodableStateFile":                                        "pre-ratchet baseline — ad hoc test-side afero write, not yet migrated (task august-bonanza, test-fs-helper slice): route through testsupport.WriteFile/WriteFileString/SeedTree when this fixture is next touched",
	"internal/ltk/state/confirm_test.go#TestConfirmByRepeatTooEarlyDoesNotWrite":                                                 "pre-ratchet baseline — ad hoc test-side afero write, not yet migrated (task august-bonanza, test-fs-helper slice): route through testsupport.WriteFile/WriteFileString/SeedTree when this fixture is next touched",
	"internal/operations/attestation_form_test.go#writeSupersededApprove":                                                        "pre-ratchet baseline — ad hoc test-side afero write, not yet migrated (task august-bonanza, test-fs-helper slice): route through testsupport.WriteFile/WriteFileString/SeedTree when this fixture is next touched",
	"internal/operations/bundle_content_layout_test.go#TestListLocalBundleNames_FindsContentTreeBundles":                         "pre-ratchet baseline — ad hoc test-side afero write, not yet migrated (task august-bonanza, test-fs-helper slice): route through testsupport.WriteFile/WriteFileString/SeedTree when this fixture is next touched",
	"internal/operations/bundle_move_test.go#failWriteFs.Create":                                                                 "pre-ratchet baseline — ad hoc test-side afero write, not yet migrated (task august-bonanza, test-fs-helper slice): route through testsupport.WriteFile/WriteFileString/SeedTree when this fixture is next touched",
	"internal/operations/bundle_move_test.go#failWriteFs.Rename":                                                                 "pre-ratchet baseline — ad hoc test-side afero write, not yet migrated (task august-bonanza, test-fs-helper slice): route through testsupport.WriteFile/WriteFileString/SeedTree when this fixture is next touched",
	"internal/operations/bundle_move_test.go#memMoveDirFS":                                                                       "pre-ratchet baseline — ad hoc test-side afero write, not yet migrated (task august-bonanza, test-fs-helper slice): route through testsupport.WriteFile/WriteFileString/SeedTree when this fixture is next touched",
	"internal/operations/bundle_move_test.go#memMoveFS":                                                                          "pre-ratchet baseline — ad hoc test-side afero write, not yet migrated (task august-bonanza, test-fs-helper slice): route through testsupport.WriteFile/WriteFileString/SeedTree when this fixture is next touched",
	"internal/operations/bundle_move_test.go#TestMoveBundle_DirectoryFormWithNoPayloadBesideTheManifest_StillMoves":              "pre-ratchet baseline — ad hoc test-side afero write, not yet migrated (task august-bonanza, test-fs-helper slice): route through testsupport.WriteFile/WriteFileString/SeedTree when this fixture is next touched",
	"internal/operations/bundle_move_test.go#TestResolveMoveDest_RemoteNameWinsOverSamePath":                                     "pre-ratchet baseline — ad hoc test-side afero write, not yet migrated (task august-bonanza, test-fs-helper slice): route through testsupport.WriteFile/WriteFileString/SeedTree when this fixture is next touched",
	"internal/operations/bundle_transfer_test.go#memBundleFS":                                                                    "pre-ratchet baseline — ad hoc test-side afero write, not yet migrated (task august-bonanza, test-fs-helper slice): route through testsupport.WriteFile/WriteFileString/SeedTree when this fixture is next touched",
	"internal/operations/bundle_transfer_test.go#TestImportBundle_CarriesDetachedSignature":                                      "pre-ratchet baseline — ad hoc test-side afero write, not yet migrated (task august-bonanza, test-fs-helper slice): route through testsupport.WriteFile/WriteFileString/SeedTree when this fixture is next touched",
	"internal/operations/bundle_transfer_test.go#TestImportBundle_InvalidFile":                                                   "pre-ratchet baseline — ad hoc test-side afero write, not yet migrated (task august-bonanza, test-fs-helper slice): route through testsupport.WriteFile/WriteFileString/SeedTree when this fixture is next touched",
	"internal/operations/bundle_transfer_test.go#TestImportBundle_RoundTrip":                                                     "pre-ratchet baseline — ad hoc test-side afero write, not yet migrated (task august-bonanza, test-fs-helper slice): route through testsupport.WriteFile/WriteFileString/SeedTree when this fixture is next touched",
	"internal/operations/bundle_transfer_test.go#TestImportBundle_WritesToCommittedContentTree":                                  "pre-ratchet baseline — ad hoc test-side afero write, not yet migrated (task august-bonanza, test-fs-helper slice): route through testsupport.WriteFile/WriteFileString/SeedTree when this fixture is next touched",
	"internal/operations/commands_test.go#setupPromptTestFS":                                                                     "pre-ratchet baseline — ad hoc test-side afero write, not yet migrated (task august-bonanza, test-fs-helper slice): route through testsupport.WriteFile/WriteFileString/SeedTree when this fixture is next touched",
	"internal/operations/context_test.go#setupContextTestFS":                                                                     "pre-ratchet baseline — ad hoc test-side afero write, not yet migrated (task august-bonanza, test-fs-helper slice): route through testsupport.WriteFile/WriteFileString/SeedTree when this fixture is next touched",
	"internal/operations/context_test.go#TestAssembleContext_TemplateParseFailureWarnsAndReturnsContentUnchanged":                "pre-ratchet baseline — ad hoc test-side afero write, not yet migrated (task august-bonanza, test-fs-helper slice): route through testsupport.WriteFile/WriteFileString/SeedTree when this fixture is next touched",
	"internal/operations/context_test.go#TestAssembleContext_UndefinedVariableWarningDedupesAcrossRepeatedCalls":                 "pre-ratchet baseline — ad hoc test-side afero write, not yet migrated (task august-bonanza, test-fs-helper slice): route through testsupport.WriteFile/WriteFileString/SeedTree when this fixture is next touched",
	"internal/operations/context_test.go#TestAssembleContext_UndefinedVariableWarningNamesFragment":                              "pre-ratchet baseline — ad hoc test-side afero write, not yet migrated (task august-bonanza, test-fs-helper slice): route through testsupport.WriteFile/WriteFileString/SeedTree when this fixture is next touched",
	"internal/operations/countersign_two_store_test.go#TestCountersignRecords_UnreadableProjectStore_FailsEvenWithAReadableUser": "pre-ratchet baseline — ad hoc test-side afero write, not yet migrated (task august-bonanza, test-fs-helper slice): route through testsupport.WriteFile/WriteFileString/SeedTree when this fixture is next touched",
	"internal/operations/exposure_withheld_characterization_test.go#realExposureProject":                                         "pre-ratchet baseline — ad hoc test-side afero write, not yet migrated (task august-bonanza, test-fs-helper slice): route through testsupport.WriteFile/WriteFileString/SeedTree when this fixture is next touched",
	"internal/operations/fragments_test.go#setupBundleTestFS":                                                                    "pre-ratchet baseline — ad hoc test-side afero write, not yet migrated (task august-bonanza, test-fs-helper slice): route through testsupport.WriteFile/WriteFileString/SeedTree when this fixture is next touched",
	"internal/operations/hooks_dryrun_test.go#TestApplyHooksDryRunLeavesExistingSettingsByteIdentical":                           "pre-ratchet baseline — ad hoc test-side afero write, not yet migrated (task august-bonanza, test-fs-helper slice): route through testsupport.WriteFile/WriteFileString/SeedTree when this fixture is next touched",
	"internal/operations/hooks_fixture_test.go#cfgWithProfileHooks":                                                              "pre-ratchet baseline — ad hoc test-side afero write, not yet migrated (task august-bonanza, test-fs-helper slice): route through testsupport.WriteFile/WriteFileString/SeedTree when this fixture is next touched",
	"internal/operations/hooks_regen_test.go#TestUpdateProfile_ValidationFailureIsRejected":                                      "pre-ratchet baseline — ad hoc test-side afero write, not yet migrated (task august-bonanza, test-fs-helper slice): route through testsupport.WriteFile/WriteFileString/SeedTree when this fixture is next touched",
	"internal/operations/hooks_test.go#TestManagedSettings_PreservesExistingSettings":                                            "pre-ratchet baseline — ad hoc test-side afero write, not yet migrated (task august-bonanza, test-fs-helper slice): route through testsupport.WriteFile/WriteFileString/SeedTree when this fixture is next touched",
	"internal/operations/ingest_test.go#writeIngestBundle":                                                                       "pre-ratchet baseline — ad hoc test-side afero write, not yet migrated (task august-bonanza, test-fs-helper slice): route through testsupport.WriteFile/WriteFileString/SeedTree when this fixture is next touched",
	"internal/operations/init_test.go#TestScaffoldSeedProfile_WriteIfAbsent":                                                     "pre-ratchet baseline — ad hoc test-side afero write, not yet migrated (task august-bonanza, test-fs-helper slice): route through testsupport.WriteFile/WriteFileString/SeedTree when this fixture is next touched",
	"internal/operations/profiles_loader_defects_test.go#TestCreateProfile_DoesNotClobberAnUnparseableProfile":                   "pre-ratchet baseline — ad hoc test-side afero write, not yet migrated (task august-bonanza, test-fs-helper slice): route through testsupport.WriteFile/WriteFileString/SeedTree when this fixture is next touched",
	"internal/operations/profiles_loader_defects_test.go#TestProfileLoader_HonoursTheInjectedFilesystem":                         "pre-ratchet baseline — ad hoc test-side afero write, not yet migrated (task august-bonanza, test-fs-helper slice): route through testsupport.WriteFile/WriteFileString/SeedTree when this fixture is next touched",
	"internal/operations/profiles_port_test.go#TestProfileOperations_ListCreateDelete_RoundTrip":                                 "pre-ratchet baseline — ad hoc test-side afero write, not yet migrated (task august-bonanza, test-fs-helper slice): route through testsupport.WriteFile/WriteFileString/SeedTree when this fixture is next touched",
	"internal/operations/profiles_test.go#setupProfileTestFS":                                                                    "pre-ratchet baseline — ad hoc test-side afero write, not yet migrated (task august-bonanza, test-fs-helper slice): route through testsupport.WriteFile/WriteFileString/SeedTree when this fixture is next touched",
	"internal/operations/profiles_test.go#TestProfileLoader_HonoursTheInjectedFSLikeItsTwin":                                     "pre-ratchet baseline — ad hoc test-side afero write, not yet migrated (task august-bonanza, test-fs-helper slice): route through testsupport.WriteFile/WriteFileString/SeedTree when this fixture is next touched",
	"internal/operations/profile_transfer_empty_test.go#profileTransferFixture":                                                  "pre-ratchet baseline — ad hoc test-side afero write, not yet migrated (task august-bonanza, test-fs-helper slice): route through testsupport.WriteFile/WriteFileString/SeedTree when this fixture is next touched",
	"internal/operations/profile_transfer_empty_test.go#TestImportProfile_EmptyFileIsRejected":                                   "pre-ratchet baseline — ad hoc test-side afero write, not yet migrated (task august-bonanza, test-fs-helper slice): route through testsupport.WriteFile/WriteFileString/SeedTree when this fixture is next touched",
	"internal/operations/profile_transfer_empty_test.go#TestProfileTransfer_RealProfileStillRoundTrips":                          "pre-ratchet baseline — ad hoc test-side afero write, not yet migrated (task august-bonanza, test-fs-helper slice): route through testsupport.WriteFile/WriteFileString/SeedTree when this fixture is next touched",
	"internal/operations/profile_transfer_test.go#memProfileFS":                                                                  "pre-ratchet baseline — ad hoc test-side afero write, not yet migrated (task august-bonanza, test-fs-helper slice): route through testsupport.WriteFile/WriteFileString/SeedTree when this fixture is next touched",
	"internal/operations/profile_transfer_test.go#TestImportProfile":                                                             "pre-ratchet baseline — ad hoc test-side afero write, not yet migrated (task august-bonanza, test-fs-helper slice): route through testsupport.WriteFile/WriteFileString/SeedTree when this fixture is next touched",
	"internal/operations/remote_ref_predicate_test.go#loaderWith":                                                                "pre-ratchet baseline — ad hoc test-side afero write, not yet migrated (task august-bonanza, test-fs-helper slice): route through testsupport.WriteFile/WriteFileString/SeedTree when this fixture is next touched",
	"internal/operations/remotes_test.go#TestListRemotes_WithFS":                                                                 "pre-ratchet baseline — ad hoc test-side afero write, not yet migrated (task august-bonanza, test-fs-helper slice): route through testsupport.WriteFile/WriteFileString/SeedTree when this fixture is next touched",
	"internal/operations/remotes_test.go#TestRemoveRemote_WithFS":                                                                "pre-ratchet baseline — ad hoc test-side afero write, not yet migrated (task august-bonanza, test-fs-helper slice): route through testsupport.WriteFile/WriteFileString/SeedTree when this fixture is next touched",
	"internal/operations/review_snapshots_migration_test.go#renameFailsFs.Rename":                                                "pre-ratchet baseline — ad hoc test-side afero write, not yet migrated (task august-bonanza, test-fs-helper slice): route through testsupport.WriteFile/WriteFileString/SeedTree when this fixture is next touched",
	"internal/operations/review_snapshots_migration_test.go#TestTrustSnapshots_BothStoresExistMovesNothing":                      "pre-ratchet baseline — ad hoc test-side afero write, not yet migrated (task august-bonanza, test-fs-helper slice): route through testsupport.WriteFile/WriteFileString/SeedTree when this fixture is next touched",
	"internal/operations/review_snapshots_migration_test.go#TestTrustSnapshots_CrossDeviceMoveCopiesThenRemoves":                 "pre-ratchet baseline — ad hoc test-side afero write, not yet migrated (task august-bonanza, test-fs-helper slice): route through testsupport.WriteFile/WriteFileString/SeedTree when this fixture is next touched",
	"internal/operations/review_snapshots_migration_test.go#TestTrustSnapshots_LegacyStoreMigratesEveryByte":                     "pre-ratchet baseline — ad hoc test-side afero write, not yet migrated (task august-bonanza, test-fs-helper slice): route through testsupport.WriteFile/WriteFileString/SeedTree when this fixture is next touched",
	"internal/operations/search_test.go#setupSearchTestFS":                                                                       "pre-ratchet baseline — ad hoc test-side afero write, not yet migrated (task august-bonanza, test-fs-helper slice): route through testsupport.WriteFile/WriteFileString/SeedTree when this fixture is next touched",
	"internal/operations/search_test.go#TestSearchContent_SearchSkills":                                                          "pre-ratchet baseline — ad hoc test-side afero write, not yet migrated (task august-bonanza, test-fs-helper slice): route through testsupport.WriteFile/WriteFileString/SeedTree when this fixture is next touched",
	"internal/operations/signer_test.go#TestRemoveFromAllowedSignersFile_UsesDurableWrite":                                       "pre-ratchet baseline — ad hoc test-side afero write, not yet migrated (task august-bonanza, test-fs-helper slice): route through testsupport.WriteFile/WriteFileString/SeedTree when this fixture is next touched",
	"internal/operations/signer_test.go#TestRemoveFromAllowedSignersFile_WarnsNamingTheRegisteredVerb":                           "pre-ratchet baseline — ad hoc test-side afero write, not yet migrated (task august-bonanza, test-fs-helper slice): route through testsupport.WriteFile/WriteFileString/SeedTree when this fixture is next touched",
	"internal/operations/signer_test.go#TestResolveSignerKey_FromFile":                                                           "pre-ratchet baseline — ad hoc test-side afero write, not yet migrated (task august-bonanza, test-fs-helper slice): route through testsupport.WriteFile/WriteFileString/SeedTree when this fixture is next touched",
	"internal/operations/signer_test.go#writeAllowedSignersLines":                                                                "pre-ratchet baseline — ad hoc test-side afero write, not yet migrated (task august-bonanza, test-fs-helper slice): route through testsupport.WriteFile/WriteFileString/SeedTree when this fixture is next touched",
	"internal/operations/sign_test.go#TestListLocalBundleNames_MatchesTheLoadersEnumeration":                                     "pre-ratchet baseline — ad hoc test-side afero write, not yet migrated (task august-bonanza, test-fs-helper slice): route through testsupport.WriteFile/WriteFileString/SeedTree when this fixture is next touched",
	"internal/operations/sign_test.go#TestSignBundleFile_RefusesAZeroByteBundle":                                                 "pre-ratchet baseline — ad hoc test-side afero write, not yet migrated (task august-bonanza, test-fs-helper slice): route through testsupport.WriteFile/WriteFileString/SeedTree when this fixture is next touched",
	"internal/operations/stale_signature_test.go#signOnDisk":                                                                     "pre-ratchet baseline — ad hoc test-side afero write, not yet migrated (task august-bonanza, test-fs-helper slice): route through testsupport.WriteFile/WriteFileString/SeedTree when this fixture is next touched",
	"internal/operations/stale_signature_test.go#TestExportBundle_StaleSignature_Refuses":                                        "pre-ratchet baseline — ad hoc test-side afero write, not yet migrated (task august-bonanza, test-fs-helper slice): route through testsupport.WriteFile/WriteFileString/SeedTree when this fixture is next touched",
	"internal/operations/stale_signature_test.go#TestMoveBundle_ToLocalPath_StaleSignature_RefusesAndKeepsSource":                "pre-ratchet baseline — ad hoc test-side afero write, not yet migrated (task august-bonanza, test-fs-helper slice): route through testsupport.WriteFile/WriteFileString/SeedTree when this fixture is next touched",
	"internal/operations/sync_test.go#TestSyncDependencies_ForceRedownload":                                                      "pre-ratchet baseline — ad hoc test-side afero write, not yet migrated (task august-bonanza, test-fs-helper slice): route through testsupport.WriteFile/WriteFileString/SeedTree when this fixture is next touched",
	"internal/operations/sync_test.go#TestSyncDependencies_NotRetractedInstalledRef":                                             "pre-ratchet baseline — ad hoc test-side afero write, not yet migrated (task august-bonanza, test-fs-helper slice): route through testsupport.WriteFile/WriteFileString/SeedTree when this fixture is next touched",
	"internal/operations/sync_test.go#TestSyncDependencies_NoUnconvergedWarningWhenLastPassConverges":                            "pre-ratchet baseline — ad hoc test-side afero write, not yet migrated (task august-bonanza, test-fs-helper slice): route through testsupport.WriteFile/WriteFileString/SeedTree when this fixture is next touched",
	"internal/operations/sync_test.go#TestSyncDependencies_PullError":                                                            "pre-ratchet baseline — ad hoc test-side afero write, not yet migrated (task august-bonanza, test-fs-helper slice): route through testsupport.WriteFile/WriteFileString/SeedTree when this fixture is next touched",
	"internal/operations/sync_test.go#TestSyncDependencies_PullOutputAvoidsStdout":                                               "pre-ratchet baseline — ad hoc test-side afero write, not yet migrated (task august-bonanza, test-fs-helper slice): route through testsupport.WriteFile/WriteFileString/SeedTree when this fixture is next touched",
	"internal/operations/sync_test.go#TestSyncDependencies_PullsRefsRevealedByEarlierPulls":                                      "pre-ratchet baseline — ad hoc test-side afero write, not yet migrated (task august-bonanza, test-fs-helper slice): route through testsupport.WriteFile/WriteFileString/SeedTree when this fixture is next touched",
	"internal/operations/sync_test.go#TestSyncDependencies_RecordRetractionSaveFailureIsWarnedNotSwallowed":                      "pre-ratchet baseline — ad hoc test-side afero write, not yet migrated (task august-bonanza, test-fs-helper slice): route through testsupport.WriteFile/WriteFileString/SeedTree when this fixture is next touched",
	"internal/operations/sync_test.go#TestSyncDependencies_RetractedInstalledRef":                                                "pre-ratchet baseline — ad hoc test-side afero write, not yet migrated (task august-bonanza, test-fs-helper slice): route through testsupport.WriteFile/WriteFileString/SeedTree when this fixture is next touched",
	"internal/operations/sync_test.go#TestSyncDependencies_SkipCanonicalizesRef":                                                 "pre-ratchet baseline — ad hoc test-side afero write, not yet migrated (task august-bonanza, test-fs-helper slice): route through testsupport.WriteFile/WriteFileString/SeedTree when this fixture is next touched",
	"internal/operations/sync_test.go#TestSyncDependencies_SkipsExisting":                                                        "pre-ratchet baseline — ad hoc test-side afero write, not yet migrated (task august-bonanza, test-fs-helper slice): route through testsupport.WriteFile/WriteFileString/SeedTree when this fixture is next touched",
	"internal/operations/sync_test.go#TestSyncDependencies_UnreachableRemoteHonorsFallbackVerdict":                               "pre-ratchet baseline — ad hoc test-side afero write, not yet migrated (task august-bonanza, test-fs-helper slice): route through testsupport.WriteFile/WriteFileString/SeedTree when this fixture is next touched",
	"internal/operations/sync_test.go#TestSyncDependencies_UpdatedStatus":                                                        "pre-ratchet baseline — ad hoc test-side afero write, not yet migrated (task august-bonanza, test-fs-helper slice): route through testsupport.WriteFile/WriteFileString/SeedTree when this fixture is next touched",
	"internal/operations/sync_test.go#TestSyncDependencies_WithRemotes":                                                          "pre-ratchet baseline — ad hoc test-side afero write, not yet migrated (task august-bonanza, test-fs-helper slice): route through testsupport.WriteFile/WriteFileString/SeedTree when this fixture is next touched",
	"internal/operations/trust_approvals_readable_test.go#TestEffectiveTrust_CorruptedRejectSignature_StaysDenied":               "pre-ratchet baseline — ad hoc test-side afero write, not yet migrated (task august-bonanza, test-fs-helper slice): route through testsupport.WriteFile/WriteFileString/SeedTree when this fixture is next touched",
	"internal/operations/trust_approvals_readable_test.go#TestEffectiveTrust_ProductionInjectedRecords_CorruptedStore_DenyAll":   "pre-ratchet baseline — ad hoc test-side afero write, not yet migrated (task august-bonanza, test-fs-helper slice): route through testsupport.WriteFile/WriteFileString/SeedTree when this fixture is next touched",
	"internal/operations/trust_gate_test.go#TestExposureGate_FullPath_LocalAllowsAndRejectionWithholds":                          "pre-ratchet baseline — ad hoc test-side afero write, not yet migrated (task august-bonanza, test-fs-helper slice): route through testsupport.WriteFile/WriteFileString/SeedTree when this fixture is next touched",
	"internal/operations/u081_findings_test.go#TestImportBundle_DoesNotDestroyOnRejection":                                       "pre-ratchet baseline — ad hoc test-side afero write, not yet migrated (task august-bonanza, test-fs-helper slice): route through testsupport.WriteFile/WriteFileString/SeedTree when this fixture is next touched",
	"internal/operations/u081_findings_test.go#TestImportBundle_RejectsEmptyBundle":                                              "pre-ratchet baseline — ad hoc test-side afero write, not yet migrated (task august-bonanza, test-fs-helper slice): route through testsupport.WriteFile/WriteFileString/SeedTree when this fixture is next touched",
	"internal/operations/u081_findings_test.go#TestImportBundle_UnreadableSignatureFailsLoudly":                                  "pre-ratchet baseline — ad hoc test-side afero write, not yet migrated (task august-bonanza, test-fs-helper slice): route through testsupport.WriteFile/WriteFileString/SeedTree when this fixture is next touched",
	"internal/operations/u081_findings_test.go#TestImport_RejectsNameTheLoaderCannotFind":                                        "pre-ratchet baseline — ad hoc test-side afero write, not yet migrated (task august-bonanza, test-fs-helper slice): route through testsupport.WriteFile/WriteFileString/SeedTree when this fixture is next touched",
	"internal/operations/u081_findings_test.go#TestReadSignature_AbsentIsNotAnError":                                             "pre-ratchet baseline — ad hoc test-side afero write, not yet migrated (task august-bonanza, test-fs-helper slice): route through testsupport.WriteFile/WriteFileString/SeedTree when this fixture is next touched",
	"internal/operations/u081_findings_test.go#TestReadSignature_StatErrorIsLoud":                                                "pre-ratchet baseline — ad hoc test-side afero write, not yet migrated (task august-bonanza, test-fs-helper slice): route through testsupport.WriteFile/WriteFileString/SeedTree when this fixture is next touched",
	"internal/operations/u081_findings_test.go#TestRemoveLocalItems_DoesNotRemoveOutsideCache":                                   "pre-ratchet baseline — ad hoc test-side afero write, not yet migrated (task august-bonanza, test-fs-helper slice): route through testsupport.WriteFile/WriteFileString/SeedTree when this fixture is next touched",
	"internal/operations/u081_w74_test.go#w74ShortNameFS":                                                                        "pre-ratchet baseline — ad hoc test-side afero write, not yet migrated (task august-bonanza, test-fs-helper slice): route through testsupport.WriteFile/WriteFileString/SeedTree when this fixture is next touched",
	"internal/operations/u082_w75_test.go#setupHeadingTestFS":                                                                    "pre-ratchet baseline — ad hoc test-side afero write, not yet migrated (task august-bonanza, test-fs-helper slice): route through testsupport.WriteFile/WriteFileString/SeedTree when this fixture is next touched",
	"internal/operations/u085_profiles_test.go#TestProfileLoaderFactories_AgreeUnderInjectedFS":                                  "pre-ratchet baseline — ad hoc test-side afero write, not yet migrated (task august-bonanza, test-fs-helper slice): route through testsupport.WriteFile/WriteFileString/SeedTree when this fixture is next touched",
	"internal/operations/withheld_reason_test.go#TestAssembleContext_WarnWithheld_NamesReason_FullPath":                          "pre-ratchet baseline — ad hoc test-side afero write, not yet migrated (task august-bonanza, test-fs-helper slice): route through testsupport.WriteFile/WriteFileString/SeedTree when this fixture is next touched",
	"internal/profiles/empty_profile_test.go#TestLoad_EmptyProfileFileIsReported":                                                "pre-ratchet baseline — ad hoc test-side afero write, not yet migrated (task august-bonanza, test-fs-helper slice): route through testsupport.WriteFile/WriteFileString/SeedTree when this fixture is next touched",
	"internal/profiles/grammar_test.go#TestResolveProfile_AliasParent":                                                           "pre-ratchet baseline — ad hoc test-side afero write, not yet migrated (task august-bonanza, test-fs-helper slice): route through testsupport.WriteFile/WriteFileString/SeedTree when this fixture is next touched",
	"internal/profiles/loader_diagnostics_test.go#TestCommitUpgrade_RefusesNothingToWrite":                                       "pre-ratchet baseline — ad hoc test-side afero write, not yet migrated (task august-bonanza, test-fs-helper slice): route through testsupport.WriteFile/WriteFileString/SeedTree when this fixture is next touched",
	"internal/profiles/loader_diagnostics_test.go#TestExists_ReportsPresenceNotLoadability":                                      "pre-ratchet baseline — ad hoc test-side afero write, not yet migrated (task august-bonanza, test-fs-helper slice): route through testsupport.WriteFile/WriteFileString/SeedTree when this fixture is next touched",
	"internal/profiles/loader_diagnostics_test.go#TestList_NamesAreDirRelativeAndNeverEmpty":                                     "pre-ratchet baseline — ad hoc test-side afero write, not yet migrated (task august-bonanza, test-fs-helper slice): route through testsupport.WriteFile/WriteFileString/SeedTree when this fixture is next touched",
	"internal/profiles/loader_diagnostics_test.go#TestList_WarnsWhenASubdirectoryCannotBeWalked":                                 "pre-ratchet baseline — ad hoc test-side afero write, not yet migrated (task august-bonanza, test-fs-helper slice): route through testsupport.WriteFile/WriteFileString/SeedTree when this fixture is next touched",
	"internal/profiles/profiles_guard_test.go#TestDelete_RejectsTraversalNames":                                                  "pre-ratchet baseline — ad hoc test-side afero write, not yet migrated (task august-bonanza, test-fs-helper slice): route through testsupport.WriteFile/WriteFileString/SeedTree when this fixture is next touched",
	"internal/profiles/profiles_guard_test.go#TestLoadFile_DedupesPendingUpgradesByPath":                                         "pre-ratchet baseline — ad hoc test-side afero write, not yet migrated (task august-bonanza, test-fs-helper slice): route through testsupport.WriteFile/WriteFileString/SeedTree when this fixture is next touched",
	"internal/profiles/profiles_guard_test.go#TestLoad_RejectsTraversalNames":                                                    "pre-ratchet baseline — ad hoc test-side afero write, not yet migrated (task august-bonanza, test-fs-helper slice): route through testsupport.WriteFile/WriteFileString/SeedTree when this fixture is next touched",
	"internal/profiles/profiles_guard_test.go#TestLoad_SeededProfileIsSharedAndFsProfileIsNot":                                   "pre-ratchet baseline — ad hoc test-side afero write, not yet migrated (task august-bonanza, test-fs-helper slice): route through testsupport.WriteFile/WriteFileString/SeedTree when this fixture is next touched",
	"internal/profiles/profiles_test.go#TestLoader_ResolveProfile_LocalParents":                                                  "pre-ratchet baseline — ad hoc test-side afero write, not yet migrated (task august-bonanza, test-fs-helper slice): route through testsupport.WriteFile/WriteFileString/SeedTree when this fixture is next touched",
	"internal/profiles/profiles_test.go#TestWithFS":                                                                              "pre-ratchet baseline — ad hoc test-side afero write, not yet migrated (task august-bonanza, test-fs-helper slice): route through testsupport.WriteFile/WriteFileString/SeedTree when this fixture is next touched",
	"internal/profiles/resolve_source_ref_test.go#TestResolveProfile_SourceRef_ChildNeverInheritsParentSource":                   "pre-ratchet baseline — ad hoc test-side afero write, not yet migrated (task august-bonanza, test-fs-helper slice): route through testsupport.WriteFile/WriteFileString/SeedTree when this fixture is next touched",
	"internal/profiles/resolve_source_ref_test.go#TestResolveProfile_SourceRef_GenuinelyLocalIsEmpty":                            "pre-ratchet baseline — ad hoc test-side afero write, not yet migrated (task august-bonanza, test-fs-helper slice): route through testsupport.WriteFile/WriteFileString/SeedTree when this fixture is next touched",
	"internal/profiles/upgrade_test.go#TestCommitUpgrade_WritesCanonicalFileAndClearsPending":                                    "pre-ratchet baseline — ad hoc test-side afero write, not yet migrated (task august-bonanza, test-fs-helper slice): route through testsupport.WriteFile/WriteFileString/SeedTree when this fixture is next touched",
	"internal/profiles/upgrade_test.go#TestLoad_CanonicalizesShortBundlesViaResolver":                                            "pre-ratchet baseline — ad hoc test-side afero write, not yet migrated (task august-bonanza, test-fs-helper slice): route through testsupport.WriteFile/WriteFileString/SeedTree when this fixture is next touched",
	"internal/profiles/upgrade_test.go#TestLoad_LocalProfileKeepsBareBundles":                                                    "pre-ratchet baseline — ad hoc test-side afero write, not yet migrated (task august-bonanza, test-fs-helper slice): route through testsupport.WriteFile/WriteFileString/SeedTree when this fixture is next touched",
	"internal/profiles/upgrade_test.go#TestLoad_NoResolverIsNoOp":                                                                "pre-ratchet baseline — ad hoc test-side afero write, not yet migrated (task august-bonanza, test-fs-helper slice): route through testsupport.WriteFile/WriteFileString/SeedTree when this fixture is next touched",
	"internal/profiles/upgrade_test.go#TestLoad_RewritesRetiredParentViaSeed":                                                    "pre-ratchet baseline — ad hoc test-side afero write, not yet migrated (task august-bonanza, test-fs-helper slice): route through testsupport.WriteFile/WriteFileString/SeedTree when this fixture is next touched",
	"internal/profiles/upgrade_test.go#TestUpgradeLedger_IsWiredToStorageAndToTheSeed":                                           "pre-ratchet baseline — ad hoc test-side afero write, not yet migrated (task august-bonanza, test-fs-helper slice): route through testsupport.WriteFile/WriteFileString/SeedTree when this fixture is next touched",
	"internal/projectroot/projectroot_test.go#TestResolve":                                                                       "pre-ratchet baseline — ad hoc test-side afero write, not yet migrated (task august-bonanza, test-fs-helper slice): route through testsupport.WriteFile/WriteFileString/SeedTree when this fixture is next touched",
	"internal/remote/lockfile_held_test.go#TestLockfile_LoadsWhenPinnedIsMerelyMentioned":                                        "pre-ratchet baseline — ad hoc test-side afero write, not yet migrated (task august-bonanza, test-fs-helper slice): route through testsupport.WriteFile/WriteFileString/SeedTree when this fixture is next touched",
	"internal/remote/lockfile_held_test.go#TestLockfile_RefusesTheRetiredPinnedKey":                                              "pre-ratchet baseline — ad hoc test-side afero write, not yet migrated (task august-bonanza, test-fs-helper slice): route through testsupport.WriteFile/WriteFileString/SeedTree when this fixture is next touched",
	"internal/remote/lockfile_test.go#TestLockfileManager_LoadDoesNotRewriteOnAMereMention":                                      "pre-ratchet baseline — ad hoc test-side afero write, not yet migrated (task august-bonanza, test-fs-helper slice): route through testsupport.WriteFile/WriteFileString/SeedTree when this fixture is next touched",
	"internal/remote/lockfile_test.go#TestLockfileManager_Load_InvalidYAML":                                                      "pre-ratchet baseline — ad hoc test-side afero write, not yet migrated (task august-bonanza, test-fs-helper slice): route through testsupport.WriteFile/WriteFileString/SeedTree when this fixture is next touched",
	"internal/remote/lockfile_test.go#TestLockfileManager_Load_NilMaps":                                                          "pre-ratchet baseline — ad hoc test-side afero write, not yet migrated (task august-bonanza, test-fs-helper slice): route through testsupport.WriteFile/WriteFileString/SeedTree when this fixture is next touched",
	"internal/remote/lockfile_test.go#TestLockfileManager_Load_PresentButEmptyFileIsRefused":                                     "pre-ratchet baseline — ad hoc test-side afero write, not yet migrated (task august-bonanza, test-fs-helper slice): route through testsupport.WriteFile/WriteFileString/SeedTree when this fixture is next touched",
	"internal/remote/lockfile_test.go#TestLockfileManager_Load_ReadError":                                                        "pre-ratchet baseline — ad hoc test-side afero write, not yet migrated (task august-bonanza, test-fs-helper slice): route through testsupport.WriteFile/WriteFileString/SeedTree when this fixture is next touched",
	"internal/remote/lockfile_test.go#TestLockfileManager_LoadSelfHealsLegacyCtxloomVersion":                                     "pre-ratchet baseline — ad hoc test-side afero write, not yet migrated (task august-bonanza, test-fs-helper slice): route through testsupport.WriteFile/WriteFileString/SeedTree when this fixture is next touched",
	"internal/remote/lockfile_test.go#TestLockfileManager_Load_WhitespaceOnlyFileIsRefused":                                      "pre-ratchet baseline — ad hoc test-side afero write, not yet migrated (task august-bonanza, test-fs-helper slice): route through testsupport.WriteFile/WriteFileString/SeedTree when this fixture is next touched",
	"internal/remote/publish_test.go#publishOnce":                                                                                "pre-ratchet baseline — ad hoc test-side afero write, not yet migrated (task august-bonanza, test-fs-helper slice): route through testsupport.WriteFile/WriteFileString/SeedTree when this fixture is next touched",
	"internal/remote/publish_test.go#TestPublishManager_Publish":                                                                 "pre-ratchet baseline — ad hoc test-side afero write, not yet migrated (task august-bonanza, test-fs-helper slice): route through testsupport.WriteFile/WriteFileString/SeedTree when this fixture is next touched",
	"internal/remote/publish_test.go#TestPublishManager_Publish_NoSignPayloadMeansNoSigWritten":                                  "pre-ratchet baseline — ad hoc test-side afero write, not yet migrated (task august-bonanza, test-fs-helper slice): route through testsupport.WriteFile/WriteFileString/SeedTree when this fixture is next touched",
	"internal/remote/publish_test.go#TestPublishManager_Publish_SignPayloadFailureAbortsBeforeAnyWrite":                          "pre-ratchet baseline — ad hoc test-side afero write, not yet migrated (task august-bonanza, test-fs-helper slice): route through testsupport.WriteFile/WriteFileString/SeedTree when this fixture is next touched",
	"internal/remote/publish_test.go#TestPublishManager_Publish_SignPayloadWritesSiblingSig":                                     "pre-ratchet baseline — ad hoc test-side afero write, not yet migrated (task august-bonanza, test-fs-helper slice): route through testsupport.WriteFile/WriteFileString/SeedTree when this fixture is next touched",
	"internal/remote/reference_local_test.go#TestLocalRefFetcher_FetchItem_FilesystemBackend":                                    "pre-ratchet baseline — ad hoc test-side afero write, not yet migrated (task august-bonanza, test-fs-helper slice): route through testsupport.WriteFile/WriteFileString/SeedTree when this fixture is next touched",
	"internal/remote/reference_local_test.go#TestLocalRefFetcher_PinnedAgainstFilesystemErrors":                                  "pre-ratchet baseline — ad hoc test-side afero write, not yet migrated (task august-bonanza, test-fs-helper slice): route through testsupport.WriteFile/WriteFileString/SeedTree when this fixture is next touched",
	"internal/remote/reference_local_test.go#TestResolver_DispatchesLocalAndRemote":                                              "pre-ratchet baseline — ad hoc test-side afero write, not yet migrated (task august-bonanza, test-fs-helper slice): route through testsupport.WriteFile/WriteFileString/SeedTree when this fixture is next touched",
	"internal/remote/vcs_listitems_test.go#TestFSVCS_ListItems_UnreadableDirIsAnError":                                           "pre-ratchet baseline — ad hoc test-side afero write, not yet migrated (task august-bonanza, test-fs-helper slice): route through testsupport.WriteFile/WriteFileString/SeedTree when this fixture is next touched",
	"internal/remote/vcs_listitems_test.go#TestFSVCS_ListItems_WorkingSet":                                                       "pre-ratchet baseline — ad hoc test-side afero write, not yet migrated (task august-bonanza, test-fs-helper slice): route through testsupport.WriteFile/WriteFileString/SeedTree when this fixture is next touched",
	"internal/remote/vcs_listitems_test.go#TestLocalRefFetcher_ListItems_LocalRefs":                                              "pre-ratchet baseline — ad hoc test-side afero write, not yet migrated (task august-bonanza, test-fs-helper slice): route through testsupport.WriteFile/WriteFileString/SeedTree when this fixture is next touched",
	"internal/remote/vcs_listitems_test.go#TestResolver_List_FansOutAcrossSchemes":                                               "pre-ratchet baseline — ad hoc test-side afero write, not yet migrated (task august-bonanza, test-fs-helper slice): route through testsupport.WriteFile/WriteFileString/SeedTree when this fixture is next touched",
	"internal/remote/vcs_test.go#TestFSVCS_ReadFile_Current":                                                                     "pre-ratchet baseline — ad hoc test-side afero write, not yet migrated (task august-bonanza, test-fs-helper slice): route through testsupport.WriteFile/WriteFileString/SeedTree when this fixture is next touched",
	"internal/remote/vcs_test.go#TestReadItemAt_VersionlessReadsCurrent":                                                         "pre-ratchet baseline — ad hoc test-side afero write, not yet migrated (task august-bonanza, test-fs-helper slice): route through testsupport.WriteFile/WriteFileString/SeedTree when this fixture is next touched",
	"internal/shared/admission/admission_test.go#TestProperty_DeciderIsPureCallerRenders":                                        "pre-ratchet baseline — ad hoc test-side afero write, not yet migrated (task august-bonanza, test-fs-helper slice): route through testsupport.WriteFile/WriteFileString/SeedTree when this fixture is next touched",
	"internal/shared/admission/admission_test.go#TestProperty_UnconfiguredStoreRefusesRatherThanReadingTheWorkingDirectory":      "pre-ratchet baseline — ad hoc test-side afero write, not yet migrated (task august-bonanza, test-fs-helper slice): route through testsupport.WriteFile/WriteFileString/SeedTree when this fixture is next touched",
	"internal/shared/admission/admission_test.go#TestStore_AbsentFileIsNotAFaultButAnUnreadableOneIs":                            "pre-ratchet baseline — ad hoc test-side afero write, not yet migrated (task august-bonanza, test-fs-helper slice): route through testsupport.WriteFile/WriteFileString/SeedTree when this fixture is next touched",
	"internal/shared/admission/admission_test.go#TestStore_VersionMismatchFaultsRatherThanReadingAsEmpty":                        "pre-ratchet baseline — ad hoc test-side afero write, not yet migrated (task august-bonanza, test-fs-helper slice): route through testsupport.WriteFile/WriteFileString/SeedTree when this fixture is next touched",
	"internal/shared/agent/commandfiles_dedup_test.go#TestWriteManagedCommandFiles_DedupHomeDir":                                 "pre-ratchet baseline — ad hoc test-side afero write, not yet migrated (task august-bonanza, test-fs-helper slice): route through testsupport.WriteFile/WriteFileString/SeedTree when this fixture is next touched",
	"internal/shared/agent/commandfiles_golden_test.go#oldWriteManagedCommandFiles":                                              "pre-ratchet baseline — ad hoc test-side afero write, not yet migrated (task august-bonanza, test-fs-helper slice): route through testsupport.WriteFile/WriteFileString/SeedTree when this fixture is next touched",
	"internal/shared/agent/commandfiles_golden_test.go#TestWriteManagedCommandFiles_GoldenByteIdentical":                         "pre-ratchet baseline — ad hoc test-side afero write, not yet migrated (task august-bonanza, test-fs-helper slice): route through testsupport.WriteFile/WriteFileString/SeedTree when this fixture is next touched",
	"internal/shared/agent/managedcontext_test.go#TestWriteManagedContext_PreservesPositionOfTrailingUserContent":                "pre-ratchet baseline — ad hoc test-side afero write, not yet migrated (task august-bonanza, test-fs-helper slice): route through testsupport.WriteFile/WriteFileString/SeedTree when this fixture is next touched",
	"internal/shared/agent/mcpfile_test.go#TestMCPFileConfig_RemoveServers_DoesNotDeleteUserAuthoredEntry":                       "pre-ratchet baseline — ad hoc test-side afero write, not yet migrated (task august-bonanza, test-fs-helper slice): route through testsupport.WriteFile/WriteFileString/SeedTree when this fixture is next touched",
	"internal/shared/agent/mcpfile_test.go#TestMCPFileConfig_WriteServers_LedgerReadErrorSurfaces":                               "pre-ratchet baseline — ad hoc test-side afero write, not yet migrated (task august-bonanza, test-fs-helper slice): route through testsupport.WriteFile/WriteFileString/SeedTree when this fixture is next touched",
	"internal/shared/agent/mcpfile_test.go#TestMCPFileConfig_WriteServers_ManagedNameRoundTripsWriteThenRemove":                  "pre-ratchet baseline — ad hoc test-side afero write, not yet migrated (task august-bonanza, test-fs-helper slice): route through testsupport.WriteFile/WriteFileString/SeedTree when this fixture is next touched",
	"internal/shared/agent/mcpfile_test.go#TestMCPFileConfig_WriteServers_PreservesLargeNumbers":                                 "pre-ratchet baseline — ad hoc test-side afero write, not yet migrated (task august-bonanza, test-fs-helper slice): route through testsupport.WriteFile/WriteFileString/SeedTree when this fixture is next touched",
	"internal/shared/agent/mcpfile_test.go#TestMCPFileConfig_WriteServers_RecreatesHandDeletedManagedServerWithWarning":          "pre-ratchet baseline — ad hoc test-side afero write, not yet migrated (task august-bonanza, test-fs-helper slice): route through testsupport.WriteFile/WriteFileString/SeedTree when this fixture is next touched",
	"internal/shared/agent/mcpfile_test.go#TestMCPFileConfig_WriteServers_RefusesCollisionWithUserAuthoredName":                  "pre-ratchet baseline — ad hoc test-side afero write, not yet migrated (task august-bonanza, test-fs-helper slice): route through testsupport.WriteFile/WriteFileString/SeedTree when this fixture is next touched",
	"internal/shared/agent/mcpfile_test.go#TestMCPFileConfig_WriteServers_RefusesUnparsableRegistry":                             "pre-ratchet baseline — ad hoc test-side afero write, not yet migrated (task august-bonanza, test-fs-helper slice): route through testsupport.WriteFile/WriteFileString/SeedTree when this fixture is next touched",
	"internal/shared/agent/mcpfile_test.go#TestMCPFileConfig_WriteServers_StaleLedgerNameIsReleasedWithAWarning":                 "pre-ratchet baseline — ad hoc test-side afero write, not yet migrated (task august-bonanza, test-fs-helper slice): route through testsupport.WriteFile/WriteFileString/SeedTree when this fixture is next touched",
	"internal/shared/agent/mcpfile_test.go#TestMCPFileConfig_WriteServers_UnmanagedEntryWithNoCollisionSurvives":                 "pre-ratchet baseline — ad hoc test-side afero write, not yet migrated (task august-bonanza, test-fs-helper slice): route through testsupport.WriteFile/WriteFileString/SeedTree when this fixture is next touched",
	"internal/shared/agent/packagefiles_test.go#TestWriteManagedPackageFiles_CleanupPreservesForeignFiles":                       "pre-ratchet baseline — ad hoc test-side afero write, not yet migrated (task august-bonanza, test-fs-helper slice): route through testsupport.WriteFile/WriteFileString/SeedTree when this fixture is next touched",
	"internal/shared/agent/sessionstore_test.go#TestParseSessionFile_UnparseableLinesAreCountableByTheCaller":                    "pre-ratchet baseline — ad hoc test-side afero write, not yet migrated (task august-bonanza, test-fs-helper slice): route through testsupport.WriteFile/WriteFileString/SeedTree when this fixture is next touched",
	"internal/shared/agent/settings_io_atomic_test.go#recordingFs.Create":                                                        "pre-ratchet baseline — ad hoc test-side afero write, not yet migrated (task august-bonanza, test-fs-helper slice): route through testsupport.WriteFile/WriteFileString/SeedTree when this fixture is next touched",
	"internal/shared/agent/settings_io_atomic_test.go#recordingFs.Rename":                                                        "pre-ratchet baseline — ad hoc test-side afero write, not yet migrated (task august-bonanza, test-fs-helper slice): route through testsupport.WriteFile/WriteFileString/SeedTree when this fixture is next touched",
	"internal/shared/agent/settings_io_atomic_test.go#TestAtomicWriteFile_ContractPreserved":                                     "pre-ratchet baseline — ad hoc test-side afero write, not yet migrated (task august-bonanza, test-fs-helper slice): route through testsupport.WriteFile/WriteFileString/SeedTree when this fixture is next touched",
	"internal/shared/agent/settings_io_atomic_test.go#TestAtomicWriteFile_RenameFailureIsAnError":                                "pre-ratchet baseline — ad hoc test-side afero write, not yet migrated (task august-bonanza, test-fs-helper slice): route through testsupport.WriteFile/WriteFileString/SeedTree when this fixture is next touched",
	"internal/shared/agent/settings_io_test.go#TestAtomicWriteFile":                                                              "pre-ratchet baseline — ad hoc test-side afero write, not yet migrated (task august-bonanza, test-fs-helper slice): route through testsupport.WriteFile/WriteFileString/SeedTree when this fixture is next touched",
	"internal/shared/ledger/ledger_test.go#TestRead_UnreadableFile_ReturnsTheError":                                              "pre-ratchet baseline — ad hoc test-side afero write, not yet migrated (task august-bonanza, test-fs-helper slice): route through testsupport.WriteFile/WriteFileString/SeedTree when this fixture is next touched",
	"internal/shared/ledger/ledger_test.go#TestRead_UntypedLine_IsSkippedAndWarned":                                              "pre-ratchet baseline — ad hoc test-side afero write, not yet migrated (task august-bonanza, test-fs-helper slice): route through testsupport.WriteFile/WriteFileString/SeedTree when this fixture is next touched",
	"internal/signing/countersign/store_test.go#TestStore_AppendIndex_CorruptIndex_RefusesRatherThanTruncates":                   "pre-ratchet baseline — ad hoc test-side afero write, not yet migrated (task august-bonanza, test-fs-helper slice): route through testsupport.WriteFile/WriteFileString/SeedTree when this fixture is next touched",
	"internal/signing/countersign/store_test.go#TestStore_CorruptedSignatureBodyAtCorrectIndex_NeverVerifies":                    "pre-ratchet baseline — ad hoc test-side afero write, not yet migrated (task august-bonanza, test-fs-helper slice): route through testsupport.WriteFile/WriteFileString/SeedTree when this fixture is next touched",
	"internal/signing/countersign/store_test.go#TestStore_LatestApprove_CorruptIndex_IsNotSilentlyEmpty":                         "pre-ratchet baseline — ad hoc test-side afero write, not yet migrated (task august-bonanza, test-fs-helper slice): route through testsupport.WriteFile/WriteFileString/SeedTree when this fixture is next touched",
	"internal/signing/countersign/store_test.go#TestStore_Readable_NonSignatureFiles_AreIgnored":                                 "pre-ratchet baseline — ad hoc test-side afero write, not yet migrated (task august-bonanza, test-fs-helper slice): route through testsupport.WriteFile/WriteFileString/SeedTree when this fixture is next touched",
	"internal/signing/countersign/store_test.go#TestStore_Readable_UnparseableSignature_IsAnError":                               "pre-ratchet baseline — ad hoc test-side afero write, not yet migrated (task august-bonanza, test-fs-helper slice): route through testsupport.WriteFile/WriteFileString/SeedTree when this fixture is next touched",
	"internal/signing/countersign/store_test.go#TestStore_ThreeRecordKindsHaveThreeAuthorityModels":                              "pre-ratchet baseline — ad hoc test-side afero write, not yet migrated (task august-bonanza, test-fs-helper slice): route through testsupport.WriteFile/WriteFileString/SeedTree when this fixture is next touched",
	"internal/transcript/vendorreader/claude/locate_test.go#seedStore":                                                           "pre-ratchet baseline — ad hoc test-side afero write, not yet migrated (task august-bonanza, test-fs-helper slice): route through testsupport.WriteFile/WriteFileString/SeedTree when this fixture is next touched",
	"internal/transcript/vendorreader/claude/locate_test.go#TestDiscover_UnrecognizedStoreRefusesRatherThanReportingZero":        "pre-ratchet baseline — ad hoc test-side afero write, not yet migrated (task august-bonanza, test-fs-helper slice): route through testsupport.WriteFile/WriteFileString/SeedTree when this fixture is next touched",
}

// scanTestWriteDiscipline walks every _test.go file under each of
// writeDisciplineScopes (skipping writeDisciplineExemptDirs, the same
// production write library the other arm exempts) whose imports name
// "github.com/spf13/afero" (fileImportsAfero), and returns every raw afero
// write call site it finds.
func scanTestWriteDiscipline(t *testing.T) []writeDisciplineViolation {
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
			case !strings.HasSuffix(d.Name(), "_test.go"):
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
			if !fileImportsAfero(f) {
				return nil
			}
			filesScanned++
			out = append(out, scanTestFileForRawAferoWrites(fset, f, rel)...)
			return nil
		})
		if err != nil {
			t.Fatalf("walk %s: %v", scope, err)
		}
	}
	// Anti-vacuity: a walk or import filter that silently stopped matching
	// files would make every assertion below pass for the wrong reason.
	if filesScanned < 100 {
		t.Fatalf("scanned only %d _test.go files importing afero under %v — the walk or the "+
			"import filter is broken, not the tree", filesScanned, writeDisciplineScopes)
	}
	return out
}

// aferoImportPath is the exact quoted import path fileImportsAfero looks
// for — a literal string comparison against an ast.ImportSpec.Path.Value,
// never a substring search over the file's text (see this file's
// KEYED ON THE IMPORT section).
const aferoImportPath = `"github.com/spf13/afero"`

// fileImportsAfero reports whether f's import declarations name
// github.com/spf13/afero, by inspecting the parsed import specs rather than
// the file's raw text.
func fileImportsAfero(f *ast.File) bool {
	for _, imp := range f.Imports {
		if imp.Path.Value == aferoImportPath {
			return true
		}
	}
	return false
}

// scanTestFileForRawAferoWrites is scanFileForRawWrites' test-file twin,
// restricted to afero write calls (collectRawAferoWrites) rather than the
// full os.*-and-afero.* set: see this file's SCOPED TO AFERO section for why
// os.* is out of scope here.
func scanTestFileForRawAferoWrites(fset *token.FileSet, f *ast.File, rel string) []writeDisciplineViolation {
	var out []writeDisciplineViolation
	for _, decl := range f.Decls {
		switch d := decl.(type) {
		case *ast.FuncDecl:
			if d.Body == nil {
				continue
			}
			out = append(out, collectRawAferoWrites(fset, d.Body, rel, funcSymbol(d))...)
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
					out = append(out, collectRawAferoWrites(fset, val, rel, "<package-level>")...)
				}
			}
		}
	}
	return out
}

// collectRawAferoWrites is collectRawWrites' afero-only subset: the
// package-level and method-call afero checks, byte-for-byte, with the
// os.*-package branch removed entirely rather than filtered out per call —
// see this file's SCOPED TO AFERO section for why os.* is never this arm's
// business.
func collectRawAferoWrites(fset *token.FileSet, node ast.Node, rel, sym string) []writeDisciplineViolation {
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
		if pkgIdent, ok := sel.X.(*ast.Ident); ok && pkgIdent.Name == "afero" {
			if forbiddenAferoPackageCalls[sel.Sel.Name] {
				out = append(out, writeDisciplineViolation{
					file: rel, symbol: sym, call: "afero." + sel.Sel.Name,
					line: fset.Position(call.Pos()).Line,
				})
			}
			return true
		}
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

// TestArch_TestWriteDiscipline_RawAferoWritesRouteThroughHelper is the test-
// file arm's gate: every raw afero write call site in a _test.go file that
// imports afero, under internal/ or cmd/ outside writeDisciplineExemptDirs,
// must either not exist, or be named (with a reason) in
// testWriteDisciplineAllowed.
func TestArch_TestWriteDiscipline_RawAferoWritesRouteThroughHelper(t *testing.T) {
	violations := scanTestWriteDiscipline(t)
	sort.Slice(violations, func(i, j int) bool {
		if violations[i].file != violations[j].file {
			return violations[i].file < violations[j].file
		}
		return violations[i].line < violations[j].line
	})

	for _, v := range violations {
		if why, ok := testWriteDisciplineAllowed[v.key()]; ok {
			t.Logf("allowed: %s:%d %s in %s (%s)", v.file, v.line, v.call, v.symbol, why)
			continue
		}
		t.Errorf("%s:%d calls %s directly in %s — raw filesystem writes in test code must route "+
			"through testsupport.WriteFile/WriteFileString/SeedTree (or an existing sanctioned "+
			"writer, e.g. iox.WriteFileAtomicFs), so a fixture never disagrees with production "+
			"about what \"write a file\" means. If this is a deliberate, reviewed exception, add "+
			"%q to testWriteDisciplineAllowed in tests/arch/write_discipline_test.go naming the "+
			"fix required to remove it.",
			v.file, v.line, v.call, v.symbol, v.key())
	}
}

// TestArch_TestWriteDiscipline_AllowlistIsLive is TestArch_WriteDiscipline_AllowlistIsLive's
// test-file twin: it fails when a testWriteDisciplineAllowed entry names a
// symbol that either does not exist or no longer contains a forbidden afero
// call — the same staleness check, so an exemption whose reason has gone
// stale fails loudly instead of silently covering whatever raw write lands
// at that symbol next.
func TestArch_TestWriteDiscipline_AllowlistIsLive(t *testing.T) {
	violations := scanTestWriteDiscipline(t)
	live := make(map[string]bool, len(violations))
	for _, v := range violations {
		live[v.key()] = true
	}

	keys := make([]string, 0, len(testWriteDisciplineAllowed))
	for k := range testWriteDisciplineAllowed {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	for _, k := range keys {
		if !live[k] {
			t.Errorf("testWriteDisciplineAllowed allows %q (%s) but the scan found no forbidden "+
				"afero write there anymore — delete the entry, or it will silently exempt whatever "+
				"raw write lands at that symbol next", k, testWriteDisciplineAllowed[k])
		}
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
