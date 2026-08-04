package backends

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

// recordingAuthorizer admits (or withholds) everything and records the ref STRING
// each exposure was addressed by, so a test can assert the ref shape the choke
// composes — the property several of these tests exist for.
func recordingAuthorizer(admit bool, gotRefs *[]string) bundles.Authorizer {
	return bundles.AuthorizerFunc(func(e bundles.Exposure) bundles.Verdict {
		*gotRefs = append(*gotRefs, e.RefString())
		if admit {
			return bundles.Verdict{Allow: true, Reason: bundles.ReasonLocal}
		}
		return bundles.Verdict{Reason: bundles.ReasonPending}
	})
}

// hashAuthorizer admits exactly the payload whose hash is want — the shape a
// countersignature has: one approval covers one set of bytes.
func hashAuthorizer(want string) bundles.Authorizer {
	return bundles.AuthorizerFunc(func(e bundles.Exposure) bundles.Verdict {
		if bundles.HashPayload(e.Bytes) == want {
			return bundles.Verdict{Allow: true, Reason: bundles.ReasonApproved}
		}
		return bundles.Verdict{Reason: bundles.ReasonPending}
	})
}
