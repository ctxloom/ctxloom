package isolation

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/adapters/git"
	"github.com/ctxloom/ctxloom/internal/core/launch"
	"github.com/ctxloom/ctxloom/internal/shared/safefs"
	"github.com/ctxloom/ctxloom/pkg/clifmt/clidiag"
)

// TestReapDeadScratch_ALiveOwnerStopsNoReaping: a dir whose owner still holds
// its lock is passed over, and the reaper goes on to the dead ones after it.
func TestReapDeadScratch_ALiveOwnerStopsNoReaping(t *testing.T) {
	parent := t.TempDir()
	const prefix = "ctxloom-reaplive-"
	live := filepath.Join(parent, prefix+"0live") // sorts first
	require.NoError(t, os.Mkdir(live, 0o700))
	writeScratchLock(t, live)
	held, err := safefs.New().Locks.TryLockExisting(context.Background(), filepath.Join(live, ownedScratchLockName))
	require.NoError(t, err)
	t.Cleanup(func() { _ = held.Unlock() })
	var dead []string
	for _, name := range []string{"1dead", "2dead"} {
		d := filepath.Join(parent, prefix+name)
		require.NoError(t, os.Mkdir(d, 0o700))
		writeScratchLock(t, d)
		dead = append(dead, d)
	}

	reapDeadScratch(safefs.New().Locks, parent, prefix)

	assert.DirExists(t, live, "a live owner's dir is never touched")
	for _, d := range dead {
		assert.NoDirExists(t, d, "%s is dead and is reaped", filepath.Base(d))
	}
}

// TestReapDeadScratch_TheSecondPassFinishesTheJob: where the delete made
// while holding the lock could not finish, the pass after the unlock does.
func TestReapDeadScratch_TheSecondPassFinishesTheJob(t *testing.T) {
	parent := t.TempDir()
	const prefix = "ctxloom-reapsecond-"
	dead := filepath.Join(parent, prefix+"dead")
	require.NoError(t, os.Mkdir(dead, 0o700))
	writeScratchLock(t, dead)
	orig := scratchReapRemove
	scratchReapRemove = func(string) error { return errors.New("busy: the open lock file") }
	t.Cleanup(func() { scratchReapRemove = orig })

	reapDeadScratch(safefs.New().Locks, parent, prefix)
	assert.NoDirExists(t, dead)
}

// TestReapDeadScratch_RechecksBeforeEveryDelete: a dir that stopped being a
// scratch dir between the two passes (its lock file gone) is not deleted by
// the second.
func TestReapDeadScratch_RechecksBeforeEveryDelete(t *testing.T) {
	parent := t.TempDir()
	const prefix = "ctxloom-reaprecheck-"
	dir := filepath.Join(parent, prefix+"dead")
	require.NoError(t, os.Mkdir(dir, 0o700))
	writeScratchLock(t, dir)
	require.NoError(t, os.WriteFile(filepath.Join(dir, "state"), []byte("keep"), 0o600))
	orig := scratchReapRemove
	scratchReapRemove = func(d string) error {
		// Only the lock file goes: the dir is no longer provably scratch.
		return os.Remove(filepath.Join(d, ownedScratchLockName))
	}
	t.Cleanup(func() { scratchReapRemove = orig })

	reapDeadScratch(safefs.New().Locks, parent, prefix)
	assert.FileExists(t, filepath.Join(dir, "state"), "a dir without its lock file is not the reaper's")
}

// addCheckoutFake is a git whose project dir resolves, for addCheckout.
func addCheckoutFake(t *testing.T) *git.Fake {
	t.Helper()
	return &git.Fake{CommonDirValue: t.TempDir()}
}

// TestAddCheckout_EveryGitFailureIsTheCheckoutsFailure: a branch probe that
// fails, an add on the existing branch that fails, or a BeforeCheckout hook
// that refuses — each stops the checkout, carries its cause, and creates
// nothing.
func TestAddCheckout_EveryGitFailureIsTheCheckoutsFailure(t *testing.T) {
	branch, err := git.ParseBranch("ctxloom/agent-x")
	require.NoError(t, err)
	cause := errors.New("git broke")

	t.Run("the branch probe fails", func(t *testing.T) {
		g := addCheckoutFake(t)
		g.BranchExistsErr = cause
		err := sessionWorktree(t, g).addCheckout(context.Background(), "/proj", "/proj-wt", branch)
		require.ErrorIs(t, err, cause)
		assert.Empty(t, g.Worktrees, "nothing was added on a branch whose existence is unknown")
	})
	t.Run("the add on the existing branch fails", func(t *testing.T) {
		g := addCheckoutFake(t)
		g.ExistingBranches = []git.Branch{branch}
		g.AddErr = cause
		err := sessionWorktree(t, g).addCheckout(context.Background(), "/proj", "/proj-wt", branch)
		require.ErrorIs(t, err, cause)
	})
	t.Run("the BeforeCheckout hook refuses", func(t *testing.T) {
		g := addCheckoutFake(t)
		w := sessionWorktree(t, g)
		w.beforeCheckout = func(context.Context, git.Branch) (string, error) { return "", cause }
		err := w.addCheckout(context.Background(), "/proj", "/proj-wt", branch)
		require.ErrorIs(t, err, errCheckoutRefused)
		require.ErrorIs(t, err, cause, "the hook's own reason is carried")
		assert.Empty(t, g.Worktrees)
	})
}

// TestReuseExistingCheckout_AnUnverifiableOccupantCarriesTheProbeFailure: a
// checkout path that is occupied when git cannot list its worktrees is
// refused as unadoptable, and the refusal carries git's failure.
func TestReuseExistingCheckout_AnUnverifiableOccupantCarriesTheProbeFailure(t *testing.T) {
	cause := errors.New("worktree list failed")
	g := addCheckoutFake(t)
	g.ListErr = cause
	branch, err := git.ParseBranch("ctxloom/agent-x")
	require.NoError(t, err)

	_, err = sessionWorktree(t, g).reuseExistingCheckout(context.Background(), "/proj", t.TempDir(), branch)
	require.ErrorIs(t, err, launch.ErrWorkspaceUnadoptable)
	require.ErrorIs(t, err, cause)
}

// TestResolveDevcontainerBase_AFieldOfTheWrongTypeIsAParseError: a
// devcontainer.json that is valid JSON but not the documented shape is
// refused, not half-read.
func TestResolveDevcontainerBase_AFieldOfTheWrongTypeIsAParseError(t *testing.T) {
	root := t.TempDir()
	writeDevcontainer(t, root, `{"image": 5}`)
	_, err := resolveDevcontainerBase(root, "")
	require.ErrorContains(t, err, "parse")
}

// TestResolveDevcontainerBase_FieldsOnlyWarnsWithoutVarsNotice: one unhonoured
// field and no ${...} variable — the warning names that field alone, and says
// nothing about variables.
func TestResolveDevcontainerBase_FieldsOnlyWarnsWithoutVarsNotice(t *testing.T) {
	clidiag.ResetWarnOnce()
	t.Cleanup(clidiag.ResetWarnOnce)
	var sink bytes.Buffer
	t.Cleanup(clidiag.SetSink(&sink))

	root := t.TempDir()
	writeDevcontainer(t, root, `{"image": "debian:13", "remoteUser": "vscode"}`)
	_, err := resolveDevcontainerBase(root, "")
	require.NoError(t, err)
	out := sink.String()
	assert.Contains(t, out, "does NOT honor these fields: remoteUser")
	assert.NotContains(t, out, "${...}")
}
