package operations

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/content/remotetree"
	"github.com/ctxloom/ctxloom/internal/remote"
)

// One pulled directory-form bundle, as the pinned clone holds it.
const (
	treeProbeCanonical = "https://github.com/trent/atelier@bundles/atelier"
	treeProbeRepoURL   = "https://github.com/trent/atelier"
	treeProbeRoot      = ".ctxloom/content/bundles/atelier"
)

// treeProbeSHA is a FULL 40-hex commit sha because remotetree refuses anything
// else outright — a short or symbolic ref is not a pin.
var treeProbeSHA = strings.Repeat("a", 40)

// stagedTreeFetcher returns a factory over a mock clone holding one
// directory-form bundle: a manifest and one skill package, the shape that
// cannot be published any other way.
func stagedTreeFetcher() remote.FetcherFactory {
	m := remote.NewMockFetcher()
	m.Dirs[treeProbeRoot] = []remote.DirEntry{
		{Name: "bundle.yaml"},
		{Name: "skills", IsDir: true},
	}
	m.Dirs[treeProbeRoot+"/skills"] = []remote.DirEntry{{Name: "good-night", IsDir: true}}
	m.Dirs[treeProbeRoot+"/skills/good-night"] = []remote.DirEntry{{Name: "SKILL.md"}}
	m.Files[treeProbeRoot+"/bundle.yaml"] = []byte("version: 1.0.0\ndescription: atelier\n")
	m.Files[treeProbeRoot+"/skills/good-night/SKILL.md"] = []byte("---\nname: good-night\ndescription: d\n---\n\nbody\n")

	return func(string, remote.AuthConfig) (remote.Fetcher, error) { return m, nil }
}

func treeProbeLock() *remote.Lockfile {
	return &remote.Lockfile{Bundles: map[string]remote.LockEntry{
		treeProbeCanonical: {SHA: treeProbeSHA, URL: treeProbeRepoURL, Tree: true},
	}}
}

// isInstalled decides installedness by READING a bundle's bytes, so a form the
// reader cannot read is a form it can never call installed. Before the reader
// had a tree surface, a fully pulled skill bundle reported as missing on every
// probe — CheckMissingDependencies planned to install what was already there,
// and every sync re-pulled it, forever.
//
// The two arms differ ONLY in whether the tree walker is wired, which is the
// whole of the fix.
func TestIsInstalled_APulledTreeBundleIsInstalled(t *testing.T) {
	factory := stagedTreeFetcher()

	wired := remote.NewBundleReader(nil, factory, remote.AuthConfig{}, treeProbeLock(),
		remote.WithReaderTreeFetcher(remotetree.PullTreeFetcher))
	assert.True(t, isInstalled(t.Context(), treeProbeCanonical, wired),
		"a directory-form bundle present at its pinned sha is installed")

	unwired := remote.NewBundleReader(nil, factory, remote.AuthConfig{}, treeProbeLock())
	assert.False(t, isInstalled(t.Context(), treeProbeCanonical, unwired),
		"without the tree surface the same bundle cannot be read, so it cannot be called installed")
}

// The production wiring, end to end: the composition point every sync probe
// goes through must hand back a source that actually serves the tree's manifest
// bytes. Asserting the BYTES (not merely that the read succeeded) is what
// catches a seam wired to something that returns empty.
func TestBundleReaderTreeSeam_ServesTheManifestBytes(t *testing.T) {
	reader := remote.NewCachingBundleReader(
		remote.NewBundleReader(nil, stagedTreeFetcher(), remote.AuthConfig{}, treeProbeLock(),
			remote.WithReaderTreeFetcher(remotetree.PullTreeFetcher)),
	)

	data, err := reader.ReadBundleBytes(t.Context(), treeProbeCanonical)

	require.NoError(t, err)
	assert.Equal(t, "version: 1.0.0\ndescription: atelier\n", string(data),
		"the tree's bundle.yaml, served through the same call a single-file bundle uses")
}
