package archlint

import (
	"go/ast"
	"go/token"
	"regexp"

	"golang.org/x/tools/go/analysis"
)

// ledgerDisciplineScopes and ledgerDisciplineExemptFiles mirror the lock
// rule's scope exactly: the same engine writers and the same two primitives.
var ledgerDisciplineScopes = lockDisciplineScopes

var ledgerDisciplineExemptFiles = lockDisciplineExemptFiles

var ledgerManagedPattern = regexp.MustCompile(`(?i)managed`)

// ledgerNamePattern catches a METHOD whose name contains "ledger", the shape
// used to keep a ledger.Ledger construction private to one file rather than
// spelling it at every call site.
var ledgerNamePattern = regexp.MustCompile(`(?i)ledger`)

var ledgerMarkerOwnershipCalls = map[string]bool{
	"WriteManagedContext":   true,
	"DeliverManagedContext": true,
	"StripManagedSection":   true,
}

// LedgerDisciplineAnalyzer enforces that a writer of a MANAGED subset of
// someone else's config file records what it owns.
//
// ctxloom writes into files an engine also owns. Without an ownership record —
// a sidecar ledger, an in-file marker pair, or a per-entry marker field —
// nothing on disk distinguishes ctxloom's entries from the user's, so a later
// reconcile cannot remove exactly what it added. The failure is silent and
// arrives as the user's own config being eaten.
//
// A function that writes AND touches a managed subset must therefore reference
// one of the three ownership mechanisms.
var LedgerDisciplineAnalyzer = &analysis.Analyzer{
	Name: "archledgerdiscipline",
	Doc:  "writers of a managed config subset must record ownership",
	Run:  runLedgerDiscipline,
}

func runLedgerDiscipline(pass *analysis.Pass) (any, error) {
	if SkipPass(pass) {
		return nil, nil
	}
	dir := PkgDir(pass)
	if dir == "" || !inScopes(dir, ledgerDisciplineScopes) {
		return nil, nil
	}
	seen := map[string]bool{}
	for _, f := range ProdFiles(pass) {
		rel := FileRel(pass, f)
		if ledgerDisciplineExemptFiles[rel] {
			continue
		}
		for _, decl := range f.Decls {
			d, ok := decl.(*ast.FuncDecl)
			if !ok || d.Body == nil {
				continue
			}
			hasWrite, hasManaged, hasRecord := false, false, false
			var at token.Pos
			ast.Inspect(d.Body, func(n ast.Node) bool {
				switch node := n.(type) {
				case *ast.CallExpr:
					name := CalleeName(node)
					if name == "" {
						return true
					}
					// Independent checks, deliberately not a switch: a name
					// like WriteManagedContext is simultaneously a write
					// primitive, a managed-subset signal, AND its own
					// ownership record. An exclusive switch would credit only
					// the first match and silently miss the other two.
					if lockSavePattern.MatchString(name) || lockWritePrimitives[name] {
						hasWrite = true
						if at == token.NoPos {
							at = node.Pos()
						}
					}
					if ledgerMarkerOwnershipCalls[name] {
						hasRecord = true
					}
					if ledgerManagedPattern.MatchString(name) {
						hasManaged = true
					}
					if ledgerNamePattern.MatchString(name) {
						hasRecord = true
					}
				case *ast.SelectorExpr:
					if pkg, ok := node.X.(*ast.Ident); ok && pkg.Name == "ledger" {
						hasRecord = true
					}
					// An ownership marker carried as a FIELD on each managed
					// entry: self-describing entries rather than a sidecar
					// record. Coarser than the other two signals, but no
					// unrelated SCM identifier exists in the scoped packages.
					if node.Sel.Name == "SCM" {
						hasRecord = true
					}
				}
				return true
			})
			if !hasWrite || !hasManaged || hasRecord {
				continue
			}
			sym := FuncSymbol(d)
			key := rel + "#" + sym
			seen[key] = true
			if _, ok := ledgerDisciplineAllowed[key]; ok {
				continue
			}
			pass.Reportf(at,
				"%s writes a managed subset of a config file without referencing any ownership record — "+
					"nothing on disk then distinguishes ctxloom's entries from the user's, so a later "+
					"reconcile cannot remove exactly what it added. Use a ledger, an in-file marker pair, "+
					"or a per-entry marker field. If this is a deliberate, reviewed exception, add %q to "+
					"ledgerDisciplineAllowed in internal/archlint/ledgerdiscipline.go naming why it stands.",
				sym, key)
		}
	}
	reportStaleAllowlist(pass, ledgerDisciplineAllowed, analyzedFiles(pass), seen, "ledgerDisciplineAllowed",
		"internal/archlint/ledgerdiscipline.go")
	return nil, nil
}

// ledgerDisciplineAllowed is the reasoned, symbol-keyed baseline.
var ledgerDisciplineAllowed = map[string]string{
	"internal/claude/commandfiles.go#WriteCommandFiles":                 "false positive, blind spot 1 (helper split across functions): delegates straight to agent.WriteManagedCommandFiles, which delegates to agent.WriteManagedPackageFiles — that is where the ledger.Ledger{...} construction and led.Write/led.Read calls actually live (packagefiles.go). WriteCommandFiles itself never spells any ownership-record signal.",
	"internal/shared/agent/managedcontext.go#writeManagedContextLocked": "false positive: the in-file-marker mechanism IS implemented here (ManagedContextBegin/ManagedContextEnd construction, splitManagedSection), which is its own ownership record by design — but this gate's marker-call signal only recognizes the EXPORTED entry points (WriteManagedContext/DeliverManagedContext/StripManagedSection), not the private splitManagedSection helper or the inline marker-constant construction actually used here. Same root cause as lock_discipline_test.go's identical entry for this symbol (blind spot 4: helper split out of the caller's lock/record).",
}
