//go:build docker_integration

// live-gag's docker-gated host-base out-of-repo-worktree proof. Build-tagged so
// the normal `just test` never compiles it; run with:
//
//	GOWORK=off just test-pkg ./internal/adapters/isolation/... -tags docker_integration -run HostBaseOutOfRepoWorktree
//
// The HOST-BASE case: the user's TOP-LEVEL project dir is ITSELF an
// out-of-repo linked worktree (a plain `git worktree add` outside the main
// repo — exactly the standing worktree layout, ~/workspace/worktrees/<proj>--
// <branch>, every managed worktree included). container.go's
// gitdirMirrorMounts already handles this (unit-tested with a git.Fake in
// container_test.go); this is its real-git, real-daemon, payload-asserting
// proof, contrasted with the worktree-only mount FAILING. The git mounts are
// gitDirMounts, which the worktree base shares, so this also proves them for
// a ctxloom-managed per-agent checkout.
package isolation

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ctxloom/ctxloom/internal/adapters/git"
	"github.com/ctxloom/ctxloom/internal/core/engine"
	"github.com/ctxloom/ctxloom/internal/testsupport"
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
	// Rootless gate: this test writes the managed-config overlay scratch and reads worktree admin files
	// the container may touch; only rootless docker maps container-root to the
	// launching user so cleanup can remove anything the container wrote.
	rt := ProbeRuntime("docker")
	if d, ok := rt.(Docker); !ok || !d.rootless {
		dockergate.SkipCapability(t, "rootful docker root-owns files the host-user teardown cannot remove; needs rootless docker")
	}
	buildGitIntegrationImage(t) // shared helper, worktree_image_docker_integration_test.go (same package)

	// No host credential is needed to clear PrepareWorkspace's auth gate: the
	// policy is keyed on the mock engine, whose declaration authenticates
	// against no vendor (Vendorless). This binary cannot link the mock kind
	// (import cycle), so nothing registers "mock" unless the test does; an
	// unregistered name reaches the fail-closed default and PrepareWorkspace
	// refuses on auth. This test never starts a runner, only PrepareWorkspace.
	registerVendorlessFixture(t, "mock", engine.DistributionTestOnly)

	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	repo := initRealRepo(t) // helper from worktree_integration_test.go (same package)

	// An OUT-OF-REPO worktree — a plain `git worktree add` outside the main
	// repo, NOT ctxloom-managed. This IS the run's top-level project dir below
	// (hostBase).
	wtDir := filepath.Join(t.TempDir(), "wt")
	gitRun(t, repo, "worktree", "add", "-b", "feature", wtDir)
	// Another checkout of the same repo, never mounted into the container: its
	// registration must survive anything git does in there.
	otherDir := filepath.Join(t.TempDir(), "other")
	gitRun(t, repo, "worktree", "add", "-b", "other", otherDir)
	// A hook in the shared common dir: it must still resolve and run in-container.
	hook := filepath.Join(repo, ".git", "hooks", "post-commit")
	require.NoError(t, os.WriteFile(hook, []byte("#!/bin/sh\ntouch hook-ran\n"), 0o755))

	testsupport.Isolate(t) // the session scratch resolves under a fake $HOME, never the real ~/.ctxloom
	pol := NewContainerFor(rt, "mock").WithImage(worktreeIntegrationImage).WithSessionState(SessionState{Harp: "brisk-teal-otter"})
	ws, err := pol.prepareWorkspace(ctx, wtDir, "hostwt-itest")
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
	assert.Contains(t, cw.extraMounts, mount{Host: common, Container: common},
		"the host base mirrors the out-of-repo worktree's common dir identical-path, exactly like the worktree base")

	// PAYLOAD: in-container git works through the mounts the policy built (a
	// standalone container proof, independent of the plugin transport): the
	// worktree as cwd plus the workspace's own mount set.
	policyMounts := append([]mount{{Host: wtDir, Container: wtDir}}, cw.extraMounts...)
	statusOut, err := dockerRun(ctx, worktreeIntegrationImage, wtDir, policyMounts,
		"git", "-c", "safe.directory=*", "status", "--porcelain")
	require.NoError(t, err, "git status must resolve inside the container via the mounted common-dir:\n%s", statusOut)

	gitDirOut, err := dockerRun(ctx, worktreeIntegrationImage, wtDir, policyMounts,
		"git", "-c", "safe.directory=*", "rev-parse", "--git-dir")
	require.NoError(t, err, "git rev-parse --git-dir must resolve inside the container:\n%s", gitDirOut)
	t.Logf("in-container --git-dir (host-base out-of-repo worktree): %s", strings.TrimSpace(gitDirOut))

	// Every kind of write git makes to the shared common dir lands on the host:
	// config and packed-refs by lock-and-rename, a commit's objects and ref,
	// the hook it runs. And the prune that motivated the registry mask cannot
	// reach the other checkout's registration.
	script := `set -e
git config --global safe.directory '*'
git config user.name itest && git config user.email itest@example.com
git config ctxloom.itest marker
echo change > change.txt && git add change.txt && git commit -qm in-container
git pack-refs --all
git branch doomed && git pack-refs --all && git branch -D doomed
git worktree prune
git worktree list --porcelain`
	writeOut, err := dockerRun(ctx, worktreeIntegrationImage, wtDir, policyMounts, "sh", "-c", script)
	require.NoError(t, err, "git writes must succeed in-container through the policy's mounts:\n%s", writeOut)
	assert.NotContains(t, writeOut, otherDir, "the other checkout's registration is not visible in-container")
	assert.Equal(t, "in-container", gitRun(t, wtDir, "log", "-1", "--format=%s"), "the commit landed on the host")
	assert.Equal(t, "marker", gitRun(t, repo, "config", "ctxloom.itest"), "the config write landed on the host")
	assert.FileExists(t, filepath.Join(wtDir, "hook-ran"), "the common dir's hook resolved and ran in-container")
	packed, err := os.ReadFile(filepath.Join(common, "packed-refs"))
	require.NoError(t, err, "pack-refs wrote the host's packed-refs")
	assert.Contains(t, string(packed), "refs/heads/feature")
	assert.NotContains(t, string(packed), "refs/heads/doomed", "deleting a packed branch rewrote the host's packed-refs")
	assert.Contains(t, gitRun(t, repo, "worktree", "list", "--porcelain"), otherDir,
		"an in-container prune must not delete another checkout's registration")

	// Contrast: under the whole common dir, the same prune deletes the other
	// checkout's registration on the host — the hazard the mask closes.
	_, err = dockerRun(ctx, worktreeIntegrationImage, wtDir,
		[]mount{{Host: wtDir, Container: wtDir}, {Host: common, Container: common}},
		"git", "-c", "safe.directory=*", "worktree", "prune")
	require.NoError(t, err)
	assert.NotContains(t, gitRun(t, repo, "worktree", "list", "--porcelain"), otherDir,
		"without the mask an in-container prune reaches the host registry")

	// Contrast: mounting ONLY the worktree (no common-dir mirror) FAILS — the
	// mirror is load-bearing, not incidental.
	noMirror, err := dockerRun(ctx, worktreeIntegrationImage, wtDir,
		[]mount{{Host: wtDir, Container: wtDir}},
		"git", "-c", "safe.directory=*", "rev-parse", "HEAD")
	require.Error(t, err, "without the common-dir mirror, git must NOT resolve inside the container")
	assert.Contains(t, noMirror, "not a git repository", "the gitdir pointer is unresolvable without the mirror")
	t.Logf("in-container git WITHOUT the mirror (expected failure): %s", strings.TrimSpace(noMirror))

	// hostBase teardown is a noop: Cleanup never removes the live project dir
	// (unlike the worktree base's WIP-safe remove) — the worktree survives,
	// still attached to its main repo.
	require.NoError(t, ws.Cleanup())
	assert.DirExists(t, wtDir, "the host base's Cleanup never removes the live project dir")
	list := gitRun(t, repo, "worktree", "list", "--porcelain")
	assert.Contains(t, list, wtDir, "the out-of-repo worktree is untouched by the host-base teardown")
}
