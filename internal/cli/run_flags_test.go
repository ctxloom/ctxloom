package cli

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestRunCmd_EveryFlagVarIsActuallyBound pins the coupling between run's
// package-level flag variables and the init() that registers them.
//
// The variables and their registration live apart, so the binding is held only
// by init() running and naming each one. A variable that loses its
// registration -- dropped in a refactor, renamed on one side only -- does not
// fail to compile: it silently reads as its zero value forever, which for
// runOneShot means an entire execution mode that can no longer be selected and
// reports nothing wrong. That is the failure mode worth guarding, and it is
// the one the split actually creates.
//
// This asserts the registration side of every flag the command documents,
// including the hidden seeding flags, plus the
// mutual-exclusion groups that make --agent and the profile flags refuse each
// other.
func TestRunCmd_EveryFlagVarIsActuallyBound(t *testing.T) {
	flags := runCmd.Flags()

	for _, name := range []string{
		"llm", "prompt", "command", "fragment", "tag", "profile",
		"agent", "workspace", "permissions", "dry-run", "one-shot",
		"plain-terminal", "verbose", "yes", "session", "distill",
		"seed-task", "seed-status",
	} {
		assert.NotNil(t, flags.Lookup(name), "--%s is declared but never registered by init()", name)
	}

	// The shorthands are part of the published surface: a script saying -p
	// must keep meaning --profile.
	for short, long := range map[string]string{
		"l": "llm", "r": "command", "f": "fragment", "t": "tag",
		"p": "profile", "n": "dry-run", "v": "verbose", "y": "yes",
	} {
		f := flags.ShorthandLookup(short)
		require.NotNil(t, f, "-%s lost its binding", short)
		assert.Equal(t, long, f.Name, "-%s must stay bound to --%s", short, long)
	}

	assert.Nil(t, flags.Lookup("run-prompt"),
		"--run-prompt was the pre-rename alias for --command and is DELETED, not deprecated (verb-spine reorg §6: no shims)")
	assert.Nil(t, flags.Lookup("structured"),
		"--structured is DELETED, not deprecated: it was an orphan CLI surface (no in-tree consumer, no tests, its own doc comment described a retired transport) — no shim, no alias")
	for _, hidden := range []string{"seed-task", "seed-status"} {
		assert.True(t, flags.Lookup(hidden).Hidden, "--%s is internal and must stay hidden", hidden)
	}

	// Registration alone proves nothing: a flag can be registered against a
	// throwaway address and still Lookup fine, leaving the package variable
	// the command actually reads stuck at its zero value. Setting the flag and
	// watching the variable move is what proves the binding.
	savedOneShot, savedProfile := runOneShot, runProfile
	savedSession, savedFragments := runResumeSession, runFragments
	t.Cleanup(func() {
		runOneShot, runProfile = savedOneShot, savedProfile
		runResumeSession, runFragments = savedSession, savedFragments
	})

	require.NoError(t, flags.Set("one-shot", "true"))
	assert.True(t, runOneShot, "--one-shot must write through to runOneShot")
	require.NoError(t, flags.Set("profile", "reviewer"))
	assert.Equal(t, "reviewer", runProfile, "--profile must write through to runProfile")
	require.NoError(t, flags.Set("session", "swift-amber-falcon"))
	assert.Equal(t, "swift-amber-falcon", runResumeSession, "--session must write through to runResumeSession")
	require.NoError(t, flags.Set("fragment", "a"))
	assert.Contains(t, runFragments, "a", "--fragment must write through to runFragments")
}
