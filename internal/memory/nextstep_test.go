package memory

import (
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/paths"
	"github.com/ctxloom/ctxloom/internal/testsupport"
)

const testHarp = "dizzy-balmy-opium"

// TestWriteNextStep_StoresTheTextWhereReadNextStepFindsIt asserts the PAYLOAD
// on disk, not that the call returned nil: a writer that reports success and
// writes zero bytes is this project's characteristic bug, and only reading the
// file back catches it.
//
// MUTATION — make WriteNextStep return nil before the iox.WriteFileAtomic call
// (or write []byte{} instead of the text) — turns this red at both the file
// read and the ReadNextStep round trip.
func TestWriteNextStep_StoresTheTextWhereReadNextStepFindsIt(t *testing.T) {
	testsupport.Isolate(t)
	const want = "Next I will run the acceptance suite and merge the branch."

	require.NoError(t, WriteNextStep(testHarp, want))

	path, err := paths.HarpNextStepPath(testHarp)
	require.NoError(t, err)
	onDisk, err := os.ReadFile(path)
	require.NoError(t, err, "the next step must exist as a file, not merely be reported written")
	assert.Equal(t, want, string(onDisk), "the stored bytes must be the text handed in")

	got, ok := ReadNextStep(testHarp)
	assert.True(t, ok, "a written next step must read back as present")
	assert.Equal(t, want, got)
}

// TestReadNextStep_MissingFileIsNotAnError pins the contract that a fresh harp
// has no next step and that this is ordinary rather than a failure.
//
// MUTATION — make ReadNextStep return (\"\", true) on a read error — turns this
// red, because a caller would then treat an absent hint as a present one and
// steer distillation by an empty string.
func TestReadNextStep_MissingFileIsNotAnError(t *testing.T) {
	testsupport.Isolate(t)

	got, ok := ReadNextStep(testHarp)
	assert.False(t, ok, "a harp that has never finished a turn has no next step")
	assert.Empty(t, got)
}

// TestWriteNextStep_EmptyIsRefusedAndLeavesThePreviousCaptureStanding is the
// load-bearing half of the empty-write refusal. Overwriting happens EVERY
// turn, so accepting an empty write would erase a good next step captured a
// turn earlier — and the erasure would be indistinguishable, downstream, from
// a session that never captured one.
//
// MUTATION — drop the `if bounded == ""` guard in WriteNextStep — turns this
// red twice: the error goes nil, and the round trip afterwards returns empty
// where the earlier capture should still stand.
func TestWriteNextStep_EmptyIsRefusedAndLeavesThePreviousCaptureStanding(t *testing.T) {
	testsupport.Isolate(t)
	const first = "Run the gates, then report."
	require.NoError(t, WriteNextStep(testHarp, first))

	for _, empty := range []string{"", "   ", "\n\t\n"} {
		err := WriteNextStep(testHarp, empty)
		require.ErrorIs(t, err, ErrEmptyNextStep, "an empty next step must be refused, not silently written")

		got, ok := ReadNextStep(testHarp)
		assert.True(t, ok, "the refused write must leave the earlier capture in place")
		assert.Equal(t, first, got, "a turn with nothing to say must not erase the turn that had something")
	}
}

// TestWriteNextStep_BoundsWhatItStores pins that a runaway final message
// cannot create an unbounded file. The assertion is on the FILE, because the
// bound exists to cap what lands on disk.
//
// MUTATION — return `s` unchanged from boundNextStep (or raise
// MaxNextStepBytes) — turns this red on the file-size assertion.
func TestWriteNextStep_BoundsWhatItStores(t *testing.T) {
	testsupport.Isolate(t)
	runaway := strings.Repeat("pasted an entire file into the reply. ", 4000)
	require.Greater(t, len(runaway), MaxNextStepBytes*4, "the fixture must actually exceed the bound")

	require.NoError(t, WriteNextStep(testHarp, runaway))

	path, err := paths.HarpNextStepPath(testHarp)
	require.NoError(t, err)
	onDisk, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.LessOrEqual(t, len(onDisk), MaxNextStepBytes,
		"a runaway final message must not create an unbounded file")
	assert.NotEmpty(t, onDisk, "bounding must cut the text, not discard it")

	got, ok := ReadNextStep(testHarp)
	require.True(t, ok)
	assert.LessOrEqual(t, len(got), MaxNextStepBytes, "the bound must hold on the way out too")
}

// TestReadNextStep_BoundsAnOversizedFileWrittenByAnyoneElse covers the READ
// boundary independently: the file is ordinary state on disk and this process
// is not guaranteed to be its only writer.
//
// MUTATION — drop the boundNextStep call in ReadNextStep — turns this red.
func TestReadNextStep_BoundsAnOversizedFileWrittenByAnyoneElse(t *testing.T) {
	testsupport.Isolate(t)
	dir, err := paths.HarpDir(testHarp)
	require.NoError(t, err)
	require.NoError(t, os.MkdirAll(dir, 0o755))
	path, err := paths.HarpNextStepPath(testHarp)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(path, []byte(strings.Repeat("x", MaxNextStepBytes*3)), 0o644))

	got, ok := ReadNextStep(testHarp)
	require.True(t, ok)
	assert.LessOrEqual(t, len(got), MaxNextStepBytes,
		"a reader that trusted the file's size would be bounded only while it was the only writer")
}

// TestWriteNextStep_RefusesAHarpThatEscapesTheSessionsRoot pins that the
// traversal guard paths.HarpDir owns is actually reached from here.
//
// MUTATION — build the path with filepath.Join on the sessions root instead of
// paths.HarpNextStepPath — turns this red.
func TestWriteNextStep_RefusesAHarpThatEscapesTheSessionsRoot(t *testing.T) {
	testsupport.Isolate(t)
	assert.Error(t, WriteNextStep("../../escaped", "anything"),
		"a harp name is one path component; one that escapes must be refused")
}
