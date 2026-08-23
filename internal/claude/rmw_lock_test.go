package claude

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/gofrs/flock"
	"github.com/spf13/afero"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/paths"
	"github.com/ctxloom/ctxloom/internal/shared/agent"
	"github.com/ctxloom/ctxloom/internal/testsupport"
)

// TestWriteInstanceConfig_SerializesAgainstConcurrentWriter pins the invariant
// that claudeInstanceConfig.WriteInstanceConfig used to rely entirely on ITS CALLER's project lock
// (isolation.CopyAmbient's lockInstanceHome) — a second, unenforced lock
// idiom over an engine config file, invisible to anyone reading this writer
// in isolation and silently absent for any OTHER caller. It now takes its
// OWN agent.WithFileLock around the whole load-modify-write cycle, at dest
// (the .claude.json path), matching the SettingsWriter family's discipline.
//
// Mirrors internal/opencode/rmw_lock_test.go's
// TestWriteOpencodeConfig_SerializesAgainstConcurrentSettingsWrite: writer A
// takes the exact home lock WriteInstanceConfig's own agent.WithFileLock
// would take (paths.HomePathFor(dest)) DIRECTLY, standing in for a
// concurrent writer already mid-critical-section (a second in-tree
// delegated child sharing this instance, racing THIS PROCESS rather than
// isolation.CopyAmbient's caller-side lock — which is a different lock
// namespace and path entirely, see WriteInstanceConfig's doc). Writer B (a
// real WriteInstanceConfig call, on a goroutine) must block until A
// releases. The seam is deterministic (A holds the lock before B is
// spawned), not wall-clock.
//
// MUTATION KILL: remove the agent.WithFileLock wrap from WriteInstanceConfig
// (leaving it call loadJSONObject/AtomicWriteFile directly), and this test
// goes red — writer B's goroutine completes unexcluded while A still holds
// the lock, tripping the assertion below.
func TestWriteInstanceConfig_SerializesAgainstConcurrentWriter(t *testing.T) {
	testsupport.Isolate(t)
	instance := t.TempDir()
	workDir := t.TempDir()
	dest := filepath.Join(instance, inTreeConfigLeaf, InstanceConfigFileName)

	// Writer A: take the real home lock directly, standing in for a
	// concurrent WriteInstanceConfig call already mid-critical-section.
	lockPath, err := paths.HomePathFor(dest)
	require.NoError(t, err)
	require.NoError(t, os.MkdirAll(filepath.Dir(lockPath), 0o755))
	aLock := flock.New(lockPath)
	require.NoError(t, aLock.Lock())

	bDone := make(chan error, 1)
	go func() {
		_, werr := NewInstanceConfigWriter(agent.SettingsOptions{}).WriteInstanceConfig(agent.InstanceConfigRequest{
			HostHome: t.TempDir(), InstanceHome: instance, WorkDir: workDir,
		})
		bDone <- werr
	}()

	select {
	case <-bDone:
		t.Fatal("WriteInstanceConfig completed its read-modify-write while a concurrent writer still held the home lock at paths.HomePathFor(dest) — it is not serializing against other writers of its own file")
	case <-time.After(20 * time.Millisecond):
	}

	require.NoError(t, aLock.Unlock())
	require.NoError(t, <-bDone)

	exists, err := afero.Exists(afero.NewOsFs(), dest)
	require.NoError(t, err)
	assert.True(t, exists, "writer B's write must land once A releases")
}

// TestWriteInstanceConfig_LockIsDistinctFromCallersProjectLock proves the
// nesting claim in WriteInstanceConfig's doc: isolation.CopyAmbient's caller
// -side lock (paths.ProjectPathFor(InstanceHome), taken on the instance
// home directory itself) and this function's own lock
// (paths.HomePathFor(dest), taken on the generated .claude.json file) are
// different lock NAMESPACES at different PATHS, so a caller already holding
// its project lock across this call can never deadlock against
// WriteInstanceConfig's own internal lock.
//
// This is a same-process regression guard, not a cross-process one: it
// demonstrates the two derived paths never collide for a real instance home,
// which is what makes holding both locks from one goroutine (as
// isolation.CopyAmbient does) safe rather than a self-deadlock waiting to
// happen.
func TestWriteInstanceConfig_LockIsDistinctFromCallersProjectLock(t *testing.T) {
	testsupport.Isolate(t)
	root := t.TempDir()
	// A real instance home lives under a .ctxloom tree — see
	// isolation.CopyAmbient's doc ("project's own .ctxloom/state/<harp>/home"
	// or the home-rooted worktree fallback under ~/.ctxloom/sessions).
	instance := filepath.Join(root, ".ctxloom", "state", "harp", "home")
	require.NoError(t, os.MkdirAll(instance, 0o700))
	dest := filepath.Join(instance, inTreeConfigLeaf, InstanceConfigFileName)

	callerLockPath, err := paths.ProjectPathFor(instance)
	require.NoError(t, err)
	ownLockPath, err := paths.HomePathFor(dest)
	require.NoError(t, err)

	assert.NotEqual(t, callerLockPath, ownLockPath,
		"the caller's project lock and WriteInstanceConfig's own home lock must never collapse onto the same path, or a caller holding one while this function waits on the other self-deadlocks")
}
