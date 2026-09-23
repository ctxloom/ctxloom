package remote

import (
	"context"
	"errors"
	"path"
	"testing"

	"github.com/spf13/afero"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/core/paths"
)

// BundleTreeRoots exists to probe every FORMAT root a bundle's tree may sit
// under. With a single live format (v2) that probe set has exactly one
// candidate; these tests pin that candidate's derivation rather than a
// multi-root overlap, which no longer exists now that format v1 is gone.
//
// The format-v2 segment is read from paths.LayoutV2 and never spelled, so a
// migration that adds a next format moves these tests with it instead of
// leaving them asserting a root nothing writes.

// treeFileRef is the reference path the tree probe derives its candidate
// from, and treeBundleName the bare bundle name underneath the format root.
const (
	treeFileRef    = ".ctxloom/content/bundles/atelier"
	treeBundleName = "atelier"
)

func formatV2TreeRoot() string {
	return path.Join(paths.RepoBundlesPrefixFor(paths.LayoutV2), treeBundleName)
}

// manifestTree is the smallest thing a fetched root can return that counts as a
// bundle: a manifest with bytes in it.
func manifestTree(marker string) map[string]TreeFile {
	return map[string]TreeFile{BundleManifestName: {Data: []byte("name: atelier\nmarker: " + marker + "\n")}}
}

// treeAt answers only for one root, so which root the production code asked for
// is the ONLY thing that can decide whether the fetch succeeds.
func treeAt(want map[string]map[string]TreeFile, seen *[]string) TreeFetchFunc {
	return func(_ context.Context, _ Fetcher, _, _, root, _, _ string) (map[string]TreeFile, error) {
		if seen != nil {
			*seen = append(*seen, root)
		}
		if tree, ok := want[root]; ok {
			return tree, nil
		}
		return nil, errors.New("no tree at " + root)
	}
}

// TestBundleTreeRoots_NamesExactlyTheFormatV2Root pins the candidate set: with
// one live format there is exactly one candidate, and it is the format-v2 root.
func TestBundleTreeRoots_NamesExactlyTheFormatV2Root(t *testing.T) {
	roots := BundleTreeRoots(treeFileRef)

	require.Len(t, roots, 1, "a bundle has exactly one possible location per live format")
	assert.Equal(t, formatV2TreeRoot(), roots[0])
}

// TestBundleTreeRoots_ComposesCandidatesInThePathsOwnPrefixFamily: a reference
// to a LOCAL bundle is built relative to the already-open content root, and a
// candidate composed in the repo family would name a path that does not exist
// for such a reader.
func TestBundleTreeRoots_ComposesCandidatesInThePathsOwnPrefixFamily(t *testing.T) {
	contentPath := path.Join(paths.ContentBundlesPrefixFor(paths.LayoutV2), treeBundleName)

	roots := BundleTreeRoots(contentPath)

	require.Len(t, roots, 1)
	assert.Equal(t, path.Join(paths.ContentBundlesPrefixFor(paths.LayoutV2), treeBundleName), roots[0])
	for _, root := range roots {
		assert.NotContains(t, root, paths.RepoContentPrefix,
			"a content-root-relative path must not gain the repo prefix its reader has already resolved")
	}
}

// TestBundleTreeRoots_ReducesAPathThatAlreadyCarriesAFormatSegment: the name is
// taken relative to the root that contains every format's subtree, so a path
// already under a format root yields the same bare name rather than one that
// composes the segment twice.
func TestBundleTreeRoots_ReducesAPathThatAlreadyCarriesAFormatSegment(t *testing.T) {
	v2Path := path.Join(paths.RepoBundlesPrefixFor(paths.LayoutV2), treeBundleName)

	assert.Equal(t, BundleTreeRoots(treeFileRef), BundleTreeRoots(v2Path),
		"the same bundle must yield the same candidate roots however its path was spelled")
}

// TestProbeBundleTreeRoots_ReportsTheRootsFailure. Quoting the root that was
// tried is the difference between a diagnosable publisher mistake and a bundle
// that resolves to nothing.
func TestProbeBundleTreeRoots_ReportsTheRootsFailure(t *testing.T) {
	failure := errors.New("tree at the format root is too large")
	var tried []string

	_, root, err := ProbeBundleTreeRoots(treeFileRef, func(root string) (map[string]TreeFile, error) {
		tried = append(tried, root)
		return nil, failure
	})

	require.Error(t, err)
	assert.ErrorIs(t, err, failure, "the real failure at the format root was swallowed")
	assert.Equal(t, formatV2TreeRoot(), root, "with nothing found, the root named is the one that was tried")
	assert.Equal(t, BundleTreeRoots(treeFileRef), tried, "every candidate must be probed before the probe gives up")
}

// TestFetchItemBytes_ResolvesATreeAtTheFormatV2Root is the ordinary case: a
// tree published under the one live format root must pull.
func TestFetchItemBytes_ResolvesATreeAtTheFormatV2Root(t *testing.T) {
	var seen []string
	p := treePuller(t, afero.NewMemMapFs(), ".ctxloom",
		treeAt(map[string]map[string]TreeFile{formatV2TreeRoot(): manifestTree("current")}, &seen))

	content, tree, treeRoot, err := p.fetchItemBytes(t.Context(), NewMockFetcher(), "trent", "atelier",
		"https://github.com/trent/atelier", treeRef(t), treeFileRef, treeTestSHA)

	require.NoError(t, err)
	assert.Contains(t, string(content), "marker: current", "the manifest of the tree at the format root must be what pulls")
	assert.NotEmpty(t, tree)
	assert.Equal(t, formatV2TreeRoot(), treeRoot)
	assert.Contains(t, seen, formatV2TreeRoot(), "the format-v2 root was never probed")
}
