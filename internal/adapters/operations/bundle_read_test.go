package operations

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestReadBundle(t *testing.T) {
	fs, cfg := memBundleFS(t) // seeds seed.yaml

	res, err := ReadBundle(context.Background(), cfg, ReadBundleRequest{Name: "seed", FS: fs})
	require.NoError(t, err)
	require.NotNil(t, res.Bundle)
	assert.Contains(t, string(res.Raw), "version: 1.0.0")
}

func TestReadBundle_NotFound(t *testing.T) {
	fs, cfg := memBundleFS(t)
	_, err := ReadBundle(context.Background(), cfg, ReadBundleRequest{Name: "ghost", FS: fs})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not found")
}
