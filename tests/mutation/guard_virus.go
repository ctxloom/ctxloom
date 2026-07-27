//go:build mutation

package mutation

import (
	"bytes"
	"go/ast"
	"go/printer"
	"go/token"
	"sort"
	"testing"

	"github.com/gtramontina/ooze/viruses"
)

// guardNegate is a custom ooze Virus that negates the boolean CONDITION of an
// `if` statement — `if C { ... }` becomes `if !(C) { ... }`.
//
// WHY IT HAD TO BE WRITTEN. ooze's default virus set (and gremlins') mutates
// only COMPARISONS and ARITHMETIC. But five of the seven steps of the
// EffectiveTrust cascade are plain boolean guards:
//
//  1. REJECTED       if records.Rejected(req.Ref, req.Payload)
//  2. RETRACTED      if retracted, _ := retraction.Retracted(req.Ref); retracted
//  3. LOCAL          if req.Ref.IsLocal
//  4. BUILTIN        if req.Ref.IsBuiltin
//  6. APPROVED       if records.Approved(req.Ref, req.Payload, req.Form)
//
// None of those contain a binary comparison, so the stock viruses emit ZERO
// mutants for them (measured: of trust.go's 114 stock mutants, not one lands
// on steps 1,2,3,4,6). Only step 5's `req.Signer != "" && req.Signer !=
// trust.BuiltinSigner` is a comparison — and its 4 mutants were all killed.
//
// That means a clean "no survivors in the cascade" from the stock run is NOT
// evidence the cascade is covered: it is evidence the tool never attacked it.
// This virus attacks it directly. Negating a guard is the sharpest possible
// probe of a first-match-wins deny cascade: it both lets through what the step
// exists to stop AND stops what it exists to let through.
//
// SCOPE: it fires ONLY on the exact guard expressions named in cascadeGuards
// below (matched by rendering the condition back to source text), never on
// every `if` in the file — trust.go has ~60 `if`s, and mutating all of them
// would cost hours to say nothing about the cascade. This is deliberately a
// scalpel, not the stock shotgun.
type guardNegate struct {
	// targets holds the rendered source text of each condition to negate.
	targets map[string]string // rendered condition -> human label
	// matched records which targets' labels actually fired during the
	// Incubate walk. Nothing counted matches before U164-F01 and nothing
	// asserted a minimum: a refactor of any of the five conditions
	// (extracting a variable, inverting a guard, renaming a parameter,
	// reordering arguments) silently drops that step's matches to zero, the
	// source-text key in targets simply never matches again, and the
	// mutation run then reports CLEAN — indistinguishable from "this step
	// is covered". AssertAllTargetsMatched turns that silent zero into a
	// loud failure.
	matched map[string]bool
}

// cascadeGuards are the EffectiveTrust decision steps that the stock viruses
// cannot reach. The key is the condition EXACTLY as go/printer renders it; the
// value is the cascade step it implements, used as the mutant's name so a
// survivor is self-describing in the report.
func newGuardNegate() *guardNegate {
	return &guardNegate{
		targets: map[string]string{
			"records.Rejected(req.Ref, req.Payload)": "cascade step 1 REJECTED",
			"retracted":                              "cascade step 2 RETRACTED",
			"req.Ref.IsLocal":                        "cascade step 3 LOCAL",
			"req.Ref.IsBuiltin":                      "cascade step 4 BUILTIN",
			"records.Approved(req.Ref, req.Payload, req.Form)": "cascade step 6 APPROVED",
		},
		matched: map[string]bool{},
	}
}

// render turns an ast.Expr back into its source text so it can be matched
// against cascadeGuards. A fresh empty FileSet is fine here: we only need the
// expression's own text, never its position.
func render(expr ast.Expr) string {
	var buf bytes.Buffer
	if err := printer.Fprint(&buf, token.NewFileSet(), expr); err != nil {
		return ""
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
// targets fired at least once during the AST walk (U164-F01). Call it AFTER
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
