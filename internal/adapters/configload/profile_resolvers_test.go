package configload

import (
	"testing"

	"github.com/spf13/afero"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/core/config"
	"github.com/ctxloom/ctxloom/internal/core/paths"
)

// A read binds the remotes registry's two profile lookups onto the Config it
// hands out: core/config holds them as values and never opens the registry.
func TestRead_BindsProfileResolversFromTheRemotesRegistry(t *testing.T) {
	const appDir = "/proj/.ctxloom"
	fs := afero.NewMemMapFs()
	require.NoError(t, afero.WriteFile(fs, paths.RemotesPath(appDir), []byte(
		"remotes:\n  personal:\n    name: personal\n    url: https://github.com/owner/repo\n"), 0o644))

	cfg, err := Load(WithFS(fs), WithAppDir(appDir))
	require.NoError(t, err)

	remoteOf := cfg.ProfileRemoteResolver()
	urlOf := cfg.ProfileRemoteURLResolver()
	require.NotNil(t, remoteOf, "a read over a readable registry binds the name -> remote lookup")
	require.NotNil(t, urlOf, "a read over a readable registry binds the alias -> URL lookup")
	assert.Equal(t, "personal", remoteOf("personal/go-developer"))
	assert.Equal(t, "", remoteOf("local-profile"), "a name no remote owns is a local profile")
	assert.Equal(t, "https://github.com/owner/repo", urlOf("personal"))
	assert.Equal(t, "", urlOf("unknown"))
}

// A Config no reader built has no registry: the profile loader then reads
// names and refs verbatim.
func TestFixture_HasNoProfileResolvers(t *testing.T) {
	cfg := config.NewFixture(config.Fixture{AppPaths: []string{"/proj/.ctxloom"}})
	assert.Nil(t, cfg.ProfileRemoteResolver())
	assert.Nil(t, cfg.ProfileRemoteURLResolver())
}
