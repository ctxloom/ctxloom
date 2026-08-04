package config

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
