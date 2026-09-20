package spawn

import (
	"testing"

	"github.com/ctxloom/ctxloom/internal/adapters/operations"
	"github.com/ctxloom/ctxloom/internal/shared/report"
	"github.com/ctxloom/ctxloom/internal/shared/strictness"
)

// newSpawner is New with the concrete type in hand, for the tests that read
// the spawner's own state.
func newSpawner(rep report.Reporter, app *operations.App, projectDir string, starter StarterFunc) *spawner {
	return New(rep, app, projectDir, starter).(*spawner)
}

func termRep() report.Reporter { return report.To(strictness.Sink("ctxloom")) }

// resetStrictness clears the process-wide strictness findings a test's
// launches record, so one test's refusal never reads as the next test's.
func resetStrictness(t *testing.T) {
	t.Helper()
	strictness.Reset()
	t.Cleanup(strictness.Reset)
}
