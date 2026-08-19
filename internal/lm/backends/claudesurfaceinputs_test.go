package backends

import (
	"encoding/json"
	"path/filepath"
	"testing"

	"github.com/spf13/afero"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/shared/agent"
	"github.com/ctxloom/ctxloom/internal/shared/wire"
)

// TestBuildSurfaces_Claude_CarriesMCPCommandOverride is the parity gate on
// claude's SurfaceInputs.
//
// A LOCAL SurfaceInputs for claude — agent.SurfaceInputs minus Fragments —
// forces two hand-maintained field-by-field mappers (claudecode.go's
// buildSurfaces and this package's registry.go closure), and those drift: one
// mapper copied ten fields and silently dropped MCPCommandOverride, so a
// surface built through the name→SurfaceSet seam stamps the HOST's self-exec
// path into .mcp.json instead of the in-container path the override names.
//
// The assertion is on the delivered BYTES, not on the struct: a dropped field
// has no compile error and no runtime error — it produces a .mcp.json that
// looks entirely plausible and names a binary the container cannot exec.
func TestBuildSurfaces_Claude_CarriesMCPCommandOverride(t *testing.T) {
	const override = "/usr/local/bin/ctxloom"

	fs := afero.NewMemMapFs()
	dir := "/cell"
	require.NoError(t, fs.MkdirAll(dir, 0o755))

	set := BuildSurfaces("claude-code", agent.SurfaceInputs{
		Context:            "ctx",
		BundleMCP:          map[string]wire.MCPServer{agent.MCPServerName: {Command: agent.CtxloomBinary, Args: []string{"mcp", "serve"}}},
		Hooks:              &wire.HooksConfig{},
		MCPCommandOverride: override,
	}, fs)

	resolved, err := agent.Select(set).WithEverything().Build()
	require.NoError(t, err)
	for _, kd := range resolved.Deliveries() {
		_, err := kd.Deliver(dir)
		require.NoError(t, err, "%s failed to deliver", kd.Kind())
	}

	raw, err := afero.ReadFile(fs, filepath.Join(dir, ".mcp.json"))
	require.NoError(t, err, "claude's MCP surface must have written .mcp.json")

	var doc struct {
		Servers map[string]struct {
			Command string `json:"command"`
		} `json:"mcpServers"`
	}
	require.NoError(t, json.Unmarshal(raw, &doc))

	entry, ok := doc.Servers[agent.MCPServerName]
	require.True(t, ok, "the ctxloom-managed MCP server must be present in %s", raw)
	assert.Equal(t, override, entry.Command,
		"MCPCommandOverride must survive the shared-inputs → claude-surfaces mapping; "+
			"a dropped field here writes a host path a container cannot exec")
}
