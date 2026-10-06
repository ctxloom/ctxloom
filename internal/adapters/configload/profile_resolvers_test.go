package configload

import (
	"testing"

	"github.com/spf13/afero"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/core/config"
	"github.com/ctxloom/ctxloom/internal/core/paths"
	"github.com/ctxloom/ctxloom/internal/shared/safefs"
	"github.com/ctxloom/ctxloom/internal/testsupport"
)

// A read binds the remotes registry's alias lookup onto the Config it hands
// out: core/config holds it as a value and never opens the registry.
func TestRead_BindsProfileResolversFromTheRemotesRegistry(t *testing.T) {
	const appDir = "/proj/.ctxloom"
	fs := afero.NewMemMapFs()
	testsupport.WriteFileString(t, fs, paths.RemotesPath(appDir),
		"remotes:\n  personal:\n    name: personal\n    url: https://github.com/owner/repo\n", 0o644)

	cfg, err := Load(WithRoot(safefs.NewMem(fs)), WithAppDir(appDir))
	require.NoError(t, err)

	urlOf := cfg.ProfileRemoteURLResolver()
	require.NotNil(t, urlOf, "a read over a readable registry binds the alias -> URL lookup")
	assert.Equal(t, "https://github.com/owner/repo", urlOf("personal"))
	assert.Equal(t, "", urlOf("unknown"))
}

// A Config no reader built has no registry: the profile loader then reads
// refs verbatim.
func TestFixture_HasNoProfileResolvers(t *testing.T) {
	cfg := config.NewFixture(config.Fixture{AppPaths: []string{"/proj/.ctxloom"}})
	assert.Nil(t, cfg.ProfileRemoteURLResolver())
}
