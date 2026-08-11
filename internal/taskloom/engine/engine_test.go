package engine

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/claude"
	"github.com/ctxloom/ctxloom/internal/kiro"
)

// Fixtures modeled on real backend configs: each holds a foreign server and
// (for claude) foreign provenance keys that a merge must not disturb.
const claudeFixture = `{
  "mcpServers": {
    "ctxloom": {
      "_ctxloom": "ctxloom-auto",
      "args": ["mcp"],
      "command": "ctxloom",
      "cwd": "${CLAUDE_PROJECT_DIR}"
    }
  }
}`

const codexFixture = `[hooks]
[[hooks.SessionStart]]
[[hooks.SessionStart.hooks]]
command = 'ctxloom hook session-bind'
type = 'command'

[mcp_servers]
[mcp_servers.ctxloom]
args = ['mcp']
command = 'ctxloom'
`

const kiroFixture = `{
  "mcpServers": {
    "ctxloom": {"args": ["mcp"], "command": "ctxloom"}
  }
}`

func jsonServers(t *testing.T, config []byte) map[string]any {
	t.Helper()
	var doc map[string]any
	require.NoError(t, json.Unmarshal(config, &doc))
	servers, _ := doc["mcpServers"].(map[string]any)
	return servers
}

func TestEngines_InstallIntoEmpty_CreatesEntry(t *testing.T) {
	for _, e := range All() {
		t.Run(e.Name(), func(t *testing.T) {
			out, err := e.Install(nil, TaskloomName, TaskloomServer())
			require.NoError(t, err)
			ok, err := e.Installed(out, TaskloomName)
			require.NoError(t, err)
			assert.True(t, ok, "fresh install must register the server")
		})
	}
}

func TestEngines_Install_PreservesForeignContent(t *testing.T) {
	fixtures := map[string]string{
		"claude-code": claudeFixture,
		"codex":       codexFixture,
		"kiro":        kiroFixture,
	}
	for _, e := range All() {
		t.Run(e.Name(), func(t *testing.T) {
			out, err := e.Install([]byte(fixtures[e.Name()]), TaskloomName, TaskloomServer())
			require.NoError(t, err)
			ok, err := e.Installed(out, TaskloomName)
			require.NoError(t, err)
			assert.True(t, ok)
			// The foreign ctxloom server must survive the merge.
			foreign, err := e.Installed(out, "ctxloom")
			require.NoError(t, err)
			assert.True(t, foreign, "foreign server must be preserved")
		})
	}
}

func TestClaudeCode_Install_PreservesProvenanceKeys(t *testing.T) {
	out, err := (claude.MCPRegistrar{}).Install([]byte(claudeFixture), TaskloomName, TaskloomServer())
	require.NoError(t, err)
	servers := jsonServers(t, out)
	ctx, ok := servers["ctxloom"].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, "ctxloom-auto", ctx["_ctxloom"], "foreign provenance keys must survive")
	assert.Equal(t, "${CLAUDE_PROJECT_DIR}", ctx["cwd"])
}

func TestEngines_Install_Idempotent(t *testing.T) {
	for _, e := range All() {
		t.Run(e.Name(), func(t *testing.T) {
			once, err := e.Install(nil, TaskloomName, TaskloomServer())
			require.NoError(t, err)
			twice, err := e.Install(once, TaskloomName, TaskloomServer())
			require.NoError(t, err)
			assert.Equal(t, string(once), string(twice), "re-install must be a no-op")
		})
	}
}

func TestEngines_Uninstall_RemovesOnlyOurs(t *testing.T) {
	fixtures := map[string]string{
		"claude-code": claudeFixture,
		"codex":       codexFixture,
		"kiro":        kiroFixture,
	}
	for _, e := range All() {
		t.Run(e.Name(), func(t *testing.T) {
			installed, err := e.Install([]byte(fixtures[e.Name()]), TaskloomName, TaskloomServer())
			require.NoError(t, err)
			out, err := e.Uninstall(installed, TaskloomName)
			require.NoError(t, err)
			gone, err := e.Installed(out, TaskloomName)
			require.NoError(t, err)
			assert.False(t, gone, "uninstall must remove the taskloom entry")
			foreign, err := e.Installed(out, "ctxloom")
			require.NoError(t, err)
			assert.True(t, foreign, "uninstall must not touch foreign servers")
		})
	}
}

func TestEngines_Uninstall_AbsentIsNoop(t *testing.T) {
	for _, e := range All() {
		t.Run(e.Name(), func(t *testing.T) {
			out, err := e.Uninstall(nil, TaskloomName)
			require.NoError(t, err)
			ok, err := e.Installed(out, TaskloomName)
			require.NoError(t, err)
			assert.False(t, ok)
		})
	}
}

func TestGet_AliasesAndUnknown(t *testing.T) {
	for _, alias := range []string{"claude", "claudecode", "claude-code", "CLAUDE"} {
		e, err := Get(alias)
		require.NoError(t, err, alias)
		assert.Equal(t, "claude-code", e.Name())
	}
	_, err := Get("cluade")
	assert.Error(t, err, "typos must error, not guess")
}

// TestKiro_RegisteredInEngineRegistry is the direct regression test for
// snowy-worst: kiro shipped no agent.MCPRegistrar while claude/codex each
// did, so `engine.All()` (what `taskloom manage install` walks
// when no --engine is named) silently never touched kiro's
// .kiro/settings/mcp.json — no error, no mention, just an absent entry.
// This pins kiro into the registry going forward (both by direct lookup and
// by appearing in the enumerated set the auto-register path iterates).
func TestKiro_RegisteredInEngineRegistry(t *testing.T) {
	e, err := Get("kiro")
	require.NoError(t, err)
	assert.Equal(t, "kiro", e.Name())

	var found bool
	for _, e := range All() {
		if e.Name() == "kiro" {
			found = true
		}
	}
	assert.True(t, found, "kiro must appear in All() for the auto-register (no --engine) path")
}

// TestKiro_GlobalScopeSupported pins kiro's global MCP config path: kiro-cli
// DOES read a home-rooted config
// ($KIRO_HOME/settings/mcp.json, default ~/.kiro/settings/mcp.json — the
// same home that holds agents/settings/skills/steering, per
// internal/kiro/session.go's storeDir), so global scope must succeed rather
// than error.
func TestKiro_GlobalScopeSupported(t *testing.T) {
	t.Setenv("KIRO_HOME", "/custom/kiro-home")
	p, err := (kiro.MCPRegistrar{}).ConfigPath("/proj", true)
	require.NoError(t, err)
	assert.Equal(t, "/custom/kiro-home/settings/mcp.json", p)
}

func TestConfigPath_Scopes(t *testing.T) {
	// The codex/kiro rows assert the DEFAULT (~/.codex / ~/.kiro) path; pin
	// CODEX_HOME/KIRO_HOME empty so an inherited value (the hostile-env suite
	// poisons it) can't redirect the home resolution.
	t.Setenv("CODEX_HOME", "")
	t.Setenv("KIRO_HOME", "")
	tests := []struct {
		engine string
		global bool
		suffix string
	}{
		{"claude-code", false, ".mcp.json"},
		{"claude-code", true, ".claude.json"},
		{"codex", false, ".codex/config.toml"},
		{"codex", true, ".codex/config.toml"},
		{"kiro", false, ".kiro/settings/mcp.json"},
		{"kiro", true, "settings/mcp.json"},
	}
	for _, tt := range tests {
		e, err := Get(tt.engine)
		require.NoError(t, err)
		p, err := e.ConfigPath("/proj", tt.global)
		require.NoError(t, err)
		if tt.global {
			assert.NotContains(t, p, "/proj", "%s global path must be home-rooted", tt.engine)
		} else {
			assert.Contains(t, p, "/proj", "%s project path must live under dir", tt.engine)
		}
		assert.Contains(t, p, tt.suffix)
	}
}
