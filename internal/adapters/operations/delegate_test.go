package operations

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/spf13/afero"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/adapters/git"
	"github.com/ctxloom/ctxloom/internal/adapters/isolation"
	"github.com/ctxloom/ctxloom/internal/core/config"
	"github.com/ctxloom/ctxloom/internal/core/launch"
	"github.com/ctxloom/ctxloom/internal/core/sessions"
	"github.com/ctxloom/ctxloom/internal/engines/mock"
	pb "github.com/ctxloom/ctxloom/internal/lm/grpc"
	"github.com/ctxloom/ctxloom/internal/shared/clidiag"
	"github.com/ctxloom/ctxloom/internal/shared/strictness"
)

// TestCellsPrepare_ContainerDegradeGate pins the cells adapter's fail-loud
// gate: an explicitly-requested container that can't start (ClassIsolation
// finding during Prepare) refuses the cell in strict mode, typed — never a
// silent host degrade — and proceeds on the degraded workspace only under
// degraded mode.
func TestCellsPrepare_ContainerDegradeGate(t *testing.T) {
	req := func(t *testing.T) launch.CellRequest {
		return launch.CellRequest{
			Axes:        launch.Axes{Workspace: launch.WorkspaceNone, Runtime: launch.RuntimeRootless},
			Engine:      mock.New(),
			Identity:    sessions.Identity{Harp: "builder"},
			ProjectRoot: t.TempDir(),
		}
	}

	t.Run("strict: the cell is refused with the finding text", func(t *testing.T) {
		resetStrictness(t)
		stubPrepareIsolation(t, map[string]bool{"builder": true}, func() pb.Client { return &stubClient{} })
		_, err := Cells{cfg: config.NewFixture(config.Fixture{})}.Prepare(context.Background(), req(t))
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
		stubPrepareIsolation(t, map[string]bool{"builder": true}, func() pb.Client { return &stubClient{} })
		cell, err := Cells{cfg: config.NewFixture(config.Fixture{}), mode: strictness.Mode{Degraded: true}}.Prepare(context.Background(), req(t))
		require.NoError(t, err)
		_ = cell.Cleanup()
	})
}

// ===== Dirty-parent-tree spawn: handler dispatch =====
//
// Worktree isolation runs `git worktree add --detach <ref>` — a checkout of
// COMMITTED state only. A delegated child spawned into a worktree while the
// parent's own project tree carries uncommitted changes needs an explicit
// decision: commit, copy, proceed stale, or refuse (dirty_tree_handler).
// These pin each handler, the config/per-call precedence, that --degraded
// softens NONE of them, and (fail's original behavior) never triggering
// when the resolved axis isn't worktree at all.

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

// TestHandleDirtyParentTree_IsDirtyErrorIsInspected pins the second of the two
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
func TestHandleDirtyParentTree_IsDirtyErrorIsInspected(t *testing.T) {
	resetStrictness(t)
	fake := &git.Fake{
		DirtyErr: assert.AnError,
		// Configured dirty AND with changes: were the error ignored, the
		// "fail" handler below would refuse the spawn instead of no-opping.
		Dirty:   map[string]bool{"/proj": true},
		Changes: []string{" M internal/foo.go"},
	}
	cfg := config.NewFixture(config.Fixture{})
	outcome, err := handleDirtyParentTree(context.Background(), cfg, fake, "/proj", "coder", launch.DirtyTreeHandlerFail)
	require.NoError(t, err, "an unreadable dirty state degrades to a no-op gate; it must never block the spawn")
	assert.Equal(t, dirtyTreeOutcome{}, outcome, "and must not carry a snapshot built on state it could not read")
}

// ----- fail -----

// TestHandleDirtyParentTree_Fail_RefusesAndNamesEverything is the crux case:
// dirty_tree_handler: "fail" (this gate's original, sole behavior before the
// other three existed) refuses the spawn, naming the agent, the dirty tree,
// the uncommitted paths, and both ways forward.
func TestHandleDirtyParentTree_Fail_RefusesAndNamesEverything(t *testing.T) {
	resetStrictness(t)
	fake := &git.Fake{
		Dirty:   map[string]bool{"/proj": true},
		Changes: []string{" M internal/foo.go", "?? internal/bar.go"},
	}
	cfg := config.NewFixture(config.Fixture{})
	_, err := handleDirtyParentTree(context.Background(), cfg, fake, "/proj", "coder", launch.DirtyTreeHandlerFail)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "coder", "names the agent")
	assert.Contains(t, err.Error(), "/proj", "names the dirty tree")
	assert.Contains(t, err.Error(), "internal/foo.go", "lists the uncommitted path")
	assert.Contains(t, err.Error(), "internal/bar.go", "lists the uncommitted path")
	assert.Contains(t, err.Error(), "committed state only", "states WHY the child can't see it")
	assert.Contains(t, err.Error(), "commit", "states the first way forward")
	assert.Contains(t, err.Error(), `workspace: "none"`, "states the escape-hatch way forward")
}

// TestHandleDirtyParentTree_Fail_UntrackedOnlyStillRefuses pins the
// untracked-files rule: a NEW file git itself does not consider
// ignored/excluded ("?? " in porcelain) counts as dirty on its own.
func TestHandleDirtyParentTree_Fail_UntrackedOnlyStillRefuses(t *testing.T) {
	resetStrictness(t)
	fake := &git.Fake{
		Dirty:   map[string]bool{"/proj": true},
		Changes: []string{"?? internal/newthing.go"},
	}
	cfg := config.NewFixture(config.Fixture{})
	_, err := handleDirtyParentTree(context.Background(), cfg, fake, "/proj", "coder", launch.DirtyTreeHandlerFail)
	require.Error(t, err, "an untracked-but-not-ignored file alone must still refuse the spawn")
	assert.Contains(t, err.Error(), "internal/newthing.go")
}

// TestHandleDirtyParentTree_Fail_BoundsFileList pins the listing bound: an
// agent worktree routinely carries dozens of modified delivered-surface
// files, and the refusal must print at most maxDirtyFilesListed of them plus
// a "+N more" tail rather than a wall of text.
func TestHandleDirtyParentTree_Fail_BoundsFileList(t *testing.T) {
	resetStrictness(t)
	var changes []string
	for i := 0; i < 15; i++ {
		changes = append(changes, fmt.Sprintf(" M internal/file%02d.go", i))
	}
	fake := &git.Fake{Dirty: map[string]bool{"/proj": true}, Changes: changes}
	cfg := config.NewFixture(config.Fixture{})
	_, err := handleDirtyParentTree(context.Background(), cfg, fake, "/proj", "coder", launch.DirtyTreeHandlerFail)
	require.Error(t, err)
	for i := 0; i < maxDirtyFilesListed; i++ {
		assert.Contains(t, err.Error(), fmt.Sprintf("file%02d.go", i))
	}
	assert.NotContains(t, err.Error(), "file14.go", "the tail collapses past the bound")
	assert.Contains(t, err.Error(), "+5 more")
}

// TestHandleDirtyParentTree_Fail_UnaffectedByMissingAck proves fail needs no
// dirty_tree_commit_ack at all — that flag gates ONLY the commit handler.
func TestHandleDirtyParentTree_Fail_UnaffectedByMissingAck(t *testing.T) {
	resetStrictness(t)
	fake := &git.Fake{Dirty: map[string]bool{"/proj": true}, Changes: []string{" M f.go"}}
	cfg := config.NewFixture(config.Fixture{})
	_, err := handleDirtyParentTree(context.Background(), cfg, fake, "/proj", "coder", launch.DirtyTreeHandlerFail)
	require.Error(t, err)
	assert.NotContains(t, err.Error(), "dirty_tree_commit_ack", "fail's refusal has nothing to do with the commit ack")
}

// TestCellsPrepare_DirtyParentTree_DegradedDoesNotSoftenFail is the
// direct proof that --degraded no longer softens this gate at all: before
// this change, --degraded downgraded the (then-only) refusal to a warning.
// Now the handler governs, and --degraded changes nothing about it.
func TestCellsPrepare_DirtyParentTree_DegradedDoesNotSoftenFail(t *testing.T) {
	resetStrictness(t)
	fake := &git.Fake{Dirty: map[string]bool{"/proj": true}, Changes: []string{" M internal/foo.go"}}
	cfg := config.NewFixture(config.Fixture{Workspace: "worktree"})
	p, err := Cells{cfg: cfg, Git: fake, mode: strictness.Mode{Degraded: true}}.Prepare(context.Background(), launch.CellRequest{
		Axes:        launch.Axes{Workspace: launch.WorkspaceAxis("worktree"), Runtime: launch.RuntimeAxis("host")},
		Engine:      mock.New(),
		Identity:    delegatedChild("coder"),
		ProjectRoot: "/proj",
		DirtyTree:   launch.DirtyTreeHandlerFail,
	})
	require.Error(t, err, "--degraded must NOT soften the fail handler's refusal")
	assert.Nil(t, p.Cleanup)
}

// ----- workspace: none / clean tree escape hatches (unchanged shape) -----

// TestCellsPrepare_DirtyParentTree_ExplicitNoneStillAllowed is the
// escape hatch every handler's message names: a dirty parent tree never
// blocks a spawn that explicitly opts OUT of worktree isolation, because a
// shared-checkout child sees the live tree exactly as-is — dirtiness is
// irrelevant to it, and the dirty-tree handler never even runs.
func TestCellsPrepare_DirtyParentTree_ExplicitNoneStillAllowed(t *testing.T) {
	resetStrictness(t)
	fake := &git.Fake{Dirty: map[string]bool{"/proj": true}}
	cfg := config.NewFixture(config.Fixture{})
	p, err := Cells{cfg: cfg, Git: fake}.Prepare(context.Background(), launch.CellRequest{
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
// control: a clean parent tree never trips any handler, even when the
// resolved axis IS worktree.
func TestCellsPrepare_CleanParentTree_WorktreeAllowed(t *testing.T) {
	resetStrictness(t)
	fake := &git.Fake{Dirty: map[string]bool{"/proj": false}}
	prev := prepareIsolation
	prepareIsolation = func(_ context.Context, axes isolation.Axes, _ string, _ isolation.ImageConfig, projectDir, _ string, _ isolation.SessionState) (isolation.Policy, isolation.Workspace) {
		return stubPolicy{mk: func() pb.Client { return &stubClient{} }}, stubWorkspace{dir: projectDir}
	}
	t.Cleanup(func() { prepareIsolation = prev })

	cfg := config.NewFixture(config.Fixture{Workspace: "worktree"})
	p, err := Cells{cfg: cfg, Git: fake}.Prepare(context.Background(), launch.CellRequest{
		Axes:        launch.Axes{Workspace: launch.WorkspaceAxis("worktree"), Runtime: launch.RuntimeAxis("host")},
		Engine:      mock.New(),
		Identity:    delegatedChild("coder"),
		ProjectRoot: "/proj",
		DirtyTree:   launch.DirtyTreeHandlerCommit,
	})
	require.NoError(t, err)
	defer func() { _ = p.Cleanup() }()
	assert.Empty(t, strictness.All())
}

// ----- stale -----

// TestHandleDirtyParentTree_Stale_ProceedsAndWarns pins the "stale" handler:
// it proceeds (nil error — no refusal, no mutation) and warns, naming the
// listed changes and both alternatives.
func TestHandleDirtyParentTree_Stale_ProceedsAndWarns(t *testing.T) {
	resetStrictness(t)
	buf := captureWarnings(t)
	fake := &git.Fake{Dirty: map[string]bool{"/proj": true}, Changes: []string{" M internal/foo.go"}}
	cfg := config.NewFixture(config.Fixture{})
	outcome, err := handleDirtyParentTree(context.Background(), cfg, fake, "/proj", "coder", launch.DirtyTreeHandlerStale)
	require.NoError(t, err)
	assert.Nil(t, outcome.copy)
	warned := buf.String()
	assert.Contains(t, warned, "coder")
	assert.Contains(t, warned, "internal/foo.go")
	assert.Contains(t, warned, `dirty_tree_handler: "stale"`)
	assert.Contains(t, warned, "will NOT see these changes")
	assert.Contains(t, warned, `"commit" or "copy"`)
	assert.Contains(t, warned, `workspace: "none"`)
	assert.Empty(t, fake.Calls, "stale never mutates or applies anything")
}

// TestHandleDirtyParentTree_Stale_UnaffectedByMissingAck proves stale needs
// no dirty_tree_commit_ack — that flag gates ONLY the commit handler.
func TestHandleDirtyParentTree_Stale_UnaffectedByMissingAck(t *testing.T) {
	resetStrictness(t)
	captureWarnings(t)
	fake := &git.Fake{Dirty: map[string]bool{"/proj": true}, Changes: []string{" M f.go"}}
	cfg := config.NewFixture(config.Fixture{})
	_, err := handleDirtyParentTree(context.Background(), cfg, fake, "/proj", "coder", launch.DirtyTreeHandlerStale)
	require.NoError(t, err)
}

// ----- copy -----

// TestHandleDirtyParentTree_Copy_CapturesPatchAndUntrackedList pins the
// capture half: "copy" reads the tracked patch and the untracked file list
// from the PARENT once, at decision time, deferring application until the
// worktree exists — it never mutates anything itself.
func TestHandleDirtyParentTree_Copy_CapturesPatchAndUntrackedList(t *testing.T) {
	resetStrictness(t)
	fake := &git.Fake{
		Dirty:          map[string]bool{"/proj": true},
		Changes:        []string{" M tracked.go", "?? untracked.go"},
		DiffPatchValue: "--- a/tracked.go\n+++ b/tracked.go\n@@ -1 +1 @@\n-old\n+new\n",
		UntrackedList:  []string{"untracked.go", "nested/other.go"},
	}
	cfg := config.NewFixture(config.Fixture{}) // copy needs no ack
	outcome, err := handleDirtyParentTree(context.Background(), cfg, fake, "/proj", "coder", launch.DirtyTreeHandlerCopy)
	require.NoError(t, err)
	require.NotNil(t, outcome.copy)
	assert.Equal(t, fake.DiffPatchValue, outcome.copy.patch)
	assert.Equal(t, []string{"untracked.go", "nested/other.go"}, outcome.copy.untracked)
	assert.Equal(t, "/proj", outcome.copy.sourceDir)
	assert.Empty(t, fake.AppliedPatches, "capture never applies — that's applyCopySnapshot's job, run later against the worktree")
}

// TestCellsPrepare_Copy_AppliesPatchAndCopiesUntrackedIntoWorktree is the
// end-to-end proof: BOTH tracked (via ApplyPatch, asserted on the exact
// patch text and target dir) AND untracked (via a REAL byte-for-byte
// filesystem copy, asserted on actual file content — this half never
// touches git.Fake at all) land in the worktree.
func TestCellsPrepare_Copy_AppliesPatchAndCopiesUntrackedIntoWorktree(t *testing.T) {
	resetStrictness(t)
	parent := t.TempDir()
	target := t.TempDir() // a REAL, separate directory standing in for the created worktree

	require.NoError(t, os.MkdirAll(filepath.Join(parent, "nested"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(parent, "untracked.go"), []byte("package untracked"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(parent, "nested", "other.go"), []byte("package nested"), 0o644))

	fake := &git.Fake{
		Dirty:          map[string]bool{parent: true},
		Changes:        []string{" M tracked.go", "?? untracked.go", "?? nested/other.go"},
		DiffPatchValue: "FAKE-PATCH-CONTENT",
		UntrackedList:  []string{"untracked.go", "nested/other.go"},
	}
	prev := prepareIsolation
	prepareIsolation = func(_ context.Context, axes isolation.Axes, _ string, _ isolation.ImageConfig, projectDir, _ string, _ isolation.SessionState) (isolation.Policy, isolation.Workspace) {
		return stubPolicy{mk: func() pb.Client { return &stubClient{} }}, stubWorkspace{dir: target}
	}
	t.Cleanup(func() { prepareIsolation = prev })

	cfg := config.NewFixture(config.Fixture{})
	p, err := Cells{cfg: cfg, Git: fake}.Prepare(context.Background(), launch.CellRequest{
		Axes:        launch.Axes{Workspace: launch.WorkspaceAxis("worktree"), Runtime: launch.RuntimeAxis("host")},
		Engine:      mock.New(),
		Identity:    delegatedChild("coder"),
		ProjectRoot: parent,
		DirtyTree:   launch.DirtyTreeHandlerCopy,
	})
	require.NoError(t, err)
	defer func() { _ = p.Cleanup() }()

	require.Len(t, fake.AppliedPatches, 1, "the tracked patch was applied exactly once")
	assert.Equal(t, "FAKE-PATCH-CONTENT", fake.AppliedPatches[0])
	require.Contains(t, fake.Calls, fmt.Sprintf("apply-patch %s", target), "applied INTO the worktree, never the parent")

	gotUntracked, err := os.ReadFile(filepath.Join(target, "untracked.go"))
	require.NoError(t, err)
	assert.Equal(t, "package untracked", string(gotUntracked), "untracked file reproduced byte-for-byte")
	gotNested, err := os.ReadFile(filepath.Join(target, "nested", "other.go"))
	require.NoError(t, err)
	assert.Equal(t, "package nested", string(gotNested), "nested untracked file reproduced, parent dirs created")

	_, err = os.ReadFile(filepath.Join(parent, "untracked.go"))
	require.NoError(t, err, "the PARENT's own copy is untouched — copy only ever reads it")
}

// TestCellsPrepare_Copy_ApplyPatchFailureFailsLoud pins "FAIL LOUDLY; do
// not half-apply and continue": an ApplyPatch error refuses the whole spawn.
func TestCellsPrepare_Copy_ApplyPatchFailureFailsLoud(t *testing.T) {
	resetStrictness(t)
	parent := t.TempDir()
	target := t.TempDir()
	fake := &git.Fake{
		Dirty:          map[string]bool{parent: true},
		Changes:        []string{" M tracked.go"},
		DiffPatchValue: "FAKE-PATCH",
		ApplyPatchErr:  fmt.Errorf("patch does not apply"),
	}
	prev := prepareIsolation
	prepareIsolation = func(_ context.Context, axes isolation.Axes, _ string, _ isolation.ImageConfig, projectDir, _ string, _ isolation.SessionState) (isolation.Policy, isolation.Workspace) {
		return stubPolicy{mk: func() pb.Client { return &stubClient{} }}, stubWorkspace{dir: target}
	}
	t.Cleanup(func() { prepareIsolation = prev })

	cfg := config.NewFixture(config.Fixture{})
	p, err := Cells{cfg: cfg, Git: fake}.Prepare(context.Background(), launch.CellRequest{
		Axes:        launch.Axes{Workspace: launch.WorkspaceAxis("worktree"), Runtime: launch.RuntimeAxis("host")},
		Engine:      mock.New(),
		Identity:    delegatedChild("coder"),
		ProjectRoot: parent,
		DirtyTree:   launch.DirtyTreeHandlerCopy,
	})
	require.Error(t, err)
	assert.Nil(t, p.Cleanup)
	assert.Contains(t, err.Error(), "patch does not apply")
}

// TestCellsPrepare_Copy_UntrackedFileMissingFailsLoud pins the same
// no-half-apply contract for the untracked-file half: a file the snapshot
// named but that vanished before application refuses the whole spawn rather
// than silently reproducing a partial WIP set.
func TestCellsPrepare_Copy_UntrackedFileMissingFailsLoud(t *testing.T) {
	resetStrictness(t)
	parent := t.TempDir() // deliberately never write untracked.go here
	target := t.TempDir()
	fake := &git.Fake{
		Dirty:         map[string]bool{parent: true},
		Changes:       []string{"?? untracked.go"},
		UntrackedList: []string{"untracked.go"},
	}
	prev := prepareIsolation
	prepareIsolation = func(_ context.Context, axes isolation.Axes, _ string, _ isolation.ImageConfig, projectDir, _ string, _ isolation.SessionState) (isolation.Policy, isolation.Workspace) {
		return stubPolicy{mk: func() pb.Client { return &stubClient{} }}, stubWorkspace{dir: target}
	}
	t.Cleanup(func() { prepareIsolation = prev })

	cfg := config.NewFixture(config.Fixture{})
	p, err := Cells{cfg: cfg, Git: fake}.Prepare(context.Background(), launch.CellRequest{
		Axes:        launch.Axes{Workspace: launch.WorkspaceAxis("worktree"), Runtime: launch.RuntimeAxis("host")},
		Engine:      mock.New(),
		Identity:    delegatedChild("coder"),
		ProjectRoot: parent,
		DirtyTree:   launch.DirtyTreeHandlerCopy,
	})
	require.Error(t, err)
	assert.Nil(t, p.Cleanup)
	assert.Contains(t, err.Error(), "untracked.go")
}

// ackedFixture builds a *config.Config carrying a REAL, on-disk dirty-tree-
// commit acknowledgement — the ack no longer lives on config.Fixture itself
// (config-layer-scope: it moved to its own admission-store file outside the
// config chain entirely), so proving the "commit" handler's authorized path
// requires writing a genuine record via config.SetDirtyTreeCommitAck and
// injecting the SAME (fs, appDir) pair commitDirtyTree reads through
// (cfg.FS()/cfg.GetAppDir()) — constructing a Fixture alone can no longer
// grant it.
func ackedFixture(t *testing.T, f config.Fixture) *config.Config {
	t.Helper()
	if f.AppDir == "" {
		f.AppDir = "/proj/.ctxloom"
	}
	fs := afero.NewMemMapFs()
	require.NoError(t, config.SetDirtyTreeCommitAck(fs, f.AppDir, true))
	cfg := config.NewFixture(f)
	cfg.SetFS(fs)
	return cfg
}

// ----- commit -----

// TestHandleDirtyParentTree_Commit_DetachedHeadRefuses pins the grandchild-
// coherence guard: committing inside a detached-HEAD checkout (exactly what
// a delegated child's OWN worktree looks like) would land on no branch and
// could be silently discarded when that worktree is torn down.
func TestHandleDirtyParentTree_Commit_DetachedHeadRefuses(t *testing.T) {
	resetStrictness(t)
	fake := &git.Fake{
		Dirty:              map[string]bool{"/child-wt": true},
		Changes:            []string{" M f.go"},
		CurrentBranchValue: "HEAD", // git's own detached-HEAD sentinel
	}
	cfg := ackedFixture(t, config.Fixture{}) // even acknowledged, this must still refuse
	_, err := handleDirtyParentTree(context.Background(), cfg, fake, "/child-wt", "grandchild", launch.DirtyTreeHandlerCommit)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "detached-HEAD")
	assert.Empty(t, fake.CommitMessages, "never even attempts the commit")
}

// TestHandleDirtyParentTree_Commit_CurrentBranchErrorRefuses pins the fix:
// before it, `branch, _ := gitClient.CurrentBranch(...)` discarded the
// error, leaving branch=="" — which is NOT "HEAD", so the detached-HEAD guard
// above never fired and the commit proceeded on an unresolvable branch name.
// An unresolvable branch is exactly the condition the guard exists to catch
// (the caller cannot tell whether this is a bare checkout), so it must refuse
// rather than silently treat the error as "not detached".
func TestHandleDirtyParentTree_Commit_CurrentBranchErrorRefuses(t *testing.T) {
	resetStrictness(t)
	fake := &git.Fake{
		Dirty:            map[string]bool{"/child-wt": true},
		Changes:          []string{" M f.go"},
		CurrentBranchErr: fmt.Errorf("git rev-parse: unknown revision or path not in the working tree"),
	}
	cfg := ackedFixture(t, config.Fixture{}) // even acknowledged, this must still refuse
	_, err := handleDirtyParentTree(context.Background(), cfg, fake, "/child-wt", "grandchild", launch.DirtyTreeHandlerCommit)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "could not determine", "names the failure rather than silently guessing a branch")
	assert.Contains(t, err.Error(), "unknown revision", "carries the underlying git error")
	assert.Empty(t, fake.CommitMessages, "never even attempts the commit")
}

// TestHandleDirtyParentTree_Commit_NoAckRefusesAndNamesKey is the first-time
// consent requirement: an absent project acknowledgement refuses the spawn
// (never commits), and the message is fully actionable — the branch, the
// bounded file list, the exact config key/file, and the alternatives.
func TestHandleDirtyParentTree_Commit_NoAckRefusesAndNamesKey(t *testing.T) {
	resetStrictness(t)
	fake := &git.Fake{
		Dirty:              map[string]bool{"/proj": true},
		Changes:            []string{" M internal/foo.go", "?? internal/bar.go"},
		CurrentBranchValue: "release/1.0",
	}
	cfg := config.NewFixture(config.Fixture{}) // no ack recorded -> DirtyTreeCommitAcknowledged defaults false
	_, err := handleDirtyParentTree(context.Background(), cfg, fake, "/proj", "coder", launch.DirtyTreeHandlerCommit)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "release/1.0", "names the branch it would commit to")
	assert.Contains(t, err.Error(), "internal/foo.go")
	assert.Contains(t, err.Error(), "internal/bar.go")
	assert.Contains(t, err.Error(), "committed state", "explains a worktree checkout's limit")
	assert.Contains(t, err.Error(), "ctxloom manage commit trust", "names the exact remedy")
	assert.Contains(t, err.Error(), "dirty_tree_commit_ack", "names the acknowledgement by its key name")
	assert.Contains(t, err.Error(), `"copy"`)
	assert.Contains(t, err.Error(), `"stale"`)
	assert.Contains(t, err.Error(), `"fail"`)
	assert.Empty(t, fake.CommitMessages, "no commit is attempted without the ack")
}

// TestHandleDirtyParentTree_Commit_PerCallHandlerCannotSupplyAck pins the
// boundary the ack is most likely to erode at: choosing "commit" via the
// per-call agent_run parameter selects the HANDLER, never the
// acknowledgement — with no project ack, it still refuses even though the
// caller explicitly asked for commit.
func TestHandleDirtyParentTree_Commit_PerCallHandlerCannotSupplyAck(t *testing.T) {
	resetStrictness(t)
	fake := &git.Fake{Dirty: map[string]bool{"/proj": true}, Changes: []string{" M f.go"}}
	cfg := config.NewFixture(config.Fixture{}) // project has NOT acknowledged
	// launch.DirtyTreeHandlerCommit is exactly what a per-call agent_run
	// dirty_tree_handler: "commit" resolves to — there is no field anywhere
	// in AgentChatRequest/agentRunInput that can also carry an ack.
	_, err := handleDirtyParentTree(context.Background(), cfg, fake, "/proj", "coder", launch.DirtyTreeHandlerCommit)
	require.Error(t, err, "an explicit per-call request for \"commit\" still refuses without the project's own ack")
	assert.Contains(t, err.Error(), "dirty_tree_commit_ack")
}

// TestHandleDirtyParentTree_Commit_AckedWarnsAndCommits pins the
// authorized path: once the project has acknowledged, "commit" warns
// (naming the branch and the bounded file list) BEFORE mutating, then
// stages and commits everything (git add -A shape) with the documented
// message format, and verifies the commit actually captured content.
func TestHandleDirtyParentTree_Commit_AckedWarnsAndCommits(t *testing.T) {
	resetStrictness(t)
	buf := captureWarnings(t)
	fake := &git.Fake{
		Dirty:              map[string]bool{"/proj": true},
		Changes:            []string{" M internal/foo.go", "?? internal/bar.go"},
		CurrentBranchValue: "main",
		CommitAllSHA:       "abc123",
		CommitAllChanged:   []string{"internal/foo.go", "internal/bar.go"},
	}
	cfg := ackedFixture(t, config.Fixture{})
	outcome, err := handleDirtyParentTree(context.Background(), cfg, fake, "/proj", "coder", launch.DirtyTreeHandlerCommit)
	require.NoError(t, err)
	assert.Nil(t, outcome.copy)

	warned := buf.String()
	assert.Contains(t, warned, "main", "the warning names the branch")
	assert.Contains(t, warned, "internal/foo.go")
	assert.Contains(t, warned, "internal/bar.go")
	assert.Contains(t, warned, "coder")
	assert.Contains(t, warned, `dirty_tree_handler is configured to "commit"`)
	assert.Contains(t, warned, `"copy"`)
	assert.Contains(t, warned, `"stale"`)
	assert.Contains(t, warned, `"fail"`)

	require.Len(t, fake.CommitMessages, 1)
	msg := fake.CommitMessages[0]
	assert.Contains(t, msg, "ctxloom: auto-commit for delegated agent spawn")
	assert.Contains(t, msg, "coder", "names the delegated agent")
	assert.Contains(t, msg, "dirty_tree_handler=commit")
	assert.Contains(t, msg, `"copy"`)
	assert.Contains(t, msg, `"stale"`)
	assert.Contains(t, msg, `"fail"`)
	assert.Contains(t, fake.Calls, "commit-all /proj")
}

// TestHandleDirtyParentTree_Commit_EmptyCommitRefusesLoud pins the
// empty-commit safety net: CommitAll reporting success but an empty
// changed-files diff (this codebase's documented empty-commit pre-commit-
// hook history) must refuse rather than spawn the child against what may be
// nothing.
func TestHandleDirtyParentTree_Commit_EmptyCommitRefusesLoud(t *testing.T) {
	resetStrictness(t)
	captureWarnings(t)
	fake := &git.Fake{
		Dirty:              map[string]bool{"/proj": true},
		Changes:            []string{" M internal/foo.go"},
		CurrentBranchValue: "main",
		CommitAllSHA:       "deadbeef",
		CommitAllChanged:   nil, // the empty-commit case
	}
	cfg := ackedFixture(t, config.Fixture{})
	_, err := handleDirtyParentTree(context.Background(), cfg, fake, "/proj", "coder", launch.DirtyTreeHandlerCommit)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "deadbeef")
	assert.Contains(t, err.Error(), "empty")
	assert.Contains(t, err.Error(), "main")
}

// TestHandleDirtyParentTree_Commit_CommitAllErrorPropagates pins that a real
// git failure (not the empty-commit case — an actual error) surfaces
// verbatim rather than being swallowed.
func TestHandleDirtyParentTree_Commit_CommitAllErrorPropagates(t *testing.T) {
	resetStrictness(t)
	captureWarnings(t)
	fake := &git.Fake{
		Dirty:              map[string]bool{"/proj": true},
		Changes:            []string{" M internal/foo.go"},
		CurrentBranchValue: "main",
		CommitAllErr:       fmt.Errorf("index.lock exists"),
	}
	cfg := ackedFixture(t, config.Fixture{})
	_, err := handleDirtyParentTree(context.Background(), cfg, fake, "/proj", "coder", launch.DirtyTreeHandlerCommit)
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
		t.Skip("git not on PATH; skipping real-git commit-handler integration test")
	}
	resetStrictness(t)
	captureWarnings(t)
	repo := initTestRepo(t)
	require.NoError(t, os.WriteFile(filepath.Join(repo, "wip.go"), []byte("package wip"), 0o644))

	real := git.NewExec()
	cfg := ackedFixture(t, config.Fixture{})
	outcome, err := handleDirtyParentTree(context.Background(), cfg, real, repo, "coder", launch.DirtyTreeHandlerCommit)
	require.NoError(t, err)
	assert.Nil(t, outcome.copy)

	dirty, err := real.IsDirty(context.Background(), repo)
	require.NoError(t, err)
	assert.False(t, dirty, "the parent tree is clean after the auto-commit")

	// Exactly what worktree isolation does next: a fresh checkout from HEAD.
	childWT := filepath.Join(t.TempDir(), "child-wt")
	require.NoError(t, real.WorktreeAdd(context.Background(), repo, childWT, "agent/child", "HEAD"))
	got, err := os.ReadFile(filepath.Join(childWT, "wip.go"))
	require.NoError(t, err, "the child's worktree sees the file — it is no longer parent-only WIP")
	assert.Equal(t, "package wip", string(got))
}

// TestCellsPrepare_DirtyTreeHandler_UnsettledDoesNotCommit is the EFFECT
// proof, at the seam that actually touches git: a handler that reaches the
// cell unsettled — a spelling no parse admitted, or none at all — must leave
// the user's working tree alone. The resolver settles the handler before
// the cell is prepared (TestResolve_DirtyTree_SettledOnce_...), so neither
// value arrives through it; this pins that the cell refuses rather than
// guesses if one ever does.
//
// The subtests share one fixture — a project that has ACKNOWLEDGED
// auto-commit and a dirty parent tree resolving to a worktree — so the only
// difference between them is the handler on the request. The control
// subtest is the vacuity guard: it proves this fixture DOES commit when the
// handler is the settled default, so the refusals below cannot be passing
// because the commit path was never reachable in the first place.
func TestCellsPrepare_DirtyTreeHandler_UnsettledDoesNotCommit(t *testing.T) {
	newFake := func() *git.Fake {
		return &git.Fake{
			Dirty:              map[string]bool{"/proj": true},
			Changes:            []string{" M internal/foo.go"},
			CurrentBranchValue: "main",
			CommitAllSHA:       "abc123",
			CommitAllChanged:   []string{"internal/foo.go"},
		}
	}
	prepare := func(t *testing.T, fake *git.Fake, handler launch.DirtyTreeHandler) (launch.Cell, error) {
		t.Helper()
		cfg := ackedFixture(t, config.Fixture{Workspace: "worktree"})
		return Cells{cfg: cfg, Git: fake}.Prepare(context.Background(), launch.CellRequest{
			Axes:        launch.Axes{Workspace: launch.WorkspaceWorktree, Runtime: launch.RuntimeHost},
			Engine:      mock.New(),
			Identity:    delegatedChild("coder"),
			ProjectRoot: "/proj",
			DirtyTree:   handler,
		})
	}

	t.Run("control: the settled default DOES commit this fixture", func(t *testing.T) {
		resetStrictness(t)
		captureWarnings(t)
		fake := newFake()
		p, err := prepare(t, fake, launch.DirtyTreeHandlerCommit)
		require.NoError(t, err)
		defer func() { _ = p.Cleanup() }()
		require.Len(t, fake.CommitMessages, 1, "the fixture reaches the auto-commit — the refusals below are therefore meaningful")
		assert.Contains(t, fake.Calls, "commit-all /proj")
	})

	for _, unsettled := range []launch.DirtyTreeHandler{"fial", ""} {
		t.Run(fmt.Sprintf("handler %q refuses and commits NOTHING", unsettled), func(t *testing.T) {
			resetStrictness(t)
			captureWarnings(t)
			fake := newFake()
			p, err := prepare(t, fake, unsettled)
			require.Error(t, err, "an unsettled handler refuses the spawn")
			assert.Contains(t, err.Error(), "reached the dirty-tree dispatch unparsed")
			assert.Nil(t, p.Cleanup)
			assert.Empty(t, fake.CommitMessages, "THE POINT: an unsettled handler must not commit the user's working tree")
			assert.NotContains(t, fake.Calls, "commit-all /proj")
		})
	}
}

// TestCellsPrepare_DirtyTree_OriginatorIsNotGated: the dirty-tree handler
// is a DELEGATED spawn's concern. The originator's own `--workspace
// worktree` run proceeds on a dirty tree without the handler running at
// all — no git probe, no refusal, no commit — because the human who asked
// for the worktree is at the terminal with the tree in front of them,
// while the handler's subject is a child an agent spawns without seeing
// what it would hand over. This is what keeps `ctxloom run --workspace
// worktree` launchable from a checkout with uncommitted work.
func TestCellsPrepare_DirtyTree_OriginatorIsNotGated(t *testing.T) {
	resetStrictness(t)
	fake := &git.Fake{Dirty: map[string]bool{"/proj": true}, Changes: []string{" M f.go"}, CurrentBranchValue: "main"}
	stubPrepareIsolation(t, map[string]bool{}, func() pb.Client { return &stubClient{} })
	cfg := config.NewFixture(config.Fixture{Workspace: "worktree"})
	for _, handler := range []launch.DirtyTreeHandler{launch.DirtyTreeHandlerCommit, launch.DirtyTreeHandlerFail} {
		t.Run(string(handler), func(t *testing.T) {
			p, err := Cells{cfg: cfg, Git: fake}.Prepare(context.Background(), launch.CellRequest{
				Axes:        launch.Axes{Workspace: launch.WorkspaceWorktree, Runtime: launch.RuntimeHost},
				Engine:      mock.New(),
				Identity:    sessions.Identity{Harp: "originator"},
				ProjectRoot: "/proj",
				DirtyTree:   handler,
			})
			require.NoError(t, err, "the originator's worktree run is not the handler's subject")
			defer func() { _ = p.Cleanup() }()
			assert.Empty(t, fake.Calls, "the handler never ran: no git probe, no commit")
		})
	}
}

// delegatedChild is the identity of a spawned child (depth > 0): the
// dirty-tree handler's subject.
func delegatedChild(harp string) sessions.Identity {
	return sessions.Identity{Harp: harp, Depth: 1}
}

// TestApplyCopySnapshot_ReproducesUntrackedSymlink pins that "copy"
// reproduces an untracked SYMLINK, not just an untracked regular file.
// `git ls-files --others` lists symlinks exactly like regular
// files, so a parent tree whose uncommitted work includes a new symlink used
// to have that link dropped on the floor — no error, no warning, a worktree
// that silently differs from what the copy handler promised to reproduce.
// applyCopySnapshot's contract is "never half-applies and continues", and a
// skipped symlink is precisely a half-application.
func TestApplyCopySnapshot_ReproducesUntrackedSymlink(t *testing.T) {
	resetStrictness(t)
	parent := t.TempDir()
	target := t.TempDir()

	require.NoError(t, os.WriteFile(filepath.Join(parent, "real.go"), []byte("package real"), 0o644))
	require.NoError(t, os.Symlink("real.go", filepath.Join(parent, "link.go")))
	require.NoError(t, os.MkdirAll(filepath.Join(parent, "nested"), 0o755))
	require.NoError(t, os.Symlink("../real.go", filepath.Join(parent, "nested", "deep.go")))

	snap := &copySnapshot{
		untracked: []string{"real.go", "link.go", "nested/deep.go"},
		sourceDir: parent,
	}
	require.NoError(t, applyCopySnapshot(context.Background(), &git.Fake{}, target, snap))

	info, err := os.Lstat(filepath.Join(target, "link.go"))
	require.NoError(t, err, "the untracked symlink must exist in the worktree")
	assert.NotZero(t, info.Mode()&os.ModeSymlink, "and must still be a symlink, not a materialized copy of its target")
	dest, err := os.Readlink(filepath.Join(target, "link.go"))
	require.NoError(t, err)
	assert.Equal(t, "real.go", dest, "the link target is reproduced verbatim (never resolved to an absolute host path)")

	deep, err := os.Readlink(filepath.Join(target, "nested", "deep.go"))
	require.NoError(t, err, "a symlink under a subdirectory needs its parent dirs created too")
	assert.Equal(t, "../real.go", deep)
}

// TestApplyCopySnapshot_UnsupportedUntrackedEntryFailsLoud pins the other
// half of the symlink guard above: an untracked path that is NEITHER a
// regular file nor a symlink (a fifo, a socket, a device node) must REFUSE
// rather than be skipped in silence. The copy handler's whole promise is
// that the child's worktree reproduces the parent's uncommitted state;
// anything it cannot reproduce is a fact the caller is entitled to.
func TestApplyCopySnapshot_UnsupportedUntrackedEntryFailsLoud(t *testing.T) {
	resetStrictness(t)
	parent := t.TempDir()
	target := t.TempDir()
	fifo := filepath.Join(parent, "pipe")
	if out, err := exec.Command("mkfifo", fifo).CombinedOutput(); err != nil {
		t.Skipf("mkfifo unavailable: %v (%s)", err, out)
	}

	snap := &copySnapshot{untracked: []string{"pipe"}, sourceDir: parent}
	err := applyCopySnapshot(context.Background(), &git.Fake{}, target, snap)
	require.Error(t, err, "an unreproducible entry must fail loud, never be skipped silently")
	assert.Contains(t, err.Error(), "pipe", "the refusal names the path it could not reproduce")
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
		Cell:     launch.Cell{Workspace: t.TempDir(), Cleanup: func() error { return nil }},
	}
	proc, serr := StartEngine(context.Background(), l, nil, 0, nil)
	require.Error(t, serr, "StartEngine must refuse a launch it has no transport for, not panic")
	assert.Nil(t, proc)
	assert.Contains(t, serr.Error(), "starter", "the refusal names the seam that was not supplied")
}

// TestHandleDirtyParentTree_Commit_ListingFailureIsNamedInThePreview pins a
// defect: the "commit" handler prints a preview naming the files it is
// about to commit on the user's branch, and boundDirtyChanges turned a
// WorkingChanges FAILURE into an empty list — so the preview immediately
// before an auto-commit of the user's tree said, with no qualification, that
// there was nothing to name. "I could not find out" must not render as the
// empty set in the one message whose job is to show what is about to be
// committed.
func TestHandleDirtyParentTree_Commit_ListingFailureIsNamedInThePreview(t *testing.T) {
	resetStrictness(t)
	warnings := captureWarnings(t)
	fake := &git.Fake{
		Dirty:            map[string]bool{"/proj": true},
		ChangesErr:       assert.AnError,
		CommitAllChanged: []string{"internal/foo.go"},
	}
	cfg := ackedFixture(t, config.Fixture{})
	_, err := handleDirtyParentTree(context.Background(), cfg, fake, "/proj", "coder", launch.DirtyTreeHandlerCommit)
	require.NoError(t, err, "a listing failure stays best-effort: it must not block the configured commit")
	assert.Contains(t, warnings.String(), "could not list",
		"the preview must SAY the file listing failed rather than showing an empty list")
}

// TestHandleDirtyParentTree_Fail_ListingFailureIsNamedInTheRefusal is the
// same defect on the refusal path: "fail" exists to tell the caller
// WHICH uncommitted paths the child would not see, and a swallowed listing
// error made it name none of them while asserting they exist.
func TestHandleDirtyParentTree_Fail_ListingFailureIsNamedInTheRefusal(t *testing.T) {
	resetStrictness(t)
	fake := &git.Fake{Dirty: map[string]bool{"/proj": true}, ChangesErr: assert.AnError}
	cfg := config.NewFixture(config.Fixture{})
	_, err := handleDirtyParentTree(context.Background(), cfg, fake, "/proj", "coder", launch.DirtyTreeHandlerFail)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "could not list",
		"the refusal must distinguish an unreadable listing from an empty one")
}

// TestHandleDirtyParentTree_Stale_ListingFailureIsNamedInTheWarning covers
// the third message: "stale" promises to list what the child will
// NOT see.
func TestHandleDirtyParentTree_Stale_ListingFailureIsNamedInTheWarning(t *testing.T) {
	resetStrictness(t)
	warnings := captureWarnings(t)
	fake := &git.Fake{Dirty: map[string]bool{"/proj": true}, ChangesErr: assert.AnError}
	cfg := config.NewFixture(config.Fixture{})
	_, err := handleDirtyParentTree(context.Background(), cfg, fake, "/proj", "coder", launch.DirtyTreeHandlerStale)
	require.NoError(t, err)
	assert.Contains(t, warnings.String(), "could not list")
}
