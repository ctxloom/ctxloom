package operations

import (
	"testing"

	"github.com/spf13/afero"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/adapters/remote"
	"github.com/ctxloom/ctxloom/internal/core/config"
	"github.com/ctxloom/ctxloom/internal/core/paths"
	"github.com/ctxloom/ctxloom/internal/testsupport/bundletree"
)

// Remote bundles the closure fixtures below reach, each pre-pinned so the
// identity resolver newTestWalker installs pins it at its own version.
const (
	closureDevBundle  = "https://github.com/o/r@bundles/devbundle@h1"
	closureBaseBundle = "https://github.com/o/r@bundles/basebundle@h2"
	closureTeamBundle = "https://github.com/o/r@bundles/teambundle@h3"
)

// closureFixture is a project with two locally authored profiles — "dev",
// which names its parent "base" by bare name — and a second LOCAL bundle,
// "team", shipping a profile "p" that nothing composes.
func closureFixture(t *testing.T) *config.Config {
	t.Helper()
	const appDir = "/proj/" + paths.AppDirName
	fs := afero.NewMemMapFs()
	cfg := cfgWithDirProfiles(t, fs, appDir, map[string]config.Profile{
		"dev":  {Parents: []string{"base"}, Bundles: []string{closureDevBundle}},
		"base": {Bundles: []string{closureBaseBundle}},
	}, config.Fixture{})
	bundletree.Write(t, fs, paths.LocalBundlesPathFor(appDir, paths.LayoutV2), "team",
		"version: \"1.0.0\"\nprofiles:\n  p:\n    bundles:\n      - "+closureTeamBundle+"\n")
	return cfg
}

// lockIdentities is the lock identity each ref pins under.
func lockIdentities(t *testing.T, refs ...string) []string {
	t.Helper()
	out := make([]string, 0, len(refs))
	for _, r := range refs {
		ref, err := remote.ParseReference(r)
		require.NoError(t, err)
		key, err := ref.LockKey()
		require.NoError(t, err)
		out = append(out, string(key))
	}
	return out
}

// The whole-project closure pins what every locally authored profile reaches —
// including through a parent named by bare name — and every LOCAL bundle's
// profiles are roots of it: a local bundle is the project's manifest, so a
// remote bundle referenced only from one of its profiles is locked.
func TestClosureRoots_PinEveryLocallyAuthoredProfile(t *testing.T) {
	cfg := closureFixture(t)
	loader := profileLoader(cfg)
	roots, unexpanded := closureRoots(cfg, loader)
	require.Empty(t, unexpanded)

	w := newTestWalker(remote.NewMockFetcher())
	w.loader = loader
	for _, p := range roots {
		w.walkProfile(p, remote.LocalSource, "")
	}
	pins, _, missing := w.result()
	assert.Empty(t, missing)
	assert.ElementsMatch(t, lockIdentities(t, closureDevBundle, closureBaseBundle, closureTeamBundle), pinIdentities(pins))
}

// A parent naming a profile in a LOCAL bundle by its full ref
// ("ctxloom:local@bundles/<bundle>#profiles/<name>") is walked as THAT bundle's
// profile: its bundles are pinned and nothing is reported missing.
func TestDepWalker_RecurseParent_LocalBundleProfileParent(t *testing.T) {
	cfg := closureFixture(t)
	w := newTestWalker(remote.NewMockFetcher())
	w.loader = profileLoader(cfg)

	w.recurseParent(remote.LocalSource + "@bundles/team#profiles/p")
	pins, _, missing := w.result()
	assert.Empty(t, missing, "a local bundle profile parent resolves; its subtree is not lost")
	assert.ElementsMatch(t, lockIdentities(t, closureTeamBundle), pinIdentities(pins))
}

// A selector-less profile name resolves to the locally authored profile, and a
// bare parent inside it to its locally authored sibling.
func TestProfileLoader_SelectorlessNameResolvesLocally(t *testing.T) {
	cfg := closureFixture(t)
	loader := profileLoader(cfg)

	dev, err := loader.Load("dev")
	require.NoError(t, err)
	assert.Equal(t, []string{closureDevBundle}, dev.Bundles)

	resolved, err := loader.ResolveProfile("dev", nil)
	require.NoError(t, err)
	assert.ElementsMatch(t, []string{closureDevBundle, closureBaseBundle}, resolved.Bundles)
}

// A local bundle's profile is addressable by its full local ref and by the
// short "<bundle>#profiles/<name>" spelling, both landing on one identity whose
// gate source is the bundle's own canonical ref.
func TestProfileLoader_LocalBundleProfileSeed(t *testing.T) {
	cfg := closureFixture(t)
	loader := profileLoader(cfg)

	full, err := loader.Load(remote.LocalSource + "@bundles/team#profiles/p")
	require.NoError(t, err)
	short, err := loader.Load("team#profiles/p")
	require.NoError(t, err)
	assert.Equal(t, full.Name, short.Name)
	assert.Equal(t, []string{closureTeamBundle}, full.Bundles)

	resolved, err := loader.ResolveProfile("team#profiles/p", nil)
	require.NoError(t, err)
	want, err := remote.CanonicalBundleRef(remote.LocalSource + "@bundles/team")
	require.NoError(t, err)
	assert.Equal(t, want, resolved.SourceRef)
}
