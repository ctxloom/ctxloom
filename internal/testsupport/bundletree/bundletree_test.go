package bundletree

import (
	"context"
	"testing"

	"github.com/spf13/afero"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/core/bundles"
	"github.com/ctxloom/ctxloom/internal/core/paths"
)

// The fixture notation must read back as exactly the bundle it spells, through
// the production reader — otherwise a suite built on it tests something else.
func TestWrite_ReadsBackThroughTheProductionReader(t *testing.T) {
	fsys := afero.NewMemMapFs()
	Write(t, fsys, paths.BundlesLayoutRoot("/bundles", paths.LayoutV2), "kit", `version: "1.0"
description: a kit
fragments:
  guide:
    content: GUIDE-BODY
commands:
  go:
    content: GO-BODY
mcp:
  srv:
    command: srv-bin
profiles:
  dev:
    description: dev profile
hooks:
  pre_tool:
    - command: first
    - command: second
`)

	reads, err := bundles.NewProjectReader(fsys, []string{"/bundles"}).Read(context.Background())
	require.NoError(t, err)
	require.Len(t, reads, 1)
	b := reads[0].Bundle
	assert.Equal(t, "a kit", b.Description)
	assert.Equal(t, "GUIDE-BODY", b.Fragments["guide"].Content)
	assert.Equal(t, "GO-BODY", b.Commands["go"].Content)
	assert.Equal(t, "srv-bin", b.MCP["srv"].Command)
	assert.Contains(t, b.Profiles, "dev")
	require.Len(t, b.Hooks.PreTool, 2)
	assert.Equal(t, "first", b.Hooks.PreTool[0].Command, "declared hook order survives the tree")
	assert.Equal(t, "second", b.Hooks.PreTool[1].Command)
}

func TestWrite_AnEmptyBundleIsAnEnvelopeOnlyTree(t *testing.T) {
	fsys := afero.NewMemMapFs()
	envelope := Write(t, fsys, paths.BundlesLayoutRoot("/bundles", paths.LayoutV2), "personal/empty", "version: \"1.0\"\n")

	assert.Equal(t, "/bundles/v2/personal/empty/bundle.yaml", envelope)
	reads, err := bundles.NewProjectReader(fsys, []string{"/bundles"}).Read(context.Background())
	require.NoError(t, err)
	require.Len(t, reads, 1)
	assert.Equal(t, "personal/empty", reads[0].DisplayName())
}
