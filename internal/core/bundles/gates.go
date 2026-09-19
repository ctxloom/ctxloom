package bundles

// "This surface is deliberately ungated" and "this surface forgot its gate" are
// different statements about content exposure, and one of them is a defect. A
// nil Authorizer spells only the second: Decide withholds on it and says why.
// The first is spelled by NAME at the call site — composite.Ungated() — and
// the authorizer it yields says so through this capability.

// ungatedAuthorizer is the OPTIONAL capability an authorizer that decides nothing
// exposes. It is a type assertion rather than a method on Authorizer so a
// one-expression AuthorizerFunc stays one expression.
type ungatedAuthorizer interface {
	Ungated() bool
}

// Gates reports whether authorizer will actually decide anything.
//
// It is the predicate the surfaces that gate CONDITIONALLY key on — the bundle
// hook and MCP extractors, which build a preimage only when something will judge
// it. False only for an authorizer that declares itself ungated: a nil
// authorizer is a fault, so it stays on the deciding path and reaches Decide,
// which withholds it loudly. Skipping on nil is precisely the silent admission
// this seam exists to end.
func Gates(authorizer Authorizer) bool {
	u, ok := authorizer.(ungatedAuthorizer)
	return !ok || !u.Ungated()
}
