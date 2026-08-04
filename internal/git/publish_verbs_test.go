package git

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The clone / blob-lookup / push / remote-ref verbs the generic-git publisher
// drives. Each is exercised against a REAL repository, because the properties
// that matter are exactly the ones a mock cannot have: what a bare repository
// holds after a push, and whether "the file is not there" is distinguishable
// from "I could not look".

// runGit runs a git command in dir and returns its trimmed stdout, failing the
// test on error. It supplies an identity from the environment for the same
// reason initRepo does: these commands must not depend on the machine's global
// git config.
func runGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=ctxloom", "GIT_AUTHOR_EMAIL=ctxloom@example.com",
		"GIT_COMMITTER_NAME=ctxloom", "GIT_COMMITTER_EMAIL=ctxloom@example.com")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

// bareWithSeed creates a bare repo on branch, seeded with one commit that
// carries seedPath. Returns the bare repo's path.
func bareWithSeed(t *testing.T, branch, seedPath string) string {
	t.Helper()
	root := t.TempDir()
	bare := filepath.Join(root, "origin.git")

	runGit(t, root, "init", "--bare", "-b", branch, bare)
	seed := filepath.Join(root, "seed")
	require.NoError(t, os.MkdirAll(seed, 0o755))
	runGit(t, seed, "init", "-b", branch)
	require.NoError(t, writeFile(filepath.Join(seed, seedPath), "seed body\n"))
	runGit(t, seed, "add", "-A")
	runGit(t, seed, "commit", "-m", "seed")
	runGit(t, seed, "remote", "add", "origin", bare)
	runGit(t, seed, "push", "origin", branch)
	return bare
}

func TestExecGit_CloneCommitPushRoundTrip(t *testing.T) {
	g := NewExec()
	ctx := context.Background()
	bare := bareWithSeed(t, "main", "README.md")

	work := filepath.Join(t.TempDir(), "clone")
	require.NoError(t, g.Clone(ctx, "file://"+bare, work, "main"))

	branch, err := g.CurrentBranch(ctx, work)
	require.NoError(t, err)
	assert.Equal(t, "main", branch)

	require.NoError(t, writeFile(filepath.Join(work, "a", "b.yaml"), "published\n"))
	sha, changed, err := g.CommitAll(ctx, work, "add b")
	require.NoError(t, err)
	require.Contains(t, changed, "a/b.yaml")

	require.NoError(t, g.Push(ctx, work, "origin", "HEAD:refs/heads/main"))

	// The evidence a push happened is what the REMOTE says, not a nil error.
	landed, err := g.RemoteRefSHA(ctx, work, "origin", "refs/heads/main")
	require.NoError(t, err)
	assert.Equal(t, sha, landed)
	assert.Equal(t, sha, runGit(t, bare, "rev-parse", "main"))
	assert.Equal(t, "published", runGit(t, bare, "show", "main:a/b.yaml"))
}

// An empty branch takes the REMOTE's own default. The fixture's default is
// "trunk" so a hardcoded "main"/"master" fails here.
func TestExecGit_CloneWithNoBranchTakesTheRemotesDefault(t *testing.T) {
	g := NewExec()
	ctx := context.Background()
	bare := bareWithSeed(t, "trunk", "README.md")

	work := filepath.Join(t.TempDir(), "clone")
	require.NoError(t, g.Clone(ctx, "file://"+bare, work, ""))

	branch, err := g.CurrentBranch(ctx, work)
	require.NoError(t, err)
	assert.Equal(t, "trunk", branch)
}

// "" means ABSENT and an error means COULD-NOT-ASK. Conflating them turns an
// update into an "Add" and hides whatever is really at the path.
func TestExecGit_FileBlobSHA_SeparatesAbsentFromUnanswerable(t *testing.T) {
	g := NewExec()
	ctx := context.Background()
	dir := initRepo(t)

	present, err := g.FileBlobSHA(ctx, dir, "main", "README.md")
	require.NoError(t, err)
	assert.NotEmpty(t, present)
	assert.Equal(t, runGit(t, dir, "rev-parse", "main:README.md"), present)

	absent, err := g.FileBlobSHA(ctx, dir, "main", "nope/missing.yaml")
	require.NoError(t, err, "a missing path is an ANSWER, not a failure")
	assert.Empty(t, absent)

	_, err = g.FileBlobSHA(ctx, dir, "no-such-branch", "README.md")
	require.Error(t, err, "an unaskable question must not answer 'absent'")
}

// A remote with no such ref is an ANSWER ("") — the case a publisher meets
// when the branch it pushed to genuinely did not appear.
func TestExecGit_RemoteRefSHA_AbsentRefIsEmptyNotAnError(t *testing.T) {
	g := NewExec()
	ctx := context.Background()
	bare := bareWithSeed(t, "main", "README.md")

	work := filepath.Join(t.TempDir(), "clone")
	require.NoError(t, g.Clone(ctx, "file://"+bare, work, "main"))

	got, err := g.RemoteRefSHA(ctx, work, "origin", "refs/heads/nonexistent")
	require.NoError(t, err)
	assert.Empty(t, got)
}

func TestExecGit_CloneOfAMissingRemoteFails(t *testing.T) {
	g := NewExec()
	work := filepath.Join(t.TempDir(), "clone")
	err := g.Clone(context.Background(), "file://"+filepath.Join(t.TempDir(), "nope.git"), work, "")
	require.Error(t, err)
}
