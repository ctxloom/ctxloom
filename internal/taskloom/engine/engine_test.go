package engine

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/claude"
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

func TestConfigPath_Scopes(t *testing.T) {
	// The codex row asserts the DEFAULT (~/.codex) path; pin CODEX_HOME empty
	// so an inherited value (the hostile-env suite poisons it) can't redirect
	// the home resolution.
	t.Setenv("CODEX_HOME", "")
	tests := []struct {
		engine string
		global bool
		suffix string
	}{
		{"claude-code", false, ".mcp.json"},
		{"claude-code", true, ".claude.json"},
		// codex's project scope is a DECLARED ABSENCE (launch-only settings;
		// no durable project home) — asserted separately below, not a row here.
		{"codex", true, ".codex/config.toml"},
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

	// codex project scope: the declared absence, not a path. The error names
	// the launch-only reason so a caller can tell "no file by design" from a
	// resolution bug; taskloom's engine adapter degrades Present to false on it.
	codexEng, err := Get("codex")
	require.NoError(t, err)
	_, err = codexEng.ConfigPath("/proj", false)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "delivered per-session at launch")
}
