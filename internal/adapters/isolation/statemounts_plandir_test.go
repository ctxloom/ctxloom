package isolation

import (
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/core/paths"
	"github.com/ctxloom/ctxloom/internal/core/sessions"
	"github.com/ctxloom/ctxloom/internal/testsupport"
)

// resolveThroughMounts models what the kernel does with a containerPath inside
// a running container: a path under a bind mount is the HOST file, and every
// other path is private to the container's overlay and dies with it. Longest
// prefix wins, the way a real mount table resolves nested mounts. The
// container side is a slash path whatever the host; only the host side is
// joined with the host's separator.
func resolveThroughMounts(t *testing.T, mounts []mount, overlayRoot, containerPath string) string {
	t.Helper()
	ordered := append([]mount(nil), mounts...)
	sort.Slice(ordered, func(i, j int) bool { return len(ordered[i].Container) > len(ordered[j].Container) })
	for _, m := range ordered {
		if containerPath == m.Container || strings.HasPrefix(containerPath, m.Container+"/") {
			rel := strings.TrimPrefix(containerPath, m.Container)
			return filepath.Join(m.Host, filepath.FromSlash(rel))
		}
	}
	return filepath.Join(overlayRoot, filepath.FromSlash(containerPath))
}

// TestSessionStateMounts_PlanDirOutlivesTheContainer is the durability claim
// behind pointing a containerized agent's plans at its output dir, asserted
// as the only thing that actually matters: whether the bytes are still on
// disk after the container is gone.
//
// It runs the whole round trip against the real mount table. An in-container
// MCP server resolves the output dir the way production does
// (sessions.OutputDirIn) from the env the container is given, so the
// directory it names its agent is computed here the same way. Two plans are
// then written through the mount table, one to the output dir and one to the
// harp's session dir under the container home, the container's private
// overlay is destroyed, and both are looked for afterwards.
//
// The session-dir write is the control, and it is the reason this test can
// fail: requiring that it is LOST pins the actual contract — the output dir
// survives, an unmounted path does not.
func TestSessionStateMounts_PlanDirOutlivesTheContainer(t *testing.T) {
	hostHome := testsupport.Isolate(t)
	const harp = "brisk-teal-otter"

	c := NewContainerFor(fakeRuntime{name: "docker", available: true}, "claude-code")
	c.state = SessionState{Harp: harp, ProjectID: "proj-1"}
	mounts, env, err := c.sessionStateMounts()
	require.NoError(t, err)

	containerEnv := map[string]string{}
	for _, kv := range env {
		k, v, _ := strings.Cut(kv, "=")
		containerEnv[k] = v
	}
	testsupport.PointHomeAt(t, c.home)
	containerPlanDir, err := sessions.OutputDirIn(harp, func(k string) string { return containerEnv[k] })
	require.NoError(t, err)
	containerHarpDir, err := paths.HarpDir(harp)
	require.NoError(t, err)
	containerPlanDir, containerHarpDir = filepath.ToSlash(containerPlanDir), filepath.ToSlash(containerHarpDir)
	testsupport.PointHomeAt(t, hostHome)

	overlay := t.TempDir() // the container's private, teardown-deleted space

	durablePlan := resolveThroughMounts(t, mounts, overlay, path.Join(containerPlanDir, "design"+paths.PlanFileExt))
	lostPlan := resolveThroughMounts(t, mounts, overlay, path.Join(containerHarpDir, "design"+paths.PlanFileExt))

	const body = "# design\n\nthe decision and why\n"
	for _, p := range []string{durablePlan, lostPlan} {
		require.NoError(t, os.MkdirAll(filepath.Dir(p), 0o755))
		require.NoError(t, os.WriteFile(p, []byte(body), 0o644))
	}

	// Teardown: `docker run --rm` drops the container's writable layer.
	require.NoError(t, os.RemoveAll(overlay))

	got, err := os.ReadFile(durablePlan)
	require.NoError(t, err, "a plan written to the output dir must still be on the host after the container is gone")
	assert.Equal(t, body, string(got), "and with its bytes intact, not an empty file at the right path")
	hostOut, err := sessionOutputDir(harp)
	require.NoError(t, err)
	assert.Equal(t, filepath.Join(hostOut, "design"+paths.PlanFileExt), durablePlan,
		"it survives because it landed in the session's host-side output dir")

	_, err = os.Stat(lostPlan)
	assert.True(t, os.IsNotExist(err),
		"a plan written at the harp's session dir is in container-ephemeral space and is gone")
}
