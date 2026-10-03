package coord

import (
	"slices"

	"github.com/ctxloom/ctxloom/internal/core/agent"
)

// heldFailures are the turn failures a run parks itself on and its
// credential's hold releases. The runner parks only on these (HoldsFailure):
// a self-park on any other kind would be a pause nothing releases.
var heldFailures = []agent.FailureKind{agent.FailureRateLimited}

// HoldsFailure reports whether a turn that failed with kind parks its run
// until its credential's hold releases it.
func HoldsFailure(kind agent.FailureKind) bool {
	return slices.Contains(heldFailures, kind)
}
