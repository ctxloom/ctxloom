package opencode

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/spf13/afero"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/shared/filelock"
	"github.com/ctxloom/ctxloom/internal/testsupport"
)

// TestWriteOpencodeConfig_SerializesAgainstConcurrentSettingsWrite pins the
// chat/interactive launch path's writeOpencodeConfig (chat.go's Chat,
// interactive.go's launchInteractive) into the SAME lock the four
// SettingsWriter entry points already take (WriteSettings/WriteContext/
// removeMCP/RemoveSettings, see settings.go) — before this fix it was the
// ONE unlocked read-modify-write of opencode.json among five call sites onto
// the same file (config-patching-review.md bypass B1).
//
// Mirrors internal/shared/agent/rmw_lock_test.go's
// TestWithFileLock_SerializesRMW_BothWritersEntriesSurvive: writer A takes
// the exact home lock writeOpencodeConfig's agent.WithFileLock would take
// (filelock.HomePathFor(target)) DIRECTLY, standing in for a concurrent
// SettingsWriter call already mid-critical-section, then a real
// writeOpencodeConfig call (writer B, on a goroutine) must block until A
// releases. The seam is deterministic (A holds the lock before B is
// spawned), not wall-clock; the sleep is a liveness nicety letting a BROKEN
// implementation prove itself broken within the grace window, not a
// mechanism this test depends on for correctness.
//
// MUTATION KILL: remove the agent.WithFileLock wrap from writeOpencodeConfig
// (leaving it call loadOpencodeConfig/applyManaged/saveOpencodeConfig
// directly), and this test goes red — writer B's goroutine completes
// unexcluded while A still holds the lock, tripping the assertion below.
func TestWriteOpencodeConfig_SerializesAgainstConcurrentSettingsWrite(t *testing.T) {
	testsupport.Isolate(t)
	dir := t.TempDir()
	target := filepath.Join(dir, ConfigFileName)
	osfs := afero.NewOsFs()
	require.NoError(t, afero.WriteFile(osfs, target, []byte(`{"theme":"tokyonight"}`), 0o644))

	// Writer A: take the real home lock directly, standing in for a
	// concurrent SettingsWriter call already mid-critical-section.
	lockPath, err := filelock.HomePathFor(target)
	require.NoError(t, err)
	aUnlock, err := filelock.Lock(lockPath)
	require.NoError(t, err)

	bDone := make(chan error, 1)
	go func() {
		bDone <- writeOpencodeConfig(osfs, dir, managedConfig{model: "openrouter/writer-b:free"})
	}()

	select {
	case <-bDone:
		t.Fatal("writeOpencodeConfig completed its read-modify-write while a concurrent settings write still held the lock — the chat/interactive path is not serialized against WriteSettings/WriteContext/removeMCP/RemoveSettings")
	case <-time.After(20 * time.Millisecond):
	}

	aUnlock()
	require.NoError(t, <-bDone)

	after, err := afero.ReadFile(osfs, target)
	require.NoError(t, err)
	assert.Contains(t, string(after), "writer-b:free", "writer B's write must land once A releases")
	assert.Contains(t, string(after), "tokyonight", "foreign key survives the serialized write")
}
