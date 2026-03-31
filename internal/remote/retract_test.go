package remote

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCheckRetracted(t *testing.T) {
	ctx := context.Background()

	t.Run("returns false when no manifest exists", func(t *testing.T) {
		mf := newMockFetcher()
		mf.defaultBranch = "main"
		// No manifest file set

		ref := &Reference{Path: "security", GitRef: "v1.0.0"}
		retracted, reason, err := CheckRetracted(ctx, mf, "owner", "repo", "v1", ref, ItemTypeBundle)

		require.NoError(t, err)
		assert.False(t, retracted)
		assert.Empty(t, reason)
	})

	t.Run("returns false when manifest has no retracted entries", func(t *testing.T) {
		mf := newMockFetcher()
		mf.defaultBranch = "main"
		mf.files["ctxloom/v1/manifest.yaml"] = []byte("version: 1\n")

		ref := &Reference{Path: "security", GitRef: "v1.0.0"}
		retracted, reason, err := CheckRetracted(ctx, mf, "owner", "repo", "v1", ref, ItemTypeBundle)

		require.NoError(t, err)
		assert.False(t, retracted)
		assert.Empty(t, reason)
	})

	t.Run("returns true when item is retracted", func(t *testing.T) {
		mf := newMockFetcher()
		mf.defaultBranch = "main"
		mf.files["ctxloom/v1/manifest.yaml"] = []byte(`
version: 1
retracted:
  - type: bundle
    name: security
    version: v1.0.0
    reason: "Security vulnerability found"
`)

		ref := &Reference{Path: "security", GitRef: "v1.0.0"}
		retracted, reason, err := CheckRetracted(ctx, mf, "owner", "repo", "v1", ref, ItemTypeBundle)

		require.NoError(t, err)
		assert.True(t, retracted)
		assert.Equal(t, "Security vulnerability found", reason)
	})

	t.Run("returns true when item retracted without version", func(t *testing.T) {
		mf := newMockFetcher()
		mf.defaultBranch = "main"
		mf.files["ctxloom/v1/manifest.yaml"] = []byte(`
version: 1
retracted:
  - type: bundle
    name: deprecated-bundle
    reason: "Deprecated, use new-bundle instead"
`)

		ref := &Reference{Path: "deprecated-bundle", GitRef: ""}
		retracted, reason, err := CheckRetracted(ctx, mf, "owner", "repo", "v1", ref, ItemTypeBundle)

		require.NoError(t, err)
		assert.True(t, retracted)
		assert.Contains(t, reason, "Deprecated")
	})

	t.Run("returns false when different item is retracted", func(t *testing.T) {
		mf := newMockFetcher()
		mf.defaultBranch = "main"
		mf.files["ctxloom/v1/manifest.yaml"] = []byte(`
version: 1
retracted:
  - type: bundle
    name: other-bundle
    version: v1.0.0
    reason: "Retracted"
`)

		ref := &Reference{Path: "security", GitRef: "v1.0.0"}
		retracted, _, err := CheckRetracted(ctx, mf, "owner", "repo", "v1", ref, ItemTypeBundle)

		require.NoError(t, err)
		assert.False(t, retracted)
	})

	t.Run("returns false when different type is retracted", func(t *testing.T) {
		mf := newMockFetcher()
		mf.defaultBranch = "main"
		mf.files["ctxloom/v1/manifest.yaml"] = []byte(`
version: 1
retracted:
  - type: profile
    name: security
    version: v1.0.0
    reason: "Retracted"
`)

		ref := &Reference{Path: "security", GitRef: "v1.0.0"}
		retracted, _, err := CheckRetracted(ctx, mf, "owner", "repo", "v1", ref, ItemTypeBundle)

		require.NoError(t, err)
		assert.False(t, retracted)
	})

	t.Run("handles invalid YAML gracefully", func(t *testing.T) {
		mf := newMockFetcher()
		mf.defaultBranch = "main"
		mf.files["ctxloom/v1/manifest.yaml"] = []byte("invalid: yaml: [[")

		ref := &Reference{Path: "security", GitRef: "v1.0.0"}
		retracted, _, err := CheckRetracted(ctx, mf, "owner", "repo", "v1", ref, ItemTypeBundle)

		require.NoError(t, err)
		assert.False(t, retracted)
	})
}
