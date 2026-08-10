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

// TestRunOneShot_StaysMutuallyExclusiveWithStructured carries the constraint
// across the rename. The two flags select different execution modes, so an
// invocation naming both is a caller who has not decided — and an exclusion
// silently dropped in a rename would resolve that by picking one and saying
// nothing.
//
// Driven through cobra's own validator rather than read off the annotation
// map: the annotation is an implementation detail of MarkFlagsMutuallyExclusive
// and asserting it proves only that a function was called, not that an
// invocation is refused.
func TestRunOneShot_StaysMutuallyExclusiveWithStructured(t *testing.T) {
	flags := runCmd.Flags()
	savedOneShot, savedStructured := runOneShot, runStructured
	t.Cleanup(func() {
		runOneShot, runStructured = savedOneShot, savedStructured
		_ = flags.Set("one-shot", "false")
		_ = flags.Set("structured", "false")
		flags.Lookup("one-shot").Changed = false
		flags.Lookup("structured").Changed = false
	})

	require.NoError(t, flags.Set("one-shot", "true"))
	require.NoError(t, flags.Set("structured", "true"))

	err := runCmd.ValidateFlagGroups()

	require.Error(t, err, "naming both execution modes must be refused, not silently resolved")
	assert.Contains(t, err.Error(), "one-shot")
}

// TestACPClient_RequiresOneShotToBeStated is the loud half of giving the acp
// leaf the same flag.
//
// `acp run` drives exactly one turn and has no interactive form. A flag
// named --one-shot implies a bare invocation is something else, so accepting a
// bare one would teach a caller that they had opened a session when they had
// not. Requiring the flag says the same thing the interactive capability would
// say, without inventing capability that does not exist. Whether that leaf
// SHOULD grow an interactive form is a product question, not this flag's to
// answer.
func TestACPClient_RequiresOneShotToBeStated(t *testing.T) {
	flags := acpRunCmd.Flags()
	require.NotNil(t, flags.Lookup("one-shot"), "acp run takes the same --one-shot spelling as run")

	assert.Error(t, acpRunCmd.ValidateRequiredFlags(),
		"a bare `acp run` must refuse rather than behave as a one-shot its own flag says it is not")

	saved := acpRunOneShot
	t.Cleanup(func() {
		acpRunOneShot = saved
		_ = flags.Set("one-shot", "false")
		flags.Lookup("one-shot").Changed = false
	})
	require.NoError(t, flags.Set("one-shot", "true"))
	assert.True(t, acpRunOneShot, "--one-shot must write through to acpRunOneShot")
	assert.NoError(t, acpRunCmd.ValidateRequiredFlags(),
		"stating the mode satisfies the requirement")
}
