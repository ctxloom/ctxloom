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
// call anywhere in internal/ outside the exempt packages fails the build
// until it either routes through iox or earns its own reviewed entry.
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

// writeDisciplineScope is the subtree this gate looks at: production code
// only, per the fs-consolidation plan's C1 scope. Test files never enter the
// scan (fsWalkNonTest below skips _test.go the same way scan() does).
const writeDisciplineScope = "internal"

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

// writeDisciplineAllowed is this gate's shrinking allowlist, in the same
// shape as layering_test.go's layeringRule.allowed and arch_test.go's
// testSupportImporters: a durable symbol reference (module-relative
// "file.go#Symbol", where Symbol is "Type.Method" for a method or the bare
// function name otherwise) mapped to the fix required to remove the entry.
//
// Generated MECHANICALLY by running this gate with an empty map and
// transcribing every reported violation (2026-08-13, base bd6b3baf). Nothing
// here was migrated as part of this slice — that is C3 (the three named
// strays: countersign, gitignore, operations.ConvertVendorTranscript) and
// C10 (per-area sweeps) — so most entries carry the generic baseline reason.
// A handful carry a more specific one where the fix is already obvious from
// the call site (a fixed temp name, a lock's own file, a test fixture that
// is itself the exempt case).
var writeDisciplineAllowed = map[string]string{
	"internal/acp/session.go#chatSession.handleFsWrite":                              "pre-ratchet baseline — migrate to iox (fs-consolidation plan C3/C10)",
	"internal/agentcoord/coord/artifactstore.go#artifactStore.publish":               "pre-ratchet baseline — migrate to iox (fs-consolidation plan C3/C10)",
	"internal/agentcoord/coord/homeartifacts.go#Home.DownloadArtifact":               "pre-ratchet baseline — migrate to iox (fs-consolidation plan C3/C10)",
	"internal/agentcoord/coord/httpserver.go#coordServing.saveEndpointLocked":        "pre-ratchet baseline — migrate to iox (fs-consolidation plan C3/C10)",
	"internal/agentcoord/coord/journal.go#openStoreFromOffset":                       "pre-ratchet baseline — migrate to iox (fs-consolidation plan C3/C10)",
	"internal/agentcoord/coord/statedir.go#claimOwner":                               "advisory lock file's own O_EXCL create — mechanically parallel to filelock's exemption but not itself filelock (fs-consolidation plan C10 to decide: fold into filelock or exempt structurally)",
	"internal/agentcoord/mcpschema/gen/main.go#generateXmlLike":                      "pre-ratchet baseline, codegen tool — migrate to iox (fs-consolidation plan C3/C10)",
	"internal/agentcoord/mcpschema/gen/main.go#writeSpec":                            "pre-ratchet baseline, codegen tool — migrate to iox (fs-consolidation plan C3/C10)",
	"internal/agentcoord/spool/ops.go#moveTo":                                        "pre-ratchet baseline — migrate to iox (fs-consolidation plan C3/C10)",
	"internal/agentcoord/spool/writer.go#writeAndSync":                               "pre-ratchet baseline — migrate to iox (fs-consolidation plan C3/C10)",
	"internal/agentcoord/spool/writer.go#Writer.Write":                               "pre-ratchet baseline — migrate to iox (fs-consolidation plan C3/C10)",
	"internal/cli/bundle_items.go#editInEditor":                                      "pre-ratchet baseline — migrate to iox (fs-consolidation plan C3/C10)",
	"internal/cli/llm_turn.go#writeRunStartHandoff":                                  "pre-ratchet baseline — migrate to iox (fs-consolidation plan C3/C10)",
	"internal/cli/run_terminal_ui.go#redirectDiagnosticsForTUI":                      "pre-ratchet baseline — migrate to iox (fs-consolidation plan C3/C10)",
	"internal/cli/tui/export.go#exportTranscript":                                    "pre-ratchet baseline — migrate to iox (fs-consolidation plan C3/C10)",
	"internal/contextmetrics/contextmetrics.go#Append":                               "pre-ratchet baseline — migrate to iox (fs-consolidation plan C3/C10)",
	"internal/docsgen/config.go#GenConfig":                                           "pre-ratchet baseline, doc generator — migrate to iox (fs-consolidation plan C3/C10)",
	"internal/docsgen/mcp.go#GenMCPTools":                                            "pre-ratchet baseline, doc generator — migrate to iox (fs-consolidation plan C3/C10)",
	"internal/gitignore/gitignore.go#appendBlock":                                    "pre-ratchet baseline — migrate to iox (fs-consolidation plan C3/C10)",
	"internal/lm/backends/mock.go#writeMockRecord":                                   "pre-ratchet baseline, test/mock backend — migrate to iox (fs-consolidation plan C3/C10)",
	"internal/lm/isolation/auth.go#copyCredentialFile":                               "pre-ratchet baseline — migrate to iox (fs-consolidation plan C3/C10)",
	"internal/lm/isolation/imagebuild.go#buildBaseImage":                             "pre-ratchet baseline — migrate to iox (fs-consolidation plan C3/C10)",
	"internal/lm/isolation/imagebuild.go#buildImage":                                 "pre-ratchet baseline — migrate to iox (fs-consolidation plan C3/C10)",
	"internal/lm/isolation/imagebuild.go#copyExecutable":                             "pre-ratchet baseline — migrate to iox (fs-consolidation plan C3/C10)",
	"internal/lm/isolation/sharedfs.go#probeOneRoot":                                 "pre-ratchet baseline — migrate to iox (fs-consolidation plan C3/C10)",
	"internal/lm/isolation/statemounts.go#ensureFile":                                "pre-ratchet baseline — migrate to iox (fs-consolidation plan C3/C10)",
	"internal/lm/isolation/traceprobe.go#traceProbeFromEnv":                          "pre-ratchet baseline — migrate to iox (fs-consolidation plan C3/C10)",
	"internal/lm/isolation/worktree_reap.go#recordWorktreeOwner":                     "pre-ratchet baseline — migrate to iox (fs-consolidation plan C3/C10)",
	"internal/ltk/tools/extract-defaults/main.go#main":                               "pre-ratchet baseline, standalone codegen tool under internal/ltk — migrate to iox (fs-consolidation plan C3/C10)",
	"internal/mockengine/runtime.go#Runtime.emitReport":                              "pre-ratchet baseline, mock engine test double — migrate to iox (fs-consolidation plan C3/C10)",
	"internal/operations/bundles.go#reserveNewBundlePath":                            "pre-ratchet baseline — migrate to iox (fs-consolidation plan C3/C10)",
	"internal/operations/delegate.go#copyUntrackedFile":                              "pre-ratchet baseline — migrate to iox (fs-consolidation plan C3/C10)",
	"internal/operations/task_triggers_cache.go#saveTriggerCache":                    "pre-ratchet baseline — migrate to iox (fs-consolidation plan C3/C10)",
	"internal/operations/vendorreader.go#appendFileBytes":                            "named in fs-consolidation plan C3 as one of the three strays to migrate: operations.ConvertVendorTranscript, fixed .rebuild temp, streaming writer — fenced on fix/recover-rotation-lineage merge",
	"internal/operations/vendorreader.go#appendRotationSegment":                      "named in fs-consolidation plan C3 as one of the three strays to migrate: operations.ConvertVendorTranscript, fixed .rebuild temp, streaming writer — fenced on fix/recover-rotation-lineage merge",
	"internal/operations/vendorreader.go#convertVendorTranscript":                    "named in fs-consolidation plan C3 as one of the three strays to migrate: operations.ConvertVendorTranscript, fixed .rebuild temp, streaming writer — fenced on fix/recover-rotation-lineage merge",
	"internal/remote/git_publisher.go#GitPublisher.CreateOrUpdateFile":               "pre-ratchet baseline — migrate to iox (fs-consolidation plan C3/C10)",
	"internal/schemagen/schemagen.go#Generate":                                       "pre-ratchet baseline, codegen tool — migrate to iox (fs-consolidation plan C3/C10)",
	"internal/sessions/index.go#linkTranscriptIntoHarpDir":                           "root vendor transcript symlink repoint (fs-consolidation plan C12, Q2 ruling pending) — Remove+Symlink is the non-atomic pattern C12 calls out",
	"internal/shared/agent/rendezvous.go#writeMarker":                                "pre-ratchet baseline — migrate to iox (fs-consolidation plan C3/C10)",
	"internal/shared/logsink/logsink.go#Open":                                        "pre-ratchet baseline — migrate to iox (fs-consolidation plan C3/C10)",
	"internal/shared/logsink/logsink.go#rollIfOversized":                             "pre-ratchet baseline — migrate to iox (fs-consolidation plan C3/C10)",
	"internal/shared/tasks/log.go#eventLog.append":                                   "pre-ratchet baseline — migrate to iox (fs-consolidation plan C3/C10)",
	"internal/shared/tasks/taskstest/gitfixture.go#RealGitWorktreeFixture":           "test fixture package, not shipped production code — pre-ratchet baseline (fs-consolidation plan C10)",
	"internal/testsupport/containercell/containercell.go#buildBinary":                "test harness, never linked into a binary — pre-ratchet baseline (fs-consolidation plan C10)",
	"internal/testsupport/containercell/containercell.go#buildProbeCat":              "test harness, never linked into a binary — pre-ratchet baseline (fs-consolidation plan C10)",
	"internal/testsupport/containercell/containercell.go#Runtime.buildImage":         "test harness, never linked into a binary — pre-ratchet baseline (fs-consolidation plan C10)",
	"internal/transcript/recorder.go#openAppendFile":                                 "deliberate streaming O_APPEND recorder, live fd held open across writes (task easeful-dial, C7 design) — not a one-shot write iox's whole-file API covers",
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

// scanWriteDiscipline walks every non-test .go file under writeDisciplineScope
// (skipping writeDisciplineExemptDirs) and returns every raw-fs-write call
// site it finds, full-body parsed rather than imports-only — the same
// technique doc_comment_test.go uses — because the subject here is call
// expressions, not the import graph.
func scanWriteDiscipline(t *testing.T) []writeDisciplineViolation {
	t.Helper()
	root := moduleRoot(t)
	scopeRoot := filepath.Join(root, writeDisciplineScope)
	fset := token.NewFileSet()
	var out []writeDisciplineViolation
	var filesScanned int

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
		t.Fatalf("walk %s: %v", writeDisciplineScope, err)
	}
	// Anti-vacuity: a walk that silently stopped matching files would make
	// every assertion below pass for the wrong reason.
	if filesScanned < 200 {
		t.Fatalf("scanned only %d non-test files under %s — the walk is broken, not the tree", filesScanned, writeDisciplineScope)
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
		pkgIdent, ok := sel.X.(*ast.Ident)
		if !ok || pkgIdent.Name != "os" {
			return true
		}
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
