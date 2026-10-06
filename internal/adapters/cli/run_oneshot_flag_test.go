package cli

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestRunOneShot_NamesTheModeNotTheOutput pins the flag that selects run's
// single-turn mode.
//
// What distinguishes the mode is that the run takes ONE turn and exits — not
// that it prints, which every mode does. The spelling matches the vocabulary
// the code already uses for it end to end (agent.CLISurfaceOneshot,
// engine.Structured, pb.ExecutionMode_ONESHOT), so a reader following the
// mode from the command line into the engine driver reads one word throughout.
func TestRunOneShot_NamesTheModeNotTheOutput(t *testing.T) {
	flags := runCmd.Flags()

	require.NotNil(t, flags.Lookup("one-shot"), "--one-shot selects the single-turn mode")

	saved := runOneShot
	t.Cleanup(func() { runOneShot = saved })
	require.NoError(t, flags.Set("one-shot", "true"))
	assert.True(t, runOneShot,
		"--one-shot must write through to runOneShot; a flag bound to nothing reads as its zero value forever")
}
