package remote

import (
	"context"
	"errors"
	"path"
	"testing"

	"github.com/spf13/afero"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/paths"
)

// A bundles root's `v` segment is the FORMAT VERSION, so a tree may sit under
// the format-v2 root while every tree published to date sits under the one the
// single-file path derives. What these tests pin is that resolution considers
// BOTH — and the trap they are shaped around is that a test seeding only the
// CURRENT location cannot observe the new probe at all, which is exactly how an
// identical defect survived in the listing code until a mutation exposed it.
//
// The format-v2 segment is read from paths.LayoutV2 and never spelled, so a
// migration that renames it moves these tests with it instead of leaving them
// asserting a root nothing writes.

// treeFileRef is the single-file path the tree probe derives its candidates
// from, and treeBundleName the bare bundle name underneath every format root.
const (
	treeFileRef    = ".ctxloom/content/bundles/atelier.yaml"
	treeBundleName = "atelier"
)

func currentTreeRoot() string {
	return path.Join(paths.RepoBundlesPrefixFor(paths.LayoutV1), treeBundleName)
}

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

// TestBundleTreeRoots_NamesTheFormatV2RootAndTheCurrentOne pins the candidate
// set itself. Both roots must be named and they must be DISTINCT: a probe whose
// two candidates collapse to one place probes one place while reporting that it
// covered the migration.
func TestBundleTreeRoots_NamesTheFormatV2RootAndTheCurrentOne(t *testing.T) {
	roots := BundleTreeRoots(treeFileRef)

	require.Len(t, roots, 2, "a bundle has exactly one possible location per live format")
	assert.Equal(t, formatV2TreeRoot(), roots[0], "the newest format is probed first, so a migrated tree wins during the overlap")
	assert.Equal(t, currentTreeRoot(), roots[1])
	assert.NotEqual(t, roots[0], roots[1], "two candidates naming one directory probe a single root")
}

// TestBundleTreeRoots_ComposesCandidatesInThePathsOwnPrefixFamily: a reference
// to a LOCAL bundle is built relative to the already-open content root, and a
// candidate composed in the repo family would name a path that does not exist
// for such a reader.
func TestBundleTreeRoots_ComposesCandidatesInThePathsOwnPrefixFamily(t *testing.T) {
	contentPath := path.Join(paths.ContentBundlesPrefixFor(paths.LayoutV1), treeBundleName+".yaml")

	roots := BundleTreeRoots(contentPath)

	require.Len(t, roots, 2)
	assert.Equal(t, path.Join(paths.ContentBundlesPrefixFor(paths.LayoutV2), treeBundleName), roots[0])
	assert.Equal(t, path.Join(paths.ContentBundlesPrefixFor(paths.LayoutV1), treeBundleName), roots[1])
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
	v2Path := path.Join(paths.RepoBundlesPrefixFor(paths.LayoutV2), treeBundleName+".yaml")

	assert.Equal(t, BundleTreeRoots(treeFileRef), BundleTreeRoots(v2Path),
		"the same bundle must yield the same candidate roots however its path was spelled")
}

// TestProbeBundleTreeRoots_ReportsEveryRootsFailure. Quoting one root tells a
// publisher mid-migration that their bundle is missing from a place they have
// already left, and quoting only the last hides a tree that EXISTS but cannot
// be read.
func TestProbeBundleTreeRoots_ReportsEveryRootsFailure(t *testing.T) {
	broken := errors.New("tree at the migrated root is too large")
	absent := errors.New("nothing here")
	var tried []string

	_, root, err := ProbeBundleTreeRoots(treeFileRef, func(root string) (map[string]TreeFile, error) {
		tried = append(tried, root)
		if root == formatV2TreeRoot() {
			return nil, broken
		}
		return nil, absent
	})

	require.Error(t, err)
	assert.ErrorIs(t, err, broken, "a real failure at the migrated root was swallowed")
	assert.ErrorIs(t, err, absent)
	assert.Equal(t, currentTreeRoot(), root, "with nothing found, the root named is where every tree is today")
	assert.Equal(t, BundleTreeRoots(treeFileRef), tried, "every candidate must be probed before the probe gives up")
}

// TestFetchItemBytes_StillResolvesATreeAtTheCurrentLocation is the NEUTRALITY
// assertion: nothing is published under the format-v2 root yet, so probing it
// must leave today's resolution exactly as it was.
func TestFetchItemBytes_StillResolvesATreeAtTheCurrentLocation(t *testing.T) {
	var seen []string
	p := treePuller(t, afero.NewMemMapFs(), ".ctxloom",
		treeAt(map[string]map[string]TreeFile{currentTreeRoot(): manifestTree("current")}, &seen))

	content, tree, treeRoot, err := p.fetchItemBytes(t.Context(), NewMockFetcher(), "trent", "atelier",
		"https://github.com/trent/atelier", treeRef(t), treeFileRef, treeTestSHA, PullOptions{ItemType: ItemTypeBundle})

	require.NoError(t, err)
	assert.Contains(t, string(content), "marker: current", "the manifest of the tree at today's location must be what pulls")
	assert.NotEmpty(t, tree)
	assert.Equal(t, currentTreeRoot(), treeRoot)
	assert.Contains(t, seen, formatV2TreeRoot(), "the format-v2 root was never probed")
}

// TestFetchItemBytes_ResolvesATreePublishedUnderTheFormatV2Root is the
// READINESS assertion: a tree seeded ONLY under the format-v2 root must pull.
// Before the probe covered both roots this fetch failed outright — the measured
// symptom being a published directory-form bundle that reached no consumer.
func TestFetchItemBytes_ResolvesATreePublishedUnderTheFormatV2Root(t *testing.T) {
	p := treePuller(t, afero.NewMemMapFs(), ".ctxloom",
		treeAt(map[string]map[string]TreeFile{formatV2TreeRoot(): manifestTree("migrated")}, nil))

	content, tree, treeRoot, err := p.fetchItemBytes(t.Context(), NewMockFetcher(), "trent", "atelier",
		"https://github.com/trent/atelier", treeRef(t), treeFileRef, treeTestSHA, PullOptions{ItemType: ItemTypeBundle})

	require.NoError(t, err)
	assert.Contains(t, string(content), "marker: migrated")
	assert.NotEmpty(t, tree)
	assert.Equal(t, formatV2TreeRoot(), treeRoot, "the root reported must be the one that actually answered")
}

// TestFetchItemBytes_PrefersTheFormatV2RootWhileBothAreLive: during an overlap
// the migrated copy is the one the publisher means, and answering from the
// older root would keep serving the copy the migration is retiring.
func TestFetchItemBytes_PrefersTheFormatV2RootWhileBothAreLive(t *testing.T) {
	p := treePuller(t, afero.NewMemMapFs(), ".ctxloom", treeAt(map[string]map[string]TreeFile{
		formatV2TreeRoot(): manifestTree("migrated"),
		currentTreeRoot():  manifestTree("current"),
	}, nil))

	content, _, treeRoot, err := p.fetchItemBytes(t.Context(), NewMockFetcher(), "trent", "atelier",
		"https://github.com/trent/atelier", treeRef(t), treeFileRef, treeTestSHA, PullOptions{ItemType: ItemTypeBundle})

	require.NoError(t, err)
	assert.Contains(t, string(content), "marker: migrated")
	assert.Equal(t, formatV2TreeRoot(), treeRoot)
}
