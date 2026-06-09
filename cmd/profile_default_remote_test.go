// Tests for expandRefsAgainstDefaultRemote — the CLI-boundary convenience that
// lets `profile create`/`modify` accept bare refs (e.g.
// "code-review-base#fragments/conduct") and store them as canonical URLs
// resolved against the configured default remote.
package cmd

import (
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/remote"
)

func newRegistryWithDefault(t *testing.T, name, url string) *remote.Registry {
	t.Helper()
	registry, err := remote.NewRegistry(filepath.Join(t.TempDir(), "remotes.yaml"))
	require.NoError(t, err)
	require.NoError(t, registry.Add(name, url))
	require.NoError(t, registry.SetDefault(name))
	return registry
}

func TestExpandRefsAgainstDefaultRemote(t *testing.T) {
	t.Run("nil registry leaves refs unchanged", func(t *testing.T) {
		in := []string{"code-review-base#fragments/conduct"}
		assert.Equal(t, in, expandRefsAgainstDefaultRemote(in, remote.ItemTypeBundle, nil))
	})

	t.Run("no default remote leaves refs unchanged", func(t *testing.T) {
		registry, err := remote.NewRegistry(filepath.Join(t.TempDir(), "remotes.yaml"))
		require.NoError(t, err)
		require.NoError(t, registry.Add("origin", "https://github.com/owner/repo"))
		in := []string{"code-review-base"}
		assert.Equal(t, in, expandRefsAgainstDefaultRemote(in, remote.ItemTypeBundle, registry))
	})

	t.Run("expands a bare bundle ref and preserves the fragment selector", func(t *testing.T) {
		registry := newRegistryWithDefault(t, "origin", "https://github.com/owner/repo")
		out := expandRefsAgainstDefaultRemote([]string{"code-review-base#fragments/conduct"}, remote.ItemTypeBundle, registry)
		assert.Equal(t, []string{"https://github.com/owner/repo@bundles/code-review-base#fragments/conduct"}, out)
	})

	t.Run("expands a bare parent ref under the profiles segment", func(t *testing.T) {
		registry := newRegistryWithDefault(t, "origin", "https://github.com/owner/repo")
		out := expandRefsAgainstDefaultRemote([]string{"developer"}, remote.ItemTypeProfile, registry)
		assert.Equal(t, []string{"https://github.com/owner/repo@profiles/developer"}, out)
	})

	t.Run("leaves scheme-qualified and local refs untouched", func(t *testing.T) {
		registry := newRegistryWithDefault(t, "origin", "https://github.com/owner/repo")
		in := []string{
			"https://github.com/other/repo@bundles/x",
			"ctxloom:local@bundles/project-conventions",
		}
		assert.Equal(t, in, expandRefsAgainstDefaultRemote(in, remote.ItemTypeBundle, registry))
	})

	t.Run("empty input returns the same slice", func(t *testing.T) {
		registry := newRegistryWithDefault(t, "origin", "https://github.com/owner/repo")
		assert.Empty(t, expandRefsAgainstDefaultRemote(nil, remote.ItemTypeBundle, registry))
	})
}
