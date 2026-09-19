package backends

import (
	"encoding/json"
	"path/filepath"
	"testing"

	"github.com/spf13/afero"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/core/agent"
	"github.com/ctxloom/ctxloom/internal/core/wire"
)

// TestDeclared_Claude_CarriesSurfaceInputs is the parity gate on claude's
// SurfaceInputs.
//
// A LOCAL SurfaceInputs for claude — agent.SurfaceInputs minus Fragments —
// forces two hand-maintained field-by-field mappers (claudecode.go's
// buildSurfaces and this package's registry.go closure), and those drift: one
// mapper once copied ten fields and silently dropped the eleventh, so a
// surface built through the name→SurfaceSet seam delivered a file missing
// what the caller asked for.
//
// The assertion is on the delivered BYTES, not on the struct: a dropped field
// has no compile error and no runtime error — it produces a settings.json and
// a .mcp.json that look entirely plausible and do not do what was asked.
func TestDeclared_Claude_CarriesSurfaceInputs(t *testing.T) {
	fs := afero.NewMemMapFs()
	dir, home := "/cell", "/engine-home"
	require.NoError(t, fs.MkdirAll(dir, 0o755))

	resolved, err := agent.Select(Declared("claude-code")).WithEverything().Build(agent.SurfaceInputs{
		Context:   "ctx",
		BundleMCP: map[string]wire.MCPServer{agent.MCPServerName: {Command: agent.CtxloomBinary, Args: []string{"mcp", "serve"}}},
		Hooks:     &wire.HooksConfig{},
		DenyTools: []string{"Task"},
	}, fs)
	require.NoError(t, err)
	for _, kd := range resolved.Deliveries() {
		_, err := kd.Deliver(isolatedCellRoots(dir, home))
		require.NoError(t, err, "%s failed to deliver", kd.Kind())
	}

	raw, err := afero.ReadFile(fs, filepath.Join(home, ".mcp.json"))
	require.NoError(t, err, "claude's default MCP surface must have written the private .mcp.json beneath the engine home")

	var doc struct {
		Servers map[string]struct {
			Command string `json:"command"`
		} `json:"mcpServers"`
	}
	require.NoError(t, json.Unmarshal(raw, &doc))

	entry, ok := doc.Servers[agent.MCPServerName]
	require.True(t, ok, "the ctxloom-managed MCP server must be present in %s", raw)
	assert.Equal(t, agent.CtxloomBinary, entry.Command,
		"a materialized .mcp.json names the bare ctxloom, resolved on PATH wherever the file is read")

	settings, err := afero.ReadFile(fs, filepath.Join(dir, ".claude", "settings.json"))
	require.NoError(t, err, "claude's settings surface must have written the private settings.json")
	assert.Contains(t, string(settings), "Task",
		"DenyTools must survive the shared-inputs → claude-surfaces mapping; "+
			"a dropped field here writes a settings.json that denies nothing")
}
