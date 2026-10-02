package archlint

import (
	"go/ast"
	"go/token"
	"regexp"

	"golang.org/x/tools/go/analysis"

	"github.com/ctxloom/ctxloom/internal/shared/archrules"
)

// ledgerDisciplineScopes and ledgerDisciplineExemptFiles are the lock rule's
// own: the same engine writers and the same primitives.
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
// one of the three ownership mechanisms. Like the lock rule it is name-based
// and per-function, so a writer that delegates its record to a helper it
// calls — or implements the marker mechanism inline rather than through its
// exported entry points — reads as a violation; such writers are named in
// archrules.LedgerDisciplineAllowed.
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
	if dir == "" || !archrules.UnderAny(dir, ledgerDisciplineScopes) {
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
					if isWriteCall(pass.TypesInfo, node, name) {
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
			if _, ok := archrules.LedgerDisciplineAllowed[key]; ok {
				continue
			}
			pass.Reportf(at,
				"%s writes a managed subset of a config file without referencing any ownership record — "+
					"nothing on disk then distinguishes ctxloom's entries from the user's, so a later "+
					"reconcile cannot remove exactly what it added. Use a ledger, an in-file marker pair, "+
					"or a per-entry marker field. If this is a deliberate, reviewed exception, add %q to "+
					"archrules.LedgerDisciplineAllowed naming why it stands.",
				sym, key)
		}
	}
	reportStaleAllowlist(pass, archrules.LedgerDisciplineAllowed, analyzedFiles(pass), seen, "archrules.LedgerDisciplineAllowed")
	return nil, nil
}
