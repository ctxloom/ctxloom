package operations

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/core/agents"
	"github.com/ctxloom/ctxloom/internal/core/config"
	"github.com/ctxloom/ctxloom/internal/core/paths"
)

// A fragment linked to an MCP server is assembled into a run's context only
// when that run was granted the server. The two profiles below reach the same
// bundle; "without" vetoes the server, so its assembly must not carry the
// fragment — and must still carry the bundle's unlinked fragment.
func TestAssembleContext_LinkedFragmentFollowsTheRunsGrantedMCPSet(t *testing.T) {
	root := t.TempDir()
	appDir := filepath.Join(root, paths.AppDirName)
	profilesDir := filepath.Join(appDir, "profiles")
	require.NoError(t, os.MkdirAll(profilesDir, 0755))
	require.NoError(t, os.WriteFile(filepath.Join(profilesDir, "with.yaml"),
		[]byte("bundles:\n  - linked\n"), 0644))
	require.NoError(t, os.WriteFile(filepath.Join(profilesDir, "without.yaml"),
		[]byte("bundles:\n  - linked\nexclude_mcp:\n  - think\n"), 0644))

	bundleDir := authoredV1(appDir)
	require.NoError(t, os.MkdirAll(bundleDir, 0755))
	require.NoError(t, os.WriteFile(filepath.Join(bundleDir, "linked.yaml"), []byte(`version: "1.0"
mcp:
  think:
    command: think-server
    tags: [ctxloom:link_id=think]
fragments:
  guide:
    content: "LINKED-GUIDE"
    tags: [ctxloom:link_id=think]
  plain:
    content: "PLAIN-FRAGMENT"
`), 0644))

	cfg := gatedFixture(config.Fixture{
		AppPaths:     []string{appDir},
		DefaultAgent: "default",
		Agents:       map[string]agents.Agent{"default": {Profiles: []string{"with"}}},
	})

	with, err := AssembleContext(context.Background(), cfg, AssembleContextRequest{Profiles: []string{"with"}})
	require.NoError(t, err)
	assert.Contains(t, with.Context, "LINKED-GUIDE")
	assert.Contains(t, with.Context, "PLAIN-FRAGMENT")

	without, err := AssembleContext(context.Background(), cfg, AssembleContextRequest{Profiles: []string{"without"}})
	require.NoError(t, err)
	assert.NotContains(t, without.Context, "LINKED-GUIDE", "the fragment is withheld with the server it is linked to")
	assert.Contains(t, without.Context, "PLAIN-FRAGMENT", "the unlinked fragment is not collateral")
	assert.NotContains(t, without.FragmentsLoaded, "linked/guide")
}
