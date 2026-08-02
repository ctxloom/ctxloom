package pidalive

import (
	"os"
	"os/exec"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestProbe_LiveSelfProcess_ReportsAlive pins the confident-alive case: this
// test's own process is unambiguously alive and signalable by itself.
func TestProbe_LiveSelfProcess_ReportsAlive(t *testing.T) {
	assert.Equal(t, Alive, Probe(os.Getpid()))
}

// TestProbe_ExitedChildPid_ReportsDead pins the confident-dead case with a
// REAL exited pid (never a guessed/fixed number, which risks colliding with
// a live unrelated process on a busy CI host): spawn a trivial child, wait
// for it to exit, then probe its now-free pid.
func TestProbe_ExitedChildPid_ReportsDead(t *testing.T) {
	cmd := exec.Command("true")
	require.NoError(t, cmd.Start())
	pid := cmd.Process.Pid
	require.NoError(t, cmd.Wait())

	assert.Equal(t, Dead, Probe(pid))
}

// TestState_MaybeAlive pins the fix: every current caller in this
// repo treats "cannot confirm dead" as "alive" for a reaping/claiming/
// spawn-refusal decision, because wrongly reaping/claiming/spawning a rogue
// duplicate on a false "dead" is worse than a needlessly conservative skip
// on a true "unsure". MaybeAlive is the single place that policy lives, so
// every caller shares it instead of re-deciding this direction per call
// site.
func TestState_MaybeAlive(t *testing.T) {
	assert.False(t, Dead.MaybeAlive(), "a confirmed-dead pid must never read as maybe-alive")
	assert.True(t, Alive.MaybeAlive(), "a confirmed-alive pid is maybe-alive")
	assert.True(t, Unsure.MaybeAlive(), "an unsure probe must err toward alive, not dead — the destructive direction for every current caller")
}
