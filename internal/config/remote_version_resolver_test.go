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

// remoteTreeContentRepo is remoteContentRepo's DIRECTORY-form twin: it publishes
// bundles/v2/go-tools as a TREE whose bundle.yaml carries the manifest, which is
// the only shape a publisher can produce since the v1 single-file format was
// removed. It commits a v1 then a v2 and returns the repo directory plus both
// commit SHAs.
func remoteTreeContentRepo(t *testing.T) (repoDir, rev1, rev2 string) {
	t.Helper()
	repoDir = filepath.Join(t.TempDir(), "tree-publisher")
	repo, err := git.PlainInit(repoDir, false)
	require.NoError(t, err)
	wt, err := repo.Worktree()
	require.NoError(t, err)

	bundleDir := filepath.Join(filepath.FromSlash(paths.RepoBundlesPrefixFor(paths.LayoutV2)), "go-tools")
	// A PUBLISHABLE tree: bundle.yaml carries envelope keys only and each item
	// is a file beside it. An inline `fragments:` key here is the one shape
	// bundles.readEnvelope refuses outright, so a fixture carrying one asserts
	// against a bundle no publisher can publish.
	commit := func(files map[string]string, msg string) string {
		for rel, body := range files {
			full := filepath.Join(repoDir, filepath.FromSlash(rel))
			require.NoError(t, os.MkdirAll(filepath.Dir(full), 0o755))
			require.NoError(t, os.WriteFile(full, []byte(body), 0o644))
			_, err := wt.Add(filepath.ToSlash(rel))
			require.NoError(t, err)
		}
		h, err := wt.Commit(msg, &git.CommitOptions{
			Author: &object.Signature{Name: "t", Email: "t@t", When: time.Now()},
		})
		require.NoError(t, err)
		return h.String()
	}

	manifest := filepath.Join(bundleDir, paths.BundleManifestName)
	fragment := filepath.Join(bundleDir, "fragments", "fmt.md")
	rev1 = commit(map[string]string{manifest: "description: v1\n", fragment: "T1-BODY"}, "v1")
	rev2 = commit(map[string]string{manifest: "description: v2\n", fragment: "T2-BODY"}, "v2")
	return repoDir, rev1, rev2
}

// TestRemoteRev_ResolvesHistoricalVersionOfATreeBundle pins the capability
// childlike-failing named as dead: resolving a version constraint against a
// DIRECTORY-form bundle at an arbitrary historical commit. remote.FetchRefBytes
// builds a single file path from the ref, and since the v1 removal that file
// does not exist for any published bundle — without a tree fallback this fails
// closed and the caller withholds the item, so no tree bundle can carry a
// version constraint at all.
func TestRemoteRev_ResolvesHistoricalVersionOfATreeBundle(t *testing.T) {
	testsupport.Isolate(t)
	repoDir, rev1, rev2 := remoteTreeContentRepo(t)
	appDir := filepath.Join(t.TempDir(), "consumer", ".ctxloom")
	require.NoError(t, os.MkdirAll(appDir, 0o755))

	cfg := &Config{appPaths: []string{appDir}}
	resolve := cfg.bundleVersionResolver()
	require.NotNil(t, resolve, "an app dir must yield a version resolver")

	canonical := "file://" + filepath.ToSlash(repoDir) + "@bundles/go-tools"

	b1, err := resolve(canonical, rev1)
	require.NoError(t, err, "a directory-form bundle must resolve at a historical commit")
	assert.Equal(t, "T1-BODY", b1.Fragments["fmt"].Content,
		"the pinned rev serves the bytes committed at that rev, read out of the tree's bundle.yaml")

	b2, err := resolve(canonical, rev2)
	require.NoError(t, err)
	assert.Equal(t, "T2-BODY", b2.Fragments["fmt"].Content, "a different rev is its own version")
}
