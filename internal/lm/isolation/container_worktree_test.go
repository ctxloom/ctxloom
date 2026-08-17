package isolation

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ctxloom/ctxloom/internal/git"
	"github.com/ctxloom/ctxloom/internal/shared/strictness"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestContainerWorktree_Axes pins the composed policy's identity: name
// "container-worktree", and that it is an isolated policy.
func TestContainerWorktree_Axes(t *testing.T) {
	c := NewContainerWorktreeFor(fakeRuntime{name: "docker", available: true}, "mock", ImageConfig{Image: "img"}, &git.Fake{})
	assert.Equal(t, "container-worktree", c.Name())
	assert.True(t, Isolated(c), "container-worktree is an isolated policy (writes per-member config into its worktree)")
}

// TestContainerWorktree_GitdirMountIsIdentityPath is the gitdir-when-mounted (§4a
// T1.1) unit: the .git mirror mount resolves the worktree's git common-dir and maps
// it at its IDENTICAL host path, so the worktree's `gitdir:` pointer resolves
// inside the container.
func TestContainerWorktree_GitdirMountIsIdentityPath(t *testing.T) {
	// The worktree base mirrors the worktree's .git common-dir identical-path
	// (gitCommonDirMount) — the collapse of the former ContainerWorktree.gitdirMount.
	m, err := gitCommonDirMount(context.Background(),
		fakeRuntime{name: "docker", available: true},
		&git.Fake{CommonDirValue: "/repo/.git"}, "/tmp/ctxloom-wt-m-abc")
	require.NoError(t, err)
	assert.Equal(t, Mount{Host: "/repo/.git", Container: "/repo/.git"}, m,
		"the .git common-dir is mirrored identical-path so gitdir resolves in-container")
}

// TestContainerWorktree_RunSpecMountsWorktreeAndGitdir proves the run spec the
// container launcher builds carries BOTH the identical-path WORKTREE mount (cwd)
// and the .git gitdir mirror — the two mounts that make git resolve inside the
// container over the member's own checkout.
func TestContainerWorktree_RunSpecMountsWorktreeAndGitdir(t *testing.T) {
	const common = "/repo/.git"
	worktreeDir := filepath.Join(os.TempDir(), "ctxloom-wt-m-xyz")

	gitMount, err := gitCommonDirMount(context.Background(),
		fakeRuntime{name: "docker", available: true},
		&git.Fake{CommonDirValue: common}, worktreeDir)
	require.NoError(t, err)

	// buildRunSpec is exactly what containerRunnerFunc renders: workDir = the
	// worktree, extraMounts carrying the gitdir mirror.
	spec := buildRunSpec("img", "name", worktreeDir, "/root",
		[]string{"/usr/local/bin/ctxloom", "llm", "serve", "mock"},
		"/run/ctxloom/plugin", "/tmp/host-sock/plugin1",
		[]string{}, nil, []Mount{gitMount}, nil)

	assert.Equal(t, worktreeDir, spec.WorkDir, "cwd is the member's worktree, not the live project")
	assert.Contains(t, spec.Mounts, Mount{Host: worktreeDir, Container: worktreeDir},
		"the worktree is bind-mounted identical-path as cwd")
	assert.Contains(t, spec.Mounts, gitMount,
		"the .git gitdir mirror is mounted so git resolves inside the container")
}

// TestContainerWorktree_PrepareDegradesBeforeWorktree: when the container can't
// launch (unavailable runtime), PrepareWorkspace fails at the container gate BEFORE
// any worktree is created — a clean degrade so the fan-out chain retries as a bare
// worktree with nothing to unwind.
func TestContainerWorktree_PrepareDegradesBeforeWorktree(t *testing.T) {
	f := &git.Fake{}
	_, err := NewContainerWorktreeFor(fakeRuntime{name: "docker", available: false}, "mock", ImageConfig{Image: "img"}, f).
		PrepareWorkspace(context.Background(), "/proj", "m")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "cannot launch")
	assert.Empty(t, f.Calls, "no worktree is created when the container gate fails")
}

// TestContainerWorktreeWorkspace_CleanupOrdering: the composed workspace's Dir() is
// the WORKTREE (not the project), and Cleanup removes the container scratch AND
// runs the WIP-safe worktree teardown, idempotently.
func TestContainerWorktreeWorkspace_CleanupOrdering(t *testing.T) {
	scratch, err := os.MkdirTemp("", "ctxloom-iso-cwt-")
	require.NoError(t, err)
	// Safety net: this test's whole point is exercising ws.Cleanup() itself, so
	// a mutant that breaks its removal logic (or an earlier failure/panic)
	// must not leak this fixture dir under the OS temp dir.
	t.Cleanup(func() { _ = os.RemoveAll(scratch) })
	outer := filepath.Join(os.TempDir(), "ctxloom-wt-cwt")
	f := &git.Fake{Worktrees: []git.Worktree{{Path: outer}}}
	wt := &worktreeWorkspace{git: f, repoDir: "/proj", dir: outer}
	// Post-collapse the worktree-in-container workspace IS the unified
	// containerWorkspace: dir = the worktree checkout, baseCleanup = the worktree's
	// WIP-safe teardown.
	ws := &containerWorkspace{dir: outer, scratchRoot: scratch, agentID: "m", baseCleanup: wt.Cleanup}

	assert.Equal(t, outer, ws.Dir(), "Dir() is the worktree checkout, not the live project")

	require.NoError(t, ws.Cleanup())
	_, statErr := os.Stat(scratch)
	assert.True(t, os.IsNotExist(statErr), "cleanup removes the host container scratch")
	assert.Equal(t, []string{outer}, f.Removed, "cleanup runs the WIP-safe worktree teardown")

	before := len(f.Calls)
	assert.NoError(t, ws.Cleanup(), "cleanup is idempotent")
	assert.Equal(t, before, len(f.Calls), "a second cleanup makes no further git calls")
}

// TestContainerWorktreeWorkspace_NoConfigHomeEnv: the composed workspace does NOT
// expose per-agent config-home envs — the engine runs inside the container with a
// fresh HOME, so those host paths would be meaningless there.
func TestContainerWorktreeWorkspace_NoConfigHomeEnv(t *testing.T) {
	ws := &containerWorkspace{agentID: "m", baseCleanup: (&worktreeWorkspace{}).Cleanup}
	assert.Nil(t, WorkspaceEnv(ws), "no host config-home envs cross into the container")
}

// TestChainFor_NonContainer pins the deterministic (runtime-independent) chains:
// default/none axes resolve to none only; a worktree workspace leads with the
// bare worktree then none; unknown axis values act as the axis defaults.
func TestChainFor_NonContainer(t *testing.T) {
	// The unknown-runtime case below records a fatal ClassIsolation finding
	// (warnUnknownAxes); reset so it never bleeds into a later test.
	resetStrictness(t)
	for _, axes := range []Axes{{}, {Workspace: WorkspaceShared, Runtime: RuntimeHost}} {
		chain := chainFor(axes, "claude-code", ImageConfig{})
		require.Len(t, chain, 1, "axes %+v", axes)
		assert.IsType(t, None{}, chain[0])
	}

	wt := chainFor(Axes{Workspace: WorkspaceWorktree}, "claude-code", ImageConfig{})
	require.Len(t, wt, 2)
	assert.IsType(t, Worktree{}, wt[0], "a worktree workspace leads with the bare worktree")
	assert.IsType(t, None{}, wt[1], "then degrades to none on a non-git repo")

	unknown := chainFor(Axes{Workspace: "bogus", Runtime: "bogus"}, "claude-code", ImageConfig{})
	require.Len(t, unknown, 1)
	assert.IsType(t, None{}, unknown[0], "unknown axis values act as the axis defaults")
}

// TestChainFor_Container pins the runtime-axis chains (with a runtime AVAILABLE)
// and their independence from the workspace axis. It drives the probe
// HERMETICALLY through the selectRuntimeProbe seam — never a real docker/podman
// daemon — so the chain shape is deterministic on any host: {worktree,
// container} leads with worktree-in-container then degrades the RUNTIME axis
// first (the worktree survives) before None; {none, container} leads with the
// live-dir Container and degrades straight to None — it never grows a worktree
// that wasn't requested. The no-runtime shape (the container tier dropped up
// front) is pinned separately, also hermetically, in
// TestChainFor_NoRuntime_FatalUnlessDegraded.
func TestChainFor_Container(t *testing.T) {
	resetStrictness(t)
	stubRuntimeProbe(t, fakeRuntime{name: "docker", available: true})

	both := chainFor(Axes{Workspace: WorkspaceWorktree, Runtime: RuntimeContainerRootless}, "claude-code", ImageConfig{})
	require.Len(t, both, 3)
	assert.IsType(t, Container{}, both[0], "{worktree, container} leads with a container (worktree base)")
	assert.Equal(t, "container-worktree", both[0].Name(), "the worktree-base container reports the composed name")
	assert.IsType(t, Worktree{}, both[1], "the runtime axis degrades first; the worktree survives")
	assert.IsType(t, None{}, both[2], "then none")

	live := chainFor(Axes{Runtime: RuntimeContainerRootless}, "claude-code", ImageConfig{})
	require.Len(t, live, 2)
	assert.IsType(t, Container{}, live[0], "{none, container} mounts the LIVE project dir")
	assert.IsType(t, None{}, live[1], "and degrades to none — never into an unrequested worktree")

	assert.Empty(t, strictness.All(), "an available runtime satisfies the container request — no isolation finding")
}

// TestPrepareChain_DegradesToFirstSuccess exercises the fan-out degrade WALK: a
// failing worktree-in-container degrades to a bare worktree; when every isolated
// tier fails (non-git repo) it falls to none on the shared project dir.
func TestPrepareChain_DegradesToFirstSuccess(t *testing.T) {
	// Each scenario degrades a container-worktree tier to a non-container tier,
	// which records a fatal ClassIsolation finding (the lost container boundary);
	// reset so those findings never bleed into a later test.
	resetStrictness(t)
	ctx := context.Background()
	common := t.TempDir() // stand-in .git common dir so the worktree exclude write succeeds

	// container-worktree can't launch (unavailable runtime) → degrade; the bare
	// worktree prepares → chain stops there.
	failing := NewContainerWorktreeFor(fakeRuntime{name: "docker", available: false}, "mock", ImageConfig{Image: "img"}, &git.Fake{CommonDirValue: common})
	working := NewWorktree(&git.Fake{CommonDirValue: common}, "")
	pol, ws := prepareChain(ctx, []Policy{failing, working, None{}}, "/proj", "m")
	require.NotNil(t, ws)
	// Safety net registered BEFORE the assertions below can fail/panic and skip
	// the manual, non-deferred ws.Cleanup() call at the end of this block (see
	// requireCleanWorkspace's doc in worktree_integration_test.go).
	requireCleanWorkspace(t, ws)
	assert.Equal(t, "worktree", pol.Name(), "container→worktree when the container can't launch")
	assert.True(t, strings.HasPrefix(ws.Dir(), os.TempDir()), "the degraded workspace is a bare worktree")
	_ = ws.Cleanup()

	// Non-git repo: both isolated tiers fail → none on the shared project dir.
	nonRepo := &git.Fake{Repos: map[string]bool{}}
	pol2, ws2 := prepareChain(ctx,
		[]Policy{
			NewContainerWorktreeFor(fakeRuntime{name: "docker", available: false}, "mock", ImageConfig{Image: "img"}, nonRepo),
			NewWorktree(nonRepo, ""),
			None{},
		}, "/proj", "m")
	require.NotNil(t, ws2)
	requireCleanWorkspace(t, ws2)
	assert.Equal(t, "none", pol2.Name(), "worktree→none on a non-git repo")
	assert.Equal(t, "/proj", ws2.Dir(), "none keeps the shared project dir")
	_ = ws2.Cleanup()
}
