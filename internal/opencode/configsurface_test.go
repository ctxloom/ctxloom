package opencode

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

// TestConfigSurface_IsTheOneProductionWriteSettingsCaller pins the ONLY
// production call site of a concrete SettingsWriter.WriteSettings in the whole
// repo: opencode's configSurface.Deliver (surfaces.go).
//
// Dropping WriteSettings from agent.SettingsWriter because "kiro's has no
// production call site" does not follow. Kiro's indeed has none — but the
// METHOD is not dead: this surface calls opencode's concrete one on every
// delivery, and the conformance suite drives three more writers through the
// interface. Removing the interface method would take this path with it, and
// nothing else names this call site.
//
// This is the guard that makes leaving the interface method in place safe.
func TestConfigSurface_IsTheOneProductionWriteSettingsCaller(t *testing.T) {
	fs := afero.NewMemMapFs()
	dir := "/proj"
	require.NoError(t, fs.MkdirAll(dir, 0o755))

	// A user-authored config the delivery must merge into, never replace.
	existing := `{"theme":"gruvbox","mcp":{"user-server":{"type":"local","command":["user-cmd"]}}}`
	require.NoError(t, afero.WriteFile(fs, filepath.Join(dir, ConfigFileName), []byte(existing), 0o644))

	surfaces := NewSurfaces(agent.SurfaceInputs{
		Context: "ctx",
		BundleMCP: map[string]wire.MCPServer{
			agent.MCPServerName: {Command: agent.CtxloomBinary, Args: []string{"mcp", "serve"}},
			"proj-tool":         {Command: "proj-cmd", Args: []string{"serve"}},
		},
	}, fs)

	handle, err := surfaces.Config.Deliver(dir)
	require.NoError(t, err)
	require.NotNil(t, handle)

	servers := mcpServers(t, fs, dir)
	assert.Contains(t, servers, agent.MCPServerName, "the ctxloom-managed server must be registered")
	assert.Contains(t, servers, "proj-tool", "the run's configured servers must be registered")
	assert.Contains(t, servers, "user-server", "a foreign server must survive the managed write")

	// Cleanup reverses ONLY ctxloom's entries.
	require.NoError(t, handle.Cleanup())
	servers = mcpServers(t, fs, dir)
	assert.NotContains(t, servers, agent.MCPServerName, "cleanup must drop the ctxloom-managed server")
	assert.Contains(t, servers, "user-server", "cleanup must leave the user's own server alone")
}

// mcpServers reads the `mcp` object out of the delivered opencode.json.
func mcpServers(t *testing.T, fs afero.Fs, dir string) map[string]any {
	t.Helper()
	raw, err := afero.ReadFile(fs, filepath.Join(dir, ConfigFileName))
	require.NoError(t, err, "the config surface must have written %s", ConfigFileName)
	var doc struct {
		MCP map[string]any `json:"mcp"`
	}
	require.NoError(t, json.Unmarshal(raw, &doc))
	return doc.MCP
}
