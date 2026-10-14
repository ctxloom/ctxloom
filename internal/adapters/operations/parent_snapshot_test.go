package operations

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/adapters/isolation"
	"github.com/ctxloom/ctxloom/internal/core/config"
	"github.com/ctxloom/ctxloom/internal/core/launch"
	"github.com/ctxloom/ctxloom/internal/core/paths"
	"github.com/ctxloom/ctxloom/internal/engines"
	"github.com/ctxloom/ctxloom/internal/engines/mock"
	"github.com/ctxloom/ctxloom/internal/shared/tasks/taskstest"
	"github.com/ctxloom/ctxloom/internal/testsupport"
)

// ===== The parent snapshot, end to end over real git =====
//
// A delegated child's NEW worktree is carved from an off-branch snapshot of
// its parent's dirty tree, and the parent's branch reaches that snapshot
// only once the child is up (its environment's Start returned). These drive
// the real cells adapter over the real isolation.Prepare and real git; only
// the runner start is stood in for (inertStart), since what is under test is
// what the launch does to the parent around it.

// inertStart is a prepared environment whose Start launches no runner: it
// returns an inert handle, or startErr — a runner or container that could
// not be started.
type inertStart struct {
	isolation.Environment
	startErr error
}

func (e inertStart) Start(context.Context, isolation.RunnerRequest) (*isolation.RunnerHandle, error) {
	if e.startErr != nil {
		return nil, e.startErr
	}
	return &isolation.RunnerHandle{Kill: func() {}, Wait: func() error { return nil }}, nil
}

// realCarving prepares every cell with the real isolation.Prepare. With
// prepareErr set, the environment is prepared — the checkout carved — and
// then torn down and refused, as a container whose mounts or start fail
// after the carve is; otherwise its Start fails with startErr (nil starts).
func realCarving(t *testing.T, prepareErr, startErr error) {
	t.Helper()
	prev := prepareEnvironment
	prepareEnvironment = func(ctx context.Context, _ launch.CellRequest, s isolation.Spec) (isolation.Environment, error) {
		env, err := isolation.Prepare(ctx, s)
		if err != nil {
			return nil, err
		}
		if prepareErr != nil {
			_ = env.Cleanup()
			return nil, prepareErr
		}
		return inertStart{Environment: env, startErr: startErr}, nil
	}
	t.Cleanup(func() { prepareEnvironment = prev })
}

// prepareChildOver prepares delegated child harp's worktree cell over the
// real repo, failing the test if isolation degraded off the worktree axis
// (the cell would then sit on the repo itself and measure nothing).
func prepareChildOver(t *testing.T, repo, harp string) (launch.Cell, error) {
	t.Helper()
	sessionDir, err := paths.HarpDir(harp)
	require.NoError(t, err)
	cell, err := Cells{engines: engines.Registry(), cfg: config.NewFixture(config.Fixture{Workspace: "worktree"})}.Prepare(context.Background(), launch.CellRequest{
		SessionDir:  sessionDir,
		Axes:        launch.Axes{Workspace: launch.WorkspaceWorktree, Runtime: launch.RuntimeHost},
		Engine:      mock.New(),
		Identity:    delegatedChild(harp),
		ProjectRoot: repo,
	})
	if err == nil {
		work, werr := paths.HarpWorkDir(harp)
		require.NoError(t, werr)
		require.True(t, strings.HasPrefix(cell.Paths.Paths().ProjectRoot.Host, work+string(filepath.Separator)),
			"isolation degraded off the worktree axis: the cell is at %q", cell.Paths.Paths().ProjectRoot.Host)
	}
	return cell, err
}

// startChild starts the cell's runner the way a delegated spawn does.
func startChild(cell launch.Cell, harp string) (*EngineProcess, error) {
	return StartEngine(context.Background(), launch.Launch{Identity: delegatedChild(harp), Engine: "mock", Cell: cell}, nil, 0, nil)
}

// gitIn runs git in dir and returns its trimmed output.
func gitIn(t *testing.T, dir string, args ...string) string {
	t.Helper()
	out, err := taskstest.GitCmd(dir, nil, args...).CombinedOutput()
	require.NoError(t, err, "git %v: %s", args, out)
	return strings.TrimSpace(string(out))
}

// parentState is everything a failed launch must leave exactly as it was:
// every ref, the real index's bytes, the working tree's files and what
// status says of them.
type parentState struct {
	refs, status, worktrees string
	index                   []byte
	files                   map[string]string
}

func captureParent(t *testing.T, repo string) parentState {
	t.Helper()
	index, err := os.ReadFile(filepath.Join(repo, ".git", "index"))
	require.NoError(t, err)
	files := map[string]string{}
	for _, name := range []string{"README.md", "draft.md", "build.log"} {
		if b, err := os.ReadFile(filepath.Join(repo, name)); err == nil {
			files[name] = string(b)
		}
	}
	return parentState{
		refs:      gitIn(t, repo, "for-each-ref", "--format=%(refname) %(objectname)"),
		status:    gitIn(t, repo, "--no-optional-locks", "status", "--porcelain"),
		worktrees: gitIn(t, repo, "worktree", "list", "--porcelain"),
		index:     index,
		files:     files,
	}
}

// dirtyParent is a parent tree mid-edit: README.md modified with PART of it
// staged in the real index, an untracked draft.md, and an ignored build.log.
func dirtyParent(t *testing.T) string {
	t.Helper()
	testsupport.Isolate(t)
	resetStrictness(t)
	captureWarnings(t)
	repo := initTestRepo(t)
	require.NoError(t, os.WriteFile(filepath.Join(repo, ".gitignore"), []byte("*.log\n"), 0o644))
	gitFixtureRun(t, repo, "add", ".gitignore")
	gitFixtureRun(t, repo, "commit", "-q", "-m", "ignore logs")
	require.NoError(t, os.WriteFile(filepath.Join(repo, "README.md"), []byte("seed\nstaged line\n"), 0o644))
	gitFixtureRun(t, repo, "add", "README.md")
	require.NoError(t, os.WriteFile(filepath.Join(repo, "README.md"), []byte("seed\nstaged line\nunstaged line\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(repo, "draft.md"), []byte("the parent's draft"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(repo, "build.log"), []byte("noise"), 0o644))
	return repo
}

// A launch whose runner cannot be started after the child's checkout was
// carved leaves the parent's refs, its real index (partial staging and all),
// and its working tree byte-identical, and no child branch or checkout
// behind: the commit the human found on main for a child that never ran.
func TestDelegatedLaunch_AStartFailureLeavesTheParentUntouched(t *testing.T) {
	repo := dirtyParent(t)
	before := captureParent(t, repo)
	realCarving(t, nil, errors.New("container exited before it was observed running"))

	cell, err := prepareChildOver(t, repo, "coder")
	require.NoError(t, err)
	_, err = startChild(cell, "coder")
	require.Error(t, err, "the start fails")

	assert.Equal(t, before, captureParent(t, repo), "THE POINT: the parent is exactly as it was — refs, index bytes, files, status — and no child branch or checkout remains")
}

// A launch refused after the carve but before any start — a container whose
// git-dir mounts or start failed inside Prepare — leaves the parent the
// same way.
func TestDelegatedLaunch_APrepareFailureAfterTheCarveLeavesTheParentUntouched(t *testing.T) {
	repo := dirtyParent(t)
	before := captureParent(t, repo)
	realCarving(t, errors.New("mounting the git common dir failed"), nil)

	_, err := prepareChildOver(t, repo, "coder")
	require.Error(t, err, "the launch is refused")

	assert.Equal(t, before, captureParent(t, repo), "THE POINT: nothing of the launch is left on the parent")
}

// A child that starts is carved from the snapshot and the parent's branch
// advances to it: the snapshot sits on the old HEAD, carries the partial
// staging, the unstaged edit and the untracked draft but not the ignored
// log, the parent's tree then reads clean, and its files are untouched.
func TestDelegatedLaunch_AStartedChildAdvancesTheParentToTheSnapshot(t *testing.T) {
	repo := dirtyParent(t)
	before := captureParent(t, repo)
	oldHead := gitIn(t, repo, "rev-parse", "HEAD")
	oldIndex := before.index
	realCarving(t, nil, nil)

	cell, err := prepareChildOver(t, repo, "coder")
	require.NoError(t, err)
	checkout := cell.Paths.Paths().ProjectRoot.Host
	child := gitIn(t, checkout, "rev-parse", "--abbrev-ref", "HEAD")
	snapshot := gitIn(t, repo, "rev-parse", child)
	assert.Equal(t, oldHead, gitIn(t, repo, "rev-parse", "main"), "nothing advances before the start")
	assert.Equal(t, oldIndex, captureParent(t, repo).index, "the real index is not written before the start")
	assert.Equal(t, oldHead, gitIn(t, repo, "rev-parse", snapshot+"^"), "the child's branch starts at a snapshot on the parent's HEAD")

	proc, err := startChild(cell, "coder")
	require.NoError(t, err)

	assert.Equal(t, snapshot, gitIn(t, repo, "rev-parse", "main"), "the started child's snapshot is now the parent's tip")
	assert.Equal(t, "ctxloom: commit working tree before delegating to coder", gitIn(t, repo, "log", "-1", "--format=%s", "main"))
	assert.Equal(t, "seed\nstaged line\nunstaged line", gitIn(t, repo, "show", "main:README.md"), "the snapshot is the working tree, not the index")
	assert.Equal(t, "the parent's draft", gitIn(t, repo, "show", "main:draft.md"), "untracked, not ignored: in the snapshot")
	assert.NotContains(t, gitIn(t, repo, "ls-tree", "--name-only", "main"), "build.log", "ignored: not in the snapshot")
	assert.Empty(t, gitIn(t, repo, "status", "--porcelain"), "the parent's tree reads clean against its new HEAD")
	assert.Equal(t, before.files, captureParent(t, repo).files, "the parent's working tree is untouched")
	draft, err := os.ReadFile(filepath.Join(checkout, "draft.md"))
	require.NoError(t, err, "the child's checkout holds the parent's uncommitted draft")
	assert.Equal(t, "the parent's draft", string(draft))

	proc.Kill()
	assert.Equal(t, snapshot, gitIn(t, repo, "rev-parse", "main"), "the run's teardown does not undo the advance")
}

// A parent branch that moves between the carve and the start (the human
// committed) is never forced: it stays where the human put it, and the
// launch says loudly that the child started from a snapshot not on it.
func TestDelegatedLaunch_AParentThatMovedIsLeftAloneAndSaysSo(t *testing.T) {
	repo := dirtyParent(t)
	warnings := captureWarnings(t)
	realCarving(t, nil, nil)

	cell, err := prepareChildOver(t, repo, "coder")
	require.NoError(t, err)
	child := gitIn(t, cell.Paths.Paths().ProjectRoot.Host, "rev-parse", "--abbrev-ref", "HEAD")
	snapshot := gitIn(t, repo, "rev-parse", child)
	gitFixtureRun(t, repo, "commit", "-q", "-m", "the human's own commit")
	human := gitIn(t, repo, "rev-parse", "HEAD")

	proc, err := startChild(cell, "coder")
	require.NoError(t, err, "the child is up; a moved parent does not refuse it")

	assert.Equal(t, human, gitIn(t, repo, "rev-parse", "main"), "THE POINT: the human's commit is the tip, never overwritten")
	_, ancestorErr := taskstest.GitCmd(repo, nil, "merge-base", "--is-ancestor", snapshot, "main").CombinedOutput()
	assert.Error(t, ancestorErr, "the snapshot is not on main")
	assert.Contains(t, warnings.String(), snapshot[:12], "the warning names the snapshot")
	assert.Contains(t, warnings.String(), "moved")
	assert.Contains(t, gitIn(t, repo, "status", "--porcelain"), "draft.md", "the parent's uncommitted work stays uncommitted")

	proc.Kill()
	assert.Equal(t, snapshot, gitIn(t, repo, "rev-parse", child), "the started child's branch — the only ref holding the snapshot — outlives its teardown")
}
