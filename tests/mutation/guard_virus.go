//go:build mutation

package mutation

import (
	"bytes"
	"fmt"
	"go/ast"
	"go/printer"
	"go/token"
	"sort"
	"testing"

	"github.com/gtramontina/ooze/viruses"
)

// cascadeGuards are the trust-cascade decision steps (composite's
// (*authorizer).Admit and the helpers it asks) that the stock viruses cannot
// reach — the guard expressions guardNegate fires ONLY on (matched by
// rendering the condition back to source text), never on every `if` in the
// file. The key is the condition EXACTLY as go/printer renders it; the value
// is the cascade step it implements, used as the mutant's name so a survivor
// is self-describing in the report.
//
// The keys are exact on purpose, and are never relaxed into something that
// would match more than one shape: an exact miss is the only thing that makes
// a refactor of the cascade visible (AssertAllTargetsMatched,
// TestGuardNegate_MatchesRealTrustGo).
//
// FIRST PARTY is keyed on localReason's posture test, not on Admit's
// `if reason, ok := localReason(e.Read); ok`: a bare "ok" key would also fire
// on fault's type assertion, which is not a cascade step. The posture test is
// a comparison, so the stock comparison viruses reach it too and this virus's
// mutant for it is largely redundant. It is kept anyway: dropping it would
// make the cascade census silently partial, and the next split or merge of
// the first-party arms has to be noticed by the same exact-match miss.
var cascadeGuards = map[string]string{
	"a.records.Rejected(e.Ref(), e.Bytes)":         "cascade step REJECTED",
	"retracted":                                    "cascade step RETRACTED",
	"read.TrustCtx() != bundles.TrustCtxLocal":     "cascade step FIRST PARTY",
	"a.records.Approved(e.Ref(), e.Bytes, e.Form)": "cascade step APPROVED",
}

// guardNegate is a custom ooze Virus that negates the boolean CONDITION of an
// `if` statement — `if C { ... }` becomes `if !(C) { ... }`.
//
// WHY IT HAD TO BE WRITTEN. ooze's default virus set (and gremlins') mutates
// only COMPARISONS and ARITHMETIC. But most steps of the trust cascade are
// plain boolean guards — a method call or a bool bound in an if-init — with no
// binary comparison in them, so the stock viruses emit ZERO mutants for those
// steps. (The trusted-signer step, `signer != ""`, is a comparison and needs
// no help.)
//
// That means a clean "no survivors in the cascade" from the stock run is NOT
// evidence the cascade is covered: it is evidence the tool never attacked it.
// This virus attacks it directly. Negating a guard is the sharpest possible
// probe of a first-match-wins deny cascade: it both lets through what the step
// exists to stop AND stops what it exists to let through.
//
// SCOPE: it fires ONLY on the exact guard expressions named in cascadeGuards
// above (matched by rendering the condition back to source text), never on
// every `if` in the file — mutating all of them would cost hours to say
// nothing about the cascade. This is deliberately a scalpel, not the stock
// shotgun.
type guardNegate struct {
	// targets holds the rendered source text of each condition to negate
	// (cascadeGuards, captured per-instance so a future caller could pass a
	// different set without touching package state).
	targets map[string]string
	// matched records which targets' labels actually fired during the
	// Incubate walk. Nothing counted matches before this and nothing
	// asserted a minimum: a refactor of any of the conditions
	// (extracting a variable, inverting a guard, renaming a parameter,
	// reordering arguments) silently drops that step's matches to zero, the
	// source-text key in targets simply never matches again, and the
	// mutation run then reports CLEAN — indistinguishable from "this step
	// is covered". AssertAllTargetsMatched turns that silent zero into a
	// loud failure.
	matched map[string]bool
}

func newGuardNegate() *guardNegate {
	return &guardNegate{
		targets: cascadeGuards,
		matched: map[string]bool{},
	}
}

// render turns an ast.Expr back into its source text so it can be matched
// against cascadeGuards. A fresh empty FileSet is fine here: we only need the
// expression's own text, never its position.
//
// printer.Fprint's error used to be swallowed, returning "" -- an
// unprintable expression then fell through the targets[""] lookup as
// "not a target", indistinguishable from an ordinary non-cascade if, and
// silently lowering the mutation floor this virus exists to enforce. A
// well-formed *ast.Expr produced by go/parser failing to print is not a
// condition this tool should paper over; panic names it instead.
func render(expr ast.Expr) string {
	var buf bytes.Buffer
	if err := printer.Fprint(&buf, token.NewFileSet(), expr); err != nil {
		panic(fmt.Sprintf("guardNegate.render: printer.Fprint failed on a parsed expression: %v", err))
	}
	return buf.String()
}

func (v *guardNegate) Incubate(node ast.Node) []*viruses.Infection {
	stmt, matches := node.(*ast.IfStmt)
	if !matches || stmt.Cond == nil {
		return nil
	}

	label, matches := v.targets[render(stmt.Cond)]
	if !matches {
		return nil
	}
	v.matched[label] = true

	original := stmt.Cond
	negated := &ast.UnaryExpr{Op: token.NOT, X: &ast.ParenExpr{X: original}}

	return []*viruses.Infection{
		viruses.NewInfection(
			"Guard Negate ("+label+")",
			func() { stmt.Cond = negated },
			func() { stmt.Cond = original },
		),
	}
}

// missingTargets returns the labels of every cascadeGuards entry that never
// matched during the Incubate walk, sorted for a stable report. Empty means
// every guard this virus exists to attack was actually found and mutated at
// least once.
func (v *guardNegate) missingTargets() []string {
	var missing []string
	for _, label := range v.targets {
		if !v.matched[label] {
			missing = append(missing, label)
		}
	}
	sort.Strings(missing)
	return missing
}

// AssertAllTargetsMatched fails t unless every cascade guard this virus
// targets fired at least once during the AST walk. Call it AFTER
// ooze.Release has walked the whole file — a missing target means a refactor
// of trust.go silently moved that guard's rendered source text out from
// under cascadeGuards' literal keys, so this virus attacked nothing for that
// step and the mutation run reported clean with no attack having happened at
// all.
func (v *guardNegate) AssertAllTargetsMatched(t *testing.T) {
	t.Helper()
	for _, label := range v.missingTargets() {
		t.Errorf("guardNegate never matched cascade guard %q — its rendered source text no longer appears in trust.go (or trust.go was never walked); the mutation run attacked NOTHING for this step, which is indistinguishable from a clean pass", label)
	}
}
