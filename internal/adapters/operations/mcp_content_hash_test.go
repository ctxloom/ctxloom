package operations

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// An MCP entry's content hash is read by nothing, so it is not persisted. A
// sidecar written before that, still carrying content_hash, must load, and
// the next edit must write the sidecar back without it.
func TestBundleMCP_EditDropsALegacyContentHashFromTheSidecar(t *testing.T) {
	ctx := context.Background()
	cfg := newItemTestBundle(t)
	_, err := UpdateBundle(ctx, cfg, UpdateBundleRequest{
		Name:          "b",
		SetMCPServers: map[string]BundleMCPInput{"srv": {Command: new("old"), Notes: new("kept notes")}},
	})
	require.NoError(t, err)

	bundle, err := GetBundle(cfg, "b")
	require.NoError(t, err)
	sidecar := filepath.Join(filepath.Dir(bundle.Path), "mcp", ".srv.meta.yaml")
	before, err := os.ReadFile(sidecar)
	require.NoError(t, err)
	legacy := append(before, []byte("content_hash: sha256:1111111111111111111111111111111111111111111111111111111111111111\n")...)
	require.NoError(t, os.WriteFile(sidecar, legacy, 0o644))

	got, err := GetBundleMCP(ctx, cfg, GetBundleMCPRequest{Bundle: "b", Name: "srv"})
	require.NoError(t, err, "a sidecar carrying a legacy content_hash still loads")
	assert.Equal(t, "kept notes", got.MCP.Notes)

	_, err = SetBundleMCP(ctx, cfg, SetBundleMCPRequest{
		Bundle: "b", Name: "srv", MCP: BundleMCPInput{Command: new("new"), Notes: new("kept notes")},
	})
	require.NoError(t, err)

	after, err := os.ReadFile(sidecar)
	require.NoError(t, err)
	assert.Contains(t, string(after), "kept notes")
	assert.NotContains(t, string(after), "content_hash", "a derived hash nothing reads is not written back")
	got, err = GetBundleMCP(ctx, cfg, GetBundleMCPRequest{Bundle: "b", Name: "srv"})
	require.NoError(t, err)
	assert.Equal(t, "new", got.MCP.Command)
}
