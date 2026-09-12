package taskstest

import (
	"strings"
	"testing"

	"github.com/ctxloom/ctxloom/internal/shared/clidiag"
	"github.com/ctxloom/ctxloom/internal/shared/strictness"
)

// Strictness puts the process into strict (degraded=false) or degraded mode
// for the duration of the test, with the findings log, the FailOnce dedup
// memory and clidiag's WarnOnce memory all cleared on entry and on cleanup,
// and returns a buffer collecting every diagnostic line clidiag prints
// meanwhile (the sink is restored on cleanup).
//
// Clearing the once-memories is what makes the helper reusable within one
// process: a refusal a PREVIOUS test already printed would otherwise be
// deduped away and a sink assertion would see nothing (see
// clidiag.ResetWarnOnce). The mode is process-global, so the calling test
// must not be parallel.
func Strictness(t *testing.T, degraded bool) *strings.Builder {
	t.Helper()
	reset := func(mode bool) {
		strictness.Reset()
		clidiag.ResetWarnOnce()
		strictness.SetDegraded(mode)
	}
	reset(degraded)
	var diag strings.Builder
	restore := clidiag.SetSink(&diag)
	t.Cleanup(func() {
		restore()
		reset(false)
	})
	return &diag
}
