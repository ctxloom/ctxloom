package isolation

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/paths"
	"github.com/ctxloom/ctxloom/internal/testsupport"
)

// resolveThroughMounts models what the kernel does with a containerPath inside
// a running container: a path under a bind mount is the HOST file, and every
// other path is private to the container's overlay and dies with it. Longest
// prefix wins, the way a real mount table resolves nested mounts.
func resolveThroughMounts(t *testing.T, mounts []Mount, overlayRoot, containerPath string) string {
	t.Helper()
	ordered := append([]Mount(nil), mounts...)
	sort.Slice(ordered, func(i, j int) bool { return len(ordered[i].Container) > len(ordered[j].Container) })
	for _, m := range ordered {
		if containerPath == m.Container || strings.HasPrefix(containerPath, m.Container+string(filepath.Separator)) {
			rel, err := filepath.Rel(m.Container, containerPath)
			require.NoError(t, err)
			return filepath.Join(m.Host, rel)
		}
	}
	return filepath.Join(overlayRoot, containerPath)
}

// TestSessionStateMounts_PlanDirOutlivesTheContainer is the durability claim
// behind repointing mcp.sessionInstructions at paths.HarpPlansDir, asserted as
// the only thing that actually matters: whether the bytes are still on disk
// after the container is gone.
//
// It runs the whole round trip against the real mount table. An in-container
// MCP server resolves paths.HarpPlansDir against the CONTAINER's home, so the
// directory it names its agent is computed here the same way — by resolving
// the path helper under that home rather than by re-deriving the string from
// the same constants production uses, which would only assert that a join
// equals itself. Two plans are then written through the mount table, one to
// the plan dir and one to the harp TOP LEVEL, the container's private overlay
// is destroyed, and both are looked for afterwards.
//
// The top-level write is the control, and it is the reason this test can fail.
// A "the plan dir is mounted" assertion on its own stays green if someone
// widens the mount to the whole harp dir or points the plan dir back at the
// top level; requiring that the top-level write is LOST pins the actual
// contract — persist/ survives, the unclassified middle does not.
func TestSessionStateMounts_PlanDirOutlivesTheContainer(t *testing.T) {
	hostHome := testsupport.Isolate(t)
	const harp = "brisk-teal-otter"

	c := NewContainerFor(fakeRuntime{name: "docker", available: true}, "claude-code")
	c.state = SessionState{Harp: harp, ProjectID: "proj-1"}
	mounts, err := c.sessionStateMounts()
	require.NoError(t, err)

	// The two directories as the in-container MCP server sees them.
	t.Setenv("HOME", c.home)
	containerPlanDir, err := paths.HarpPlansDir(harp)
	require.NoError(t, err)
	containerHarpDir, err := paths.HarpDir(harp)
	require.NoError(t, err)
	t.Setenv("HOME", hostHome)

	require.NotEqual(t, containerHarpDir, containerPlanDir,
		"the plan dir must not BE the harp top level — that identity is the defect")

	overlay := t.TempDir() // the container's private, teardown-deleted space

	durablePlan := resolveThroughMounts(t, mounts, overlay, filepath.Join(containerPlanDir, "design"+paths.PlanFileExt))
	lostPlan := resolveThroughMounts(t, mounts, overlay, filepath.Join(containerHarpDir, "design"+paths.PlanFileExt))

	const body = "# design\n\nthe decision and why\n"
	for _, p := range []string{durablePlan, lostPlan} {
		require.NoError(t, os.MkdirAll(filepath.Dir(p), 0o755))
		require.NoError(t, os.WriteFile(p, []byte(body), 0o644))
	}

	// Teardown: `docker run --rm` drops the container's writable layer.
	require.NoError(t, os.RemoveAll(overlay))

	got, err := os.ReadFile(durablePlan)
	require.NoError(t, err, "a plan written to the plan dir must still be on the host after the container is gone")
	assert.Equal(t, body, string(got), "and with its bytes intact, not an empty file at the right path")
	hostPlanDir, err := paths.HarpPlansDir(harp)
	require.NoError(t, err)
	assert.Equal(t, filepath.Join(hostPlanDir, "design"+paths.PlanFileExt), durablePlan,
		"it survives because it landed in the harp's host-side persist dir")

	_, err = os.Stat(lostPlan)
	assert.True(t, os.IsNotExist(err),
		"a plan written at the harp TOP LEVEL is in container-ephemeral space and is gone — this is the failure the plan dir exists to avoid")
}
