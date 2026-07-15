//go:build mutation

package mutation

import (
	"bytes"
	"go/ast"
	"go/printer"
	"go/token"

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
