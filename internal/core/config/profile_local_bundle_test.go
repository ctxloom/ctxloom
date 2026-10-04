package config

import (
	"testing"

	"github.com/spf13/afero"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/adapters/remote"
	"github.com/ctxloom/ctxloom/internal/core/paths"
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
	bundletree.WriteDirProfiles(t, fs, appDir, map[string]any{
		"dev": Profile{Bundles: []string{"team/reviews"}},
		"ctl": Profile{Bundles: []string{"team/absent"}},
	})

	b := NewBuilder(fs, true, appDir, SourceProject)
	b.BindProfileResolvers(func(alias string) string {
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

	ctl, err := cfg.GetProfileLoader().Load("ctl")
	require.NoError(t, err)
	assert.Equal(t, []string{remote.CanonicalSpelling(teamURL + "@bundles/absent")}, ctl.Bundles, "control: a non-local ref resolves through the alias, canonically spelled")
}
