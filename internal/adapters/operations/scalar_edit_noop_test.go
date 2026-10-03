package operations

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Restating a value is not a change: setting the description or version to
// what the bundle already holds reports no change and writes nothing.
func TestUpdateBundle_ScalarSetToItsCurrentValueIsNoChange(t *testing.T) {
	ctx := context.Background()
	cfg := newItemTestBundle(t)
	_, err := UpdateBundle(ctx, cfg, UpdateBundleRequest{Name: "b", SetDescription: sp("held"), SetVersion: sp("1.2.3")})
	require.NoError(t, err)

	res, err := UpdateBundle(ctx, cfg, UpdateBundleRequest{Name: "b", SetDescription: sp("held")})
	require.NoError(t, err)
	assert.Equal(t, "no_changes", res.Status)
	assert.Empty(t, res.Changes)

	res, err = UpdateBundle(ctx, cfg, UpdateBundleRequest{Name: "b", SetVersion: sp("1.2.3")})
	require.NoError(t, err)
	assert.Equal(t, "no_changes", res.Status)
	assert.Empty(t, res.Changes)

	res, err = UpdateBundle(ctx, cfg, UpdateBundleRequest{Name: "b", SetDescription: sp("held"), SetVersion: sp("1.2.4")})
	require.NoError(t, err)
	assert.Equal(t, "updated", res.Status)
	assert.Equal(t, []string{"updated version"}, res.Changes)
}
