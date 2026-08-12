package codex

import (
	"os"
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
	assert.Equal(t, filepath.Join(ProjectHome("/proj"), "config.toml"), p,
		"project scope resolves through the engine-home policy's single owner (StateHome), not a bare <workDir>/.codex")

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
	assert.Equal(t, filepath.Join(ProjectHome("/proj"), "config.toml"), p,
		"project scope ignores CODEX_HOME")
}

// TestMCPRegistrar_UninstallParityWithSettingsWriter is the parity test across
// the TWO writers of .codex/config.toml's [mcp_servers] table: MCPRegistrar
// (byte-level, taskloom manage) and CodexHookWriter's removeManagedMCP
// (whole-file, ctxloom's own reconcile). Removing the last server must leave
// the SAME shape either way — an empty `[mcp_servers]` stanza from one and a
// deleted key from the other is one file described two ways.
func TestMCPRegistrar_UninstallParityWithSettingsWriter(t *testing.T) {
	// The ctxloom server is the one BOTH writers own — removeManagedMCP only
	// ever removes ctxloom's own entries, so it is the only name on which the
	// two can be compared at all.
	const oneServer = `[mcp_servers]
[mcp_servers.ctxloom]
args = ['mcp']
command = 'ctxloom'
`
	viaRegistrar, err := (MCPRegistrar{}).Uninstall([]byte(oneServer), "ctxloom")
	require.NoError(t, err)

	cfg := map[string]any{"mcp_servers": map[string]any{
		"ctxloom": map[string]any{"command": "ctxloom", "args": []any{"mcp"}},
	}}
	removeManagedMCP(cfg)
	_, viaWriter := cfg["mcp_servers"]

	assert.False(t, viaWriter, "the settings writer deletes an emptied mcp_servers key")
	assert.NotContains(t, string(viaRegistrar), "mcp_servers",
		"the registrar must agree: no empty [mcp_servers] stanza left behind")
}

// TestMCPRegistrar_UninstallAbsentIsByteForByteNoop pins agent.MCPRegistrar's
// own contract — "removing a server that is not present is a no-op". A TOML
// round-trip is not a no-op: it drops every comment, and a config.toml holding
// only comments round-trips to ZERO BYTES, which taskloom manage then writes
// over the user's file while reporting success.
func TestMCPRegistrar_UninstallAbsentIsByteForByteNoop(t *testing.T) {
	for _, tc := range []struct{ name, config string }{
		{"comments only", "# codex config\n# model = 'gpt-5'\n"},
		{"foreign keys and comments", "# keep me\nmodel = 'gpt-5'\n"},
		{"other servers only", "[mcp_servers]\n[mcp_servers.ctxloom]\ncommand = 'ctxloom'\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			out, err := (MCPRegistrar{}).Uninstall([]byte(tc.config), "taskloom")
			require.NoError(t, err)
			assert.Equal(t, tc.config, string(out), "nothing to remove: the bytes are returned untouched")
		})
	}
}

// TestCodexPathVocabularyIsSingleSourced holds the constants block in
// enginecli.go to its own claim: it is "the SINGLE source of the names codex
// reads", shared by every writer AND by the probe declarations, so that a
// rename cannot leave one of them describing a path nothing writes. Each
// consumer it names is checked against the constants rather than against a
// spelled-out path — a consumer that goes back to its own literal passes only
// until the constant changes, which is exactly the drift this pins.
func TestCodexPathVocabularyIsSingleSourced(t *testing.T) {
	t.Setenv(CodexHomeEnv, "")
	home, err := codexHome()
	require.NoError(t, err)
	userHome, err := os.UserHomeDir()
	require.NoError(t, err)
	assert.Equal(t, filepath.Join(userHome, ConfigDirName), home, "codexHome")

	t.Setenv(CodexHomeEnv, "/env/provided/.codex")
	envHome, err := codexHome()
	require.NoError(t, err)
	assert.Equal(t, "/env/provided/.codex", envHome, "codexHome honours CodexHomeEnv")

	global, err := (MCPRegistrar{}).ConfigPath("/proj", true)
	require.NoError(t, err)
	assert.Equal(t, filepath.Join(envHome, ConfigFileName), global, "MCPRegistrar.ConfigPath (global)")

	// The HARPLESS project-scoped consumers all hang off the project root (the
	// S7-interim join — no run resolves here, see CodexHookWriter.SettingsPath)
	// with the SAME vocabulary constants layered on top: they share one name for
	// one file, which is what makes the conformance suite's write-then-read-back
	// meaningful.
	stateHome := "/proj"
	project, err := (MCPRegistrar{}).ConfigPath("/proj", false)
	require.NoError(t, err)
	assert.Equal(t, filepath.Join(stateHome, ConfigDirName, ConfigFileName), project, "MCPRegistrar.ConfigPath (project)")

	assert.Equal(t, filepath.Join(stateHome, ConfigDirName, ConfigFileName),
		(&CodexHookWriter{}).SettingsPath("/proj"), "CodexHookWriter.SettingsPath")
	assert.Equal(t, filepath.Join(stateHome, ConfigDirName), ProjectHome("/proj"), "codex.ProjectHome")
	assert.Equal(t, filepath.Join(stateHome, ConfigDirName), cellScopedCodexHome(stateHome))
	assert.Equal(t, filepath.Join(stateHome, ConfigDirName, PromptsDirName), cellScopedPromptsDir(stateHome))
	assert.Equal(t, filepath.Join(stateHome, ConfigDirName, SkillsDirName), cellScopedSkillsDir(stateHome))
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
