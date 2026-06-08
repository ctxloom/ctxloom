package operations

import (
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/config"
	"github.com/ctxloom/ctxloom/internal/profiles"
	"github.com/ctxloom/ctxloom/internal/remote"
)

func TestWireRemoteRef(t *testing.T) {
	const baseRef = "https://github.com/alice/ctxloom@profiles/base"
	const bundleRef = "https://github.com/alice/ctxloom@bundles/demo"

	newLoader := func(tmp string) *profiles.Loader {
		return profiles.NewLoader([]string{filepath.Join(tmp, "profiles")})
	}

	t.Run("creates a default profile with the remote profile as a parent", func(t *testing.T) {
		tmp := t.TempDir()
		loader := newLoader(tmp)
		cfg := &config.Config{AppPaths: []string{tmp}}

		name, err := WireRemoteRef(cfg, WireRefRequest{
			Ref: baseRef, ItemType: remote.ItemTypeProfile, Loader: loader,
		})
		require.NoError(t, err)
		assert.Equal(t, "default", name)

		p, lerr := loader.Load("default")
		require.NoError(t, lerr)
		assert.Contains(t, p.Parents, baseRef, "remote profile is wired in as a parent")
	})

	t.Run("wires a remote bundle into bundles and dedups", func(t *testing.T) {
		tmp := t.TempDir()
		loader := newLoader(tmp)
		cfg := &config.Config{AppPaths: []string{tmp}}

		_, err := WireRemoteRef(cfg, WireRefRequest{Ref: bundleRef, ItemType: remote.ItemTypeBundle, Loader: loader})
		require.NoError(t, err)
		// Idempotent: a second wire of the same ref does not duplicate it.
		_, err = WireRemoteRef(cfg, WireRefRequest{Ref: bundleRef, ItemType: remote.ItemTypeBundle, Loader: loader})
		require.NoError(t, err)

		p, lerr := loader.Load("default")
		require.NoError(t, lerr)
		assert.Equal(t, []string{bundleRef}, p.Bundles, "bundle wired once into bundles")
	})

	t.Run("honors an explicit target profile", func(t *testing.T) {
		tmp := t.TempDir()
		loader := newLoader(tmp)
		cfg := &config.Config{AppPaths: []string{tmp}}

		name, err := WireRemoteRef(cfg, WireRefRequest{
			Ref: baseRef, ItemType: remote.ItemTypeProfile, ProfileName: "work", Loader: loader,
		})
		require.NoError(t, err)
		assert.Equal(t, "work", name)

		p, lerr := loader.Load("work")
		require.NoError(t, lerr)
		assert.Contains(t, p.Parents, baseRef)
	})
}
