package isolation

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/core/paths"
	"github.com/ctxloom/ctxloom/internal/core/sessions"
	"github.com/ctxloom/ctxloom/internal/core/spool"
	"github.com/ctxloom/ctxloom/internal/testsupport"
)

// sessionDirMounts returns the mounts whose HOST source lies under the harp's
// session dir and whose CONTAINER target is the same path relative to the
// container home — the session-state mounts proper, as opposed to the
// engine's native transcript store (a session member bound to an
// engine-defined target), the task log and the locks dir.
func sessionDirMounts(t *testing.T, l sessions.Layout, harp string, mounts []Mount) map[string]string {
	t.Helper()
	got := map[string]string{}
	for _, m := range mounts {
		rel, err := filepath.Rel(l.Dir(harp), m.Host)
		if err != nil || strings.HasPrefix(rel, "..") {
			continue
		}
		want := filepath.Join(defaultContainerHome, paths.AppDirName, paths.SessionsDir, harp, rel)
		if m.Container != want {
			continue
		}
		got[rel] = m.Container
	}
	return got
}

// TestSessionStateMounts_EqualTheTablesMountedRows is the table-vs-mounts
// gate: the session-state mounts a container gets ARE the location
// directories of paths.HarpMembers' Mounted rows — no more (a member the
// table does not mark is not silently exposed) and no fewer (moving a
// Mounted member without its mount is a red test here, not a silent
// unmount).
func TestSessionStateMounts_EqualTheTablesMountedRows(t *testing.T) {
	testsupport.Isolate(t)
	const harp = "brisk-teal-otter"
	l, err := sessions.HomeLayout()
	require.NoError(t, err)

	c := NewContainerFor(fakeRuntime{name: "docker", available: true}, "claude-code")
	c.state = SessionState{Harp: harp, ProjectID: "proj-1"}
	mounts, err := c.sessionStateMounts()
	require.NoError(t, err)

	// An empty-input guard: a table with no Mounted row would make the
	// equality below vacuous — and would mean no container child can be
	// reached by mail at all.
	require.NotEmpty(t, paths.MountedLocations(), "the table marks no member Mounted")
	require.NotEmpty(t, l.Dir(harp))

	want := map[string]string{}
	for _, dir := range paths.MountedLocations() {
		want[dir] = filepath.Join(defaultContainerHome, paths.AppDirName, paths.SessionsDir, harp, dir)
	}
	assert.Equal(t, want, sessionDirMounts(t, l, harp, mounts))
}

// TestSessionStateMounts_CarryTheSpool_R3 pins reach-back R3 under the fake
// runtime: the spool root the host's HomeMapper resolves lies under a
// table-derived mount, and the same file seen through that mount's container
// target is exactly the path the container's own HomeMapper resolves against
// the container home — so a doorbell ref written on the host names a file
// the child can open. The docker-gated twin
// (TestSpoolCrossBoundary_DoorbellRefResolvesInTheContainerView) follows the
// ref across a real bind mount.
func TestSessionStateMounts_CarryTheSpool_R3(t *testing.T) {
	testsupport.Isolate(t)
	const harp = "brisk-teal-otter"

	c := NewContainerFor(fakeRuntime{name: "docker", available: true}, "claude-code")
	c.state = SessionState{Harp: harp, ProjectID: "proj-1"}
	mounts, err := c.sessionStateMounts()
	require.NoError(t, err)

	hostSpool, err := spool.Root(spool.NewHomeMapper(), harp)
	require.NoError(t, err)

	var carried *Mount
	for i := range mounts {
		rel, err := filepath.Rel(mounts[i].Host, hostSpool)
		if err == nil && !strings.HasPrefix(rel, "..") {
			carried = &mounts[i]
			break
		}
	}
	require.NotNil(t, carried, "no session-state mount carries the spool root %s", hostSpool)
	assert.False(t, carried.ReadOnly, "the child answers through the spool: the mount is read-write")

	rel, err := filepath.Rel(carried.Host, hostSpool)
	require.NoError(t, err)
	containerSpool := filepath.Join(carried.Container, rel)
	assert.Equal(t,
		filepath.Join(defaultContainerHome, paths.AppDirName, paths.SessionsDir, harp, paths.PersistDirName, paths.SpoolDirName),
		containerSpool,
		"the container view of the spool is the home-relative path the child's HomeMapper resolves")
}
