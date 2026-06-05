package claude

import (
	"github.com/ctxloom/ctxloom/internal/agent"
	"github.com/ctxloom/ctxloom/internal/config"
)

// argPair reports whether args contains flag immediately followed by value.
func argPair(args []string, flag, value string) bool {
	for i, a := range args {
		if a == flag && i+1 < len(args) && args[i+1] == value {
			return true
		}
	}
	return false
}

// argValue returns the value following flag in args, or "" if the flag is
// absent or has no following element.
func argValue(args []string, flag string) string {
	for i, a := range args {
		if a == flag && i+1 < len(args) {
			return args[i+1]
		}
	}
	return ""
}

// writeClaudeSettings is the claude WriteSettingsFunc used by the launch tests.
// It routes through the package's own settings writer, mirroring the registry
// dispatch the launch backend is injected with in production — so a test that
// exercises Setup/Flush/Clear writes real claude settings.
func writeClaudeSettings(_ string, hooks *config.HooksConfig, mcp *config.MCPConfig, bundleMCP map[string]config.MCPServer, projectDir string, opts ...agent.SettingsOption) error {
	o := agent.SettingsOptions{}
	for _, opt := range opts {
		opt(&o)
	}
	return NewWriter(o).WriteSettings(hooks, mcp, bundleMCP, projectDir)
}
