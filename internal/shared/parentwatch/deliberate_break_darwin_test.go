//go:build darwin

package parentwatch

import "testing"

// Deliberately broken (uncounted-coagulant): proves the macOS job fails on a
// darwin-only test. Reverted by the next commit.
func TestDeliberateBreak_DarwinOnly(t *testing.T) {
	t.Fatal("deliberate darwin-only failure: the macOS job must go red")
}
