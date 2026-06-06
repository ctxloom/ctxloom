package gemini

import (
	"github.com/ctxloom/shared/agent"
	"github.com/ctxloom/shared/wire"
)

// writeGeminiSettings is the gemini WriteSettingsFunc used by the launch tests.
// It routes through the package's own settings writer, mirroring the registry
// dispatch the launch backend is injected with in production — so a test that
// exercises Setup/Flush/Clear writes real gemini settings.
func writeGeminiSettings(_ string, hooks *wire.HooksConfig, mcp *wire.MCPConfig, bundleMCP map[string]wire.MCPServer, projectDir string, opts ...agent.SettingsOption) error {
	o := agent.SettingsOptions{}
	for _, opt := range opts {
		opt(&o)
	}
	return NewWriter(o).WriteSettings(hooks, mcp, bundleMCP, projectDir)
}
