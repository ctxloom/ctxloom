package isolation

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/git"
	"github.com/ctxloom/ctxloom/internal/shared/agent/present"
)

// The engine-home root every case here presents: a session instance's leaf
// on the host, exactly as operations.ResolveInTreeAgentHome hands it over.
const hostEngineHome = "/proj/.ctxloom/state/ugly-icy-squid/home/claude"

// A container workspace's advice tells the engine an in-container path under
// the container's own $HOME, keeps the host path as the mount source, and
// records the one mount that makes the engine-side path true. The leaf is
// preserved: the engine composes its home from that name.
func TestRuntimeAdvice_ContainerPresentsTheHomeUnderTheContainerHome(t *testing.T) {
	cw := &containerWorkspace{home: "/home/ctxloom", runtime: fakeRuntime{}}

	paths, mounts := RuntimeAdvice(cw).ApplyPaths(present.Paths{EngineHome: present.Root{Host: hostEngineHome}})

	assert.Equal(t, present.Root{Host: hostEngineHome, Engine: "/home/ctxloom/.ctxloom/home/claude"}, paths.EngineHome)
	require.Len(t, mounts, 1, "exactly one mount makes the engine-side path true")
	assert.Equal(t, present.Mount{HostDir: hostEngineHome, TargetDir: "/home/ctxloom/.ctxloom/home/claude"}, mounts[0])
}

// The identity half: a workspace that executes on the host (none, worktree)
// leaves the engine reading the host path and mounts nothing.
func TestRuntimeAdvice_HostWorkspacesAreTheIdentity(t *testing.T) {
	noneWS, err := None{}.PrepareWorkspace(context.Background(), "/proj", "m")
	require.NoError(t, err)
	wtWS, err := NewWorktree(&git.Fake{CommonDirValue: t.TempDir()}).PrepareWorkspace(context.Background(), "/proj", "m")
	require.NoError(t, err)
	t.Cleanup(func() { _ = wtWS.Cleanup() })

	for name, ws := range map[string]Workspace{"none": noneWS, "worktree": wtWS} {
		paths, mounts := RuntimeAdvice(ws).ApplyPaths(present.Paths{EngineHome: present.Root{Host: hostEngineHome}})
		assert.Equal(t, present.Root{Host: hostEngineHome, Engine: hostEngineHome}, paths.EngineHome, name)
		assert.Empty(t, mounts, name)
	}
}

// A root the advice is never handed contributes nothing: an empty Host is
// left alone, not turned into a mount of "" at a fabricated target.
func TestRuntimeAdvice_ContainerLeavesAnUnresolvedRootAlone(t *testing.T) {
	cw := &containerWorkspace{home: "/home/ctxloom", runtime: fakeRuntime{}}
	paths, mounts := RuntimeAdvice(cw).ApplyPaths(present.Paths{})
	assert.Equal(t, present.Root{}, paths.EngineHome)
	assert.Empty(t, mounts)
}

// MountEngineHome is what turns the advice's mount into a bind the launch
// actually carries: it lands in the workspace's extraMounts, read-write (the
// engine writes its session state into its home), rendered through the
// runtime's Expose so a path-mapping runtime maps it like every other mount.
func TestMountEngineHome_ContainerCarriesTheMountIntoItsLaunchSpec(t *testing.T) {
	cw := &containerWorkspace{home: "/home/ctxloom", runtime: fakeRuntime{}}
	m := present.Mount{HostDir: hostEngineHome, TargetDir: "/home/ctxloom/.ctxloom/home/claude"}

	require.NoError(t, MountEngineHome(cw, m))

	c := NewContainerFor(fakeRuntime{name: "docker", available: true}, "claude-code")
	spec := c.launchSpec("claude-code", "", 0, cw)
	require.Len(t, spec.ExtraMounts, 1)
	assert.Equal(t, Mount{Host: hostEngineHome, Container: m.TargetDir, ReadOnly: false}, spec.ExtraMounts[0])
}

// A host-executing workspace cannot mount, and its own advice never produces
// a mount to hand back — so being handed one is a disagreement between the
// advice and the workspace, and it is refused rather than dropped.
func TestMountEngineHome_HostWorkspaceRefuses(t *testing.T) {
	noneWS, err := None{}.PrepareWorkspace(context.Background(), "/proj", "m")
	require.NoError(t, err)
	err = MountEngineHome(noneWS, present.Mount{HostDir: hostEngineHome, TargetDir: "/elsewhere"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "cannot mount")
}
