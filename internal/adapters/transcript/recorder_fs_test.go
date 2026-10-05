package transcript

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/afero"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/core/agent"
	"github.com/ctxloom/ctxloom/internal/core/paths"
	"github.com/ctxloom/ctxloom/internal/shared/safefs"
	"github.com/ctxloom/ctxloom/internal/testsupport"
)

// TestNewRecorder_WritesThroughTheGivenFs: the recorder's transcripts dir and its
// held append handle both come from the fs it was given — never past it on the
// OS filesystem. (The ownership lock is a kernel lock and lives on the OS
// filesystem by design.)
func TestNewRecorder_WritesThroughTheGivenFs(t *testing.T) {
	testsupport.Isolate(t)
	fs := afero.NewMemMapFs()
	path, err := paths.HarpCanonicalTranscriptPath("fs-harp")
	require.NoError(t, err)
	rec, err := NewRecorder(fs, "fs-harp", "mock")
	require.NoError(t, err)

	for _, text := range []string{"first", "second"} {
		require.NoError(t, rec.Record(agent.ChatEvent{Entry: &agent.SessionEntry{Type: agent.EntryTypeUser, Content: text}}))
	}
	require.NoError(t, rec.Close())

	got, err := afero.ReadFile(fs, path)
	require.NoError(t, err, "the transcript is in the given fs")
	lines := strings.Split(strings.TrimSpace(string(got)), "\n")
	require.Len(t, lines, 2, "one held handle appends both records")
	require.Contains(t, lines[1], "second")
	_, err = os.Stat(path)
	require.True(t, os.IsNotExist(err), "nothing reached the OS filesystem, got %v", err)
}

func TestNewRecorder_RequiresAnFs(t *testing.T) {
	_, err := NewRecorder(nil, "fs-harp", "mock")
	require.Error(t, err)
}

// closeCountingWriter is an io.Writer that also records Close calls, so a
// test can see whether the recorder closed a writer it does not own.
type closeCountingWriter struct {
	bytes.Buffer
	closes int
}

func (w *closeCountingWriter) Close() error { w.closes++; return nil }

// TestNewRecorder_WithWriter_FillsAnAtomicFileThroughItsFs: a recorder given
// a writer appends its lines there and opens nothing by path, so filling a
// safefs.AtomicFile over an in-memory fs puts every byte in that fs on Commit
// and nothing on the OS filesystem.
func TestNewRecorder_WithWriter_FillsAnAtomicFileThroughItsFs(t *testing.T) {
	fs := afero.NewMemMapFs()
	dir := filepath.Join(t.TempDir(), "persist")
	require.NoError(t, fs.MkdirAll(dir, 0o755))
	target := filepath.Join(dir, "transcript.jsonl")

	af, err := safefs.NewAtomicFile(fs, target, 0o644)
	require.NoError(t, err)
	rec, err := NewRecorder(fs, "fs-harp", "mock", WithWriter(af))
	require.NoError(t, err)
	for _, text := range []string{"first", "second"} {
		require.NoError(t, rec.Record(agent.ChatEvent{Entry: &agent.SessionEntry{Type: agent.EntryTypeUser, Content: text}}))
	}
	require.NoError(t, rec.Close())
	require.NoError(t, af.Commit(), "the recorder must not have closed or committed the writer itself")

	got, err := afero.ReadFile(fs, target)
	require.NoError(t, err, "the transcript is in the given fs")
	lines := strings.Split(strings.TrimSpace(string(got)), "\n")
	require.Len(t, lines, 2)
	require.Contains(t, lines[1], "second")
	_, err = os.Stat(target)
	require.True(t, os.IsNotExist(err), "nothing reached the OS filesystem, got %v", err)
}

// TestNewRecorder_WithWriter_LeavesTheWriterOpen: the caller owns the writer's
// lifetime (an AtomicFile is committed or aborted by its owner), so Close
// never closes it, and the recorder neither creates the canonical transcript
// nor its directory.
func TestNewRecorder_WithWriter_LeavesTheWriterOpen(t *testing.T) {
	fs := afero.NewMemMapFs()
	w := &closeCountingWriter{}
	rec, err := NewRecorder(fs, "writer-harp", "mock", WithWriter(w))
	require.NoError(t, err)
	require.NoError(t, rec.Record(agent.ChatEvent{Entry: &agent.SessionEntry{Type: agent.EntryTypeUser, Content: "hi"}}))
	require.NoError(t, rec.Close())

	require.Equal(t, 0, w.closes, "Close must not close a caller-owned writer")
	require.Contains(t, w.String(), `"hi"`)
	entries, err := afero.ReadDir(fs, "/")
	require.NoError(t, err)
	require.Empty(t, entries, "a writer-backed recorder must not touch its fs")
}
