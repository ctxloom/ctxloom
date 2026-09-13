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

// relocatedEngineHome is where a container cell's advice tells the engine its
// home is (containerEngineHome's target for hostEngineHome).
const relocatedEngineHome = "/home/ctxloom/.ctxloom/home/claude"

// claudeContainerWorkspace is a container workspace whose auth resolved to the
// credential mount, for claude, exactly as prepareContainerScratch leaves it —
// the only state MountEngineHome reads to decide the credential overlay.
func claudeContainerWorkspace(mode containerAuthMode) *containerWorkspace {
	return &containerWorkspace{
		home:       "/home/ctxloom",
		runtime:    fakeRuntime{},
		engineSpec: engineContainerSpecFor("claude-code"),
		authMode:   mode,
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
	cw := &containerWorkspace{home: "/home/ctxloom", runtime: fakeRuntime{}, engineSpec: engineContainerSpecFor("mock"), authMode: authCredentialMount}

	require.NoError(t, MountEngineHome(cw, present.Mount{HostDir: "/proj/.ctxloom/state/h/home/mock", TargetDir: "/home/ctxloom/.ctxloom/home/mock"}))

	require.Len(t, cw.extraMounts, 1)
	assert.Empty(t, sink.String())
}
