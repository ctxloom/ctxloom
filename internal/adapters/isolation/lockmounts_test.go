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
	"github.com/ctxloom/ctxloom/internal/shared/filelock"
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
	inContainer, err := c.runtime.paths().targetFor(protected)
	require.NoError(t, err)
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

// A symlink planted at the host lock path is refused, not bound: binding it
// would hand the child whatever file the link names.
func TestLockMounts_RefusesSymlinkedHostLock(t *testing.T) {
	testsupport.Isolate(t)
	projectDir, scratch := t.TempDir(), t.TempDir()
	c := NewContainerFor(fakeRuntime{name: "docker", available: true}, "claude-code")
	hostLock, err := paths.HomePathFor(filepath.Join(projectDir, c.engineSpec.inPlaceFiles[0]))
	require.NoError(t, err)
	require.NoError(t, os.MkdirAll(filepath.Dir(hostLock), 0o755))
	require.NoError(t, os.Symlink(filepath.Join(t.TempDir(), "elsewhere"), hostLock))

	_, err = c.lockMounts(projectDir, scratch)
	require.ErrorIs(t, err, filelock.ErrNotRegularFile)
}
