package operations

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/adapters/git"
	"github.com/ctxloom/ctxloom/internal/adapters/isolation"
	"github.com/ctxloom/ctxloom/internal/core/launch"
	"github.com/ctxloom/ctxloom/internal/shared/report"
	"github.com/ctxloom/ctxloom/internal/shared/strictness"
)

// TestCellsPrepare_AFailedSnapshotIsTheLaunchsRefusal: a delegated child's
// carve snapshots the parent first; when that snapshot fails, the launch is
// refused with the snapshot's own failure — never as a runtime that was
// unavailable, and never prepared without the snapshot.
func TestCellsPrepare_AFailedSnapshotIsTheLaunchsRefusal(t *testing.T) {
	resetStrictness(t)
	captureWarnings(t)
	stubCarvingEnvironment(t, "/proj", nil)
	snapErr := errors.New("commit-tree failed")
	fake := commitFixture()
	fake.SnapshotErr = snapErr

	_, err := prepareDelegatedWorktree(t, fake)
	require.ErrorIs(t, err, snapErr)
	assert.NotErrorIs(t, err, launch.ErrRuntimeUnavailable, "the runtime is not what failed")
}

// TestCellsPrepare_AnEarlierFindingDoesNotGateThisCarve: the isolation gate
// the carve consults reads THIS launch's findings window — a finding recorded
// before the launch began is not its refusal.
func TestCellsPrepare_AnEarlierFindingDoesNotGateThisCarve(t *testing.T) {
	resetStrictness(t)
	captureWarnings(t)
	strictness.FailAlways(report.KindIsolation, "start the runtime", "an earlier launch's container could not start")
	base := stubCarvingEnvironment(t, "/proj", nil)
	fake := commitFixture()

	cell, err := prepareDelegatedWorktree(t, fake)
	require.NoError(t, err)
	t.Cleanup(func() { _ = cell.Cleanup() })
	assert.Equal(t, "abc123", *base, "carved from the snapshot")
}

// TestParentCommit_ARefusedWindowCarvesNothing: once this launch's window
// holds an isolation refusal, the hook refuses — it names no commit to carve
// from and snapshots nothing.
func TestParentCommit_ARefusedWindowCarvesNothing(t *testing.T) {
	resetStrictness(t)
	fake := commitFixture()
	p := &parentCommit{git: fake, project: "/proj", harp: "coder", mode: strictness.Mode{}}
	mark := strictness.Checkpoint()
	t.Cleanup(func() { strictness.Close(mark) })
	p.opened(mark)
	strictness.FailAlways(report.KindIsolation, "start the runtime", "container isolation was requested but could not start")

	_, err := p.beforeCheckout(context.Background(), childBranch)
	require.Error(t, err)
	assert.Empty(t, fake.SnapshotMessages)
}

// TestParentCommit_AFailedSnapshotNamesNoBase: the hook that could not
// snapshot the parent names no commit to carve from — it fails, so nothing
// is carved from a parent that was not captured.
func TestParentCommit_AFailedSnapshotNamesNoBase(t *testing.T) {
	resetStrictness(t)
	captureWarnings(t)
	snapErr := errors.New("commit-tree failed")
	fake := commitFixture()
	fake.SnapshotErr = snapErr
	p := &parentCommit{git: fake, project: "/proj", harp: "coder", mode: strictness.Mode{}}

	_, err := p.beforeCheckout(context.Background(), childBranch)
	require.ErrorIs(t, err, snapErr)
	require.ErrorIs(t, p.refusal(), snapErr, "and it is the launch's refusal")
}

// TestParentCommit_StartedAdvancesOnlyASnapshot: a cell that is not a
// delegated child's (nil) has nothing to advance, and neither has a child
// carved from HEAD with nothing snapshotted.
func TestParentCommit_StartedAdvancesOnlyASnapshot(t *testing.T) {
	var none *parentCommit
	none.started(context.Background())

	fake := &git.Fake{}
	(&parentCommit{git: fake, project: "/proj"}).started(context.Background())
	assert.Empty(t, fake.Advances, "nothing was snapshotted, so the parent's branch stays put")
}

// TestParentCommit_AbandonedDeletesOnlyWhatItCarved: an abandoned cell
// deletes its child branch only when it knows both the branch and the commit
// it was carved at; missing either, it deletes nothing.
func TestParentCommit_AbandonedDeletesOnlyWhatItCarved(t *testing.T) {
	for name, carved := range map[string]struct {
		child git.Branch
		base  string
	}{
		"no commit known":  {child: childBranch},
		"no branch carved": {base: "abc123"},
	} {
		t.Run(name, func(t *testing.T) {
			fake := &git.Fake{ExistingBranches: []git.Branch{childBranch, {}}}
			p := &parentCommit{git: fake, project: "/proj", child: carved.child, base: carved.base}
			p.abandoned()
			assert.Empty(t, fake.DeletedAt)
		})
	}
}

// interactiveFails is an environment whose interactive start fails.
type interactiveFails struct {
	isolation.Environment
	err error
}

func (e interactiveFails) Interactive(context.Context, isolation.RunnerRequest) (isolation.Interactive, error) {
	return isolation.Interactive{}, e.err
}

// TestParentCommitEnvironment_AFailedInteractiveStartIsNotUp: the failure is
// the caller's, and the child is not up, so the parent is not advanced.
func TestParentCommitEnvironment_AFailedInteractiveStartIsNotUp(t *testing.T) {
	cause := errors.New("pty failed")
	fake := &git.Fake{}
	p := &parentCommit{git: fake, project: "/proj", snap: parentSnapshot{commit: "abc123"}}
	env := parentCommitEnvironment{Environment: interactiveFails{Environment: stubEnvAt("/proj", nil), err: cause}, commits: p}

	_, err := env.Interactive(context.Background(), isolation.RunnerRequest{})
	require.ErrorIs(t, err, cause)
	assert.Empty(t, fake.Advances)
}

// TestShortSHA_TwelveCharacters: a commit is named by its first twelve
// characters; anything that short or shorter is named whole.
func TestShortSHA_TwelveCharacters(t *testing.T) {
	assert.Equal(t, "0123456789ab", shortSHA("0123456789abcdef0123456789abcdef01234567"))
	assert.Equal(t, "0123456789ab", shortSHA("0123456789abc"))
	assert.Equal(t, "abc123", shortSHA("abc123"))
}
