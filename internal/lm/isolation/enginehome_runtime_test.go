package isolation

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/git"
	"github.com/ctxloom/ctxloom/internal/shared/agent/present"
	"github.com/ctxloom/ctxloom/internal/shared/clidiag"
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
func TestMountEngineHome_ContainerCarriesTheMountIntoItsLaunchSpec(t *testing.T) {
	cw := &containerWorkspace{instanceHome: "/ctxloom/home", runtime: fakeRuntime{}}
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

// relocatedEngineHome is where a container cell tells the engine its
// home is: hostEngineHome mounted under the fixed root, at the declared leaf.
const relocatedEngineHome = "/ctxloom/home/claude"

// claudeContainerWorkspace is a container workspace whose auth resolved to the
// credential mount, for claude, exactly as prepareContainerScratch leaves it —
// the only state MountEngineHome reads to decide the credential overlay.
func claudeContainerWorkspace(mode containerAuthMode) *containerWorkspace {
	return &containerWorkspace{
		instanceHome: "/ctxloom/home",
		runtime:      fakeRuntime{},
		engineSpec:   engineContainerSpecFor("claude-code"),
		authMode:     mode,
	}
}

// RULED: a container run with a relocated home still refreshes its token in
// place. Once CLAUDE_CONFIG_DIR relocates, claude stops reading the RW
// credential bind-mounted into $HOME/.claude and would authenticate from the
// access-token-only copy seeded into the instance — a long run then dies at
// expiry. So the REAL host credential FILE is bind-mounted read-write OVER the
// instance's copy, at <engine home>/.credentials.json: the directory mount
// gives the engine its home, the file mount inside it gives the engine the
// one refreshable credential. The file only — never ~/.claude.json, which
// carries the user's own mcpServers registrations.
func TestMountEngineHome_ContainerMountsTheRealCredentialOverTheInstanceCopy(t *testing.T) {
	home := withFakeHome(t)
	writeCreds(t, home, true)
	cw := claudeContainerWorkspace(authCredentialMount)

	require.NoError(t, MountEngineHome(cw, present.Mount{HostDir: hostEngineHome, TargetDir: relocatedEngineHome}))

	c := NewContainerFor(fakeRuntime{name: "docker", available: true}, "claude-code")
	spec := c.launchSpec("claude-code", "", 0, cw)
	require.Len(t, spec.ExtraMounts, 2, "the home directory, then the credential file over it")
	assert.Equal(t, Mount{Host: hostEngineHome, Container: relocatedEngineHome, ReadOnly: false}, spec.ExtraMounts[0])
	assert.Equal(t, Mount{
		Host:      filepath.Join(home, ".claude", ".credentials.json"),
		Container: relocatedEngineHome + "/.credentials.json",
		ReadOnly:  false,
	}, spec.ExtraMounts[1], "the REAL host file, read-write, so the container's refresh lands in the one real credential")
	for _, m := range spec.ExtraMounts {
		assert.NotContains(t, m.Host, ".claude.json", "the user's own top-level config never crosses")
	}
}

// Auth that rides the environment (an API key) needs no credential file at
// all: nothing was seeded, nothing is overlaid, and the home mount stands
// alone — the same precedence resolveEnvOrMountAuth already applies.
func TestMountEngineHome_EnvAuthMountsNoCredential(t *testing.T) {
	home := withFakeHome(t)
	writeCreds(t, home, true)
	cw := claudeContainerWorkspace(authEnv)

	require.NoError(t, MountEngineHome(cw, present.Mount{HostDir: hostEngineHome, TargetDir: relocatedEngineHome}))

	require.Len(t, cw.extraMounts, 1)
	assert.Equal(t, relocatedEngineHome, cw.extraMounts[0].Container)
}

// The real credential file gone between auth resolution and the mount must
// not turn a working run into a broken one: the home still mounts, the run
// authenticates from the copy seeded into it (and cannot refresh in place),
// and the degradation is said out loud rather than discovered at expiry.
func TestMountEngineHome_AbsentRealCredentialFallsBackToTheSeededCopyAndSaysSo(t *testing.T) {
	withFakeHome(t) // no ~/.claude at all
	var sink strings.Builder
	t.Cleanup(clidiag.SetSink(&sink))
	cw := claudeContainerWorkspace(authCredentialMount)

	require.NoError(t, MountEngineHome(cw, present.Mount{HostDir: hostEngineHome, TargetDir: relocatedEngineHome}))

	require.Len(t, cw.extraMounts, 1, "the home mount stands; there is no credential to overlay")
	assert.Equal(t, relocatedEngineHome, cw.extraMounts[0].Container)
	assert.Contains(t, sink.String(), ".credentials.json", "the fallback names the file it could not mount")
	assert.Contains(t, sink.String(), "refresh", "…and the cost: no in-place refresh")
}

// An engine with no relocatable credential (mock authenticates against no
// vendor) gets the home mount and nothing else, silently: there is no
// credential whose absence could be a degradation.
func TestMountEngineHome_EngineWithoutACredentialMountsOnlyTheHome(t *testing.T) {
	var sink strings.Builder
	t.Cleanup(clidiag.SetSink(&sink))
	cw := &containerWorkspace{instanceHome: "/ctxloom/home", runtime: fakeRuntime{}, engineSpec: engineContainerSpecFor("mock"), authMode: authCredentialMount}

	require.NoError(t, MountEngineHome(cw, present.Mount{HostDir: "/proj/.ctxloom/state/h/home/mock", TargetDir: "/home/ctxloom/.ctxloom/home/mock"}))

	require.Len(t, cw.extraMounts, 1)
	assert.Empty(t, sink.String())
}
