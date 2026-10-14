package isolation

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/adapters/git"
	"github.com/ctxloom/ctxloom/internal/core/agents"
	"github.com/ctxloom/ctxloom/internal/core/launch"
	"github.com/ctxloom/ctxloom/internal/shared/tasks/taskstest"
)

// worktreeAxes is a host run on the worktree axis: the run that carves a
// checkout with no container in the way.
var worktreeAxes = launch.Axes{Workspace: launch.WorkspaceWorktree, Runtime: launch.RuntimeHost}

// hookedSpec is envSpec with a BeforeCheckout hook.
func hookedSpec(t *testing.T, home, project string, hook func(context.Context) error) Spec {
	t.Helper()
	s, err := NewSpec(worktreeAxes, claudeEngine(t)).Project(project).
		Session(sessionDir(home, harpA), SessionState{Harp: harpA}).
		Image(ImageConfig{Image: "img"}).Home(agents.HomeModeSession).
		BeforeCheckout(hook).Build()
	require.NoError(t, err)
	return s
}

// linkedWorktrees counts project's linked worktrees (the main one excluded).
func linkedWorktrees(t *testing.T, project string) int {
	t.Helper()
	list, err := git.NewExec().WorktreeList(context.Background(), project)
	require.NoError(t, err)
	return len(list) - 1
}

// The hook runs once, BEFORE the checkout is carved: what it does to the
// project's branch (the dirty-tree "commit") is what the checkout is carved
// from.
func TestPrepare_BeforeCheckoutRunsOnceBeforeTheCheckoutIsCarved(t *testing.T) {
	home := fakeHostHome(t, tokenFixture)
	project := gitRepo(t)
	calls, seen := 0, -1
	env, err := Prepare(context.Background(), hookedSpec(t, home, project, func(context.Context) error {
		calls++
		seen = linkedWorktrees(t, project)
		return nil
	}))
	require.NoError(t, err)
	t.Cleanup(func() { _ = env.Cleanup() })

	assert.Equal(t, 1, calls, "the hook runs once")
	assert.Equal(t, 0, seen, "no checkout existed yet when the hook ran")
	assert.Equal(t, 1, linkedWorktrees(t, project), "the checkout was carved after it")
}

// A refusal from the hook refuses the run with the hook's own error: no
// checkout is carved and the chain does not degrade to the live tree.
func TestPrepare_BeforeCheckoutRefusalRefusesTheRun(t *testing.T) {
	home := fakeHostHome(t, tokenFixture)
	project := gitRepo(t)
	refusal := errors.New("the handler says no")
	env, err := Prepare(context.Background(), hookedSpec(t, home, project, func(context.Context) error { return refusal }))

	require.ErrorIs(t, err, refusal)
	assert.Nil(t, env, "no environment, not a degraded one on the live tree")
	assert.Zero(t, linkedWorktrees(t, project), "no checkout is carved")
}

// A resume that REUSES its surviving checkout carves nothing, so the hook
// does not run: a commit on the project's branch would never reach a
// checkout already on its own branch.
func TestPrepare_BeforeCheckoutDoesNotRunOnAReusedCheckout(t *testing.T) {
	home := fakeHostHome(t, tokenFixture)
	project := gitRepo(t)
	first, err := Prepare(context.Background(), hookedSpec(t, home, project, nil))
	require.NoError(t, err)
	t.Cleanup(func() { _ = first.Cleanup() })
	require.Equal(t, 1, linkedWorktrees(t, project), "precondition: the first run's checkout survives")

	calls := 0
	resumed, err := Prepare(context.Background(), hookedSpec(t, home, project, func(context.Context) error {
		calls++
		return nil
	}))
	require.NoError(t, err)
	t.Cleanup(func() { _ = resumed.Cleanup() })

	assert.Equal(t, first.Placement().Paths.Paths().ProjectRoot.Host, resumed.Placement().Paths.Paths().ProjectRoot.Host,
		"precondition: the resume reused the surviving checkout")
	assert.Zero(t, calls, "nothing was carved, so the hook did not run")
}

// The checkout stamps reach every link that can carve a checkout: the host
// worktree policy and a container's worktree base.
func TestWithCheckoutStamps_StampsEveryLinkThatCarves(t *testing.T) {
	hook := func(context.Context) error { return nil }
	chain := withCheckoutStamps([]policy{
		NewWorktree(&git.Fake{}),
		Container{base: worktreeBase{wt: NewWorktree(&git.Fake{})}},
		None{},
	}, checkoutStamps{beforeCheckout: hook, commitOnTeardown: true})

	host := chain[0].(Worktree)
	inContainer := chain[1].(Container).base.(worktreeBase).wt
	assert.NotNil(t, host.beforeCheckout, "the host worktree's hook")
	assert.True(t, host.commitOnTeardown, "the host worktree's teardown commit")
	assert.NotNil(t, inContainer.beforeCheckout, "the container worktree base's hook")
	assert.True(t, inContainer.commitOnTeardown, "the container worktree base's teardown commit")
}

// A hook refusal stops the chain where it is: no later link is tried, so a
// container chain never falls through to its host worktree link and runs
// the hook (and the handler behind it) a second time.
func TestPrepareChain_StopsOnACheckoutRefusal(t *testing.T) {
	refusing := sessionWorktree(t, &git.Fake{CommonDirValue: t.TempDir()})
	refusing.beforeCheckout = func(context.Context) error { return errors.New("no") }
	next := &git.Fake{CommonDirValue: t.TempDir()}
	later := sessionWorktree(t, next)
	laterCalls := 0
	later.beforeCheckout = func(context.Context) error { laterCalls++; return nil }

	pol, ws, _ := prepareChain(context.Background(), []policy{refusing, later, None{}}, surveyRuntimes(), RuntimeHost, "/proj", "member-a")

	assert.Equal(t, None{}.Name(), pol.Name(), "the chain ends without a workspace link")
	require.NotNil(t, ws)
	assert.Zero(t, laterCalls, "no later link ran the hook again")
	assert.Empty(t, next.Calls, "no later link touched git")
}

// teardownSpec is hookedSpec with CommitOnTeardown.
func teardownSpec(t *testing.T, home, project string, hook func(context.Context) error) Spec {
	t.Helper()
	s, err := NewSpec(worktreeAxes, claudeEngine(t)).Project(project).
		Session(sessionDir(home, harpA), SessionState{Harp: harpA}).
		Image(ImageConfig{Image: "img"}).Home(agents.HomeModeSession).
		BeforeCheckout(hook).CommitOnTeardown().Build()
	require.NoError(t, err)
	return s
}

// committingRepo is gitRepo with a repo-local identity, so a commit ctxloom
// makes in it (and in its linked worktrees) needs nothing from the host.
func committingRepo(t *testing.T) string {
	t.Helper()
	project := gitRepo(t)
	gitOut(t, project, "config", "user.name", "t")
	gitOut(t, project, "config", "user.email", "t@t")
	return project
}

// gitOut runs git in dir and returns its trimmed output.
func gitOut(t *testing.T, dir string, args ...string) string {
	t.Helper()
	out, err := taskstest.GitCmd(dir, nil, args...).CombinedOutput()
	require.NoError(t, err, "git %v: %s", args, out)
	return strings.TrimSpace(string(out))
}

// A run that commits on teardown leaves nothing uncommitted: the work in
// its checkout — tracked and untracked — is committed on the checkout's own
// branch under a message naming the agent, the checkout is then clean and is
// removed, and the branch keeps the work. Without it, the same work is left
// uncommitted in a checkout the teardown must preserve.
func TestCleanup_CommitsTheRunsUncommittedWorkOnItsBranch(t *testing.T) {
	for _, commit := range []bool{true, false} {
		t.Run(map[bool]string{true: "commit on teardown", false: "no commit on teardown"}[commit], func(t *testing.T) {
			home := fakeHostHome(t, tokenFixture)
			project := committingRepo(t)
			s := hookedSpec(t, home, project, nil)
			if commit {
				s = teardownSpec(t, home, project, nil)
			}
			env, err := Prepare(context.Background(), s)
			require.NoError(t, err)
			checkout := env.Placement().Paths.Paths().ProjectRoot.Host
			branch := mustWorktreeBranch(t, checkout)
			require.NoError(t, os.WriteFile(filepath.Join(checkout, "work.txt"), []byte("the agent's work"), 0o644))

			require.NoError(t, env.Cleanup())

			if !commit {
				assert.DirExists(t, checkout, "uncommitted work keeps its checkout")
				assert.Equal(t, "?? work.txt", gitOut(t, checkout, "status", "--porcelain"))
				return
			}
			assert.NoDirExists(t, checkout, "a committed, clean checkout is torn down")
			assert.Equal(t, "the agent's work", gitOut(t, project, "show", branch.Short()+":work.txt"), "the work is on the agent's branch")
			subject := gitOut(t, project, "log", "-1", "--format=%s", branch.Short())
			assert.Equal(t, "ctxloom: commit "+harpA+"'s uncommitted work at run end", subject)
		})
	}
}

// A resume after a committed teardown re-enters the agent's own branch: the
// checkout is recreated on the EXISTING branch, at its tip, so the agent
// continues its own work — and nothing is carved from the project's HEAD, so
// the BeforeCheckout hook (the parent commit) does not run.
func TestPrepare_ReentersTheAgentsExistingBranch(t *testing.T) {
	home := fakeHostHome(t, tokenFixture)
	project := committingRepo(t)
	first, err := Prepare(context.Background(), teardownSpec(t, home, project, nil))
	require.NoError(t, err)
	checkout := first.Placement().Paths.Paths().ProjectRoot.Host
	require.NoError(t, os.WriteFile(filepath.Join(checkout, "work.txt"), []byte("the agent's work"), 0o644))
	require.NoError(t, first.Cleanup())
	require.NoDirExists(t, checkout, "precondition: the checkout was torn down, its branch kept")

	calls := 0
	resumed, err := Prepare(context.Background(), teardownSpec(t, home, project, func(context.Context) error {
		calls++
		return nil
	}))
	require.NoError(t, err, "the existing branch is re-entered, not refused by `worktree add -b`")
	t.Cleanup(func() { _ = resumed.Cleanup() })

	got, err := os.ReadFile(filepath.Join(resumed.Placement().Paths.Paths().ProjectRoot.Host, "work.txt"))
	require.NoError(t, err)
	assert.Equal(t, "the agent's work", string(got), "the resumed checkout holds the agent's committed work")
	assert.Zero(t, calls, "nothing was carved from the project's HEAD, so the parent is not committed")
}

// A run-end commit that fails leaves the work uncommitted, where the
// teardown then preserves it: a failed commit is never read as leave to
// remove the checkout.
func TestCommitRunEnd_AFailedCommitLeavesTheWorkForTheTeardownToKeep(t *testing.T) {
	f := &git.Fake{
		Dirty:              map[string]bool{"/wt": true},
		CurrentBranchValue: git.MustParseBranch("ctxloom/x"),
		CommitAllErr:       errors.New("index.lock exists"),
	}
	commitRunEnd(context.Background(), f, "/wt", "brisk-teal-otter")
	require.Len(t, f.CommitMessages, 1, "the commit was attempted")
	teardownWorktree(context.Background(), f, "/repo", "/wt")
	assert.Empty(t, f.Removed, "the dirty checkout is kept")
}

// A clean checkout is not committed: nothing would be in the commit.
func TestCommitRunEnd_ACleanCheckoutIsNotCommitted(t *testing.T) {
	f := &git.Fake{CurrentBranchValue: git.MustParseBranch("ctxloom/x")}
	commitRunEnd(context.Background(), f, "/wt", "brisk-teal-otter")
	assert.Empty(t, f.CommitMessages)
}
