package backends

import "github.com/ctxloom/ctxloom/internal/bundles"

// testFilter is the two-valued Filter a test reaches for when it is exercising
// what happens AROUND a decision rather than the decision itself: admit
// everything, or withhold everything. The Reasons are the plainest true ones —
// a blanket admit is not claiming a provenance it checked, and a blanket
// withhold is the ordinary pending state.
func testFilter(admit bool) bundles.Filter {
	return bundles.FilterFunc(func(bundles.Exposure) bundles.Verdict {
		if admit {
			return bundles.Verdict{Admit: true, Reason: bundles.ReasonLocal}
		}
		return bundles.Verdict{Reason: bundles.ReasonPending}
	})
}

// recordingFilter admits (or withholds) everything and records the ref STRING
// each exposure was addressed by, so a test can assert the ref shape the choke
// composes — the property several of these tests exist for.
func recordingFilter(admit bool, gotRefs *[]string) bundles.Filter {
	return bundles.FilterFunc(func(e bundles.Exposure) bundles.Verdict {
		*gotRefs = append(*gotRefs, e.RefString())
		if admit {
			return bundles.Verdict{Admit: true, Reason: bundles.ReasonLocal}
		}
		return bundles.Verdict{Reason: bundles.ReasonPending}
	})
}

// hashFilter admits exactly the payload whose hash is want — the shape a
// countersignature has: one approval covers one set of bytes.
func hashFilter(want string) bundles.Filter {
	return bundles.FilterFunc(func(e bundles.Exposure) bundles.Verdict {
		if bundles.HashPayload(e.Bytes) == want {
			return bundles.Verdict{Admit: true, Reason: bundles.ReasonApproved}
		}
		return bundles.Verdict{Reason: bundles.ReasonPending}
	})
}
