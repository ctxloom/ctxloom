package sessions

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/core/paths"
	"github.com/ctxloom/ctxloom/internal/testsupport"
)

// An engine reports its transcript under its config home, whose history dir
// is a link into native/. The binding records the RESOLVED path, so it stays
// true after Close deletes the home.
func TestBindSession_RecordsTheTranscriptThroughTheHomeLinkUnderNative(t *testing.T) {
	testsupport.Isolate(t)
	m, err := Open(nil)
	require.NoError(t, err)
	e, err := m.AssignHarp("/tmp/demo", "claude-code")
	require.NoError(t, err)

	native, err := paths.HarpNativeDir(e.HarpName)
	require.NoError(t, err)
	target := filepath.Join(native, "claude", "projects")
	require.NoError(t, os.MkdirAll(filepath.Join(target, "-tmp-demo"), 0o700))
	home, err := paths.HarpSessionEngineHomes(e.HarpName)
	require.NoError(t, err)
	require.NoError(t, os.MkdirAll(filepath.Join(home, "claude"), 0o700))
	require.NoError(t, os.Symlink(filepath.Join("..", "..", "native", "claude", "projects"), filepath.Join(home, "claude", "projects")))
	inNative := filepath.Join(target, "-tmp-demo", "s1.jsonl")
	require.NoError(t, os.WriteFile(inNative, []byte("{}\n"), 0o600))

	require.NoError(t, m.BindSession(e.HarpName, "s1", filepath.Join(home, "claude", "projects", "-tmp-demo", "s1.jsonl")))
	got, err := m.Find(e.HarpName)
	require.NoError(t, err)
	want, err := filepath.EvalSymlinks(inNative)
	require.NoError(t, err)
	assert.Equal(t, want, got.TranscriptPath)

	require.NoError(t, os.RemoveAll(home))
	_, err = os.Stat(got.TranscriptPath)
	assert.NoError(t, err, "the recorded binding survives the home's deletion")
}

// A transcript the engine has not flushed yet does not resolve; it is
// recorded as given rather than refused.
func TestBindSession_AnUnresolvedTranscriptIsRecordedAsGiven(t *testing.T) {
	testsupport.Isolate(t)
	m, err := Open(nil)
	require.NoError(t, err)
	e, err := m.AssignHarp("/tmp/demo", "claude-code")
	require.NoError(t, err)
	p := filepath.Join(t.TempDir(), "not-yet.jsonl")
	require.NoError(t, m.BindSession(e.HarpName, "s1", p))
	got, err := m.Find(e.HarpName)
	require.NoError(t, err)
	assert.Equal(t, p, got.TranscriptPath)
}
