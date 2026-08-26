package operations

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/config"
	"github.com/ctxloom/ctxloom/internal/content/remotetree"
	"github.com/ctxloom/ctxloom/internal/remote"
	"github.com/ctxloom/ctxloom/internal/testsupport"
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

// The PRODUCTION composition point must carry the tree surface. Everything
// above builds a reader by hand, which proves the seam works and says nothing
// about whether the reader every sync probe actually uses was given one.
//
// ErrTreeBundleUnreadable is precisely what a reader with NO tree surface
// answers for a directory-form entry, so its absence here discriminates the
// wiring and nothing else. The read still fails — there is no clone behind this
// fixture — but it fails for a transport reason, one step past the refusal.
func TestNewBundleReaderForConfig_CarriesTheTreeReadSurface(t *testing.T) {
	appDir := filepath.Join(testsupport.ProjectDir(t), ".ctxloom")
	require.NoError(t, os.MkdirAll(appDir, 0755))

	lm := remote.NewLockfileManager(appDir)
	require.NoError(t, lm.Save(treeProbeLock()))
	// Prove the fixture really records a DIRECTORY-form entry through the
	// code's own loader; a lockfile that lost the flag would make the
	// assertion below pass for the wrong reason.
	reloaded, err := lm.Load()
	require.NoError(t, err)
	require.True(t, reloaded.Bundles[treeProbeCanonical].Tree, "fixture is not directory-form")

	reader := NewBundleReaderForConfig(config.NewFixture(config.Fixture{AppPaths: []string{appDir}}))
	require.NotNil(t, reader)

	_, readErr := reader.ReadBundleBytes(t.Context(), treeProbeCanonical)

	require.Error(t, readErr, "there is no clone behind this fixture")
	assert.NotErrorIs(t, readErr, remote.ErrTreeBundleUnreadable,
		"the probe's own reader must not refuse directory-form bundles outright")
}
