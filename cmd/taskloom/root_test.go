package main

import (
	"io"
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// captureStderr swaps os.Stderr for a pipe while fn runs. noteTaskProject
// writes there directly, and its output is the only place the transposition
// this file pins would ever become visible.
func captureStderr(t *testing.T, fn func()) string {
	t.Helper()
	r, w, err := os.Pipe()
	require.NoError(t, err)
	orig := os.Stderr
	os.Stderr = w
	defer func() { os.Stderr = orig }()
	fn()
	require.NoError(t, w.Close())
	out, err := io.ReadAll(r)
	require.NoError(t, err)
	return string(out)
}

// formatProjectLabel is the one renderer for "which store did this land in".
// All three arms are pinned so the collapse of noteTaskProject's argument
// order onto it is provably behaviour-preserving.
func TestFormatProjectLabel_AllArms(t *testing.T) {
	assert.Equal(t, "/p/dir (an-id)", formatProjectLabel("/p/dir", "an-id"))
	assert.Equal(t, "/p/dir", formatProjectLabel("/p/dir", ""), "repo-homed mode mints no id; never print a bare ()")
	assert.Equal(t, "an-id", formatProjectLabel("", "an-id"))
	assert.Equal(t, "", formatProjectLabel("", ""))
}

// noteTaskProject and formatProjectLabel take the SAME pair of same-typed
// strings, and used to take them in OPPOSITE orders — noteTaskProject(id,
// dir) delegating to formatProjectLabel(dir, id). Nothing in the type system
// separates the two, so a call site written from either function's signature
// is a coin flip, and a transposition renders as a plausible-looking
// "an-id (/p/dir)" that no other assertion in this package would catch.
//
// This pins that the note a user sees is exactly what the shared renderer
// produces for the same store, whichever order the two functions settle on.
func TestNoteTaskProject_RendersTheSharedLabelNotATransposition(t *testing.T) {
	got := captureStderr(t, func() { noteTaskProject("/p/dir", "an-id") })

	assert.Contains(t, got, formatProjectLabel("/p/dir", "an-id"))
	assert.NotContains(t, got, formatProjectLabel("an-id", "/p/dir"),
		"the arguments are transposed: the directory and the id have swapped places")
}

// TestRootCommandTree_ShipsEveryCommandAndInitOrderNeverReachesTheUser is
// U007-F24's pin. Twelve init() functions in twelve files each mutate the one
// package-level rootCmd, so the command surface is assembled by a convention
// (Go runs init() in filename order) that nothing checks. Two things follow,
// and this pins both.
//
// First, a lost or renamed init() silently ships a CLI missing a command:
// nothing else in the package names the whole surface, so the expected set is
// written down here and a disappearance is red.
//
// Second, filename order must not be OBSERVABLE. cobra sorts subcommands for
// help, so the order twelve inits happen to run in never reaches the user —
// which is the reason the arrangement has survived, and is worth holding
// rather than assuming.
//
// The tree is completed the way main() completes it: newLoadoutCmd is a
// factory added at run time rather than by an init(), so rootCmd alone — what
// every other test in this package walks — is NOT the tree the binary ships.
func TestRootCommandTree_ShipsEveryCommandAndInitOrderNeverReachesTheUser(t *testing.T) {
	loadout := newLoadoutCmd()
	rootCmd.AddCommand(loadout)
	t.Cleanup(func() { rootCmd.RemoveCommand(loadout) })

	got := make([]string, 0, len(rootCmd.Commands()))
	for _, c := range rootCmd.Commands() {
		got = append(got, c.Name())
	}

	for _, want := range []string{
		"add", "edit", "lint", "list", "loadout", "manage", "mcp", "plan",
		"repair", "run", "show", "status", "statuses", "summary", "tag",
		"tags", "version", "watch",
	} {
		assert.Contains(t, got, want, "a command vanished from the tree twelve init() functions assemble")
	}

	assert.True(t, sort.StringsAreSorted(got),
		"cobra must present subcommands sorted, so the order the init() functions ran in is invisible to the user: %v", got)
}
