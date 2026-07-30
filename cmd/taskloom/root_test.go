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
