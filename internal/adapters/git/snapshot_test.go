package git

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/shared/tasks/taskstest"
)

// gitIn runs git in dir and returns its trimmed output.
func gitIn(t *testing.T, dir string, args ...string) string {
	t.Helper()
	out, err := taskstest.GitCmd(dir, nil, args...).CombinedOutput()
	require.NoError(t, err, "git %v: %s", args, out)
	return strings.TrimSpace(string(out))
}

// midEdit is initRepo mid-edit: README.md modified with part of the edit
// staged, an untracked draft.md, an ignored build.log.
func midEdit(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}
	dir := initRepo(t)
	commit(t, dir, ".gitignore", "*.log\n", "ignore logs")
	require.NoError(t, writeFile(filepath.Join(dir, "README.md"), "seed\nstaged\n"))
	gitIn(t, dir, "add", "README.md")
	require.NoError(t, writeFile(filepath.Join(dir, "README.md"), "seed\nstaged\nunstaged\n"))
	require.NoError(t, writeFile(filepath.Join(dir, "draft.md"), "draft"))
	require.NoError(t, writeFile(filepath.Join(dir, "build.log"), "noise"))
	return dir
}

func readIndex(t *testing.T, dir string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(dir, ".git", "index"))
	require.NoError(t, err)
	return b
}

// SnapshotCommit commits the working tree on top of HEAD — tracked edits
// staged or not, untracked files, never ignored ones — and moves nothing:
// HEAD, the branch, the real index's bytes and status are as they were.
func TestSnapshotCommit_CommitsTheTreeOffBranchAndTouchesNothing(t *testing.T) {
	dir := midEdit(t)
	head := gitIn(t, dir, "rev-parse", "HEAD")
	index := readIndex(t, dir)
	status := gitIn(t, dir, "--no-optional-locks", "status", "--porcelain")

	snap, base, err := NewExec().SnapshotCommit(context.Background(), dir, "the subject\n\nthe body\n")
	require.NoError(t, err)

	assert.Equal(t, head, base, "the snapshot sits on HEAD")
	assert.Equal(t, head, gitIn(t, dir, "rev-parse", snap+"^"))
	assert.Equal(t, "the subject", gitIn(t, dir, "log", "-1", "--format=%s", snap))
	assert.Equal(t, "ctxloom <ctxloom@example.com>", gitIn(t, dir, "log", "-1", "--format=%an <%ae>", snap), "the identity `git commit` would use")
	assert.Equal(t, "seed\nstaged\nunstaged", gitIn(t, dir, "show", snap+":README.md"), "the working tree, not the index")
	assert.Equal(t, "draft", gitIn(t, dir, "show", snap+":draft.md"), "untracked, not ignored: in")
	assert.NotContains(t, gitIn(t, dir, "ls-tree", "--name-only", snap), "build.log", "ignored: out")

	assert.Equal(t, head, gitIn(t, dir, "rev-parse", "HEAD"), "HEAD did not move")
	assert.Equal(t, "refs/heads/main", gitIn(t, dir, "symbolic-ref", "HEAD"))
	assert.Equal(t, index, readIndex(t, dir), "THE POINT: the real index is byte-identical")
	assert.Equal(t, status, gitIn(t, dir, "--no-optional-locks", "status", "--porcelain"))
	leftovers, _ := filepath.Glob(filepath.Join(dir, ".git", "ctxloom-snapshot-index-*"))
	assert.Empty(t, leftovers, "the private index is removed")
}

// A tree equal to HEAD's is no snapshot.
func TestSnapshotCommit_ATreeEqualToHeadIsNoSnapshot(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}
	dir := initRepo(t)
	require.NoError(t, writeFile(filepath.Join(dir, "x.log"), "ignored only"))
	commit(t, dir, ".gitignore", "*.log\n", "ignore logs")
	snap, base, err := NewExec().SnapshotCommit(context.Background(), dir, "m")
	require.NoError(t, err)
	assert.Empty(t, snap)
	assert.Equal(t, gitIn(t, dir, "rev-parse", "HEAD"), base)
}

// An unborn HEAD has nothing to snapshot on: refused, not a root commit.
func TestSnapshotCommit_AnUnbornHeadIsRefused(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}
	dir := initRepoUnborn(t)
	require.NoError(t, writeFile(filepath.Join(dir, "a.txt"), "a"))
	_, _, err := NewExec().SnapshotCommit(context.Background(), dir, "m")
	require.Error(t, err)
}

// AdvanceBranch moves the checked-out branch to the snapshot and resets the
// index to it, so a tree holding exactly the snapshot's content reads clean.
func TestAdvanceBranch_AdvancesAndTheTreeReadsClean(t *testing.T) {
	dir := midEdit(t)
	snap, head, err := NewExec().SnapshotCommit(context.Background(), dir, "m")
	require.NoError(t, err)

	require.NoError(t, NewExec().AdvanceBranch(context.Background(), dir, MustParseBranch("main"), snap, head))
	assert.Equal(t, snap, gitIn(t, dir, "rev-parse", "main"))
	assert.Equal(t, "refs/heads/main", gitIn(t, dir, "symbolic-ref", "HEAD"), "HEAD still names the branch")
	assert.Empty(t, gitIn(t, dir, "status", "--porcelain"), "the index matches the new HEAD")
	got, err := os.ReadFile(filepath.Join(dir, "README.md"))
	require.NoError(t, err)
	assert.Equal(t, "seed\nstaged\nunstaged\n", string(got), "the working tree is untouched")
}

// A branch that moved since the snapshot is never forced: ErrRefMoved, and
// the branch, index and tree stay as the human left them.
func TestAdvanceBranch_AMovedBranchIsLeftAlone(t *testing.T) {
	dir := midEdit(t)
	snap, head, err := NewExec().SnapshotCommit(context.Background(), dir, "m")
	require.NoError(t, err)
	human := commit(t, dir, "human.txt", "mine", "the human's commit")
	index := readIndex(t, dir)

	err = NewExec().AdvanceBranch(context.Background(), dir, MustParseBranch("main"), snap, head)
	require.ErrorIs(t, err, ErrRefMoved)
	assert.Equal(t, human, gitIn(t, dir, "rev-parse", "main"), "THE POINT: the human's commit stays the tip")
	assert.Equal(t, index, readIndex(t, dir), "the index is not reset")
}

// An update-ref that fails while the branch is STILL at `from` is that
// failure, not ErrRefMoved: nothing moved, so nothing says the human did.
func TestAdvanceBranch_AFailureWithTheBranchUnmovedIsNotRefMoved(t *testing.T) {
	dir := midEdit(t)
	head := gitIn(t, dir, "rev-parse", "HEAD")
	missing := strings.Repeat("1", len(head))

	err := NewExec().AdvanceBranch(context.Background(), dir, MustParseBranch("main"), missing, head)
	require.Error(t, err, "a target that is no object cannot be advanced to")
	assert.NotErrorIs(t, err, ErrRefMoved)
	assert.Equal(t, head, gitIn(t, dir, "rev-parse", "main"))
}

// A repository with no index file yet still snapshots: git reads HEAD into
// the private index itself, and the snapshot holds HEAD's tree plus the
// working tree's untracked file.
func TestSnapshotCommit_ARepoWithNoIndexFileReadsHead(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}
	dir := initRepo(t)
	head := gitIn(t, dir, "rev-parse", "HEAD")
	tracked := gitIn(t, dir, "ls-tree", "--name-only", head)
	require.NoError(t, os.Remove(filepath.Join(dir, ".git", "index")))
	require.NoError(t, writeFile(filepath.Join(dir, "new.txt"), "new"))

	snap, base, err := NewExec().SnapshotCommit(context.Background(), dir, "m")
	require.NoError(t, err)
	assert.Equal(t, head, base)
	assert.Equal(t, "new", gitIn(t, dir, "show", snap+":new.txt"))
	assert.Contains(t, gitIn(t, dir, "ls-tree", "--name-only", snap), tracked, "HEAD's files are in")
	leftovers, _ := filepath.Glob(filepath.Join(dir, ".git", "ctxloom-snapshot-index-*"))
	assert.Empty(t, leftovers, "the private index is removed")
}

// An index that exists but cannot be read is an error, never an empty
// private index (which would snapshot a tree missing every tracked file).
func TestSnapshotCommit_AnUnreadableIndexIsAnError(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}
	dir := initRepo(t)
	index := filepath.Join(dir, ".git", "index")
	require.NoError(t, os.Remove(index))
	require.NoError(t, os.Mkdir(index, 0o755))

	_, _, err := NewExec().SnapshotCommit(context.Background(), dir, "m")
	require.ErrorContains(t, err, "reading the index")
}

// A parent whose HEAD left the branch meanwhile keeps its index: the branch
// advances, and the checkout now on another branch is not reset against it.
func TestAdvanceBranch_AHeadOnAnotherBranchKeepsItsIndex(t *testing.T) {
	dir := midEdit(t)
	snap, head, err := NewExec().SnapshotCommit(context.Background(), dir, "m")
	require.NoError(t, err)
	gitIn(t, dir, "switch", "-q", "-c", "elsewhere")
	index := readIndex(t, dir)

	require.NoError(t, NewExec().AdvanceBranch(context.Background(), dir, MustParseBranch("main"), snap, head))
	assert.Equal(t, snap, gitIn(t, dir, "rev-parse", "main"))
	assert.Equal(t, index, readIndex(t, dir), "the other branch's index is not reset")
}

// DeleteBranchAt deletes a branch at the expected commit, and nothing else:
// not one that moved, not one a worktree holds.
func TestDeleteBranchAt_DeletesOnlyAnUntouchedFreeBranch(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}
	dir := initRepo(t)
	head := gitIn(t, dir, "rev-parse", "HEAD")
	g := NewExec()
	ctx := context.Background()

	gitIn(t, dir, "branch", "moved")
	other := commit(t, dir, "x.txt", "x", "x")
	gitIn(t, dir, "branch", "-f", "moved", other)
	require.Error(t, g.DeleteBranchAt(ctx, dir, MustParseBranch("moved"), head), "a branch not at the expected commit is kept")
	assert.Equal(t, other, gitIn(t, dir, "rev-parse", "moved"))

	require.Error(t, g.DeleteBranchAt(ctx, dir, MustParseBranch("main"), other), "a checked-out branch is kept")
	assert.Equal(t, other, gitIn(t, dir, "rev-parse", "main"))

	gitIn(t, dir, "branch", "free", head)
	require.NoError(t, g.DeleteBranchAt(ctx, dir, MustParseBranch("free"), head))
	exists, err := g.BranchExists(ctx, dir, MustParseBranch("free"))
	require.NoError(t, err)
	assert.False(t, exists)
}

// The dirty probes run on the parent before its child is up, so they must
// not write its index: `git status` refreshes a stale stat cache and writes
// the index back unless told not to.
func TestDirtyProbes_DoNotWriteTheIndex(t *testing.T) {
	dir := midEdit(t)
	future := time.Now().Add(time.Hour)
	require.NoError(t, os.Chtimes(filepath.Join(dir, ".gitignore"), future, future))
	index := readIndex(t, dir)

	_, err := NewExec().IsDirty(context.Background(), dir)
	require.NoError(t, err)
	_, err = NewExec().WorkingChanges(context.Background(), dir, 0)
	require.NoError(t, err)
	assert.Equal(t, index, readIndex(t, dir))
}

// A repository that signs its commits (commit.gpgSign) gets a signed
// snapshot, as `git commit` would make: commit-tree is asked to sign, and a
// signer that fails refuses the snapshot rather than leaving it unsigned.
func TestSnapshotCommit_SignsWhereTheRepositorySigns(t *testing.T) {
	dir := midEdit(t)
	gitIn(t, dir, "config", "commit.gpgSign", "true")
	gitIn(t, dir, "config", "gpg.format", "openpgp") // a host's global format must not pick the signer
	gitIn(t, dir, "config", "gpg.program", "false")
	_, _, err := NewExec().SnapshotCommit(context.Background(), dir, "m")
	require.Error(t, err, "the snapshot was asked to be signed")
}
