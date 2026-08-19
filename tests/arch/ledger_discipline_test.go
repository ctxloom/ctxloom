//go:build arch

// LEDGER DISCIPLINE: every managed-SUBSET writer of a file it does not
// exclusively own must record what it owns — a sidecar ledger
// (internal/shared/ledger) or in-file markers (agent.WriteManagedContext /
// DeliverManagedContext) — so a later run can withdraw exactly what it wrote
// and nothing else.
//
// R6 is this gate's other half: a file ctxloom exclusively owns
// inside a foreign engine's directory is locked AND ledgered like a shared
// file. tests/arch/lock_discipline_test.go proves the lock half; this proves
// the record half. Caveat C1 (codex's [mcp_servers] had structural-only
// ownership until unit 4 added a SurfaceMCP ledger — a renamed managed
// server orphaned its old entry forever) is exactly the failure mode this
// gate exists to stop from recurring anywhere else in the module.
//
// THE HEURISTIC. For every non-test top-level function in the scoped
// packages, three name-based questions over every call expression AND every
// selector expression in its body (closures included):
//
//   - WRITE SIGNAL: identical to lock_discipline_test.go's — a call matching
//     /^(?i)save/ or one of the known write primitives (AtomicWriteFile,
//     WriteFileAtomicFs, WriteManagedContext, WriteManagedPackageFiles,
//     WriteManagedCommandFiles, WriteServers, RemoveServers).
//   - MANAGED-SUBSET SIGNAL: a call whose name CONTAINS "managed"
//     case-insensitively (removeManagedHooks, removeManagedMCP, dropManaged,
//     isManagedServer, hasManagedHook, managedHookDigests,
//     isManagedHookCommand, WriteManagedContext, WriteManagedPackageFiles,
//     WriteManagedCommandFiles, ...). This is the signal that the write
//     touches a MANAGED SUBSET of the file rather than the whole thing —
//     the shape that needs an ownership record at all. A function that
//     writes a file it wholly owns (kiro's writeAgentConfig/writeSteering,
//     B8) never calls anything spelled "managed" because there is no subset
//     to distinguish, and is correctly invisible to this gate — same
//     rationale lock_discipline_test.go documents for the same functions.
//   - OWNERSHIP-RECORD SIGNAL, any of three shapes — this module has THREE
//     real ownership mechanisms, not one:
//     1. a SELECTOR expression anywhere in the body whose package
//     qualifier is the identifier "ledger" (catches `ledger.Ledger{...}`
//     composite literals, `ledger.SurfaceMCP` and every other
//     package-qualified reference, whether inside a call, a literal, or
//     a bare expression);
//     2. a CALL to a method whose name contains "ledger" case-
//     insensitively (writeLedger/readLedger/reconcileLedger/ledger()) —
//     MCPFileConfig (mcpfile.go) wraps its own `ledger.Ledger{...}`
//     construction behind these names, so signal 1 alone never fires
//     inside WriteServers/RemoveServers themselves;
//     3. a call to WriteManagedContext, DeliverManagedContext, or
//     StripManagedSection (the in-file-marker mechanism,
//     managedcontext.go) — the markers live in the bytes it writes, so
//     there is no separate ledger call to find; OR a SELECTOR whose
//     field name is exactly "SCM" — claude's THIRD mechanism, an
//     ownership marker carried as a FIELD on each managed entry itself
//     (claudeCodeMCPConfig's server.SCM, claudeCodeHook.SCM) rather
//     than a sidecar or in-file-text record.
//
// A function with a write signal AND a managed-subset signal, but NO
// ownership-record signal anywhere in its own body, is a violation.
//
// WHY A SELECTOR SCAN, NOT JUST A CALL SCAN (unlike lock_discipline_test.go,
// which only inspects CallExpr). `ledger.Ledger{FS: fs, Dir: dir}` is a
// COMPOSITE LITERAL, not a call — the ownership record is often
// CONSTRUCTED, then its Read/Write methods are called on a local variable
// (`led := ledger.Ledger{...}; led.Write(...)`), and `led.Write` alone
// carries no "ledger" spelling for a name-only heuristic to catch. Scanning
// every SelectorExpr for the package-qualifier "ledger" catches the
// construction site instead, which every real usage in this module has
// exactly once per function.
//
// SAME FUNCTION-BODY-LEVEL CAVEAT as lock_discipline_test.go: this asks
// whether the three signals co-occur ANYWHERE in one top-level function, not
// whether the ownership record actually corresponds to what the write
// removed or added. It cannot prove the ledger records the RIGHT names, only
// that some ledger or marker call exists in the same function as a managed
// write.
//
// KNOWN BLIND SPOTS:
//
//   - Exactly lock_discipline_test.go's blind spot 4: a managed write split
//     across a caller (which owns the ledger read/write) and a callee (which
//     does the byte-level edit) is invisible if the callee alone is
//     inspected — the ownership-record calls live in the OUTER function.
//     codex's addMCPServers/removeManagedMCP/removeLedgeredMCPServers are
//     all called FROM writeSettingsIn/removeSettingsIn, which is where the
//     ledger.Ledger{...} and led.Write/led.Read calls actually sit; the
//     helpers themselves have the "managed" signal but not the "ledger"
//     signal and would be false positives if the gate reached them — they
//     do not have a write signal of their own (they mutate the in-memory
//     cfg map, not the file), so they are not even candidates here. Recorded
//     for a future reader who adds a helper that DOES write directly.
//   - A ledger reference under a name that is not literally the package
//     identifier "ledger" (a dot-import, or a local package alias) is
//     invisible — this module uses neither anywhere today.
//   - A function that ledgers the WRONG thing (records names it did not
//     actually write, or omits names it did) passes this gate exactly as
//     readily as one that gets it right — this is a co-occurrence gate, not
//     a correctness proof.
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

// ledgerDisciplineScopes mirrors lock_discipline_test.go's scope exactly — see
// its doc for why these five packages. The two primitive files that scope
// exempts are exempt here too: the walk below asks lockDisciplineFileExempt
// directly rather than carrying its own copy of the list.
var ledgerDisciplineScopes = lockDisciplineScopes

var ledgerManagedPattern = regexp.MustCompile(`(?i)managed`)

// ledgerNamePattern catches a METHOD whose name contains "ledger" — the
// shape MCPFileConfig uses (writeLedger/readLedger/reconcileLedger/ledger())
// to keep its ledger.Ledger construction private to mcpfile.go rather than
// spelling `ledger.Ledger{...}` at every call site. See the header doc.
var ledgerNamePattern = regexp.MustCompile(`(?i)ledger`)

var ledgerMarkerOwnershipCalls = map[string]bool{
	"WriteManagedContext":   true,
	"DeliverManagedContext": true,
	"StripManagedSection":   true,
}

// ledgerDisciplineAllowed is this gate's reasoned, symbol-keyed allowlist —
// same shape and generation discipline as writeDisciplineAllowed and
// lockDisciplineAllowed.
//
// Generated by running this gate against an empty map: 9 violations
// found. 8 are heuristic false positives (blind spots already documented
// above); 1 (opencode's writeOpencodeConfig) is a REAL, ALREADY-KNOWN gap
// (R6 bypass B1's second half) this gate correctly surfaces and this batch
// does not fix.
var ledgerDisciplineAllowed = map[string]string{
	"internal/claude/commandfiles.go#WriteCommandFiles":                 "false positive, blind spot 1 (helper split across functions): delegates straight to agent.WriteManagedCommandFiles, which delegates to agent.WriteManagedPackageFiles — that is where the ledger.Ledger{...} construction and led.Write/led.Read calls actually live (packagefiles.go). WriteCommandFiles itself never spells any ownership-record signal.",
	"internal/codex/surfaces.go#NewSurfaces":                            "false positive, blind spot 1: the commands closure captured inside NewSurfaces calls agent.WriteManagedCommandFiles directly, same delegation chain as claude's WriteCommandFiles above — the ledger reference lives in packagefiles.go, not here.",
	"internal/kiro/capabilities.go#WriteCommandFiles":                   "false positive, blind spot 1: identical shape to claude's WriteCommandFiles — delegates to agent.WriteManagedCommandFiles.",
	"internal/opencode/commandfiles.go#WriteCommandFiles":               "false positive, blind spot 1: identical shape to claude's WriteCommandFiles — delegates to agent.WriteManagedCommandFiles.",
	"internal/opencode/settings.go#writeOpencodeConfig":                 "REAL, ALREADY-KNOWN GAP, not a heuristic false positive: writeOpencodeConfig is the live chat/interactive overlay's read-modify-write (model+mcp+read-only permission) — it calls applyManaged (the managed-subset signal) and saveOpencodeConfig (the write signal) but references no ledger and writes no marker, because this overlay's ownership model is TRANSIENT snapshot/restore (snapshotOpencodeConfig), not a persisted record. config-patching-review.md bypass B1's second half, left open by its own text: \"nothing on disk survives a crash to say so\" — recommendation R1 names the fix (a `ledger.Surface(\"opencode.overlay\")` recording the snapshot path, ~half a day) as a separate, not-yet-decided piece of work. Not in this batch's units.",
	"internal/opencode/settings.go#registerSkillsPath":                  "false positive: reconciles a SINGLE well-known constant (opencodeSkillDir) via stripManagedSkillPath's direct value comparison, not a variable set of names — there is nothing to ENUMERATE the way a ledger exists to enumerate, the same reason claude's well-known agent.MCPServerName check needs no ledger entry of its own (claude.go's SCM marker covers the variable set; the constant is recognized structurally).",
	"internal/opencode/settings.go#unregisterSkillsPath":                "false positive: converse of registerSkillsPath above, same single-well-known-value reasoning.",
	"internal/opencode/settings.go#OpencodeWriter.WriteContext":         "false positive: reconciles the single well-known opencodeContextFile path via applyManaged/stripManagedInstructions, same single-well-known-value reasoning as registerSkillsPath.",
	"internal/shared/agent/managedcontext.go#writeManagedContextLocked": "false positive: the in-file-marker mechanism IS implemented here (ManagedContextBegin/ManagedContextEnd construction, splitManagedSection), which is its own ownership record by design — but this gate's marker-call signal only recognizes the EXPORTED entry points (WriteManagedContext/DeliverManagedContext/StripManagedSection), not the private splitManagedSection helper or the inline marker-constant construction actually used here. Same root cause as lock_discipline_test.go's identical entry for this symbol (blind spot 4: helper split out of the caller's lock/record).",
}

type ledgerDisciplineViolation struct {
	file   string
	symbol string
	line   int
}

func (v ledgerDisciplineViolation) key() string { return v.file + "#" + v.symbol }

func scanLedgerDiscipline(t *testing.T) []ledgerDisciplineViolation {
	t.Helper()
	root := moduleRoot(t)
	fset := token.NewFileSet()
	var out []ledgerDisciplineViolation
	var filesScanned int

	for _, scope := range ledgerDisciplineScopes {
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
			out = append(out, scanFileForLedgerDiscipline(fset, f, rel)...)
			return nil
		})
		if err != nil {
			t.Fatalf("walk %s: %v", scope, err)
		}
	}
	if filesScanned < 20 {
		t.Fatalf("scanned only %d non-test files under %v — the walk is broken, not the tree", filesScanned, ledgerDisciplineScopes)
	}
	return out
}

func scanFileForLedgerDiscipline(fset *token.FileSet, f *ast.File, rel string) []ledgerDisciplineViolation {
	var out []ledgerDisciplineViolation
	for _, decl := range f.Decls {
		d, ok := decl.(*ast.FuncDecl)
		if !ok || d.Body == nil {
			continue
		}
		hasWrite, hasManaged, hasRecord := false, false, false
		var line int
		ast.Inspect(d.Body, func(n ast.Node) bool {
			switch node := n.(type) {
			case *ast.CallExpr:
				name := calleeName(node)
				if name == "" {
					return true
				}
				// Independent checks, deliberately NOT a switch/case: a name
				// like "WriteManagedContext" is simultaneously a write
				// primitive, a managed-subset signal, AND its own ownership
				// record — an exclusive switch would only ever credit the
				// first matching case and silently miss the other two.
				if lockSavePattern.MatchString(name) || lockWritePrimitives[name] {
					hasWrite = true
					if line == 0 {
						line = fset.Position(node.Pos()).Line
					}
				}
				if ledgerMarkerOwnershipCalls[name] {
					hasRecord = true
				}
				if ledgerManagedPattern.MatchString(name) {
					hasManaged = true
				}
				// A METHOD named *Ledger (writeLedger, readLedger,
				// reconcileLedger, or the bare accessor `ledger()`) is the
				// ownership record too, even though its RECEIVER is not the
				// package identifier "ledger" — MCPFileConfig wraps its own
				// ledger.Ledger construction behind exactly these names
				// (mcpfile.go), so the package-qualifier selector check
				// below never fires inside WriteServers/RemoveServers
				// themselves; this call-name check is what catches it.
				if ledgerNamePattern.MatchString(name) {
					hasRecord = true
				}
			case *ast.SelectorExpr:
				if pkg, ok := node.X.(*ast.Ident); ok && pkg.Name == "ledger" {
					hasRecord = true
				}
				// SCM is claude's THIRD ownership mechanism (in ADDITION to
				// the sidecar ledger and in-file markers): an ownership
				// marker carried as a FIELD on each managed entry itself
				// (claudeCodeMCPConfig's server.SCM, claudeCodeHook.SCM) —
				// self-describing entries rather than a sidecar record. A
				// bare field-name match is coarser than the other two
				// signals (it cannot tell a real marker field from an
				// unrelated identically-named one), but no other "SCM"
				// identifier exists in this module's scoped packages today.
				if node.Sel.Name == "SCM" {
					hasRecord = true
				}
			}
			return true
		})
		if hasWrite && hasManaged && !hasRecord {
			out = append(out, ledgerDisciplineViolation{file: rel, symbol: funcSymbol(d), line: line})
		}
	}
	return out
}

// TestArch_LedgerDiscipline_ManagedWritersRecordOwnership is the gate: every
// managed-subset writer in the scoped packages must reference the
// internal/shared/ledger package or the in-file-marker mechanism somewhere
// in its own body, or be named (with a reason) in ledgerDisciplineAllowed.
func TestArch_LedgerDiscipline_ManagedWritersRecordOwnership(t *testing.T) {
	violations := scanLedgerDiscipline(t)
	sort.Slice(violations, func(i, j int) bool {
		if violations[i].file != violations[j].file {
			return violations[i].file < violations[j].file
		}
		return violations[i].line < violations[j].line
	})

	for _, v := range violations {
		if why, ok := ledgerDisciplineAllowed[v.key()]; ok {
			t.Logf("allowed: %s:%d %s (%s)", v.file, v.line, v.symbol, why)
			continue
		}
		t.Errorf("%s:%d %s writes a MANAGED SUBSET of a file but never references internal/shared/ledger or "+
			"the in-file marker mechanism (WriteManagedContext/DeliverManagedContext/StripManagedSection) — "+
			"a writer that cannot tell what it owns cannot withdraw exactly that later. If this is a reviewed "+
			"exception (or a heuristic false-positive — see this file's blind-spot list), add %q to "+
			"ledgerDisciplineAllowed in tests/arch/ledger_discipline_test.go naming why.",
			v.file, v.line, v.symbol, v.key())
	}
}

// TestArch_LedgerDisciplineAllowlist_IsLive is the staleness check, same
// shape as its write- and lock-discipline siblings.
func TestArch_LedgerDisciplineAllowlist_IsLive(t *testing.T) {
	violations := scanLedgerDiscipline(t)
	live := make(map[string]bool, len(violations))
	for _, v := range violations {
		live[v.key()] = true
	}

	keys := make([]string, 0, len(ledgerDisciplineAllowed))
	for k := range ledgerDisciplineAllowed {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	for _, k := range keys {
		if !live[k] {
			t.Errorf("ledgerDisciplineAllowed allows %q (%s) but the scan found no unrecorded managed write "+
				"there anymore — delete the entry, or it will silently exempt whatever regresses at that "+
				"symbol next", k, ledgerDisciplineAllowed[k])
		}
	}
}
