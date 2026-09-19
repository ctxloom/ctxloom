package config

import (
	"testing"

	"github.com/ctxloom/ctxloom/internal/shared/report"

	"github.com/ctxloom/ctxloom/internal/shared/strictness"
)

// resetStrictness starts a test from a clean strictness window and restores
// one after it, so a finding recorded here never leaks into the next test.
// ledgerReporter reports through the real rendering sink, so a test that
// asserts on the strictness ledger or the clidiag output sees exactly what
// the binary's user sees.
func ledgerReporter() report.Reporter { return report.To(strictness.Sink("ctxloom")) }

func resetStrictness(t *testing.T) {
	t.Helper()
	strictness.Reset()
	strictness.SetDegraded(false)
	t.Cleanup(func() {
		strictness.Reset()
		strictness.SetDegraded(false)
	})
}
