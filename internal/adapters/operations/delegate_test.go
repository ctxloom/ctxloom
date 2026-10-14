package operations

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

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

// ===== Dirty-parent-tree spawn: the parent commit =====
//
// Worktree isolation carves a branch with `git worktree add -b` — a checkout
// of COMMITTED state only. A delegated child spawned into a NEW worktree
// while the parent's own project tree carries uncommitted changes gets them
// by the parent commit (commitParentTree). These pin that commit, when it
// does and does not happen, and that it never triggers when the resolved
// axis isn't worktree at all.

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

// TestCommitParentTree_IsDirtyErrorIsInspected pins the second of the two
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
func TestCommitParentTree_IsDirtyErrorIsInspected(t *testing.T) {
	resetStrictness(t)
	fake := &git.Fake{
		DirtyErr: assert.AnError,
		// Configured dirty AND with changes: were the error ignored, the
		// commit below would be attempted instead of no-opping.
		Dirty:   map[string]bool{"/proj": true},
		Changes: []string{" M internal/foo.go"},
	}
	err := commitParentTree(context.Background(), fake, "/proj", "coder")
	require.NoError(t, err, "an unreadable dirty state degrades to a no-op gate; it must never block the spawn")
	assert.Empty(t, fake.CommitMessages, "and must not commit on state it could not read")
}

// ----- workspace: none / clean tree escape hatches -----

// TestCellsPrepare_DirtyParentTree_ExplicitNoneStillAllowed: a dirty parent
// tree is never committed for a spawn that explicitly opts OUT of worktree
// isolation, because a shared-checkout child sees the live tree exactly
// as-is — dirtiness is irrelevant to it, and the parent commit never runs.
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
// control: a clean parent tree is never committed, even when the
// resolved axis IS worktree.
func TestCellsPrepare_CleanParentTree_WorktreeAllowed(t *testing.T) {
	resetStrictness(t)
	fake := &git.Fake{Dirty: map[string]bool{"/proj": false}}
	stubCarvingEnvironment(t, "/proj")

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
}

// ----- commit -----

// TestCommitParentTree_Commit_DetachedHeadRefuses pins the grandchild-
// coherence guard: committing inside a detached-HEAD checkout (exactly what
// a delegated child's OWN worktree looks like) would land on no branch and
// could be silently discarded when that worktree is torn down.
func TestCommitParentTree_Commit_DetachedHeadRefuses(t *testing.T) {
	resetStrictness(t)
	fake := &git.Fake{
		Dirty:                 map[string]bool{"/child-wt": true},
		Changes:               []string{" M f.go"},
		CurrentBranchDetached: true, // git's own "HEAD" sentinel, surfaced as the zero Branch
	}
	err := commitParentTree(context.Background(), fake, "/child-wt", "grandchild")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "detached-HEAD")
	assert.Empty(t, fake.CommitMessages, "never even attempts the commit")
}

// TestCommitParentTree_Commit_CurrentBranchErrorRefuses pins the fix:
// before it, `branch, _ := gitClient.CurrentBranch(...)` discarded the
// error, leaving branch=="" — which is NOT "HEAD", so the detached-HEAD guard
// above never fired and the commit proceeded on an unresolvable branch name.
// An unresolvable branch is exactly the condition the guard exists to catch
// (the caller cannot tell whether this is a bare checkout), so it must refuse
// rather than silently treat the error as "not detached".
func TestCommitParentTree_Commit_CurrentBranchErrorRefuses(t *testing.T) {
	resetStrictness(t)
	fake := &git.Fake{
		Dirty:            map[string]bool{"/child-wt": true},
		Changes:          []string{" M f.go"},
		CurrentBranchErr: fmt.Errorf("git rev-parse: unknown revision or path not in the working tree"),
	}
	err := commitParentTree(context.Background(), fake, "/child-wt", "grandchild")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "could not determine", "names the failure rather than silently guessing a branch")
	assert.Contains(t, err.Error(), "unknown revision", "carries the underlying git error")
	assert.Empty(t, fake.CommitMessages, "never even attempts the commit")
}

// TestCommitParentTree_Commit_WarnsAndCommits pins the commit path:
// it warns (naming the branch and the bounded file list) BEFORE mutating, then
// stages and commits everything (git add -A shape) with the documented
// message format, and verifies the commit actually captured content.
func TestCommitParentTree_Commit_WarnsAndCommits(t *testing.T) {
	resetStrictness(t)
	buf := captureWarnings(t)
	fake := &git.Fake{
		Dirty:              map[string]bool{"/proj": true},
		Changes:            []string{" M internal/foo.go", "?? internal/bar.go"},
		CurrentBranchValue: git.MustParseBranch("main"),
		CommitAllSHA:       "abc123",
		CommitAllChanged:   []string{"internal/foo.go", "internal/bar.go"},
	}
	err := commitParentTree(context.Background(), fake, "/proj", "coder")
	require.NoError(t, err)

	warned := buf.String()
	assert.Contains(t, warned, "main", "the warning names the branch")
	assert.Contains(t, warned, "internal/foo.go")
	assert.Contains(t, warned, "internal/bar.go")
	assert.Contains(t, warned, "coder")
	assert.NotContains(t, warned, "dirty_tree_handler", "commit is the only behaviour; the warning offers no alternatives")

	require.Len(t, fake.CommitMessages, 1)
	msg := fake.CommitMessages[0]
	assert.True(t, strings.HasPrefix(msg, "ctxloom: commit working tree before delegating to coder\n"), "the subject names the delegated agent")
	assert.NotContains(t, msg, "dirty_tree_handler")
	assert.Contains(t, fake.Calls, "commit-all /proj")
}

// TestCommitParentTree_Commit_EmptyCommitRefusesLoud pins the
// empty-commit safety net: CommitAll reporting success but an empty
// changed-files diff (this codebase's documented empty-commit pre-commit-
// hook history) must refuse rather than spawn the child against what may be
// nothing.
func TestCommitParentTree_Commit_EmptyCommitRefusesLoud(t *testing.T) {
	resetStrictness(t)
	captureWarnings(t)
	fake := &git.Fake{
		Dirty:              map[string]bool{"/proj": true},
		Changes:            []string{" M internal/foo.go"},
		CurrentBranchValue: git.MustParseBranch("main"),
		CommitAllSHA:       "deadbeef",
		CommitAllChanged:   nil, // the empty-commit case
	}
	err := commitParentTree(context.Background(), fake, "/proj", "coder")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "deadbeef")
	assert.Contains(t, err.Error(), "empty")
	assert.Contains(t, err.Error(), "main")
}

// TestCommitParentTree_Commit_CommitAllErrorPropagates pins that a real
// git failure (not the empty-commit case — an actual error) surfaces
// verbatim rather than being swallowed.
func TestCommitParentTree_Commit_CommitAllErrorPropagates(t *testing.T) {
	resetStrictness(t)
	captureWarnings(t)
	fake := &git.Fake{
		Dirty:              map[string]bool{"/proj": true},
		Changes:            []string{" M internal/foo.go"},
		CurrentBranchValue: git.MustParseBranch("main"),
		CommitAllErr:       fmt.Errorf("index.lock exists"),
	}
	err := commitParentTree(context.Background(), fake, "/proj", "coder")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "index.lock exists")
}

// TestCellsPrepare_Commit_ChildSeesCommittedContent is the full,
// REAL-git end-to-end proof that "commit" actually achieves its purpose: an
// uncommitted file on the parent's branch, once auto-committed, is visible
// to a FRESH worktree checked out from HEAD afterward — exactly what a
// delegated child's own worktree creation does next. Skips cleanly when git
// is unavailable.
func TestCellsPrepare_Commit_ChildSeesCommittedContent(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH; skipping real-git parent-commit integration test")
	}
	resetStrictness(t)
	captureWarnings(t)
	repo := initTestRepo(t)
	require.NoError(t, os.WriteFile(filepath.Join(repo, "wip.go"), []byte("package wip"), 0o644))

	real := git.NewExec()
	err := commitParentTree(context.Background(), real, repo, "coder")
	require.NoError(t, err)

	dirty, err := real.IsDirty(context.Background(), repo)
	require.NoError(t, err)
	assert.False(t, dirty, "the parent tree is clean after the auto-commit")

	// Exactly what worktree isolation does next: a fresh checkout from HEAD.
	childWT := filepath.Join(t.TempDir(), "child-wt")
	require.NoError(t, real.WorktreeAdd(context.Background(), repo, childWT, git.MustParseBranch("agent/child"), "HEAD", false))
	got, err := os.ReadFile(filepath.Join(childWT, "wip.go"))
	require.NoError(t, err, "the child's worktree sees the file — it is no longer parent-only WIP")
	assert.Equal(t, "package wip", string(got))
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
			if err := hook(ctx); err != nil {
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
// worktree commits the parent before it is carved, and asks isolation to
// commit the child's checkout on its own branch at teardown.
func TestCellsPrepare_DelegatedChildCommitsAtBothEnds(t *testing.T) {
	resetStrictness(t)
	captureWarnings(t)
	var commitsOnTeardown bool
	prev := prepareEnvironment
	prepareEnvironment = func(ctx context.Context, _ launch.CellRequest, s isolation.Spec) (isolation.Environment, error) {
		commitsOnTeardown = s.CommitsOnTeardown()
		if err := s.BeforeCheckoutHook()(ctx); err != nil {
			return nil, err
		}
		return stubEnvAt("/proj", nil), nil
	}
	t.Cleanup(func() { prepareEnvironment = prev })
	fake := commitFixture()
	p, err := prepareDelegatedWorktree(t, fake)
	require.NoError(t, err)
	defer func() { _ = p.Cleanup() }()
	require.Len(t, fake.CommitMessages, 1, "the parent is committed before the checkout is carved")
	assert.Contains(t, fake.Calls, "commit-all /proj")
	assert.True(t, commitsOnTeardown, "the child's checkout commits its own work at teardown")
}

// stubCarvingEnvironment stands in for isolation carving a NEW worktree
// checkout at dir: it runs the Spec's BeforeCheckout hook first, as
// isolation.Prepare does just before `git worktree add`, and refuses the run
// with the hook's error.
func stubCarvingEnvironment(t *testing.T, dir string) {
	t.Helper()
	prev := prepareEnvironment
	prepareEnvironment = func(ctx context.Context, _ launch.CellRequest, s isolation.Spec) (isolation.Environment, error) {
		if hook := s.BeforeCheckoutHook(); hook != nil {
			if err := hook(ctx); err != nil {
				return nil, err
			}
		}
		return stubEnvAt(dir, nil), nil
	}
	t.Cleanup(func() { prepareEnvironment = prev })
}

// commitFixture is a dirty parent tree the parent commit can commit.
func commitFixture() *git.Fake {
	return &git.Fake{
		Dirty:              map[string]bool{"/proj": true},
		Changes:            []string{" M internal/foo.go"},
		CurrentBranchValue: git.MustParseBranch("main"),
		CommitAllSHA:       "abc123",
		CommitAllChanged:   []string{"internal/foo.go"},
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
	assert.Empty(t, fake.CommitMessages, "nothing is committed for a checkout that is not carved")
	assert.Empty(t, fake.Calls, "the parent is not even probed")
}

// TestCellsPrepare_RefusedLaunchCommitsNothing: a launch that an
// isolation finding has already refused (here, a container that could not
// start) does not commit the parent's tree on its way to the refusal — the
// commit verprop's hoary-quick-elm resume left behind for a run that never
// started.
func TestCellsPrepare_RefusedLaunchCommitsNothing(t *testing.T) {
	resetStrictness(t)
	captureWarnings(t)
	prev := prepareEnvironment
	prepareEnvironment = func(ctx context.Context, _ launch.CellRequest, s isolation.Spec) (isolation.Environment, error) {
		strictness.FailAlways(report.KindIsolation, "start the runtime", "container isolation was requested but could not start")
		if err := s.BeforeCheckoutHook()(ctx); err != nil {
			return nil, err
		}
		return stubEnvAt("/proj", nil), nil
	}
	t.Cleanup(func() { prepareEnvironment = prev })

	fake := commitFixture()
	p, err := prepareDelegatedWorktree(t, fake)
	require.Error(t, err, "the launch is refused")
	assert.Nil(t, p.Cleanup)
	assert.Empty(t, fake.CommitMessages, "THE POINT: nothing is committed for a run that will not start")
	assert.NotContains(t, fake.Calls, "commit-all /proj")
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
	assert.Empty(t, fake.CommitMessages, "nothing is committed for a run that will not start")
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

// TestCommitParentTree_Commit_ListingFailureIsNamedInThePreview pins a
// defect: the parent commit prints a preview naming the files it is
// about to commit on the user's branch, and boundDirtyChanges turned a
// WorkingChanges FAILURE into an empty list — so the preview immediately
// before an auto-commit of the user's tree said, with no qualification, that
// there was nothing to name. "I could not find out" must not render as the
// empty set in the one message whose job is to show what is about to be
// committed.
func TestCommitParentTree_Commit_ListingFailureIsNamedInThePreview(t *testing.T) {
	resetStrictness(t)
	warnings := captureWarnings(t)
	fake := &git.Fake{
		Dirty:            map[string]bool{"/proj": true},
		ChangesErr:       assert.AnError,
		CommitAllChanged: []string{"internal/foo.go"},
	}
	err := commitParentTree(context.Background(), fake, "/proj", "coder")
	require.NoError(t, err, "a listing failure stays best-effort: it must not block the configured commit")
	assert.Contains(t, warnings.String(), "could not list",
		"the preview must SAY the file listing failed rather than showing an empty list")
}

// TestCommitParentTree_Commit_PreviewListsTenFilesThenCounts pins the preview's
// bound: the warning shown before the user's tree is auto-committed names the
// first ten changed files and counts the rest, so a large commit cannot flood
// the terminal yet still says how much more it carries.
func TestCommitParentTree_Commit_PreviewListsTenFilesThenCounts(t *testing.T) {
	resetStrictness(t)
	warnings := captureWarnings(t)
	var changes []string
	for i := range 12 {
		changes = append(changes, fmt.Sprintf("file-%02d.go", i))
	}
	fake := &git.Fake{
		Dirty:            map[string]bool{"/proj": true},
		Changes:          changes,
		CommitAllChanged: changes,
	}
	require.NoError(t, commitParentTree(context.Background(), fake, "/proj", "coder"))
	out := warnings.String()
	assert.Contains(t, out, "file-09.go", "the tenth file is listed")
	assert.NotContains(t, out, "file-10.go", "the eleventh is counted, not listed")
	assert.Contains(t, out, "(+2 more)")
}
