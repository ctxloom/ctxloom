package isolation

import (
	"errors"
	"fmt"
	"path"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/core/paths"
	"github.com/ctxloom/ctxloom/internal/engines/claude"
	"github.com/ctxloom/ctxloom/internal/testsupport"
)

// exactMapper routes only the host paths it lists. A mount site that derives
// its target from anything but mapping the exact host path it mounts — the
// raw host path, or a mapped parent joined with a host separator — asks for a
// path the table does not hold and fails.
type exactMapper map[string]string

// errUnrouted is exactMapper's refusal, so a test can assert that a caller
// propagated THIS failure rather than some other one.
var errUnrouted = errors.New("exactMapper: not a path this test routes")

func (m exactMapper) toContainer(host string) (string, error) {
	if c, ok := m[host]; ok {
		return c, nil
	}
	return "", fmt.Errorf("%w: %s", errUnrouted, host)
}

// B1: an overlay's target is where the runtime maps the project path it
// shadows, never that host path verbatim; the mountpoint is still created
// at the host path.
func TestContainerConfigOverlay_TargetIsMapped(t *testing.T) {
	proj := t.TempDir()
	rt := fakeRuntime{name: "docker", available: true}
	mounts, err := containerConfigOverlay(rt, proj, t.TempDir(), claudeOverlayDirs(t))
	require.NoError(t, err)
	require.NotEmpty(t, mounts)
	for i, rel := range claudeOverlayDirs(t) {
		host := filepath.Join(proj, rel)
		assert.Equal(t, "/ctr"+host, mounts[i].Container)
		assert.DirExists(t, host, "the mountpoint is created host-side")
	}
}

// An overlay target the runtime cannot route refuses the whole overlay with
// the mapper's error. Mounting it anyway — at the raw host path, or skipping
// it — would leave the engine's managed-config writes landing in the HOST
// project through the project bind, which is the one thing the overlay exists
// to prevent.
func TestContainerConfigOverlay_UnroutableTargetIsRefused(t *testing.T) {
	proj := t.TempDir()
	rt := mapperRuntime{fakeRuntime: fakeRuntime{name: "docker", available: true}, m: exactMapper{}}

	mounts, err := containerConfigOverlay(rt, proj, t.TempDir(), claudeOverlayDirs(t))
	require.ErrorIs(t, err, errUnrouted)
	assert.Nil(t, mounts, "no partial overlay is handed back alongside the refusal")
}

// B3: the delivered config's target is the mapping of the host mountpoint
// itself, not a mapped checkout joined with the host's separator.
func TestProjectConfigMount_TargetIsTheMappedMountpoint(t *testing.T) {
	projectDir, worktreeDir := newCell(t)
	writeConfigTree(t, projectDir, "project config")
	host := filepath.Join(worktreeDir, paths.AppDirName)
	rt := mapperRuntime{fakeRuntime: fakeRuntime{name: "docker", available: true}, m: exactMapper{host: "/mnt/c/wt/.ctxloom"}}

	m, ok, err := projectConfigMount(rt, projectDir, worktreeDir)
	require.NoError(t, err)
	require.True(t, ok)
	assert.Equal(t, "/mnt/c/wt/.ctxloom", m.Container)
}

// B2: every session-state target is container-anchored under the POSIX
// $HOME, so it is a clean slash path whatever the host separator.
func assertPOSIXTargets(t *testing.T, mounts []mount) {
	t.Helper()
	for _, m := range mounts {
		assert.NotContains(t, m.Container, `\`, m.Container)
		assert.True(t, path.IsAbs(m.Container), m.Container)
		assert.Equal(t, path.Clean(m.Container), m.Container)
	}
}

// B2: the full session-state set — transcript store, mounted locations,
// task log and lock, locks dir — lands under the container's POSIX $HOME.
func TestSessionStateMounts_TargetsArePOSIX(t *testing.T) {
	testsupport.Isolate(t)
	c := NewContainerFor(fakeRuntime{name: "docker", available: true}, claude.EngineName)
	c.state = SessionState{Harp: "brisk-teal-otter", ProjectID: "proj-1"}
	mounts, err := c.sessionStateMounts()
	require.NoError(t, err)
	require.NotEmpty(t, mounts)
	assertPOSIXTargets(t, mounts)
	assert.Contains(t, targetsOf(mounts), path.Join(defaultContainerHome, ".claude/projects"))
}

func targetsOf(mounts []mount) []string {
	out := make([]string, 0, len(mounts))
	for _, m := range mounts {
		out = append(out, m.Container)
	}
	return out
}

// The engine's transcript store is kept slash-form: it only ever names a
// path inside the container.
func TestEngineContainerSpec_TranscriptStoreRelIsSlashForm(t *testing.T) {
	rel := engineContainerSpecFor(claude.EngineName).transcriptStoreRel
	require.NotEmpty(t, rel)
	assert.Equal(t, ".claude/projects", rel)
}
