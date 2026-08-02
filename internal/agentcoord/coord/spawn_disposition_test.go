package coord

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

// agent_run answered "spawned <harp> (engine X, runtime container)"
// BEFORE any spawn had been attempted — the disposition asserted a fact the
// coordinator did not yet have, so a launch that had already failed by the
// time the answer was composed still reported as spawned.
//
// The SUCCESS wording is deliberately byte-identical to what shipped (an
// out-of-repo consumer may match on it); only the failure case reads
// differently.
func TestSpawnDisposition(t *testing.T) {
	out := &RunOutcome{Harp: "tidy-blue-otter", Engine: "claude", Runtime: "container"}

	t.Run("success is unchanged, byte for byte", func(t *testing.T) {
		assert.Equal(t, "spawned tidy-blue-otter (engine claude, runtime container)",
			spawnDisposition(out, "container", ""))
	})

	t.Run("queued and degraded suffixes are unchanged", func(t *testing.T) {
		q := &RunOutcome{Harp: "tidy-blue-otter", Engine: "claude", Queued: true, Degraded: []string{"no reach-back"}}
		assert.Equal(t, "spawned tidy-blue-otter (engine claude, runtime host); queued behind the execution cap; degraded: no reach-back",
			spawnDisposition(q, "host", ""))
	})

	t.Run("a launch that already failed does not report as spawned", func(t *testing.T) {
		got := spawnDisposition(out, "container", CauseLaunchFailed)
		assert.NotContains(t, got, "spawned ", "a failed launch must not claim the child was spawned")
		assert.Contains(t, got, "tidy-blue-otter")
		assert.True(t, strings.Contains(strings.ToLower(got), "fail"),
			"the failure must be named in the disposition, got %q", got)
	})
}
