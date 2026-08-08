package transcript

import (
	"github.com/ctxloom/ctxloom/internal/shared/agent"
	"github.com/ctxloom/ctxloom/internal/transcript/policy"
)

// Filtered wraps rec so every event passes through p before being recorded.
//
// The DECORATOR is the point. Record is the one seam both write paths already
// go through — the vendor importers (internal/transcript/importer/*) and the
// live structured-chat tee — so wrapping it puts the same policy on both for
// free, keeps every adapter pure, and leaves exactly one place to look to
// know what a transcript omits. Applying the policy inside each adapter
// instead would put the same decision in four files and let them drift, which
// is the shape that produced the losses this layer was written to stop.
//
// Not to be confused with RawPolicy (see WithRawPolicy), which governs
// whether a record keeps its IR3 raw side channel. This one governs which
// canonical tool-content blocks survive.
func Filtered(rec Recorder, p policy.Policy) Recorder {
	return &filteredRecorder{inner: rec, policy: p}
}

type filteredRecorder struct {
	inner  Recorder
	policy policy.Policy
}

func (f *filteredRecorder) Record(ev agent.ChatEvent) error {
	return f.inner.Record(f.policy.Apply(ev))
}

func (f *filteredRecorder) Close() error { return f.inner.Close() }
