package cli

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/core/paths"
)

const (
	crTranscript = "{\"bulk\":true}\n"
	crCredential = "{\"token\":\"copied\"}\n"
)

// crSeedSession is cotSeedSession plus the two members this file is about:
// the canonical transcript under persist/ and a credential copy in the
// session home.
func crSeedSession(t *testing.T, harp string, age time.Duration) string {
	t.Helper()
	dir := cotSeedSession(t, harp, age)
	require.NoError(t, os.WriteFile(filepath.Join(dir, paths.PersistDirName, paths.CanonicalTranscriptFileName), []byte(crTranscript), 0o644))
	require.NoError(t, os.MkdirAll(filepath.Join(dir, paths.SessionHomeDirName), 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(dir, paths.SessionHomeDirName, ".credentials.json"), []byte(crCredential), 0o600))
	cotBackdate(t, dir, age)
	return dir
}

// TestClean_IncludePersist_RemovesTheTranscript is the gate: --include-persist
// takes the transcripts with the rest of persist/ — there is no
// transcript-sparing arm — and the report names persist/ among the members
// it took.
func TestClean_IncludePersist_RemovesTheTranscript(t *testing.T) {
	cotProject(t)
	dir := crSeedSession(t, "aged-quiet-heron", 90*24*time.Hour)
	transcript := filepath.Join(dir, paths.PersistDirName, paths.CanonicalTranscriptFileName)

	rep := cotRun(t, "--yes", "--format", "json")
	assert.NotContains(t, rep.Sessions.Members, paths.PersistDirName)
	got, err := os.ReadFile(transcript)
	require.NoError(t, err, "without --include-persist the transcript survives")
	assert.Equal(t, crTranscript, string(got))

	rep = cotRun(t, "--include-persist", "--yes", "--format", "json")
	assert.Contains(t, rep.Sessions.Members, paths.PersistDirName)
	assert.Equal(t, 1, rep.Sessions.Reclaimed)
	assert.NoFileExists(t, transcript, "--include-persist takes the transcript")
	cotAssertPlan(t, dir, false)
	assert.DirExists(t, dir, "the session directory itself is never removed")
}

// TestClean_ReapsTheSessionHomeWithTheEphemeralMembers: the session home is
// an Ephemeral row of paths.HarpMembers, so the default sweep takes it — the
// credential copied into it at instance time stops living on disk once the
// session has aged out.
func TestClean_ReapsTheSessionHomeWithTheEphemeralMembers(t *testing.T) {
	cotProject(t)
	aged := crSeedSession(t, "aged-quiet-heron", 90*24*time.Hour)
	young := crSeedSession(t, "young-quiet-heron", 10*24*time.Hour)

	rep := cotRun(t, "--yes", "--format", "json")

	assert.Contains(t, rep.Sessions.Members, paths.SessionHomeDirName)
	assert.NoDirExists(t, filepath.Join(aged, paths.SessionHomeDirName), "the aged session's home is reaped")
	cotAssertScratch(t, aged, false)
	assert.FileExists(t, filepath.Join(young, paths.SessionHomeDirName, ".credentials.json"), "a session inside the bound keeps its home")
}
