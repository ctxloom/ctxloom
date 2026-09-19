package bundles

// "This surface is deliberately ungated" and "this surface forgot its gate" are
// different statements about content exposure, and one of them is a defect. A
// nil Authorizer spells both, which makes the defect invisible at the call site
// and resolves it to the permissive answer — content delivered that a gate would
// have withheld.
//
// AdmitAll is the spelling for the first statement. It is a VALUE a caller has
// to write, so an ungated surface declares itself and a reader of the call site
// can see the choice. Nil keeps the second meaning and only the second meaning:
// Decide withholds on it and says why.

// AdmitAll is the authorizer of a surface that decides nothing: it admits every
// exposure, which is what management and listing paths need (they resolve
// pending content precisely so a human can review, accept or stamp it).
//
// It answers ABOVE ref parsing in Decide, so an ungated surface behaves exactly
// as the nil it replaces — including for a ref nothing can address. Parsing
// first would turn every listing path into a new source of withholds.
func AdmitAll() Authorizer { return admitAll{} }

// admitAll is a named type rather than an AuthorizerFunc so Gates can recognize
// it: a closure that happens to return an admit is indistinguishable from a real
// decision, and the whole point here is that the two are distinguishable.
type admitAll struct{}

// Admit implements Authorizer. ReasonUngated names WHY it admitted — "nobody
// gates this surface" — rather than claiming a rule decided.
func (admitAll) Admit(Exposure) Verdict { return Verdict{Allow: true, Reason: ReasonUngated} }

// Gates reports whether authorizer will actually decide anything.
//
// It is the predicate the surfaces that gate CONDITIONALLY key on — the bundle
// hook and MCP extractors, which build a preimage only when something will judge
// it. False for AdmitAll alone: a nil authorizer is a fault, so it stays on the
// deciding path and reaches Decide, which withholds it loudly. Skipping on nil
// is precisely the silent admission this seam exists to end.
func Gates(authorizer Authorizer) bool {
	_, ungated := authorizer.(admitAll)
	return !ungated
}
