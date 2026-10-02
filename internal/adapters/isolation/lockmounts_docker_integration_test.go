//go:build docker_integration

// Live-container proof that a container child never reaches the HOST's
// ~/.ctxloom/locks. Run with:
//
//	GOWORK=off just test-pkg ./internal/adapters/isolation/... -tags docker_integration -run TestContainerLockMounts_
package isolation

import (
	"context"
	"os"
	"path"
	"path/filepath"
	"testing"
	"time"

	"github.com/gofrs/flock"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/core/engine"
	"github.com/ctxloom/ctxloom/internal/core/paths"
	"github.com/ctxloom/ctxloom/internal/shared/filelock"
	"github.com/ctxloom/ctxloom/internal/testsupport"
	"github.com/ctxloom/ctxloom/internal/testsupport/dockergate"
)

// The host's own lock files (the countersign trust index's, another session's
// file's — anything paths.HomePathFor resolves on the host) must be neither
// visible to nor removable by a container child, and the child must not be
// able to plant anything where the host will next open a lock. The mount set
// is the REAL one a run gets (prepareWorkspace), not a hand-picked subset.
func TestContainerLockMounts_HostLocksUnreachableFromChild(t *testing.T) {
	dockergate.RequireRuntime(t, (Docker{}).Available(), "the container lock-mount isolation test")
	rt := ProbeRuntime("docker")
	registerVendorlessFixture(t, "mock", engine.DistributionTestOnly)
	home := testsupport.Isolate(t)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	var hostLocks []string
	for _, protected := range []string{
		filepath.Join(home, ".ctxloom", "trust", "index.json"),
		filepath.Join(home, ".ctxloom", "sessions", "other-session", "state.json"),
	} {
		lp, err := paths.HomePathFor(protected)
		require.NoError(t, err)
		require.NoError(t, filelock.Prepare(lp))
		require.NoError(t, os.WriteFile(lp, []byte("host"), 0o644))
		hostLocks = append(hostLocks, lp)
	}
	hostLocksDir, err := paths.HomeLocksDir()
	require.NoError(t, err)
	before, err := os.ReadDir(hostLocksDir)
	require.NoError(t, err)

	projectDir := t.TempDir()
	c := NewContainerFor(rt, "mock").WithImage("alpine:latest").WithSessionState(SessionState{Harp: "brisk-teal-otter"})
	ws, err := c.prepareWorkspace(ctx, projectDir, "locks-itest")
	require.NoError(t, err)
	t.Cleanup(func() { _ = ws.Cleanup() })
	cw, ok := ws.(*containerWorkspace)
	require.True(t, ok)
	mounts := append([]mount{{Host: projectDir, Container: projectDir}}, cw.extraMounts...)

	locksIn := path.Join(defaultContainerHome, paths.AppDirName, paths.HomeLocksDirName)
	script := "mkdir -p " + locksIn + " && ls -A " + locksIn + "; echo ---"
	for _, lp := range hostLocks {
		in := path.Join(locksIn, filepath.Base(lp))
		script += "; rm -f " + in + "; ln -s /etc/hostname " + in
	}
	script += "; echo planted > " + path.Join(locksIn, "planted.lock") + "; true"
	out, err := dockerRun(ctx, "alpine:latest", projectDir, mounts, "sh", "-c", script)
	require.NoError(t, err, out)

	for _, lp := range hostLocks {
		assert.NotContains(t, out, filepath.Base(lp), "the child must not see the host lock %s", lp)
		info, err := os.Lstat(lp)
		require.NoError(t, err, "the child must not remove the host lock %s", lp)
		assert.True(t, info.Mode().IsRegular(), "the child must not replace the host lock %s", lp)
		got, err := os.ReadFile(lp)
		require.NoError(t, err)
		assert.Equal(t, "host", string(got))
	}
	after, err := os.ReadDir(hostLocksDir)
	require.NoError(t, err)
	assert.Equal(t, len(before), len(after), "the child must not plant anything in the host locks dir")
}

// The one lock that DOES cross: a file the engine rewrites in place through
// the project bind (inPlaceFiles). While the host holds that file's lock, a
// child taking the lock at the path its OWN paths.HomePathFor resolves to
// (HOME is defaultContainerHome in every real run) must be refused — the two
// sides exclude each other on one inode. Released, the child gets it.
func TestContainerLockMounts_InPlaceFileLockExcludesAcrossBoundary(t *testing.T) {
	dockergate.RequireRuntime(t, (Docker{}).Available(), "the container lock-mount exclusion test")
	rt := ProbeRuntime("docker")
	registerVendorlessFixture(t, "mock", engine.DistributionTestOnly)
	testsupport.Isolate(t)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	projectDir := t.TempDir()
	c := NewContainerFor(rt, "mock").WithImage("alpine:latest").WithSessionState(SessionState{Harp: "brisk-teal-otter"})
	c.engineSpec.inPlaceFiles = []string{".mcp.json"}
	ws, err := c.prepareWorkspace(ctx, projectDir, "locks-itest")
	require.NoError(t, err)
	t.Cleanup(func() { _ = ws.Cleanup() })
	cw, ok := ws.(*containerWorkspace)
	require.True(t, ok)
	mounts := append([]mount{{Host: projectDir, Container: projectDir}}, cw.extraMounts...)

	protected := filepath.Join(projectDir, ".mcp.json")
	hostLock, err := paths.HomePathFor(protected)
	require.NoError(t, err)
	containerLock := path.Join(defaultContainerHome, paths.AppDirName, paths.HomeLocksDirName, paths.HomeLockName(protected))
	try := func() string {
		out, err := dockerRun(ctx, "alpine:latest", projectDir, mounts, "sh", "-c", "flock -n "+containerLock+" true; echo rc=$?")
		require.NoError(t, err, out)
		return out
	}

	fl := flock.New(hostLock)
	require.NoError(t, fl.Lock())
	assert.Contains(t, try(), "rc=1", "the child must not take the lock the host holds")
	require.NoError(t, fl.Unlock())
	assert.Contains(t, try(), "rc=0", "released by the host, the lock is the child's to take")
}
