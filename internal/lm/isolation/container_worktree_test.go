package isolation

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ctxloom/ctxloom/internal/git"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestContainerWorktree_Axes pins the composed policy's identity: name
// "container-worktree", approvals BYPASS (the container is the boundary — the
// worktree half alone would only Prompt).
func TestContainerWorktree_Axes(t *testing.T) {
	c := NewContainerWorktree(fakeRuntime{name: "docker", available: true}, "img", &git.Fake{})
	assert.Equal(t, "container-worktree", c.Name())
	assert.Equal(t, ApprovalsBypass, c.Approvals(), "a fan-out container member bypasses the in-engine prompt")
	assert.True(t, Isolated(c), "container-worktree is an isolated policy (writes per-member config into its worktree)")
}

// TestContainerWorktree_GitdirMountIsIdentityPath is the gitdir-when-mounted (§4a
// T1.1) unit: the .git mirror mount resolves the worktree's git common-dir and maps
// it at its IDENTICAL host path, so the worktree's `gitdir:` pointer resolves
// inside the container.
func TestContainerWorktree_GitdirMountIsIdentityPath(t *testing.T) {
	c := NewContainerWorktree(fakeRuntime{name: "docker", available: true}, "img",
		&git.Fake{CommonDirValue: "/repo/.git"})
	m, err := c.gitdirMount(context.Background(), "/tmp/ctxloom-wt-m-abc")
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

	c := NewContainerWorktree(fakeRuntime{name: "docker", available: true}, "img",
		&git.Fake{CommonDirValue: common})
	gitMount, err := c.gitdirMount(context.Background(), worktreeDir)
	require.NoError(t, err)

	// buildRunSpec is exactly what containerRunnerFunc renders: workDir = the
	// worktree, extraMounts carrying the gitdir mirror.
	spec := buildRunSpec("img", "name", worktreeDir, "/root",
		[]string{"/usr/local/bin/ctxloom", "llm", "serve", "mock"},
		"/run/ctxloom/plugin", "/tmp/host-sock/plugin1",
		[]string{}, nil, []Mount{gitMount})

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
	_, err := NewContainerWorktree(fakeRuntime{name: "docker", available: false}, "img", f).
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
	outer := filepath.Join(os.TempDir(), "ctxloom-wt-cwt")
	f := &git.Fake{Worktrees: []git.Worktree{{Path: outer}}}
	wt := &worktreeWorkspace{git: f, repoDir: "/proj", dir: outer}
	ws := &containerWorktreeWorkspace{wt: wt, scratchRoot: scratch, agentID: "m"}

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
	ws := &containerWorktreeWorkspace{wt: &worktreeWorkspace{}, agentID: "m"}
	assert.Nil(t, WorkspaceEnv(ws), "no host config-home envs cross into the container")
}

// TestChainFor_NonContainer pins the deterministic (runtime-independent) chains:
// default/none axes resolve to none only; a worktree workspace leads with the
// bare worktree then none; unknown axis values act as the axis defaults.
func TestChainFor_NonContainer(t *testing.T) {
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

// TestChainFor_Container pins the runtime-axis chains and their independence
// from the workspace axis: {worktree, container} leads with
// worktree-in-container then degrades the RUNTIME axis first (worktree
// survives); {none, container} leads with the live-dir Container and degrades
// straight to none — it never grows a worktree that wasn't requested. With no
// runtime available the container tier is dropped up front, leaving exactly
// the requested workspace.
func TestChainFor_Container(t *testing.T) {
	both := chainFor(Axes{Workspace: WorkspaceWorktree, Runtime: RuntimeContainer}, "claude-code", ImageConfig{})
	live := chainFor(Axes{Runtime: RuntimeContainer}, "claude-code", ImageConfig{})
	if (Docker{}).Available() || (Podman{}).Available() {
		require.Len(t, both, 3)
		assert.IsType(t, ContainerWorktree{}, both[0], "{worktree, container} leads with worktree-in-container")
		assert.IsType(t, Worktree{}, both[1], "the runtime axis degrades first; the worktree survives")
		assert.IsType(t, None{}, both[2], "then none")

		require.Len(t, live, 2)
		assert.IsType(t, Container{}, live[0], "{none, container} mounts the LIVE project dir")
		assert.IsType(t, None{}, live[1], "and degrades to none — never into an unrequested worktree")
	} else {
		require.Len(t, both, 2)
		assert.IsType(t, Worktree{}, both[0], "no runtime drops only the container dimension")
		assert.IsType(t, None{}, both[1])

		require.Len(t, live, 1)
		assert.IsType(t, None{}, live[0], "no runtime and no workspace request leaves none")
	}
}

// TestPrepareChain_DegradesToFirstSuccess exercises the fan-out degrade WALK: a
// failing worktree-in-container degrades to a bare worktree; when every isolated
// tier fails (non-git repo) it falls to none on the shared project dir.
func TestPrepareChain_DegradesToFirstSuccess(t *testing.T) {
	ctx := context.Background()
	common := t.TempDir() // stand-in .git common dir so the worktree exclude write succeeds

	// container-worktree can't launch (unavailable runtime) → degrade; the bare
	// worktree prepares → chain stops there.
	failing := NewContainerWorktree(fakeRuntime{name: "docker", available: false}, "img",
		&git.Fake{CommonDirValue: common})
	working := NewWorktree(&git.Fake{CommonDirValue: common})
	pol, ws := prepareChain(ctx, []Policy{failing, working, None{}}, "/proj", "m")
	require.NotNil(t, ws)
	assert.Equal(t, "worktree", pol.Name(), "container→worktree when the container can't launch")
	assert.True(t, strings.HasPrefix(ws.Dir(), os.TempDir()), "the degraded workspace is a bare worktree")
	_ = ws.Cleanup()

	// Non-git repo: both isolated tiers fail → none on the shared project dir.
	nonRepo := &git.Fake{Repos: map[string]bool{}}
	pol2, ws2 := prepareChain(ctx,
		[]Policy{
			NewContainerWorktree(fakeRuntime{name: "docker", available: false}, "img", nonRepo),
			NewWorktree(nonRepo),
			None{},
		}, "/proj", "m")
	require.NotNil(t, ws2)
	assert.Equal(t, "none", pol2.Name(), "worktree→none on a non-git repo")
	assert.Equal(t, "/proj", ws2.Dir(), "none keeps the shared project dir")
	_ = ws2.Cleanup()
}
