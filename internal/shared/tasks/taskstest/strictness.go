package taskstest

import (
	"strings"
	"testing"

	"github.com/ctxloom/ctxloom/internal/shared/clidiag"
	"github.com/ctxloom/ctxloom/internal/shared/strictness"
)

// Strictness gives the calling test a clean fail-loudly ledger and a clean
// dedup memory, and captures the diagnostic stream so the test can read what
// the user was told. The MODE is not a process fact any more: a test that
// wants degraded behaviour sets it on the TaskContext (or the binary's flag)
// it hands the code under test.
func Strictness(t *testing.T) *strings.Builder {
	t.Helper()
	reset := func() {
		strictness.Reset()
		clidiag.ResetWarnOnce()
	}
	reset()
	var diag strings.Builder
	restore := clidiag.SetSink(&diag)
	t.Cleanup(func() {
		restore()
		reset()
	})
	return &diag
}
