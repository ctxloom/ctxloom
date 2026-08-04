// Directory-profile inline-field parity tests verify that a directory profile
// (.ctxloom/profiles/<name>.yaml) carrying inline fragments:/bundle_items:
// declarations assembles the SAME content as the equivalent inline profile
// (config.yaml profiles: map), honoring "@<commit>" version pins on both — the
// directory mirror of version_pinned_refs_test.go's inline path. The directory
// path reaches the shared assembly pipeline through operations.resolveProfile's
// directory fallback (profiles.ResolvedProfile.Fragments/BundleItems), not the
// inline config map.
package operations

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/bundles"
	"github.com/ctxloom/ctxloom/internal/config"
	"github.com/ctxloom/ctxloom/internal/paths"
)

// writeDirProfile drops a directory profile body at .ctxloom/profiles/<name>.yaml
// under a fresh tempdir and returns the .ctxloom app dir, for pointing a cfg's
// AppPaths at a real on-disk directory profile (GetProfileDirs uses os.Stat).
func writeDirProfile(t *testing.T, name, body string) string {
	t.Helper()
	appDir := filepath.Join(t.TempDir(), paths.AppDirName)
	profilesDir := paths.ProfilesPath(appDir)
	require.NoError(t, os.MkdirAll(profilesDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(profilesDir, name+".yaml"), []byte(body), 0o644))
	return appDir
}

// TestDirProfile_FragmentRef_VersionPinned_MatchesInline proves a directory
// profile's inline fragments: ref carrying "@<commit>" assembles the historical
// version — byte-for-byte identical to the inline profile with the same ref. This
// is the headline parity assertion for the fragments: field: the directory path
// closes the gap where a directory profile's own fragments: were dropped.
func TestDirProfile_FragmentRef_VersionPinned_MatchesInline(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	def := &bundles.Bundle{Fragments: map[string]bundles.BundleFragment{"solid": {Content: "DEFAULT-BODY"}}}
	versions := map[string]*bundles.Bundle{
		"c1": {Fragments: map[string]bundles.BundleFragment{"solid": {Content: "V1-BODY"}}},
	}
	fx := newTrustFixture(t)
	fx.approveFragment("cq", "solid", "V1-BODY")
	ref := cqVersionRef + "@c1#fragments/solid"

	// Inline profile path (the proven baseline).
	loaderInline, cfgInline := versionPinnedLoader(t, fx.records(), def, versions)
	cfgInline = profileCfg(cfgInline, "fragpin", config.Profile{
		Fragments: []config.FragmentRef{{Name: ref}},
	})
	inlineRes, err := AssembleContext(context.Background(), cfgInline, AssembleContextRequest{Profile: "fragpin", Pipeline: loaderInline})
	require.NoError(t, err)
	require.Contains(t, inlineRes.Context, "V1-BODY")

	// Directory profile equivalent: the SAME ref, declared in a .yaml on disk.
	loaderDir, cfgDir := versionPinnedLoader(t, fx.records(), def, versions)
	dirFixture := cfgDir.ToFixture()
	dirFixture.AppPaths = []string{writeDirProfile(t, "fragpin", "fragments:\n  - \""+ref+"\"\n")}
	cfgDir = config.NewFixture(dirFixture)
	dirRes, err := AssembleContext(context.Background(), cfgDir, AssembleContextRequest{Profile: "fragpin", Pipeline: loaderDir})
	require.NoError(t, err)

	assert.Equal(t, inlineRes.Context, dirRes.Context,
		"a directory profile's pinned fragments: ref assembles identically to the inline equivalent")
	assert.Contains(t, dirRes.Context, "V1-BODY", "the pinned historical version assembles")
	assert.NotContains(t, dirRes.Context, "DEFAULT-BODY", "the lockfile default must NOT be used when pinned")
}

// TestDirProfile_BundleItem_VersionPinned_MatchesInline proves the same for the
// bundle_items: field: a directory profile's "@<commit>"-pinned cherry-pick is
// expanded via the shared ExpandBundleRefs path and assembles the historical
// version, identical to the inline equivalent.
func TestDirProfile_BundleItem_VersionPinned_MatchesInline(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	def := &bundles.Bundle{Fragments: map[string]bundles.BundleFragment{"solid": {Content: "DEFAULT-BODY"}}}
	versions := map[string]*bundles.Bundle{
		"c1": {Fragments: map[string]bundles.BundleFragment{"solid": {Content: "V1-BODY"}}},
	}
	fx := newTrustFixture(t)
	fx.approveFragment("cq", "solid", "V1-BODY")
	ref := cqVersionRef + "@c1:fragments/solid"

	loaderInline, cfgInline := versionPinnedLoader(t, fx.records(), def, versions)
	cfgInline = profileCfg(cfgInline, "pinned", config.Profile{BundleItems: []string{ref}})
	inlineRes, err := AssembleContext(context.Background(), cfgInline, AssembleContextRequest{Profile: "pinned", Pipeline: loaderInline})
	require.NoError(t, err)
	require.Contains(t, inlineRes.Context, "V1-BODY")

	loaderDir, cfgDir := versionPinnedLoader(t, fx.records(), def, versions)
	dirFixture := cfgDir.ToFixture()
	dirFixture.AppPaths = []string{writeDirProfile(t, "pinned", "bundle_items:\n  - \""+ref+"\"\n")}
	cfgDir = config.NewFixture(dirFixture)
	dirRes, err := AssembleContext(context.Background(), cfgDir, AssembleContextRequest{Profile: "pinned", Pipeline: loaderDir})
	require.NoError(t, err)

	assert.Equal(t, inlineRes.Context, dirRes.Context,
		"a directory profile's pinned bundle_items: cherry-pick assembles identically to the inline equivalent")
	assert.Contains(t, dirRes.Context, "V1-BODY")
	assert.NotContains(t, dirRes.Context, "DEFAULT-BODY")
}
