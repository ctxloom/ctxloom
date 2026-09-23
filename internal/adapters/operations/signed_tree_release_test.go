package operations

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/adapters/content"
	"github.com/ctxloom/ctxloom/internal/core/bundles"
	"github.com/ctxloom/ctxloom/internal/core/release"
)

// treeRelease is the release a fixture signs a tree under, built the way
// `bundle sign` builds it: from the tree's own bundle.yaml.
func treeRelease(t *testing.T, tree content.Bundle) release.Release {
	t.Helper()
	raw, err := tree.ReadFile(context.Background(), bundles.DirectoryFormManifest)
	require.NoError(t, err)
	env, err := bundles.ParseBundle(raw)
	require.NoError(t, err)
	rel, err := bundleRelease(string(tree.ID()), env)
	require.NoError(t, err)
	return rel
}
