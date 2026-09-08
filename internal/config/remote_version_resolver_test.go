// Tests for the REMOTE half of the bundleVersionResolver seam: a canonical
// "@<commit>" ref must fetch that commit's bundle document out of the local git
// clone cache and turn those exact bytes into a Bundle through the schema
// upgrade pipeline. Its local sibling is covered in local_version_resolver_test.go.
package config

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing/object"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/paths"
	"github.com/ctxloom/ctxloom/internal/testsupport"
)

// remoteContentRepo creates a git repo that PUBLISHES bundles/v2/go-tools (no
// extension: format v2 holds only trees, so its leaf is the bundle's own name)
// the way a publisher does — under the repo-relative bundles prefix, in the
// format root, which is the only place a canonical fetch looks. It commits a
// v1 then a v2 and returns the repo directory plus both commit SHAs.
//
// The document declares its command under the LEGACY `prompts:` key on purpose:
// nothing but bundles.ParseBundle's schema upgrade turns that into a command, so
// a resolver that reached the bytes and unmarshalled them raw would serve a
// bundle with no commands at all — silently, which is the failure this asserts
// against.
func remoteContentRepo(t *testing.T) (repoDir, rev1, rev2 string) {
	t.Helper()
	repoDir = filepath.Join(t.TempDir(), "publisher")
	repo, err := git.PlainInit(repoDir, false)
	require.NoError(t, err)
	wt, err := repo.Worktree()
	require.NoError(t, err)

	rel := filepath.Join(filepath.FromSlash(paths.RepoBundlesPrefixFor(paths.LayoutV2)), "go-tools")
	commit := func(body, msg string) string {
		full := filepath.Join(repoDir, rel)
		require.NoError(t, os.MkdirAll(filepath.Dir(full), 0o755))
		require.NoError(t, os.WriteFile(full, []byte(body), 0o644))
		_, err := wt.Add(filepath.ToSlash(rel))
		require.NoError(t, err)
		h, err := wt.Commit(msg, &git.CommitOptions{
			Author: &object.Signature{Name: "t", Email: "t@t", When: time.Now()},
		})
		require.NoError(t, err)
		return h.String()
	}

	rev1 = commit("description: v1\nfragments:\n  fmt:\n    content: R1-BODY\nprompts:\n  review:\n    content: RP1-BODY\n", "v1")
	rev2 = commit("description: v2\nfragments:\n  fmt:\n    content: R2-BODY\nprompts:\n  review:\n    content: RP2-BODY\n", "v2")
	return repoDir, rev1, rev2
}

// TestRemoteRev_ResolvesHistoricalVersionThroughParse proves the remote arm of
// bundleVersionResolver serves the bytes committed AT THAT COMMIT — a different
// rev is a different body — and that those bytes reached the bundle through
// ParseBundle: the legacy `prompts:` key arrives as a command.
func TestRemoteRev_ResolvesHistoricalVersionThroughParse(t *testing.T) {
	testsupport.Isolate(t)
	repoDir, rev1, rev2 := remoteContentRepo(t)
	appDir := filepath.Join(t.TempDir(), "consumer", ".ctxloom")
	require.NoError(t, os.MkdirAll(appDir, 0o755))

	cfg := &Config{appPaths: []string{appDir}}
	resolve := cfg.bundleVersionResolver()
	require.NotNil(t, resolve, "an app dir must yield a version resolver")

	canonical := "file://" + filepath.ToSlash(repoDir) + "@bundles/go-tools"

	b1, err := resolve(canonical, rev1)
	require.NoError(t, err)
	assert.Equal(t, "R1-BODY", b1.Fragments["fmt"].Content,
		"the pinned rev serves the bytes committed at that rev")
	assert.Equal(t, "RP1-BODY", b1.Commands["review"].Content,
		"the fetched bytes reached the bundle through ParseBundle: a legacy prompts: key arrives as a command")

	b2, err := resolve(canonical, rev2)
	require.NoError(t, err)
	assert.Equal(t, "R2-BODY", b2.Fragments["fmt"].Content, "a different rev is its own version")
	assert.Equal(t, "RP2-BODY", b2.Commands["review"].Content)
}
