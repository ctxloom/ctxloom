package config

import (
	"path/filepath"
	"testing"

	"github.com/spf13/afero"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/core/paths"
	"github.com/ctxloom/ctxloom/internal/testsupport"
	"github.com/ctxloom/ctxloom/internal/testsupport/bundletree"
)

// A LOCAL bundle at <bundles>/team/reviews, listed in a profile as
// "team/reviews" while a remote alias "team" is configured, must stay the local
// bundle: the profile loader the config hands out wires the project's local
// bundles in as the local-file-wins oracle. The control profile names a bundle
// that does NOT exist locally, proving the alias would otherwise be applied.
func TestProfileLoader_LocalBundleWinsOverSameSpelledRemoteAlias(t *testing.T) {
	const (
		appDir  = "/proj/.ctxloom"
		teamURL = "https://github.com/acme/team"
	)
	fs := afero.NewMemMapFs()
	bundletree.Write(t, fs, paths.BundlesLayoutRoot(paths.LocalBundlesPath(appDir), paths.LayoutV2),
		"team/reviews", "version: \"1.0\"\ndescription: local reviews\n")
	profileDir := paths.ProfilesPath(appDir)
	testsupport.WriteFileString(t, fs, filepath.Join(profileDir, "dev.yaml"), "bundles:\n  - team/reviews\n", 0o644)
	testsupport.WriteFileString(t, fs, filepath.Join(profileDir, "ctl.yaml"), "bundles:\n  - team/absent\n", 0o644)

	b := NewBuilder(fs, true, appDir, SourceProject)
	b.BindProfileResolvers(nil, func(alias string) string {
		if alias == "team" {
			return teamURL
		}
		return ""
	})
	cfg := b.Build()

	loader := cfg.GetProfileLoader()
	dev, err := loader.Load("dev")
	require.NoError(t, err)
	assert.Equal(t, []string{"team/reviews"}, dev.Bundles, "the local bundle must not be re-pointed at the remote")
	assert.Empty(t, loader.PendingUpgrades(), "no on-disk migration may be staged for a local bundle")

	ctl, err := cfg.GetProfileLoader().Load("ctl")
	require.NoError(t, err)
	assert.Equal(t, []string{teamURL + "@bundles/absent"}, ctl.Bundles, "control: a non-local ref resolves through the alias")
}
