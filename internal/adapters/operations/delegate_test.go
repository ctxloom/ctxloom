package operations

import (
	"bytes"
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/adapters/git"
	"github.com/ctxloom/ctxloom/internal/adapters/isolation"
	"github.com/ctxloom/ctxloom/internal/core/config"
	"github.com/ctxloom/ctxloom/internal/core/launch"
	"github.com/ctxloom/ctxloom/internal/core/sessions"
	"github.com/ctxloom/ctxloom/internal/engines"
	"github.com/ctxloom/ctxloom/internal/engines/mock"
	"github.com/ctxloom/ctxloom/internal/shared/report"
	"github.com/ctxloom/ctxloom/internal/shared/strictness"
	"github.com/ctxloom/ctxloom/pkg/clifmt/clidiag"
)

// TestCellsPrepare_ContainerDegradeGate pins the cells adapter's fail-loud
// gate: an explicitly-requested container that can't start (ClassIsolation
// finding during Prepare) refuses the cell in strict mode, typed — never a
// silent host degrade — and proceeds on the degraded workspace only under
// degraded mode.
func TestCellsPrepare_ContainerDegradeGate(t *testing.T) {
	req := func(t *testing.T) launch.CellRequest {
		return launch.CellRequest{
			SessionDir:  t.TempDir(),
			Axes:        launch.Axes{Workspace: launch.WorkspaceNone, Runtime: launch.RuntimeRootless},
			Engine:      mock.New(),
			Identity:    sessions.Identity{Harp: "builder"},
			ProjectRoot: t.TempDir(),
		}
	}

	t.Run("strict: the cell is refused with the finding text", func(t *testing.T) {
		resetStrictness(t)
		stubPrepareEnvironment(t, map[string]bool{"builder": true})
		_, err := Cells{engines: engines.Registry(), cfg: config.NewFixture(config.Fixture{})}.Prepare(context.Background(), req(t))
		require.Error(t, err)
		assert.ErrorIs(t, err, launch.ErrRuntimeUnavailable)
		// "NOT sandboxed" comes from the FINDING's own message (prepareChain,
		// isolation.go), not the gate's wrapper text — isolationGateErr's
		// wrapper is deliberately neutral so it never misdescribes a
		// non-container ClassIsolation finding (e.g. a worktree's
		// credential-seed gate) using container-specific vocabulary.
		assert.Contains(t, err.Error(), "NOT sandboxed")
		assert.Contains(t, err.Error(), "container isolation was requested but could not start")
	})

	t.Run("degraded: the cell proceeds on the degraded workspace", func(t *testing.T) {
		resetStrictness(t)
		stubPrepareEnvironment(t, map[string]bool{"builder": true})
		cell, err := Cells{engines: engines.Registry(), cfg: config.NewFixture(config.Fixture{}), mode: strictness.Mode{Degraded: true}}.Prepare(context.Background(), req(t))
		require.NoError(t, err)
		_ = cell.Cleanup()
	})
}

// ===== Dirty-parent-tree spawn: the parent snapshot =====
//
// Worktree isolation carves a branch with `git worktree add -b` — a checkout
// of COMMITTED state only. A delegated child spawned into a NEW worktree
// while the parent's own project tree carries uncommitted changes gets them
// from the parent snapshot (snapshotParentTree), an off-branch commit its
// branch is carved from; the parent's branch advances to it once the child
// has started. These pin the snapshot, when it does and does not happen,
// and that it never triggers when the resolved axis isn't worktree at all.
// parent_snapshot_test.go drives the same over real git.

// captureWarnings redirects clidiag's Warn/WarnOnce sink to a buffer for the
// test's duration, returning it so assertions can inspect the exact printed
// text (payload, not just "an error happened").
func captureWarnings(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	restore := clidiag.SetSink(&buf)
	t.Cleanup(restore)
	return &buf
}

// TestSnapshotParentTree_IsDirtyErrorIsInspected pins the second of the two
// production IsDirty call sites against the claim that a caller
// writing `dirty, _ := IsDirty(…)` would silently receive the UNSAFE zero
// value. No such caller exists: isolation's unsafeToRemove is fail-closed
// (TestWorktree_TeardownAbortsOnUnknownIgnoredContentState pins that), and
// this gate is deliberately best-effort — a git failure (no binary, a workDir
// that is not a repo, which several test doubles pass) must never block the
// spawn, matching how the isolation chain's own git checks degrade.
//
// What must NOT happen either way is the error going uninspected: this pins
// that an IsDirty error yields the no-op outcome BY DECISION, so a future
// `dirty, _ :=` — which would reach the same outcome by accident, and reach a
// wrong one the moment this gate's policy changes — is a visible change here.
func TestSnapshotParentTree_IsDirtyErrorIsInspected(t *testing.T) {
	resetStrictness(t)
	fake := &git.Fake{
		DirtyErr: assert.AnError,
		// Configured dirty AND with changes: were the error ignored, the
		// commit below would be attempted instead of no-opping.
		Dirty:   map[string]bool{"/proj": true},
		Changes: []string{" M internal/foo.go"},
	}
	snap, err := snapshotParentTree(context.Background(), fake, "/proj", "coder")
	require.NoError(t, err, "an unreadable dirty state degrades to a no-op gate; it must never block the spawn")
	assert.Zero(t, snap)
	assert.Empty(t, fake.SnapshotMessages, "and must not snapshot state it could not read")
}

// ----- workspace: none / clean tree escape hatches -----

// TestCellsPrepare_DirtyParentTree_ExplicitNoneStillAllowed: a dirty parent
// tree is never committed for a spawn that explicitly opts OUT of worktree
// isolation, because a shared-checkout child sees the live tree exactly
// as-is — dirtiness is irrelevant to it, and the parent snapshot never runs.
func TestCellsPrepare_DirtyParentTree_ExplicitNoneStillAllowed(t *testing.T) {
	resetStrictness(t)
	fake := &git.Fake{Dirty: map[string]bool{"/proj": true}}
	cfg := config.NewFixture(config.Fixture{})
	p, err := Cells{engines: engines.Registry(), cfg: cfg, Git: fake}.Prepare(context.Background(), launch.CellRequest{
		SessionDir:  t.TempDir(),
		Axes:        launch.Axes{Workspace: launch.WorkspaceAxis("none"), Runtime: launch.RuntimeAxis("host")},
		Engine:      mock.New(),
		Identity:    sessions.Identity{Harp: "coder"},
		ProjectRoot: "/proj",
	})
	require.NoError(t, err)
	defer func() { _ = p.Cleanup() }()
	assert.Empty(t, fake.Calls, "the none axis never even probes commit-related git operations")
}

// TestCellsPrepare_CleanParentTree_WorktreeAllowed is the negative
// control: a clean parent tree is never snapshotted, even when the
// resolved axis IS worktree.
func TestCellsPrepare_CleanParentTree_WorktreeAllowed(t *testing.T) {
	resetStrictness(t)
	fake := &git.Fake{Dirty: map[string]bool{"/proj": false}}
	stubCarvingEnvironment(t, "/proj", nil)

	cfg := config.NewFixture(config.Fixture{Workspace: "worktree"})
	p, err := Cells{engines: engines.Registry(), cfg: cfg, Git: fake}.Prepare(context.Background(), launch.CellRequest{
		SessionDir:  t.TempDir(),
		Axes:        launch.Axes{Workspace: launch.WorkspaceAxis("worktree"), Runtime: launch.RuntimeAxis("host")},
		Engine:      mock.New(),
		Identity:    delegatedChild("coder"),
		ProjectRoot: "/proj",
	})
	require.NoError(t, err)
	defer func() { _ = p.Cleanup() }()
	assert.Empty(t, strictness.All())
	assert.Empty(t, fake.SnapshotMessages)
}

// ----- commit -----

// TestSnapshotParentTree_DetachedHeadRefuses pins the grandchild-
// coherence guard: committing inside a detached-HEAD checkout (exactly what
// a delegated child's OWN worktree looks like) would land on no branch and
// could be silently discarded when that worktree is torn down.
func TestSnapshotParentTree_DetachedHeadRefuses(t *testing.T) {
	resetStrictness(t)
	fake := &git.Fake{
		Dirty:                 map[string]bool{"/child-wt": true},
		Changes:               []string{" M f.go"},
		CurrentBranchDetached: true, // git's own "HEAD" sentinel, surfaced as the zero Branch
	}
	_, err := snapshotParentTree(context.Background(), fake, "/child-wt", "grandchild")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "detached-HEAD")
	assert.Empty(t, fake.SnapshotMessages, "never even attempts the snapshot")
}

// TestSnapshotParentTree_CurrentBranchErrorRefuses pins the fix:
// before it, `branch, _ := gitClient.CurrentBranch(...)` discarded the
// error, leaving branch=="" — which is NOT "HEAD", so the detached-HEAD guard
// above never fired and the commit proceeded on an unresolvable branch name.
// An unresolvable branch is exactly the condition the guard exists to catch
// (the caller cannot tell whether this is a bare checkout), so it must refuse
// rather than silently treat the error as "not detached".
func TestSnapshotParentTree_CurrentBranchErrorRefuses(t *testing.T) {
	resetStrictness(t)
	fake := &git.Fake{
		Dirty:            map[string]bool{"/child-wt": true},
		Changes:          []string{" M f.go"},
		CurrentBranchErr: fmt.Errorf("git rev-parse: unknown revision or path not in the working tree"),
	}
	_, err := snapshotParentTree(context.Background(), fake, "/child-wt", "grandchild")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "could not determine", "names the failure rather than silently guessing a branch")
	assert.Contains(t, err.Error(), "unknown revision", "carries the underlying git error")
	assert.Empty(t, fake.SnapshotMessages, "never even attempts the snapshot")
}

// TestSnapshotParentTree_SnapshotsOffBranchAndSaysSo pins the snapshot
// path: it commits everything (the git add -A shape) OFF-BRANCH under the
// documented message, moves no ref, and announces the snapshot naming the
// branch that will advance to it and the bounded file list.
func TestSnapshotParentTree_SnapshotsOffBranchAndSaysSo(t *testing.T) {
	resetStrictness(t)
	buf := captureWarnings(t)
	fake := &git.Fake{
		Dirty:              map[string]bool{"/proj": true},
		Changes:            []string{" M internal/foo.go", "?? internal/bar.go"},
		CurrentBranchValue: git.MustParseBranch("main"),
		SnapshotSHA:        "abc123def4567890",
		SnapshotHead:       "0ld",
	}
	snap, err := snapshotParentTree(context.Background(), fake, "/proj", "coder")
	require.NoError(t, err)
	assert.Equal(t, parentSnapshot{branch: git.MustParseBranch("main"), head: "0ld", commit: "abc123def4567890"}, snap,
		"the snapshot carries the compare-and-swap the branch advances by")

	warned := buf.String()
	assert.Contains(t, warned, "main", "the notice names the branch")
	assert.Contains(t, warned, "abc123def456", "and the snapshot")
	assert.Contains(t, warned, "internal/foo.go")
	assert.Contains(t, warned, "internal/bar.go")
	assert.Contains(t, warned, "coder")
	assert.NotContains(t, warned, "dirty_tree_handler", "the snapshot is the only behaviour; the notice offers no alternatives")

	require.Len(t, fake.SnapshotMessages, 1)
	msg := fake.SnapshotMessages[0]
	assert.True(t, strings.HasPrefix(msg, "ctxloom: commit working tree before delegating to coder\n"), "the subject names the delegated agent")
	assert.Equal(t, []string{"snapshot /proj"}, fake.Calls, "a snapshot, and nothing that moves a ref or commits in place")
}

// TestSnapshotParentTree_ATreeEqualToHeadIsNoSnapshot: a status git calls
// dirty whose staged tree nonetheless equals HEAD's yields no snapshot — the
// child is carved from HEAD — and no notice.
func TestSnapshotParentTree_ATreeEqualToHeadIsNoSnapshot(t *testing.T) {
	resetStrictness(t)
	buf := captureWarnings(t)
	fake := &git.Fake{
		Dirty:              map[string]bool{"/proj": true},
		Changes:            []string{" M internal/foo.go"},
		CurrentBranchValue: git.MustParseBranch("main"),
	}
	snap, err := snapshotParentTree(context.Background(), fake, "/proj", "coder")
	require.NoError(t, err)
	assert.Zero(t, snap)
	assert.Empty(t, buf.String())
}

// TestSnapshotParentTree_ASnapshotErrorRefuses pins that a real git failure
// surfaces verbatim rather than being swallowed: the spawn is refused.
func TestSnapshotParentTree_ASnapshotErrorRefuses(t *testing.T) {
	resetStrictness(t)
	captureWarnings(t)
	fake := &git.Fake{
		Dirty:              map[string]bool{"/proj": true},
		Changes:            []string{" M internal/foo.go"},
		CurrentBranchValue: git.MustParseBranch("main"),
		SnapshotErr:        fmt.Errorf("index.lock exists"),
	}
	_, err := snapshotParentTree(context.Background(), fake, "/proj", "coder")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "index.lock exists")
}

// TestCellsPrepare_OriginatorIsNotCommittedFor: the commits around a
// worktree are a DELEGATED spawn's concern. The originator's own
// `--workspace worktree` run proceeds on a dirty tree with no git probe and
// no commit, at either end, because the human who asked for the worktree is
// at the terminal with the tree in front of them. This is what keeps
// `ctxloom run --workspace worktree` launchable from a checkout with
// uncommitted work.
func TestCellsPrepare_OriginatorIsNotCommittedFor(t *testing.T) {
	resetStrictness(t)
	fake := commitFixture()
	var commitsOnTeardown bool
	prev := prepareEnvironment
	prepareEnvironment = func(ctx context.Context, _ launch.CellRequest, s isolation.Spec) (isolation.Environment, error) {
		commitsOnTeardown = s.CommitsOnTeardown()
		if hook := s.BeforeCheckoutHook(); hook != nil {
			if _, err := hook(ctx, childBranch); err != nil {
				return nil, err
			}
		}
		return stubEnvAt("/proj", nil), nil
	}
	t.Cleanup(func() { prepareEnvironment = prev })
	cfg := config.NewFixture(config.Fixture{Workspace: "worktree"})
	p, err := Cells{engines: engines.Registry(), cfg: cfg, Git: fake}.Prepare(context.Background(), launch.CellRequest{
		SessionDir:  t.TempDir(),
		Axes:        launch.Axes{Workspace: launch.WorkspaceWorktree, Runtime: launch.RuntimeHost},
		Engine:      mock.New(),
		Identity:    sessions.Identity{Harp: "originator"},
		ProjectRoot: "/proj",
	})
	require.NoError(t, err, "the originator's worktree run is not committed for")
	defer func() { _ = p.Cleanup() }()
	assert.Empty(t, fake.Calls, "no git probe, no commit")
	assert.False(t, commitsOnTeardown, "nor is its checkout committed at teardown")
}

// TestCellsPrepare_DelegatedChildCommitsAtBothEnds: a delegated child's new
// worktree is carved from a snapshot of the parent, the parent's branch
// advances to it only once the child's environment has started, and
// isolation is asked to commit the child's checkout on its own branch at
// teardown.
func TestCellsPrepare_DelegatedChildCommitsAtBothEnds(t *testing.T) {
	resetStrictness(t)
	captureWarnings(t)
	var commitsOnTeardown bool
	carved := stubCarvingEnvironment(t, "/proj", func(s isolation.Spec) { commitsOnTeardown = s.CommitsOnTeardown() })
	fake := commitFixture()
	fake.ExistingBranches = []git.Branch{childBranch}
	p, err := prepareDelegatedWorktree(t, fake)
	require.NoError(t, err)
	assert.Equal(t, "abc123", *carved, "the child's branch is carved from the snapshot")
	assert.Empty(t, fake.Advances, "the parent's branch does not move before the child starts")
	assert.True(t, commitsOnTeardown, "the child's checkout commits its own work at teardown")

	env, ok := EnvironmentOf(p)
	require.True(t, ok)
	_, err = env.Start(context.Background(), isolation.RunnerRequest{})
	require.NoError(t, err)
	assert.Equal(t, []string{"main fake-head-sha->abc123"}, fake.Advances, "once started, the parent's branch advances from the HEAD the snapshot sits on")
	_, _ = env.Start(context.Background(), isolation.RunnerRequest{})
	assert.Len(t, fake.Advances, 1, "once")

	require.NoError(t, p.Cleanup())
	assert.Empty(t, fake.DeletedAt, "a started child's branch is its own; the teardown keeps it")
}

// TestCellsPrepare_AnAbandonedLaunchDeletesTheCarvedBranch: a cell torn down
// before its environment started never touches the parent's branch, and
// removes the branch it carved — by a compare-and-swap from the commit it
// was carved at.
func TestCellsPrepare_AnAbandonedLaunchDeletesTheCarvedBranch(t *testing.T) {
	resetStrictness(t)
	captureWarnings(t)
	stubCarvingEnvironment(t, "/proj", nil)
	fake := commitFixture()
	fake.ExistingBranches = []git.Branch{childBranch}
	p, err := prepareDelegatedWorktree(t, fake)
	require.NoError(t, err)

	require.NoError(t, p.Cleanup())
	assert.Empty(t, fake.Advances, "the parent's branch never moved")
	assert.Equal(t, []string{childBranch.Short() + "@abc123"}, fake.DeletedAt, "the carved branch is deleted at its carve point")
	require.NoError(t, p.Cleanup())
	assert.Len(t, fake.DeletedAt, 1, "once")
}

// TestCellsPrepare_ACleanParentIsCarvedFromItsHead: with nothing to
// snapshot the child is carved from the parent's HEAD, nothing advances at
// the start, and an abandoned launch deletes the branch at that HEAD.
func TestCellsPrepare_ACleanParentIsCarvedFromItsHead(t *testing.T) {
	resetStrictness(t)
	captureWarnings(t)
	carved := stubCarvingEnvironment(t, "/proj", nil)
	fake := &git.Fake{HeadSHAValue: "h3ad", ExistingBranches: []git.Branch{childBranch}}
	p, err := prepareDelegatedWorktree(t, fake)
	require.NoError(t, err)
	assert.Equal(t, "h3ad", *carved)

	require.NoError(t, p.Cleanup())
	assert.Empty(t, fake.SnapshotMessages)
	assert.Empty(t, fake.Advances)
	assert.Equal(t, []string{childBranch.Short() + "@h3ad"}, fake.DeletedAt)
}

// TestParentCommit_AFailedAdvanceIsSaidAndNotRetried: a parent branch that
// moved between the carve and the start is left alone, and the launch says
// so naming the snapshot; the child's start is not undone for it.
func TestParentCommit_AFailedAdvanceIsSaidAndNotRetried(t *testing.T) {
	resetStrictness(t)
	warnings := captureWarnings(t)
	stubCarvingEnvironment(t, "/proj", nil)
	fake := commitFixture()
	fake.ExistingBranches = []git.Branch{childBranch}
	fake.AdvanceErr = fmt.Errorf("%w: main is at h, not fake-head-sha", git.ErrRefMoved)
	p, err := prepareDelegatedWorktree(t, fake)
	require.NoError(t, err)
	env, _ := EnvironmentOf(p)
	_, err = env.Start(context.Background(), isolation.RunnerRequest{})
	require.NoError(t, err, "the child is up; a moved parent does not refuse it")

	assert.Contains(t, warnings.String(), "abc123")
	assert.Contains(t, warnings.String(), "moved")
	require.NoError(t, p.Cleanup())
	assert.Empty(t, fake.DeletedAt, "the started child keeps the branch holding the snapshot")
}

// TestParentCommit_ACleanAdvanceSaysNothing: an advance that succeeds is
// the ordinary outcome, and the launch says nothing about it.
func TestParentCommit_ACleanAdvanceSaysNothing(t *testing.T) {
	resetStrictness(t)
	warnings := captureWarnings(t)
	stubCarvingEnvironment(t, "/proj", nil)
	fake := commitFixture()
	p, err := prepareDelegatedWorktree(t, fake)
	require.NoError(t, err)
	warnings.Reset() // the snapshot's own notice
	env, _ := EnvironmentOf(p)
	_, err = env.Start(context.Background(), isolation.RunnerRequest{})
	require.NoError(t, err)
	assert.Len(t, fake.Advances, 1)
	assert.Empty(t, warnings.String(), "a clean advance is silent")
}

// TestParentCommit_AnAdvanceThatFailsOtherwiseIsSaidAsAFailure: a failure
// other than a moved branch is said as that failure — not as a branch the
// human moved.
func TestParentCommit_AnAdvanceThatFailsOtherwiseIsSaidAsAFailure(t *testing.T) {
	resetStrictness(t)
	warnings := captureWarnings(t)
	stubCarvingEnvironment(t, "/proj", nil)
	fake := commitFixture()
	fake.AdvanceErr = fmt.Errorf("update-ref: disk full")
	p, err := prepareDelegatedWorktree(t, fake)
	require.NoError(t, err)
	env, _ := EnvironmentOf(p)
	_, err = env.Start(context.Background(), isolation.RunnerRequest{})
	require.NoError(t, err, "the child is up; a failed advance does not refuse it")
	assert.Contains(t, warnings.String(), "advancing branch \"main\" to it failed: update-ref: disk full")
	assert.NotContains(t, warnings.String(), "moved")
}

// deadlineGit is a Fake that records how long AdvanceBranch's context had
// left when it was called.
type deadlineGit struct {
	*git.Fake
	left time.Duration
	err  error
}

func (d *deadlineGit) AdvanceBranch(ctx context.Context, dir string, branch git.Branch, to, from string) error {
	if dl, ok := ctx.Deadline(); ok {
		d.left = time.Until(dl)
	}
	d.err = ctx.Err()
	return d.Fake.AdvanceBranch(ctx, dir, branch, to, from)
}

// TestParentCommit_TheAdvanceOutlivesTheLaunchContext: the advance runs on
// a context of its own, bounded by upTimeout — live when the launch's has
// already ended, and with the whole bound to spend.
func TestParentCommit_TheAdvanceOutlivesTheLaunchContext(t *testing.T) {
	resetStrictness(t)
	captureWarnings(t)
	stubCarvingEnvironment(t, "/proj", nil)
	g := &deadlineGit{Fake: commitFixture()}
	cfg := config.NewFixture(config.Fixture{Workspace: "worktree"})
	p, err := Cells{engines: engines.Registry(), cfg: cfg, Git: g}.Prepare(context.Background(), launch.CellRequest{
		SessionDir:  t.TempDir(),
		Axes:        launch.Axes{Workspace: launch.WorkspaceWorktree, Runtime: launch.RuntimeHost},
		Engine:      mock.New(),
		Identity:    delegatedChild("coder"),
		ProjectRoot: "/proj",
	})
	require.NoError(t, err)
	env, _ := EnvironmentOf(p)
	ended, cancel := context.WithCancel(context.Background())
	cancel()
	_, _ = env.Start(ended, isolation.RunnerRequest{})

	require.Len(t, g.Advances, 1)
	require.NoError(t, g.err, "the launch's ended context does not end the advance")
	assert.Greater(t, g.left, upTimeout/2, "bounded by upTimeout, not already spent")
	assert.LessOrEqual(t, g.left, upTimeout)
}

// TestParentCommit_AnAbandonedLaunchLeavesAGoneBranchAlone: a carved branch
// that no longer exists (or whose existence cannot be told) is not deleted.
func TestParentCommit_AnAbandonedLaunchLeavesAGoneBranchAlone(t *testing.T) {
	for name, fake := range map[string]*git.Fake{
		"gone":    commitFixture(),
		"unknown": func() *git.Fake { f := commitFixture(); f.BranchExistsErr = fmt.Errorf("boom"); return f }(),
	} {
		t.Run(name, func(t *testing.T) {
			resetStrictness(t)
			captureWarnings(t)
			stubCarvingEnvironment(t, "/proj", nil)
			p, err := prepareDelegatedWorktree(t, fake)
			require.NoError(t, err)
			require.NoError(t, p.Cleanup())
			assert.Empty(t, fake.DeletedAt)
		})
	}
}

// TestParentCommit_AnAbandonedBranchThatWillNotGoIsSaid: a carved branch
// whose delete fails is said; one that goes is not.
func TestParentCommit_AnAbandonedBranchThatWillNotGoIsSaid(t *testing.T) {
	for _, failing := range []bool{false, true} {
		resetStrictness(t)
		warnings := captureWarnings(t)
		stubCarvingEnvironment(t, "/proj", nil)
		fake := commitFixture()
		fake.ExistingBranches = []git.Branch{childBranch}
		if failing {
			fake.DeleteAtErr = fmt.Errorf("checked out elsewhere")
		}
		p, err := prepareDelegatedWorktree(t, fake)
		require.NoError(t, err)
		warnings.Reset() // the snapshot's own notice
		require.NoError(t, p.Cleanup())
		require.Len(t, fake.DeletedAt, 1)
		if failing {
			assert.Contains(t, warnings.String(), "could not be removed: checked out elsewhere")
		} else {
			assert.Empty(t, warnings.String())
		}
	}
}

// TestCellsPrepare_AnInteractiveStartIsTheChildUp: an interactive start is
// the child up as much as a structured one.
func TestCellsPrepare_AnInteractiveStartIsTheChildUp(t *testing.T) {
	resetStrictness(t)
	captureWarnings(t)
	stubCarvingEnvironment(t, "/proj", nil)
	fake := commitFixture()
	p, err := prepareDelegatedWorktree(t, fake)
	require.NoError(t, err)
	env, _ := EnvironmentOf(p)
	_, err = env.Interactive(context.Background(), isolation.RunnerRequest{})
	require.NoError(t, err)
	assert.Equal(t, []string{"main fake-head-sha->abc123"}, fake.Advances)
}

// childBranch is the branch the carving stand-in carves.
var childBranch = git.MustParseBranch("ctxloom/coder")

// stubCarvingEnvironment stands in for isolation carving a NEW worktree
// checkout at dir: it runs the Spec's BeforeCheckout hook first, as
// isolation.Prepare does just before `git worktree add`, and refuses the run
// with the hook's error. It returns where the hook said to carve from; see,
// when set, is shown the Spec.
func stubCarvingEnvironment(t *testing.T, dir string, see func(isolation.Spec)) *string {
	t.Helper()
	var base string
	prev := prepareEnvironment
	prepareEnvironment = func(ctx context.Context, _ launch.CellRequest, s isolation.Spec) (isolation.Environment, error) {
		if see != nil {
			see(s)
		}
		if hook := s.BeforeCheckoutHook(); hook != nil {
			b, err := hook(ctx, childBranch)
			if err != nil {
				return nil, err
			}
			base = b
		}
		return stubEnvAt(dir, nil), nil
	}
	t.Cleanup(func() { prepareEnvironment = prev })
	return &base
}

// commitFixture is a dirty parent tree the parent snapshot can snapshot.
func commitFixture() *git.Fake {
	return &git.Fake{
		Dirty:              map[string]bool{"/proj": true},
		Changes:            []string{" M internal/foo.go"},
		CurrentBranchValue: git.MustParseBranch("main"),
		SnapshotSHA:        "abc123",
	}
}

// prepareDelegatedWorktree prepares a delegated child's worktree cell over
// fake.
func prepareDelegatedWorktree(t *testing.T, fake *git.Fake) (launch.Cell, error) {
	t.Helper()
	cfg := config.NewFixture(config.Fixture{Workspace: "worktree"})
	return Cells{engines: engines.Registry(), cfg: cfg, Git: fake}.Prepare(context.Background(), launch.CellRequest{
		SessionDir:  t.TempDir(),
		Axes:        launch.Axes{Workspace: launch.WorkspaceWorktree, Runtime: launch.RuntimeHost},
		Engine:      mock.New(),
		Identity:    delegatedChild("coder"),
		ProjectRoot: "/proj",
	})
}

// TestCellsPrepare_ReusedCheckoutCommitsNoParent: a resume that RE-ENTERS
// its own checkout (or its own branch) carves nothing from the parent's
// HEAD, so isolation never calls the hook and the parent is not committed:
// the commit would land on the parent's branch for nothing.
func TestCellsPrepare_ReusedCheckoutCommitsNoParent(t *testing.T) {
	resetStrictness(t)
	captureWarnings(t)
	stubPrepareEnvironment(t, map[string]bool{}) // re-enters: never calls the hook
	fake := commitFixture()
	p, err := prepareDelegatedWorktree(t, fake)
	require.NoError(t, err)
	defer func() { _ = p.Cleanup() }()
	assert.Empty(t, fake.SnapshotMessages, "nothing is snapshotted for a checkout that is not carved")
	assert.Empty(t, fake.Calls, "the parent is not even probed")
}

// TestCellsPrepare_RefusedLaunchCommitsNothing: a launch that an
// isolation finding has already refused (here, a container that could not
// start) does not snapshot the parent's tree on its way to the refusal —
// the commit verprop's hoary-quick-elm resume left behind for a run that
// never started.
func TestCellsPrepare_RefusedLaunchCommitsNothing(t *testing.T) {
	resetStrictness(t)
	captureWarnings(t)
	prev := prepareEnvironment
	prepareEnvironment = func(ctx context.Context, _ launch.CellRequest, s isolation.Spec) (isolation.Environment, error) {
		strictness.FailAlways(report.KindIsolation, "start the runtime", "container isolation was requested but could not start")
		if _, err := s.BeforeCheckoutHook()(ctx, childBranch); err != nil {
			return nil, err
		}
		return stubEnvAt("/proj", nil), nil
	}
	t.Cleanup(func() { prepareEnvironment = prev })

	fake := commitFixture()
	p, err := prepareDelegatedWorktree(t, fake)
	require.Error(t, err, "the launch is refused")
	assert.Nil(t, p.Cleanup)
	assert.Empty(t, fake.SnapshotMessages, "THE POINT: nothing is snapshotted for a run that will not start")
	assert.Empty(t, fake.Advances)
}

// TestCellsPrepare_AnUnadoptableCheckoutIsTheWorkspacesRefusal: a resume
// whose checkout path holds a worktree it cannot adopt is refused as the
// WORKSPACE's failure, not as "the requested runtime is not available" —
// verprop's lame-wide-chant relaunch read as a runtime failure four times
// over — and the parent is not committed on the way.
func TestCellsPrepare_AnUnadoptableCheckoutIsTheWorkspacesRefusal(t *testing.T) {
	resetStrictness(t)
	captureWarnings(t)
	prev := prepareEnvironment
	prepareEnvironment = func(context.Context, launch.CellRequest, isolation.Spec) (isolation.Environment, error) {
		return nil, fmt.Errorf("worktree isolation: occupied: %w", launch.ErrWorkspaceUnadoptable)
	}
	t.Cleanup(func() { prepareEnvironment = prev })

	fake := commitFixture()
	_, err := prepareDelegatedWorktree(t, fake)
	require.ErrorIs(t, err, launch.ErrWorkspaceUnadoptable)
	assert.NotErrorIs(t, err, launch.ErrRuntimeUnavailable, "THE POINT: the runtime is not what failed")
	assert.Empty(t, fake.SnapshotMessages, "nothing is snapshotted for a run that will not start")
}

// delegatedChild is the identity of a spawned child (depth > 0): the
// subject of the commits around a worktree.
func delegatedChild(harp string) sessions.Identity {
	return sessions.Identity{Harp: harp, Depth: 1}
}

// TestStartEngine_CellWithoutTransportRefusesInsteadOfPanicking pins the
// refusal: a launch whose cell was prepared elsewhere (a test double, a
// dry-run cell) carries no transport handle, and StartEngine with no
// starter supplied must refuse by name rather than reach a nil.
func TestStartEngine_CellWithoutTransportRefusesInsteadOfPanicking(t *testing.T) {
	resetStrictness(t)
	l := launch.Launch{
		Identity: sessions.Identity{Harp: "coder"},
		Engine:   "mock",
		Cell:     launch.Cell{Cleanup: func() error { return nil }},
	}
	proc, serr := StartEngine(context.Background(), l, nil, 0, nil)
	require.Error(t, serr, "StartEngine must refuse a launch it has no transport for, not panic")
	assert.Nil(t, proc)
	assert.Contains(t, serr.Error(), "starter", "the refusal names the seam that was not supplied")
}

// TestSnapshotParentTree_ListingFailureIsNamedInThePreview pins a
// defect: the parent snapshot prints a notice naming the files it
// commits for the user's branch, and boundDirtyChanges turned a
// WorkingChanges FAILURE into an empty list — so the preview immediately
// before an auto-commit of the user's tree said, with no qualification, that
// there was nothing to name. "I could not find out" must not render as the
// empty set in the one message whose job is to show what is about to be
// committed.
func TestSnapshotParentTree_ListingFailureIsNamedInThePreview(t *testing.T) {
	resetStrictness(t)
	warnings := captureWarnings(t)
	fake := &git.Fake{
		Dirty:       map[string]bool{"/proj": true},
		ChangesErr:  assert.AnError,
		SnapshotSHA: "abc123",
	}
	_, err := snapshotParentTree(context.Background(), fake, "/proj", "coder")
	require.NoError(t, err, "a listing failure stays best-effort: it must not block the configured commit")
	assert.Contains(t, warnings.String(), "could not list",
		"the preview must SAY the file listing failed rather than showing an empty list")
}

// TestSnapshotParentTree_PreviewListsTenFilesThenCounts pins the preview's
// bound: the warning shown before the user's tree is auto-committed names the
// first ten changed files and counts the rest, so a large commit cannot flood
// the terminal yet still says how much more it carries.
func TestSnapshotParentTree_PreviewListsTenFilesThenCounts(t *testing.T) {
	resetStrictness(t)
	warnings := captureWarnings(t)
	var changes []string
	for i := range 12 {
		changes = append(changes, fmt.Sprintf("file-%02d.go", i))
	}
	fake := &git.Fake{
		Dirty:       map[string]bool{"/proj": true},
		Changes:     changes,
		SnapshotSHA: "abc123",
	}
	_, err := snapshotParentTree(context.Background(), fake, "/proj", "coder")
	require.NoError(t, err)
	out := warnings.String()
	assert.Contains(t, out, "file-09.go", "the tenth file is listed")
	assert.NotContains(t, out, "file-10.go", "the eleventh is counted, not listed")
	assert.Contains(t, out, "(+2 more)")
}
