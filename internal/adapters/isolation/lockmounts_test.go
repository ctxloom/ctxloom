package isolation

import (
	"os"
	"path"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/core/paths"
	"github.com/ctxloom/ctxloom/internal/testsupport"
)

// A container gets its OWN locks dir — a per-run dir under the run's scratch —
// and nothing of the host's ~/.ctxloom/locks except the single lock FILE of
// each in-place project file the engine declares.
func TestLockMounts_OwnPerRunDirNeverTheHosts(t *testing.T) {
	testsupport.Isolate(t)
	projectDir, scratch := t.TempDir(), t.TempDir()
	c := NewContainerFor(fakeRuntime{name: "docker", available: true}, "claude-code")
	require.NotEmpty(t, c.engineSpec.inPlaceFiles, "claude declares the in-place file this test needs")

	mounts, err := c.lockMounts(projectDir, scratch)
	require.NoError(t, err)
	require.Len(t, mounts, 1+len(c.engineSpec.inPlaceFiles))

	runLocks := filepath.Join(scratch, paths.HomeLocksDirName)
	assert.Equal(t, mount{Host: runLocks, Container: path.Join(defaultContainerHome, ".ctxloom", "locks")}, mounts[0],
		"the container's ~/.ctxloom/locks is a per-run dir, removed with the scratch")

	hostLocks, err := paths.HomeLocksDir()
	require.NoError(t, err)
	for _, m := range mounts {
		assert.NotEqual(t, hostLocks, m.Host, "the host locks dir is never handed to a child")
		info, err := os.Lstat(m.Host)
		require.NoError(t, err, "bind source %s must exist before `run`", m.Host)
		if strings.HasPrefix(m.Host, hostLocks+string(filepath.Separator)) {
			assert.True(t, info.Mode().IsRegular(), "only single lock FILES of the host's are bound: %s", m.Host)
		}
	}
	for _, m := range mounts[1:] {
		target, err := os.Lstat(filepath.Join(runLocks, path.Base(m.Container)))
		require.NoError(t, err, "the nested bind target must exist in the per-run dir, created by us — not by a rootful runtime as root")
		assert.True(t, target.Mode().IsRegular())
	}
}

// The in-place file's host lock is bound exactly where the container's OWN
// paths.HomePathFor looks — derived from the IN-CONTAINER path, which this
// runtime maps under /ctr. Naming it from the host path would put the two
// sides on different files, excluding nobody.
func TestLockMounts_ContainerResolverFindsTheHostLock(t *testing.T) {
	realHome := testsupport.Isolate(t)
	projectDir, scratch := t.TempDir(), t.TempDir()
	c := NewContainerFor(fakeRuntime{name: "docker", available: true}, "claude-code")
	protected := filepath.Join(projectDir, c.engineSpec.inPlaceFiles[0])

	hostLock, err := paths.HomePathFor(protected)
	require.NoError(t, err)
	inContainer := mapped(t, c.runtime, protected)
	require.NotEqual(t, protected, inContainer, "the fake runtime maps paths, so host and container names differ")
	// What runs inside: HOME is the container home in every container run.
	testsupport.PointHomeAt(t, defaultContainerHome)
	containerLock, err := paths.HomePathFor(inContainer)
	require.NoError(t, err)
	testsupport.PointHomeAt(t, realHome)

	mounts, err := c.lockMounts(projectDir, scratch)
	require.NoError(t, err)
	assert.Contains(t, mounts, mount{Host: hostLock, Container: filepath.ToSlash(containerLock)})
}

// A worktree checkout under the session's work/ dir locks its in-place file
// in the SESSION's own locks dir on the host, while the container, whose home
// is not the host's, looks in its per-run locks dir. The two paths look
// nothing alike, and the bind must still join them: the host's session lock
// file is bound exactly where the container's own paths.HomePathFor looks,
// with the target prepared in the per-run dir.
func TestLockMounts_SessionWorktreeLockReachesTheContainersResolver(t *testing.T) {
	realHome := testsupport.Isolate(t)
	const harp = "brisk-teal-otter"
	work, err := paths.HarpWorkDir(harp)
	require.NoError(t, err)
	projectDir := filepath.Join(work, "ctxloom-wt-agent")
	require.NoError(t, os.MkdirAll(projectDir, 0o755))
	scratch := t.TempDir()
	c := NewContainerFor(fakeRuntime{name: "docker", available: true}, "claude-code")
	protected := filepath.Join(projectDir, c.engineSpec.inPlaceFiles[0])

	hostLock, err := paths.HomePathFor(protected)
	require.NoError(t, err)
	sessionLocks, err := paths.HarpLocksDir(harp)
	require.NoError(t, err)
	require.Equal(t, sessionLocks, filepath.Dir(hostLock), "on the host a session worktree's file locks in the session's own locks dir")
	inContainer := mapped(t, c.runtime, protected)
	testsupport.PointHomeAt(t, defaultContainerHome)
	containerLock, err := paths.HomePathFor(inContainer)
	require.NoError(t, err)
	testsupport.PointHomeAt(t, realHome)

	mounts, err := c.lockMounts(projectDir, scratch)
	require.NoError(t, err)
	assert.Contains(t, mounts, mount{Host: hostLock, Container: filepath.ToSlash(containerLock)})
	assert.FileExists(t, hostLock, "the bind source must exist as a file before `run`")
	target, err := os.Lstat(filepath.Join(scratch, paths.HomeLocksDirName, filepath.Base(containerLock)))
	require.NoError(t, err, "the bind target in the per-run dir must be created by us, not by a rootful runtime as root")
	assert.True(t, target.Mode().IsRegular())
}

// A container whose home the project path falls under — a controller
// sharing the container's home layout, its worktree inside one of that
// home's session dirs — resolves the in-place file's lock in THAT session's
// locks dir, not its per-run dir. The host lock is bound there, because that
// is where the container's own paths.HomePathFor looks; binding it at a
// per-run name the container never computes would exclude nobody.
func TestLockMounts_ContainerSessionPathLocksInTheContainersSessionDir(t *testing.T) {
	realHome := testsupport.Isolate(t)
	const harp = "brisk-teal-otter"
	work, err := paths.HarpWorkDir(harp)
	require.NoError(t, err)
	projectDir := filepath.Join(work, "ctxloom-wt-agent")
	require.NoError(t, os.MkdirAll(projectDir, 0o755))
	scratch := t.TempDir()
	c := NewContainerFor(fakeRuntime{name: "docker", available: true}, "claude-code")
	c.home = mapped(t, c.runtime, realHome)
	protected := filepath.Join(projectDir, c.engineSpec.inPlaceFiles[0])

	hostLock, err := paths.HomePathFor(protected)
	require.NoError(t, err)
	inContainer := mapped(t, c.runtime, protected)
	testsupport.PointHomeAt(t, c.home)
	containerLock, err := paths.HomePathFor(inContainer)
	require.NoError(t, err)
	testsupport.PointHomeAt(t, realHome)
	require.Equal(t, path.Join(c.home, paths.AppDirName, paths.SessionsDir, harp, paths.HarpLocksDirName), path.Dir(filepath.ToSlash(containerLock)),
		"the fixture must place the project in a session dir of the container's own home, or it tests nothing")

	mounts, err := c.lockMounts(projectDir, scratch)
	require.NoError(t, err)
	assert.Contains(t, mounts, mount{Host: hostLock, Container: filepath.ToSlash(containerLock)})
}
