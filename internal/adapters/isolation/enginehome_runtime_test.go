package isolation

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/adapters/git"
	"github.com/ctxloom/ctxloom/internal/core/present"
)

// The engine-home root every case here presents: a session instance's leaf
// on the host, exactly as operations.ResolveInTreeAgentHome hands it over.
const hostEngineHome = "/proj/.ctxloom/state/ugly-icy-squid/home/claude"

// THE FIXED ROOT, pinned by value so a refactor cannot quietly move it: every
// container cell hangs its relocated engine home under this well-known
// in-container path, at the leaf the engine declares. It is a property of
// the container filesystem ctxloom owns, not of $HOME — so it is not derived
// from Container.home either.
func TestContainerInstanceHome_IsTheFixedWellKnownRoot(t *testing.T) {
	c := NewContainerFor(fakeRuntime{name: "docker", available: true}, "claude-code")
	cw := &containerWorkspace{instanceHome: c.instanceHome}
	assert.Equal(t, "/ctxloom/home", ContainerInstanceHome(cw))
}

// The override is a builder on the policy (like WithImage), so a caller that
// owns an image whose filesystem cannot host the default root pins its own —
// and a test can pin one without touching package state.
func TestContainerInstanceHome_IsOverridableOnThePolicy(t *testing.T) {
	c := NewContainerFor(fakeRuntime{name: "docker", available: true}, "claude-code").WithInstanceHome("/opt/agent-home")
	cw := &containerWorkspace{instanceHome: c.instanceHome}
	assert.Equal(t, "/opt/agent-home", ContainerInstanceHome(cw))
}

// A workspace that executes on the host has no container root: the engine is
// told the host path itself, and the resolver mounts nothing.
func TestContainerInstanceHome_HostWorkspacesHaveNone(t *testing.T) {
	noneWS, err := None{}.PrepareWorkspace(context.Background(), "/proj", "m")
	require.NoError(t, err)
	wtWS, err := NewWorktree(&git.Fake{CommonDirValue: t.TempDir()}).PrepareWorkspace(context.Background(), "/proj", "m")
	require.NoError(t, err)
	t.Cleanup(func() { _ = wtWS.Cleanup() })
	assert.Empty(t, ContainerInstanceHome(noneWS))
	assert.Empty(t, ContainerInstanceHome(wtWS))
}

// MountEngineHome is what turns the advice's mount into a bind the launch
// actually carries: it lands in the workspace's extraMounts, read-write (the
// engine writes its session state into its home), rendered through the
// runtime's Expose so a path-mapping runtime maps it like every other mount.
func TestMountEngineHome_ContainerCarriesTheMountIntoItsRunnerSpec(t *testing.T) {
	cw := &containerWorkspace{instanceHome: "/ctxloom/home", runtime: fakeRuntime{}}
	m := present.Mount{HostDir: hostEngineHome, TargetDir: "/home/ctxloom/.ctxloom/home/claude"}

	require.NoError(t, MountEngineHome(cw, m))

	c := NewContainerFor(fakeRuntime{name: "docker", available: true}, "claude-code")
	spec := c.buildRunnerSpec("claude-code", "name", cw, nil)
	require.Len(t, spec.Mounts, 2, "the project mount, then the engine home")
	assert.Equal(t, Mount{Host: hostEngineHome, Container: m.TargetDir, ReadOnly: false}, spec.Mounts[1])
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

// A relocated claude container gets its session home mounted and nothing
// else: no credential file is ever bound into it, from the host's own
// ~/.claude or from anywhere else. It authenticates from its env.
func TestMountEngineHome_MountsTheHomeAndNoCredential(t *testing.T) {
	cw := &containerWorkspace{instanceHome: "/ctxloom/home", runtime: fakeRuntime{}, engineSpec: engineContainerSpecFor("claude-code"), authMode: authEnv}
	sessionHome := t.TempDir()
	require.NoError(t, MountEngineHome(cw, present.Mount{HostDir: sessionHome, TargetDir: "/ctxloom/home/claude"}))

	c := NewContainerFor(fakeRuntime{name: "docker", available: true}, "claude-code")
	spec := c.buildRunnerSpec("claude-code", "name", cw, nil)
	extra := spec.Mounts[1:] // after the project mount
	require.Len(t, extra, 1, "the home directory alone")
	assert.Equal(t, Mount{Host: sessionHome, Container: "/ctxloom/home/claude", ReadOnly: false}, extra[0])
	for _, m := range spec.Mounts {
		assert.NotContains(t, m.Container, ".credentials.json", "no credential file is mounted into a container")
	}
}
