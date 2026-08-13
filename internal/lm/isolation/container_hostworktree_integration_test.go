//go:build docker_integration

// live-gag's docker-gated host-base out-of-repo-worktree proof. Build-tagged so
// the normal `just test` never compiles it; run with:
//
//	GOWORK=off just test-pkg ./internal/lm/isolation/... -tags docker_integration -run HostBaseOutOfRepoWorktree
//
// container_worktree_integration_test.go proves the WORKTREE-BASE path (a
// ctxloom-managed per-agent checkout); this proves the companion HOST-BASE
// case the plan calls out: the user's TOP-LEVEL project dir is ITSELF an
// out-of-repo linked worktree (a plain `git worktree add` outside the main
// repo — exactly the standing worktree layout, ~/workspace/worktrees/<proj>--
// <branch>, every managed worktree included). container.go's
// gitdirMirrorMount already handles this (unit-tested with a git.Fake in
// container_test.go); this is its real-git, real-daemon, payload-asserting
// proof, contrasted with the worktree-only mount FAILING exactly as
// container_worktree_integration_test.go's contrast does.
package isolation

import (
	"context"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ctxloom/ctxloom/internal/git"
	"github.com/ctxloom/ctxloom/internal/testsupport/dockergate"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestContainerPolicy_HostBaseOutOfRepoWorktree_GitResolves is live-gag's gate.
func TestContainerPolicy_HostBaseOutOfRepoWorktree_GitResolves(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		dockergate.SkipCapability(t, "git not on PATH, and the host-base out-of-repo-worktree test needs a real repo")
	}
	dockergate.RequireRuntime(t, (Docker{}).Available(), "the host-base out-of-repo-worktree integration test")
	// Mirrors container_worktree_integration_test.go's rootless gate: this test
	// writes the managed-config overlay scratch and reads worktree admin files
	// the container may touch; only rootless docker maps container-root to the
	// launching user so cleanup can remove anything the container wrote.
	rt := SelectRuntime("docker")
	if d, ok := rt.(Docker); !ok || !d.rootless {
		dockergate.SkipCapability(t, "rootful docker root-owns files the host-user teardown cannot remove; needs rootless docker")
	}
	buildGitIntegrationImage(t) // shared helper, container_worktree_integration_test.go (same package)

	// Env-passthrough auth satisfies the container gate without host creds
	// (PrepareWorkspace resolves auth regardless of whether a plugin is ever
	// spawned — this test never calls SpawnClient, only PrepareWorkspace).
	t.Setenv("ANTHROPIC_API_KEY", "itest-mock-key")

	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	repo := initRealRepo(t) // helper from worktree_integration_test.go (same package)

	// An OUT-OF-REPO worktree — a plain `git worktree add` outside the main
	// repo, NOT ctxloom-managed. This IS the run's top-level project dir below
	// (hostBase), unlike container_worktree_integration_test.go where the
	// worktree is the ctxloom-managed per-agent checkout.
	wtDir := filepath.Join(t.TempDir(), "wt")
	gitRun(t, repo, "worktree", "add", "-b", "feature", wtDir)

	pol := NewContainerFor(rt, "mock").WithImage(worktreeIntegrationImage)
	ws, err := pol.PrepareWorkspace(ctx, wtDir, "hostwt-itest")
	require.NoError(t, err, "PrepareWorkspace must mirror the out-of-repo worktree's git common dir")
	t.Cleanup(func() { _ = ws.Cleanup() })

	assert.Equal(t, wtDir, ws.Dir(), "the host base's workspace IS the live project dir — the out-of-repo worktree itself")

	// The mount set: {worktree identical-path (the WorkDir mount, built at spawn
	// time from ws.Dir()), <main>/.git identical-path (extraMounts, built here)}.
	cw, ok := ws.(*containerWorkspace)
	require.True(t, ok, "the host-base workspace is a container workspace")
	common, err := git.NewExec().CommonDir(ctx, wtDir)
	require.NoError(t, err)
	assert.NotEqual(t, filepath.Join(wtDir, ".git"), common, "an out-of-repo worktree's common dir lives OUTSIDE the worktree — the whole point of the mirror")
	assert.Contains(t, cw.extraMounts, Mount{Host: common, Container: common},
		"the host base mirrors the out-of-repo worktree's common dir identical-path, exactly like the worktree base")

	// PAYLOAD: in-container git resolves via the SAME two mounts the policy
	// built (a standalone container proof, independent of the plugin
	// transport — mirrors container_worktree_integration_test.go's "(2) GIT
	// RESOLVES INSIDE" step).
	statusOut, err := dockerRun(ctx, worktreeIntegrationImage, wtDir,
		[]Mount{{Host: wtDir, Container: wtDir}, {Host: common, Container: common}},
		"git", "-c", "safe.directory=*", "status", "--porcelain")
	require.NoError(t, err, "git status must resolve inside the container via the mounted common-dir:\n%s", statusOut)

	gitDirOut, err := dockerRun(ctx, worktreeIntegrationImage, wtDir,
		[]Mount{{Host: wtDir, Container: wtDir}, {Host: common, Container: common}},
		"git", "-c", "safe.directory=*", "rev-parse", "--git-dir")
	require.NoError(t, err, "git rev-parse --git-dir must resolve inside the container:\n%s", gitDirOut)
	t.Logf("in-container --git-dir (host-base out-of-repo worktree): %s", strings.TrimSpace(gitDirOut))

	// Contrast: mounting ONLY the worktree (no common-dir mirror) FAILS — the
	// mirror is load-bearing, not incidental (the exact contrast
	// container_worktree_integration_test.go draws for the worktree base).
	noMirror, err := dockerRun(ctx, worktreeIntegrationImage, wtDir,
		[]Mount{{Host: wtDir, Container: wtDir}},
		"git", "-c", "safe.directory=*", "rev-parse", "HEAD")
	require.Error(t, err, "without the common-dir mirror, git must NOT resolve inside the container")
	assert.Contains(t, noMirror, "not a git repository", "the gitdir pointer is unresolvable without the mirror")
	t.Logf("in-container git WITHOUT the mirror (expected failure): %s", strings.TrimSpace(noMirror))

	// hostBase teardown is a noop: Cleanup never removes the live project dir
	// (unlike the worktree base's WIP-safe remove) — the worktree survives,
	// still attached to its main repo.
	require.NoError(t, ws.Cleanup())
	assert.DirExists(t, wtDir, "the host base's Cleanup never removes the live project dir")
	list := gitOut(t, repo, "worktree", "list", "--porcelain")
	assert.Contains(t, list, wtDir, "the out-of-repo worktree is untouched by the host-base teardown")
}
