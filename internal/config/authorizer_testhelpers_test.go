package config

import "github.com/ctxloom/ctxloom/internal/bundles"

// testAuthorizer is the two-valued Authorizer a test reaches for when it is exercising
// what happens AROUND a decision rather than the decision itself: admit
// everything, or withhold everything. The Reasons are the plainest true ones —
// a blanket admit is not claiming a provenance it checked, and a blanket
// withhold is the ordinary pending state.
func testAuthorizer(admit bool) bundles.Authorizer {
	return bundles.AuthorizerFunc(func(bundles.Exposure) bundles.Verdict {
		if admit {
			return bundles.Verdict{Allow: true, Reason: bundles.ReasonLocal}
		}
		return bundles.Verdict{Reason: bundles.ReasonPending}
	})
}
