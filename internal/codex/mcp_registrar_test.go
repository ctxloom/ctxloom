package codex

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/shared/wire"
)

func TestMCPRegistrar_Name(t *testing.T) {
	assert.Equal(t, "codex", MCPRegistrar{}.Name())
}

func TestMCPRegistrar_ConfigPath(t *testing.T) {
	// This test asserts the DEFAULT (~/.codex) global path; pin CODEX_HOME
	// empty so an inherited value (the hostile-env suite poisons it) can't
	// redirect the home resolution. The override itself is covered below.
	t.Setenv("CODEX_HOME", "")
	p, err := (MCPRegistrar{}).ConfigPath("/proj", false)
	require.NoError(t, err)
	assert.Equal(t, filepath.Join("/proj", ".codex", "config.toml"), p)

	g, err := (MCPRegistrar{}).ConfigPath("/proj", true)
	require.NoError(t, err)
	assert.True(t, strings.HasSuffix(g, filepath.Join(".codex", "config.toml")), g)
	assert.NotContains(t, g, "/proj", "global path is home-rooted")
}

// TestMCPRegistrar_ConfigPath_CodexHome verifies the global config path honors
// $CODEX_HOME (which IS the .codex dir) so it matches where codex actually reads
// its global config — the same precedence as codexPromptsDir/getSessionsDir
// (codex-code-01-001). Project scope must ignore CODEX_HOME.
func TestMCPRegistrar_ConfigPath_CodexHome(t *testing.T) {
	t.Setenv("CODEX_HOME", "/custom/codexhome")

	g, err := (MCPRegistrar{}).ConfigPath("/proj", true)
	require.NoError(t, err)
	assert.Equal(t, filepath.Join("/custom/codexhome", "config.toml"), g,
		"global config rooted at $CODEX_HOME, not ~/.codex")

	p, err := (MCPRegistrar{}).ConfigPath("/proj", false)
	require.NoError(t, err)
	assert.Equal(t, filepath.Join("/proj", ".codex", "config.toml"), p,
		"project scope ignores CODEX_HOME")
}

func TestMCPRegistrar_InstallPreservesForeignTables(t *testing.T) {
	existing := `[hooks]
[[hooks.SessionStart]]
[[hooks.SessionStart.hooks]]
command = 'ctxloom hook session-bind'
type = 'command'

[mcp_servers]
[mcp_servers.ctxloom]
args = ['mcp']
command = 'ctxloom'
`
	r := MCPRegistrar{}
	out, err := r.Install([]byte(existing), "taskloom", wire.MCPServer{Command: "taskloom", Args: []string{"mcp"}})
	require.NoError(t, err)

	ok, err := r.Installed(out, "taskloom")
	require.NoError(t, err)
	assert.True(t, ok)
	assert.Contains(t, string(out), "session-bind", "foreign hooks table survives")

	// Idempotent.
	again, err := r.Install(out, "taskloom", wire.MCPServer{Command: "taskloom", Args: []string{"mcp"}})
	require.NoError(t, err)
	assert.Equal(t, string(out), string(again))

	removed, err := r.Uninstall(out, "taskloom")
	require.NoError(t, err)
	gone, err := r.Installed(removed, "taskloom")
	require.NoError(t, err)
	assert.False(t, gone)
	foreign, err := r.Installed(removed, "ctxloom")
	require.NoError(t, err)
	assert.True(t, foreign)
}
