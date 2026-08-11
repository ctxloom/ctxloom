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
// operations.RunOneshot, pb.ExecutionMode_ONESHOT), so a reader following the
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

// TestRunPrint_IsRetiredNotAliased holds the no-shims line. An alias would
// leave two spellings for one mode in help, in scripts, and in every future
// reader's head; breaking the old one and documenting re-spelling is this
// project's stated upgrade path.
func TestRunPrint_IsRetiredNotAliased(t *testing.T) {
	assert.Nil(t, runCmd.Flags().Lookup("print"),
		"--print is retired, not kept as an alias")
	assert.Empty(t, runCmd.Flags().Lookup("one-shot").Deprecated,
		"--one-shot is the flag, not a deprecation wrapper around one")
}

// TestACPRun_DrawsTheSameModeSplitAsRun pins that the acp leaf splits its two
// forms on the same flag, in the same word, as root `run`.
//
// Bare is the session — a conversation you keep talking to — and --one-shot is
// the single turn. The flag is optional on both leaves BECAUSE both forms
// exist on both: a required --one-shot would say that the other form does not,
// and a caller reading `run` help would learn a rule that the acp leaf then
// breaks.
func TestACPRun_DrawsTheSameModeSplitAsRun(t *testing.T) {
	flags := acpRunCmd.Flags()
	require.NotNil(t, flags.Lookup("one-shot"), "acp run takes the same --one-shot spelling as run")

	assert.NoError(t, acpRunCmd.ValidateRequiredFlags(),
		"a bare `acp run` opens a session, so naming the single-turn mode is a choice, not a requirement")

	saved := acpRunOneShot
	t.Cleanup(func() {
		acpRunOneShot = saved
		_ = flags.Set("one-shot", "false")
		flags.Lookup("one-shot").Changed = false
	})
	require.NoError(t, flags.Set("one-shot", "true"))
	assert.True(t, acpRunOneShot, "--one-shot must write through to acpRunOneShot")
}
