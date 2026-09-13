package testsupport

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// deadPid spawns a trivial child and waits for it, returning its now-free pid:
// a REAL exited pid, never a guessed number that could collide with a live
// unrelated process on a busy host.
func deadPid(t *testing.T) int {
	t.Helper()
	cmd := exec.Command("true")
	require.NoError(t, cmd.Start())
	require.NoError(t, cmd.Wait())
	return cmd.Process.Pid
}

// livePid starts a child that outlives the test and returns its pid; the
// child is killed on cleanup.
func livePid(t *testing.T) int {
	t.Helper()
	cmd := exec.Command("sleep", "60")
	require.NoError(t, cmd.Start())
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	})
	return cmd.Process.Pid
}

func pidDir(t *testing.T, root string, name string) string {
	t.Helper()
	dir := filepath.Join(root, name)
	require.NoError(t, os.MkdirAll(dir, 0o700))
	return dir
}

func TestReapSandboxes_DeadPidDir_Removed(t *testing.T) {
	root := t.TempDir()
	dir := pidDir(t, root, strconv.Itoa(deadPid(t)))

	reapSandboxes(root)

	assert.NoDirExists(t, dir, "a directory owned by a verifiably dead pid must be reaped")
}

func TestReapSandboxes_OwnPidDir_Removed(t *testing.T) {
	// The reaper always runs BEFORE this process stakes its own directory, so
	// an existing entry under our pid is a leftover from whichever earlier
	// process last held this recycled pid.
	root := t.TempDir()
	dir := pidDir(t, root, strconv.Itoa(os.Getpid()))

	reapSandboxes(root)

	assert.NoDirExists(t, dir, "a leftover under this process's own pid cannot be ours and must be reaped")
}

func TestReapSandboxes_LivePidDir_Kept(t *testing.T) {
	root := t.TempDir()
	dir := pidDir(t, root, strconv.Itoa(livePid(t)))

	reapSandboxes(root)

	assert.DirExists(t, dir, "a concurrent sibling's directory must never be touched")
}

func TestReapSandboxes_LivePidDirOlderThanOrphanAge_Removed(t *testing.T) {
	root := t.TempDir()
	dir := pidDir(t, root, strconv.Itoa(livePid(t)))
	stale := time.Now().Add(-maxOrphanAge - time.Hour)
	require.NoError(t, os.Chtimes(dir, stale, stale))

	reapSandboxes(root)

	assert.NoDirExists(t, dir, "a directory older than maxOrphanAge is reclaimed even if its pid was recycled by a live process")
}

func TestReapSandboxes_NonPidName_Kept(t *testing.T) {
	root := t.TempDir()
	dir := pidDir(t, root, "not-a-pid")
	file := filepath.Join(root, strconv.Itoa(deadPid(t)))
	require.NoError(t, os.WriteFile(file, nil, 0o600))

	reapSandboxes(root)

	assert.DirExists(t, dir, "only pid-named directories belong to the reaper")
	assert.FileExists(t, file, "a plain file is not a sandbox, whatever its name")
}

func TestReapSandboxes_MissingRoot_IsANoop(t *testing.T) {
	assert.NotPanics(t, func() { reapSandboxes(filepath.Join(t.TempDir(), "absent")) })
}

func TestAcquireSandbox_ReapsDeadThenStakesOwnPidDir(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("TMPDIR", tmp)
	root := filepath.Join(tmp, sandboxRootName)
	leftover := pidDir(t, root, strconv.Itoa(deadPid(t)))

	dir, cleanup, err := acquireSandbox()
	require.NoError(t, err)

	assert.Equal(t, filepath.Join(root, strconv.Itoa(os.Getpid())), dir, "the sandbox is keyed by this process's pid so a later process can judge its liveness")
	assert.DirExists(t, dir)
	assert.NoDirExists(t, leftover, "a dead run's sandbox is reaped before this run stakes its own")

	cleanup()
	assert.NoDirExists(t, dir, "the normal exit path removes the whole sandbox")
}
