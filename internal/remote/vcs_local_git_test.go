// Tests for the working-copy VCS backend behind ctxloom:local version pinning:
// LocalGitVCSFactory opens the project repo enclosing the committed local-content
// tree and reads an item's bytes as of a project revision (git show <rev>:<path>).
// They prove the historical bytes are distinct from the working tree, that a
// versionless read serves the working copy, and that the fail-closed paths
// (unknown rev, non-git project) error rather than serving a wrong version.
package remote

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing/object"
	"github.com/spf13/afero"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// localProjectRepo creates a git repo whose committed .ctxloom/local/ tree holds
// bundles/go-tools.yaml. It commits "v1", then rewrites + commits "v2", and
// returns the repo dir plus the two commit SHAs. The repo IS the project — its
// own history is the version space for ctxloom:local refs.
func localProjectRepo(t *testing.T) (repoDir, rev1, rev2 string) {
	t.Helper()
	repoDir = filepath.Join(t.TempDir(), "project")
	repo, err := git.PlainInit(repoDir, false)
	require.NoError(t, err)
	wt, err := repo.Worktree()
	require.NoError(t, err)

	rel := filepath.Join(".ctxloom", "local", "bundles", "go-tools.yaml")
	write := func(body string) {
		full := filepath.Join(repoDir, rel)
		require.NoError(t, os.MkdirAll(filepath.Dir(full), 0o755))
		require.NoError(t, os.WriteFile(full, []byte(body), 0o644))
		_, err := wt.Add(filepath.ToSlash(rel))
		require.NoError(t, err)
	}
	commit := func(msg string) string {
		h, err := wt.Commit(msg, &git.CommitOptions{
			Author: &object.Signature{Name: "t", Email: "t@t", When: time.Now()},
		})
		require.NoError(t, err)
		return h.String()
	}

	write("description: tools v1\nfragments:\n  fmt:\n    content: V1-BODY\n")
	rev1 = commit("v1")
	write("description: tools v2\nfragments:\n  fmt:\n    content: V2-BODY\n")
	rev2 = commit("v2")
	return repoDir, rev1, rev2
}

func localFetcher(repoDir string) (*LocalRefFetcher, *Reference) {
	root := filepath.Join(repoDir, ".ctxloom", "local")
	f := NewLocalRefFetcher(LocalGitVCSFactory(afero.NewOsFs()), root)
	ref, _ := ParseReference("ctxloom:local@bundles/go-tools")
	return f, ref
}

func TestLocalGitVCS_FetchItem_HistoricalRev(t *testing.T) {
	repoDir, rev1, rev2 := localProjectRepo(t)
	f, ref := localFetcher(repoDir)

	// Pinning rev1 reads the project history, NOT the working tree (which is v2).
	v1, err := f.FetchItem(context.Background(), ref, rev1)
	require.NoError(t, err)
	assert.Contains(t, string(v1), "V1-BODY")
	assert.NotContains(t, string(v1), "V2-BODY", "the pinned rev must not leak the newer version")

	// rev2 is its own version.
	v2, err := f.FetchItem(context.Background(), ref, rev2)
	require.NoError(t, err)
	assert.Contains(t, string(v2), "V2-BODY")
}

func TestLocalGitVCS_FetchItem_VersionlessReadsWorkingCopy(t *testing.T) {
	repoDir, _, _ := localProjectRepo(t)
	f, ref := localFetcher(repoDir)

	// Overwrite the working copy WITHOUT committing — a versionless read serves
	// exactly the current bytes on disk, unaffected by history.
	full := filepath.Join(repoDir, ".ctxloom", "local", "bundles", "go-tools.yaml")
	require.NoError(t, os.WriteFile(full, []byte("fragments:\n  fmt:\n    content: WORKTREE-ONLY\n"), 0o644))

	data, err := f.FetchItem(context.Background(), ref, "")
	require.NoError(t, err)
	assert.Contains(t, string(data), "WORKTREE-ONLY")
}

func TestLocalGitVCS_FetchItem_UnknownRevFailsClosed(t *testing.T) {
	repoDir, _, _ := localProjectRepo(t)
	f, ref := localFetcher(repoDir)

	_, err := f.FetchItem(context.Background(), ref, "deadbeefdeadbeefdeadbeefdeadbeefdeadbeef")
	require.Error(t, err, "an unknown rev must fail closed, never fall back to the working tree")
}

func TestLocalGitVCS_FetchItem_PathAbsentAtRevFailsClosed(t *testing.T) {
	repoDir, rev1, _ := localProjectRepo(t)
	root := filepath.Join(repoDir, ".ctxloom", "local")
	f := NewLocalRefFetcher(LocalGitVCSFactory(afero.NewOsFs()), root)
	// A bundle that does not exist at rev1.
	ref, _ := ParseReference("ctxloom:local@bundles/missing")

	_, err := f.FetchItem(context.Background(), ref, rev1)
	require.Error(t, err, "a path absent at the rev must fail closed")
}

func TestLocalGitVCS_FetchItem_NonGitProjectFailsClosedOnPin(t *testing.T) {
	// A local-content root NOT under any git repository.
	root := filepath.Join(t.TempDir(), "loose", ".ctxloom", "local")
	require.NoError(t, os.MkdirAll(filepath.Join(root, "bundles"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(root, "bundles", "go-tools.yaml"),
		[]byte("fragments:\n  fmt:\n    content: LOOSE\n"), 0o644))

	f := NewLocalRefFetcher(LocalGitVCSFactory(afero.NewOsFs()), root)
	ref, _ := ParseReference("ctxloom:local@bundles/go-tools")

	// A versionless read still works (current-only fsVCS fallback)...
	data, err := f.FetchItem(context.Background(), ref, "")
	require.NoError(t, err)
	assert.Contains(t, string(data), "LOOSE")

	// ...but a pinned read of a non-git project fails closed (no Versioned backend).
	_, err = f.FetchItem(context.Background(), ref, "abc123")
	require.Error(t, err, "a non-git project cannot serve a pinned version → withheld")
}

// TestLocalGitVCS_FetchItem_NonGitProjectErrorNamesCause pins that
// LocalGitVCSFactory used to discard the git.PlainOpenWithOptions error when
// degrading to the current-only fsVCS, so a later pinned read's error named
// only the generic "does not support revisions" message — never the actual
// cause (e.g. no .git found at all, an unreadable .git, or a bare/worktree-less
// repo). The degradation cause must be carried through and surfaced.
func TestLocalGitVCS_FetchItem_NonGitProjectErrorNamesCause(t *testing.T) {
	root := filepath.Join(t.TempDir(), "loose", ".ctxloom", "local")
	require.NoError(t, os.MkdirAll(filepath.Join(root, "bundles"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(root, "bundles", "go-tools.yaml"),
		[]byte("fragments:\n  fmt:\n    content: LOOSE\n"), 0o644))

	f := NewLocalRefFetcher(LocalGitVCSFactory(afero.NewOsFs()), root)
	ref, _ := ParseReference("ctxloom:local@bundles/go-tools")

	_, err := f.FetchItem(context.Background(), ref, "abc123")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "repository does not exist",
		"the degradation's real cause (git.PlainOpenWithOptions' error) must be named, not just the generic 'does not support revisions' message")
}
