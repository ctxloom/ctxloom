package remote

import (
	"testing"

	"github.com/spf13/afero"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A forge label bound with SetForge must change how that remote's URL
// resolves. The URL below is on a custom domain no configured forge names, so
// URL inference alone yields generic git; only the stored label can make it
// resolve as the corp github forge with its own token_env.
func TestRegistry_ResolveForgeForURL_HonoursStoredForgeLabel(t *testing.T) {
	const (
		path      = "/proj/remotes.yaml"
		customURL = "https://code.custom.example/team/bundles"
	)
	fs := afero.NewMemMapFs()
	require.NoError(t, afero.WriteFile(fs, path, []byte(
		"forges:\n  corp:\n    type: github\n    base_url: https://github.corp.example\n    token_env: CORP_TOKEN\n"), 0o644))
	reg, err := NewRegistry(path, WithRegistryFS(fs))
	require.NoError(t, err)
	require.NoError(t, reg.Add("team", customURL))

	unlabelled := reg.ResolveForgeForURL(customURL)
	require.Equal(t, ForgeGitGeneric, unlabelled.Type,
		"precondition: with no label the custom domain infers as generic git")

	require.NoError(t, reg.SetForge("team", "corp"))

	got := reg.ResolveForgeForURL(customURL)
	assert.Equal(t, ForgeGitHub, got.Type, "the stored label must select the corp forge")
	assert.Equal(t, "CORP_TOKEN", got.TokenEnv)
	assert.Equal(t, "https://github.corp.example", got.BaseURL)

	t.Run("a different spelling of the same repository still finds the label", func(t *testing.T) {
		assert.Equal(t, ForgeGitHub, reg.ResolveForgeForURL("https://Code.Custom.example/team/bundles/").Type)
	})

	t.Run("an unregistered URL falls back to inference", func(t *testing.T) {
		assert.Equal(t, ForgeGitGeneric, reg.ResolveForgeForURL("https://code.custom.example/other/repo").Type)
	})
}
