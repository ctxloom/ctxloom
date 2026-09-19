package paths

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/testsupport"
)

// unstattable plants a self-referencing symlink at path. os.Stat follows
// symlinks, so the lookup terminates in ELOOP — a failure that is emphatically
// NOT "no such file". It is the cheapest fault that is unprivileged,
// deterministic, and distinguishable from absence; a permissions fixture would
// be none of those under a root-running test.
//
// The hostility is asserted here rather than assumed: a fixture that fails to
// break anything is green against fixed and unfixed code alike, so the caller
// gets a fault it has been shown to have.
func unstattable(t *testing.T, path string) {
	t.Helper()
	require.NoError(t, os.Symlink(path, path))

	_, statErr := os.Stat(path)
	require.Error(t, statErr, "the fixture did not make os.Stat(%s) fail at all", path)
	require.False(t, errors.Is(statErr, fs.ErrNotExist),
		"the fixture produced plain absence rather than an unstattable path (%v); "+
			"the defect under test is exactly the difference between the two", statErr)
}

// TestResolveHarpCanonicalTranscriptPath_UnstattableCurrentIsNotAbsence pins
// the distinction the resolver's fallback depends on.
//
// The fallback to the pre-rename name is licensed by ONE precondition: the
// current name is not there. Treating every os.Stat failure as absence
// discards that precondition — an ELOOP or EACCES on the current-name file
// reads identically to "not captured yet", and the resolver hands back the
// legacy path instead. The caller then parses a pre-rename transcript while a
// current one sits on disk, and is told nothing, because a path plus a nil
// error is what "found it" looks like.
//
// Absence is a fact the resolver may act on. A failure to determine absence is
// not, and must not be laundered into one.
func TestResolveHarpCanonicalTranscriptPath_UnstattableCurrentIsNotAbsence(t *testing.T) {
	testsupport.Isolate(t)
	const harp = "unstattable-current-harp"

	dir, err := HarpPersistDir(harp)
	require.NoError(t, err)
	require.NoError(t, os.MkdirAll(dir, 0o755))

	current := filepath.Join(dir, CanonicalTranscriptFileName)
	legacy := filepath.Join(dir, "transcript.acp.jsonl")

	// The legacy file is real and readable: without it the promotion this test
	// exists to catch could not happen, and the test would prove nothing.
	require.NoError(t, os.WriteFile(legacy, []byte(`{"legacy":true}`+"\n"), 0o644))
	unstattable(t, current)

	got, err := ResolveHarpCanonicalTranscriptPath(harp)

	require.Error(t, err,
		"an unstattable current-name transcript was reported as a successful resolution")
	assert.NotEqual(t, legacy, got,
		"the pre-rename transcript was silently promoted over a current one whose absence "+
			"was never established")
	assert.Empty(t, got, "no path may be returned alongside an unresolved stat failure")
}

// TestResolveHarpCanonicalTranscriptPath_UnstattableLegacyIsNotAbsence is the
// same rule on the second stat. Here the resolver's documented no-file answer
// (the current name, unstated) is indistinguishable from a genuine fallback
// decision, so an undetermined legacy file must surface rather than collapse
// into it.
func TestResolveHarpCanonicalTranscriptPath_UnstattableLegacyIsNotAbsence(t *testing.T) {
	testsupport.Isolate(t)
	const harp = "unstattable-legacy-harp"

	dir, err := HarpPersistDir(harp)
	require.NoError(t, err)
	require.NoError(t, os.MkdirAll(dir, 0o755))

	unstattable(t, filepath.Join(dir, "transcript.acp.jsonl"))

	got, err := ResolveHarpCanonicalTranscriptPath(harp)

	require.Error(t, err, "an unstattable legacy transcript was reported as simply absent")
	assert.Empty(t, got, "no path may be returned alongside an unresolved stat failure")
}

// TestResolveHarpCanonicalTranscriptPath_PlainAbsenceStillResolves guards the
// other direction: ordinary "nothing captured yet" must keep its existing
// contract — the CURRENT name, no error — or every caller's clean
// not-captured-yet check turns into an error path.
func TestResolveHarpCanonicalTranscriptPath_PlainAbsenceStillResolves(t *testing.T) {
	testsupport.Isolate(t)
	const harp = "nothing-captured-harp"

	dir, err := HarpPersistDir(harp)
	require.NoError(t, err)
	require.NoError(t, os.MkdirAll(dir, 0o755))

	got, err := ResolveHarpCanonicalTranscriptPath(harp)
	require.NoError(t, err, "plain absence is not a failure")
	assert.Equal(t, filepath.Join(dir, CanonicalTranscriptFileName), got,
		"with neither file present the resolver still names the canonical, current path")
}
