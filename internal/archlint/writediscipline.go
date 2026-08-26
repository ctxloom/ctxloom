package archlint

import (
	"go/ast"
	"go/token"
	"strings"

	"golang.org/x/tools/go/analysis"
)

// writeDisciplineScopes are the subtrees this rule governs: production code
// only, both the library and the binaries that drive it.
var writeDisciplineScopes = []string{"internal", "cmd"}

// writeDisciplineExemptDirs are the packages that ARE the write library, and
// so are structurally exempt: they are not a second copy of iox, they are
// the thing this rule protects. The lock primitive is github.com/gofrs/flock,
// a third-party module rather than an in-tree package, so unlike its
// predecessor (internal/shared/filelock, deleted — every lock call site
// calls flock.New directly per internal/shared/agent/rendezvous.go's idiom)
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
// into writeDisciplineAllowed with the fix required to remove it. What it buys
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
	if dir == "" || !inScopes(dir, writeDisciplineScopes) {
		return nil, nil
	}
	for _, ex := range writeDisciplineExemptDirs {
		if UnderSubtree(dir, ex) {
			return nil, nil
		}
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
				if _, ok := writeDisciplineAllowed[key]; ok {
					return
				}
				pass.Reportf(pos,
					"%s calls %s directly — raw filesystem writes must route through "+
						"internal/shared/iox, which is where the atomic write-then-rename sequence and the "+
						"ownership ledger live. If this is a deliberate, reviewed exception, add %q to "+
						"writeDisciplineAllowed in internal/archlint/writediscipline.go naming the fix "+
						"required to remove it.", sym, call, key)
			})
		}
	}
	reportStaleAllowlist(pass, writeDisciplineAllowed, analyzedFiles(pass), seen, "writeDisciplineAllowed",
		"internal/archlint/writediscipline.go")
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

// inScopes reports whether dir lies under any of the named subtrees.
func inScopes(dir string, scopes []string) bool {
	for _, s := range scopes {
		if UnderSubtree(dir, s) {
			return true
		}
	}
	return false
}

// writeDisciplineAllowed is the ratchet baseline: a durable symbol reference
// ("file.go#Symbol") mapped to the fix required to remove the entry.
var writeDisciplineAllowed = map[string]string{
	"internal/operations/harp_artifacts.go#migrateOneHarp":                    "the flagged call MOVES an existing file — os.Rename of one regular file from a harp's top level into its persist/ dir — and iox has no move primitive to delegate to: WriteFileAtomic, WriteFileAtomicFs and NewAtomicFile all write NEW BYTES to a destination path. Copy-then-delete would satisfy the gate and WEAKEN the guarantee this path exists to keep: rename(2) leaves the user's authored plan file at exactly one of src or dst, while a read-write-unlink pair has a window in which a crash leaves it at neither, and these are documents the user wrote. Removing this entry requires a rename/move primitive on iox (a public API addition), not a rewrite of this call site.",
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
	"cmd/probe-mcp-server/main.go#server.record":                              "APPEND-ONLY LOG, and iox's atomic write-then-rename is the wrong primitive for it rather than a heavier one: every primitive iox offers writes a COMPLETE file to a destination, so routing this call through one would rewrite the whole log per record instead of appending. Two things in P2's verdict depend on the append semantics — a second tools/call must ADD a record rather than replace one, and the ABSENCE of the file (the server never started) must stay distinguishable from its EMPTINESS (it started and the tool was never called), which are findings about different subsystems. The file is a throwaway fixture artifact in a temp tree, never user data, so the crash-atomicity the gate protects buys nothing here. Removing this entry requires an append primitive on iox, not a rewrite of this call site.",
	"internal/bundles/skill_archive.go#ImportSkillArchive":                    "C10 content/skill_archive sweep: fsys.Rename here is a WHOLE-DIRECTORY swap (staged tree -> final, and final -> aside on replace), not a single-file content write — outside iox's WriteFileAtomicFs API, which has no directory-rename surface. This is a deliberate, already-safe swap-never-clear-then-hope idiom (see the function's own doc) with its own aside/restore recovery; exempt, not a violation to migrate.",
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
	"internal/lm/isolation/sharedfs.go#probeOneRoot":                          "C10 isolation sweep: writes a marker file inside os.MkdirTemp(root, probeScratchPrefix), deferred RemoveAll in the same function — temp-dir-scoped, verified. Migration deferred to a future slice (mechanical, low priority).",
	"internal/lm/isolation/statemounts.go#ensureFile":                         "C10 isolation sweep: the plan's 'all ~26 seams are temp-dir-scoped' claim is WRONG for this one — ensureFile's caller passes the LIVE ~/.ctxloom/tasks/<project>.jsonl path (taskpaths.HomeTasksLogPath) and its advisory-lock sidecar, to stand up the container bind-mount SOURCE before `run`. Not a write-discipline risk in practice: O_CREATE|O_WRONLY with no O_TRUNC only creates-if-absent and immediately Closes, matching the doc's 'never truncating a log that already has tasks in it' — but it is not temp-scoped, and iox's whole-file-replace API is the wrong shape for a create-if-absent primitive anyway. Reported, not migrated.",
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
	"internal/shared/tasks/log.go#eventLog.append":                            "O_APPEND write of one event line onto a log that must never lose prior entries — already runs under a flock.Flock lock (eventLog.lock, callers hold it across append) with its own f.Sync() (appendLine). NOT a 'migrate to iox' candidate: iox's whole family is REPLACE-file-contents (unique temp + rename over the target), and has no append primitive to migrate an O_APPEND writer onto — this entry is a legitimate, permanent exemption, not a queued fix (fs-consolidation closing verification, stale-reason finding)",
	"internal/shared/tasks/taskstest/gitfixture.go#RealGitWorktreeFixture":    "test fixture package, not shipped production code — pre-ratchet baseline (fs-consolidation plan C10)",
	"internal/testsupport/containercell/containercell.go#Runtime.buildImage":  "test harness, never linked into a binary — pre-ratchet baseline (fs-consolidation plan C10)",
	"internal/testsupport/containercell/containercell.go#buildBinary":         "test harness, never linked into a binary — pre-ratchet baseline (fs-consolidation plan C10)",
	"internal/testsupport/containercell/containercell.go#buildProbeCat":       "test harness, never linked into a binary — pre-ratchet baseline (fs-consolidation plan C10)",
	"internal/transcript/recorder.go#openAppendFile":                          "deliberate streaming O_APPEND recorder, live fd held open across writes (task easeful-dial, C7 design) — not a one-shot write iox's whole-file API covers",
}
