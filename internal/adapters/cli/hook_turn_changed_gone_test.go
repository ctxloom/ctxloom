package cli

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// TestHook_TurnChangedIsGone pins the retirement of `ctxloom hook
// turn-changed`. Nothing wired it — no hook configuration invoked it — and a
// machine callback that exists only "for vocabulary completeness" is a
// command whose verdict nobody reads. It stays gone: a caller that wants to
// know whether a turn changed anything reads the transcript through the
// turnchange package, not through a hidden CLI verb.
func TestHook_TurnChangedIsGone(t *testing.T) {
	for _, c := range hookCmd.Commands() {
		assert.NotEqual(t, "turn-changed", c.Name(), "hook turn-changed was deleted; do not re-register it")
	}
}
