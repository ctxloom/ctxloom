// Tests for ctxloom:local version pinning at the resolver seam: the
// bundleVersionResolver wired by SeededBundleLoader must dispatch a local
// "@<rev>" ref to the PROJECT's own git history (read the .ctxloom/content/
// bundle bytes as of that revision), while an unversioned local ref keeps resolving to
// the working-tree default and a fail-closed case (unknown rev, non-git project)
// withholds just that item. These exercise the exact loader methods profile
// assembly calls (GetFragmentAtVersion / GetPromptAtVersion), so they cover a
// profile pinning a local fragment/prompt to a historical project revision.
package config

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing/object"
	"github.com/stretchr/testify/assert"
	"github.com/spf13/afero"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/bundles"
	"github.com/ctxloom/ctxloom/internal/remote"
	"github.com/ctxloom/ctxloom/internal/testsupport"
)

const localGoTools = "ctxloom:local@bundles/go-tools"

// localContentRepo creates a git repo whose committed .ctxloom/content/ tree
// holds bundles/go-tools.yaml. It commits a v1 (fmt=V1-BODY, review=PV1-BODY),
// then a v2, and returns the appDir (<repo>/.ctxloom) plus the two commit SHAs.
func localContentRepo(t *testing.T) (appDir, rev1, rev2 string) {
	t.Helper()
	repoDir := filepath.Join(t.TempDir(), "project")
	repo, err := git.PlainInit(repoDir, false)
	require.NoError(t, err)
	wt, err := repo.Worktree()
	require.NoError(t, err)

	rel := filepath.Join(".ctxloom", "content", "bundles", "go-tools.yaml")
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

	rev1 = commit("description: v1\nfragments:\n  fmt:\n    content: V1-BODY\nprompts:\n  review:\n    content: PV1-BODY\n", "v1")
	rev2 = commit("description: v2\nfragments:\n  fmt:\n    content: V2-BODY\nprompts:\n  review:\n    content: PV2-BODY\n", "v2")
	return filepath.Join(repoDir, ".ctxloom"), rev1, rev2
}

// localResolverLoader wires a real bundleVersionResolver over appDir into a loader,
// seeding a version-less working-tree default so the unversioned path has a bundle
// to resolve (mirroring how assembly sees today's working copy).
func localResolverLoader(t *testing.T, appDir string) *bundles.Pipeline {
	t.Helper()
	cfg := &Config{appPaths: []string{appDir}}
	resolver := cfg.bundleVersionResolver()
	require.NotNil(t, resolver, "an app dir must yield a version resolver")
	// The working-tree default is read as what it is: a project bundle on a
	// filesystem, through the project reader.
	fsys := afero.NewMemMapFs()
	require.NoError(t, afero.WriteFile(fsys, "/bundles/go-tools.yaml", []byte(
		"version: \"1.0\"\nfragments:\n  fmt:\n    content: WORKTREE-BODY\ncommands:\n  review:\n    content: WORKTREE-PROMPT\n"), 0o644))
	loader := bundles.NewLoader(bundles.NewProjectReader(fsys, []string{"/bundles"})).WithVersionResolver(resolver)
	return bundles.NewPipeline(loader, nil, false)
}

// TestLocalRev_FragmentResolvesHistoricalVersion proves a local fragment ref
// pinned to a project revision assembles that revision's bytes from the project
// git history — distinct from both the newer commit and the working-tree default.
func TestLocalRev_FragmentResolvesHistoricalVersion(t *testing.T) {
	testsupport.Isolate(t)
	appDir, rev1, rev2 := localContentRepo(t)
	loader := localResolverLoader(t, appDir)

	v1, err := loader.GetFragmentAtVersion(localGoTools+"#fragments/fmt", rev1)
	require.NoError(t, err)
	assert.Equal(t, "V1-BODY", v1.Content, "the pinned rev assembles the historical version")

	v2, err := loader.GetFragmentAtVersion(localGoTools+"#fragments/fmt", rev2)
	require.NoError(t, err)
	assert.Equal(t, "V2-BODY", v2.Content, "a different rev is its own version")
}

// TestLocalRev_UnversionedRefUnchanged proves an unversioned local ref keeps
// resolving to the working-tree default (the seeded bundle) — the resolver is
// only consulted for an explicit "@<rev>".
func TestLocalRev_UnversionedRefUnchanged(t *testing.T) {
	testsupport.Isolate(t)
	appDir, _, _ := localContentRepo(t)
	loader := localResolverLoader(t, appDir)

	lc, err := loader.GetFragmentAtVersion(localGoTools+"#fragments/fmt", "")
	require.NoError(t, err)
	assert.Equal(t, "WORKTREE-BODY", lc.Content, "an unversioned local ref uses today's working-tree path")
}

// TestLocalRev_PromptViaNameAddressedPath proves the name-addressed prompt form
// ("<ref>#prompts/<name>@<rev>") splits to the version-less ref + rev and resolves
// the historical prompt from the project git history — the same routing
// operations.GetPrompt uses.
func TestLocalRev_PromptViaNameAddressedPath(t *testing.T) {
	testsupport.Isolate(t)
	appDir, rev1, _ := localContentRepo(t)
	loader := localResolverLoader(t, appDir)

	ref, version := remote.SplitPromptVersion(localGoTools + "#commands/review@" + rev1)
	require.Equal(t, rev1, version, "the name-addressed @rev is split off")
	lc, err := loader.GetPromptAtVersion(ref, version)
	require.NoError(t, err)
	assert.Equal(t, "PV1-BODY", lc.Content, "the pinned local prompt resolves its historical version")
}

// TestLocalRev_UnknownRevFailsClosed proves an unknown rev withholds ONLY that
// item (errors), while an unversioned sibling still resolves — assembly continues
// for the rest.
func TestLocalRev_UnknownRevFailsClosed(t *testing.T) {
	testsupport.Isolate(t)
	appDir, _, _ := localContentRepo(t)
	loader := localResolverLoader(t, appDir)

	_, err := loader.GetFragmentAtVersion(localGoTools+"#fragments/fmt", "deadbeefdeadbeefdeadbeefdeadbeefdeadbeef")
	require.Error(t, err, "an unknown local rev fails closed, never serving the working tree")

	// The rest still assembles: an unversioned ref is unaffected.
	lc, err := loader.GetFragmentAtVersion(localGoTools+"#fragments/fmt", "")
	require.NoError(t, err)
	assert.Equal(t, "WORKTREE-BODY", lc.Content)
}

// TestLocalRev_NonGitProjectFailsClosed proves a local "@<rev>" against a project
// that is NOT a git repo withholds that item (errors), while the unversioned path
// keeps working — fault-tolerant + fail-closed.
func TestLocalRev_NonGitProjectFailsClosed(t *testing.T) {
	testsupport.Isolate(t)
	// An app dir NOT under any git repository.
	appDir := filepath.Join(t.TempDir(), "loose", ".ctxloom")
	require.NoError(t, os.MkdirAll(filepath.Join(appDir, "content", "bundles"), 0o755))
	loader := localResolverLoader(t, appDir)

	_, err := loader.GetFragmentAtVersion(localGoTools+"#fragments/fmt", "abc123")
	require.Error(t, err, "a non-git project cannot serve a pinned version → withheld")

	lc, err := loader.GetFragmentAtVersion(localGoTools+"#fragments/fmt", "")
	require.NoError(t, err, "the unversioned path is unaffected by the missing history")
	assert.Equal(t, "WORKTREE-BODY", lc.Content)
}
