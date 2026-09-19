package config

import (
	"testing"

	"github.com/ctxloom/ctxloom/internal/shared/strictness"
)

// resetStrictness starts a test from a clean strictness window and restores
// one after it, so a finding recorded here never leaks into the next test.
func resetStrictness(t *testing.T) {
	t.Helper()
	strictness.Reset()
	strictness.SetDegraded(false)
	t.Cleanup(func() {
		strictness.Reset()
		strictness.SetDegraded(false)
	})
}
