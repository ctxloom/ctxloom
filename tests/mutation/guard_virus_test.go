//go:build mutation

package mutation

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"testing"
)

// parseIfStmt builds a minimal *ast.IfStmt with the given condition source
// text, for feeding directly to guardNegate.Incubate without running a real
// ooze mutation campaign (which rebuilds the ctxloom binary and drives the
// acceptance suite per mutant — far too expensive for a unit test).
func parseIfStmt(t *testing.T, cond string) *ast.IfStmt {
	t.Helper()
	src := "package p\nfunc f() { if " + cond + " { } }"
	file, err := parser.ParseFile(token.NewFileSet(), "", src, 0)
	if err != nil {
		t.Fatalf("parse %q: %v", cond, err)
	}
	fn := file.Decls[0].(*ast.FuncDecl)
	return fn.Body.List[0].(*ast.IfStmt)
}

// TestGuardNegate_MissingTargets_ReportsAnUnfiredGuard pins that, before
// tracking matches, nothing recorded which of the cascade guards Incubate
// actually found, so a guard whose rendered source text drifted (a refactor
// extracting a variable, inverting the condition, renaming a parameter,
// MERGING TWO ARMS INTO ONE — which is what really happened to steps 3 and
// 4; see cascadeGuards' drift note) silently stopped being attacked with no
// signal anywhere.
func TestGuardNegate_MissingTargets_ReportsAnUnfiredGuard(t *testing.T) {
	v := newGuardNegate()
	// Exercise all but one target — simulating a refactor that changed one
	// guard's rendered source text out from under cascadeGuards.
	v.Incubate(parseIfStmt(t, "records.Rejected(req.Ref, req.Payload)"))
	v.Incubate(parseIfStmt(t, "retracted"))
	v.Incubate(parseIfStmt(t, "req.Posture == bundles.TrustCtxLocal"))
	// "records.Approved(req.Ref, req.Payload, req.Form)" deliberately never
	// exercised here.

	missing := v.missingTargets()
	if len(missing) != 1 || missing[0] != "cascade step 6 APPROVED" {
		t.Fatalf("missingTargets = %v, want [\"cascade step 6 APPROVED\"]", missing)
	}
}

// TestGuardNegate_MissingTargets_EmptyWhenEveryGuardFired is the ordinary
// (real, unrefactored trust.go) case: every one of the five conditions is
// found and missingTargets reports nothing.
func TestGuardNegate_MissingTargets_EmptyWhenEveryGuardFired(t *testing.T) {
	v := newGuardNegate()
	for cond := range v.targets {
		v.Incubate(parseIfStmt(t, cond))
	}
	if missing := v.missingTargets(); len(missing) != 0 {
		t.Fatalf("missingTargets = %v, want none", missing)
	}
}

// TestGuardNegate_MatchesRealTrustGo walks the REAL internal/operations/
// trust.go (not a synthetic snippet) and confirms every one of the
// cascade guards' rendered source text still matches — a cheap
// (no-rebuild, no-subprocess) sanity check that the census-time keys
// haven't already drifted out from under a since-landed refactor, without
// paying for a full ooze mutation campaign (which rebuilds the ctxloom
// binary and drives the acceptance suite per mutant).
func TestGuardNegate_MatchesRealTrustGo(t *testing.T) {
	root := repoRoot(t)
	path := filepath.Join(root, trustCascadeTarget.SourceRelPath)
	file, err := parser.ParseFile(token.NewFileSet(), path, nil, 0)
	if err != nil {
		t.Fatalf("parse %s: %v", path, err)
	}

	v := newGuardNegate()
	ast.Inspect(file, func(n ast.Node) bool {
		if n != nil {
			v.Incubate(n)
		}
		return true
	})

	if missing := v.missingTargets(); len(missing) != 0 {
		t.Fatalf("guardNegate found no match in the real trust.go for: %v — a refactor likely changed one of these guards' rendered source text; update cascadeGuards in guard_virus.go", missing)
	}
}

// TestGuardNegate_Incubate_IgnoresNonTargetGuards confirms an ordinary,
// non-cascade `if` (trust.go has ~60 of them) is left alone: no mutant, no
// match recorded.
func TestGuardNegate_Incubate_IgnoresNonTargetGuards(t *testing.T) {
	v := newGuardNegate()
	infections := v.Incubate(parseIfStmt(t, "someUnrelatedCondition"))
	if len(infections) != 0 {
		t.Fatalf("expected no infection for a non-target guard, got %d", len(infections))
	}
	if len(v.matched) != 0 {
		t.Fatalf("expected no matches recorded, got %v", v.matched)
	}
}
