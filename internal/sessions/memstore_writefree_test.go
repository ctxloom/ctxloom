package sessions

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/paths"
	"github.com/ctxloom/ctxloom/internal/testsupport"
)

// TestMemStore_BindSessionWritesNothingToDisk pins the half of MemStore's
// contract that survives: it performs no WRITE side effects. (The other half of
// the old doc -- "without touching disk" -- was simply false: Find/
// ListForProject/ListAll stat the harp's real HOME-rooted transcript, which
// store_test.go's cross-adapter Find case already relies on.)
//
// The contrast against *Manager is the point, and it is also what makes the
// fixture provably hostile: the same call through the real store DOES create
// the per-harp symlink under HOME, so an assertion of absence against MemStore
// is measuring something rather than measuring an empty temp dir.
func TestMemStore_BindSessionWritesNothingToDisk(t *testing.T) {
	home := testsupport.Isolate(t)

	transcript := filepath.Join(t.TempDir(), "engine.jsonl")
	require.NoError(t, os.WriteFile(transcript, []byte("{}\n"), 0o644))

	// --- the fixture must be hostile: prove the real store writes here. ---
	mgr, err := Open(filepath.Join(t.TempDir(), "index.yaml"))
	require.NoError(t, err)
	real, err := mgr.AssignHarp("/proj", "codex")
	require.NoError(t, err)
	require.NoError(t, mgr.BindSession(real.HarpName, "sid-1", transcript))

	realLink, err := paths.HarpEngineTranscriptLinkPath(real.HarpName, "codex", "sid-1")
	require.NoError(t, err)
	require.FileExists(t, realLink,
		"the real store must drop the per-harp engine-transcript link here, or this test cannot detect MemStore doing so")

	// --- MemStore, same lifecycle, must leave HOME untouched. ---
	mem := NewMemStore()
	e, err := mem.AssignHarp("/proj", "codex")
	require.NoError(t, err)
	require.NoError(t, mem.BindSession(e.HarpName, "sid-2", transcript))
	require.NoError(t, mem.MarkEnded(e.HarpName, real.StartedAt))

	memDir := filepath.Join(home, ".ctxloom", "sessions", e.HarpName)
	require.NoDirExists(t, memDir,
		"MemStore must create no per-harp state on disk; found %s", memDir)

	// And the bind must still be observable in memory, so "wrote nothing" is
	// not passing by having done nothing at all.
	got, err := mem.Find(e.HarpName)
	require.NoError(t, err)
	require.NotNil(t, got)
	require.Equal(t, "sid-2", got.SessionID)
	require.Equal(t, transcript, got.TranscriptPath)
	require.NotNil(t, got.EndedAt)
}
