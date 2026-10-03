package transcript

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/afero"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/core/agent"
)

// TestNewRecorder_WritesThroughTheGivenFs: the recorder's transcripts dir and its
// held append handle both come from the fs it was given, so a caller filling a
// safefs.AtomicFile's temp file over some fs reaches that file through the
// same fs — never past it on the OS filesystem.
func TestNewRecorder_WritesThroughTheGivenFs(t *testing.T) {
	fs := afero.NewMemMapFs()
	path := filepath.Join(t.TempDir(), "persist", "transcript.jsonl")
	rec, err := NewRecorder(fs, "fs-harp", "mock", WithPath(path))
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
