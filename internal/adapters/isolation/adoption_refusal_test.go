package isolation

import (
	"context"
	"errors"
	"os"
	"strconv"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/adapters/git"
	"github.com/ctxloom/ctxloom/internal/core/launch"
	"github.com/ctxloom/ctxloom/internal/shared/report"
	"github.com/ctxloom/ctxloom/internal/shared/strictness"
	"github.com/ctxloom/ctxloom/internal/testsupport"
)

// An occupied checkout path the resume cannot adopt is the WORKSPACE's
// refusal. verprop's lame-wide-chant relaunch (session jumpy-mossy-gusto)
// was refused four times over its own preserved checkout, and each refusal
// reached the operator as "container isolation was requested but could not
// start" with a fix to install the image or run with `runtime: host` — a
// remedy that fixes nothing and weakens the sandbox.

// unadoptableCheckoutErr is the refusal a resume gets when its deterministic
// checkout path holds a registered worktree on another branch.
func unadoptableCheckoutErr(t *testing.T) (path string, err error) {
	t.Helper()
	testsupport.Isolate(t)
	f := &git.Fake{CommonDirValue: t.TempDir()}
	w := sessionWorktree(t, f)
	path, perr := w.checkoutPath("worker")
	require.NoError(t, perr)
	require.NoError(t, os.MkdirAll(path, 0o755))
	f.Worktrees = []git.Worktree{{Path: path, Branch: git.MustParseBranch("someone/else")}}
	_, err = w.prepareWorkspace(context.Background(), "/proj", "worker")
	require.Error(t, err, "precondition: the occupant is refused")
	return path, err
}

// The refusal is typed (launch.ErrWorkspaceUnadoptable), names the path and
// both branches, and carries its own remedy — one that never sends the
// operator to the runtime.
func TestReuseExistingCheckout_AWrongBranchIsATypedWorkspaceRefusal(t *testing.T) {
	path, err := unadoptableCheckoutErr(t)

	assert.ErrorIs(t, err, launch.ErrWorkspaceUnadoptable)
	assert.Contains(t, err.Error(), strconv.Quote(path), "the refusal names the checkout")
	assert.Contains(t, err.Error(), "someone/else", "and the branch it found there")
	assert.Contains(t, err.Error(), "ctxloom/", "and the branch this agent's naming expects")

	var r report.Remediable
	require.True(t, errors.As(err, &r), "the refusal names its own fix")
	assert.Contains(t, r.Remedy(), path, "the fix names the checkout to inspect")
	assert.NotContains(t, r.Remedy(), "runtime", "the runtime is not what failed")
}

// unadoptablePolicy is a container link whose workspace is a checkout it
// cannot adopt — the incident's container-worktree, reduced to the error its
// worktree base returned.
type unadoptablePolicy struct {
	failingPolicy
	err error
}

func (u unadoptablePolicy) resolveWorkspace(context.Context, string, string) (workspace, error) {
	return nil, u.err
}

func (u unadoptablePolicy) prepareWorkspace(ctx context.Context, projectDir, agentID string) (workspace, error) {
	return resolveAndBind(ctx, u, projectDir, agentID)
}

// The chain stops on an unadoptable checkout: it is not a lost container
// (no isolation finding, so no "runtime: host" remedy), and no later link
// is tried — the host worktree link meets the same occupant, and None would
// run the agent in the parent's live tree.
func TestPrepareChain_AnUnadoptableCheckoutIsNotALostContainer(t *testing.T) {
	resetStrictness(t)
	stubRuntimeCandidates(t)
	_, refusal := unadoptableCheckoutErr(t)
	next := &git.Fake{CommonDirValue: t.TempDir()}
	chain := []policy{
		unadoptablePolicy{failingPolicy: failingPolicy{name: PolicyNameContainerWorktree}, err: refusal},
		sessionWorktree(t, next),
		None{},
	}

	pol, ws, err := prepareChain(context.Background(), chain, surveyRuntimes(), RuntimeContainerRootless, "/proj", "worker")

	require.ErrorIs(t, err, launch.ErrWorkspaceUnadoptable, "the chain returns the workspace's refusal")
	assert.Equal(t, None{}.Name(), pol.Name())
	require.NotNil(t, ws)
	assert.Empty(t, strictness.All(), "THE POINT: no container-could-not-start finding, no runtime remedy")
	assert.Empty(t, next.Calls, "no later link touched git")
}

// End to end on the host worktree axis, against real git: a resume whose
// checkout path holds a worktree on another branch is REFUSED with the
// typed error — not degraded to the parent's live tree — and the
// BeforeCheckout hook (the parent snapshot) never runs.
func TestPrepare_AnUnadoptableCheckoutRefusesTheRunAndCommitsNothing(t *testing.T) {
	home := fakeHostHome(t, tokenFixture)
	project := gitRepo(t)
	w := NewWorktree(git.NewExec())
	w.state = SessionState{Harp: harpA}
	path, err := w.checkoutPath(harpA)
	require.NoError(t, err)
	gitOut(t, project, "worktree", "add", "-q", "-b", "someone/else", path)
	t.Cleanup(func() { _ = os.RemoveAll(path) })

	calls := 0
	env, err := Prepare(context.Background(), hookedSpec(t, home, project, func(context.Context, git.Branch) (string, error) {
		calls++
		return "", nil
	}))

	require.ErrorIs(t, err, launch.ErrWorkspaceUnadoptable)
	assert.Nil(t, env, "no environment — not one degraded onto the live tree")
	assert.Zero(t, calls, "the parent is not committed for a run that will not start")
	assert.Empty(t, strictness.All(), "a workspace refusal is not an isolation finding")
	assert.Equal(t, 1, linkedWorktrees(t, project), "the occupant is left as it was")
}
