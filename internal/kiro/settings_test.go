package kiro

import (
	"encoding/json"
	"testing"

	"github.com/spf13/afero"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/shared/agent"
	"github.com/ctxloom/ctxloom/internal/shared/wire"
)

func newTestWriter() (*KiroWriter, afero.Fs) {
	fs := afero.NewMemMapFs()
	return &KiroWriter{FS: fs}, fs
}

func readAgent(t *testing.T, fs afero.Fs, projectDir string) kiroAgent {
	t.Helper()
	data, err := afero.ReadFile(fs, projectDir+"/.kiro/agents/ctxloom.json")
	require.NoError(t, err)
	var a kiroAgent
	require.NoError(t, json.Unmarshal(data, &a))
	return a
}

func TestKiroWriter_AgentConfigAndHookMapping(t *testing.T) {
	w, fs := newTestWriter()
	hooks := &wire.HooksConfig{Unified: wire.UnifiedHooks{
		PreShell:     []wire.Hook{{Command: "ltk evaluate"}},
		PostFileEdit: []wire.Hook{{Command: "ctxloom hook stamp-plan"}},
		PreTool:      []wire.Hook{{Matcher: "fs_read", Command: "audit"}},
		SessionEnd:   []wire.Hook{{Command: "ctxloom hook session-bind"}},
	}}
	require.NoError(t, w.WriteSettings(hooks, nil, nil, "/proj"))

	a := readAgent(t, fs, "/proj")
	assert.Equal(t, defaultAgentName, a.Name)
	assert.True(t, a.IncludeMCPJSON)
	assert.Equal(t, []string{kiroSkillsGlob}, a.Resources)
	require.NotNil(t, a.Hooks)
	// PreShell defaults to the execute_bash matcher; ltk rewrite stays a hook.
	assert.Contains(t, a.Hooks.PreToolUse, kiroHook{Matcher: kiroShellMatcher, Command: "ltk evaluate"})
	// explicit matcher is preserved.
	assert.Contains(t, a.Hooks.PreToolUse, kiroHook{Matcher: "fs_read", Command: "audit"})
	// PostFileEdit defaults to the fs_write matcher.
	assert.Contains(t, a.Hooks.PostToolUse, kiroHook{Matcher: kiroFileEditMatcher, Command: "ctxloom hook stamp-plan"})
	// SessionEnd → stop.
	assert.Contains(t, a.Hooks.Stop, kiroHook{Command: "ctxloom hook session-bind"})
}

func TestKiroWriter_NonCommandHookSkipped(t *testing.T) {
	w, fs := newTestWriter()
	hooks := &wire.HooksConfig{Unified: wire.UnifiedHooks{
		PreTool: []wire.Hook{{Type: "prompt", Prompt: "hi"}},
	}}
	require.NoError(t, w.WriteSettings(hooks, nil, nil, "/proj"))
	a := readAgent(t, fs, "/proj")
	assert.Nil(t, a.Hooks) // the only hook was a non-command type and was dropped
}

func TestKiroWriter_ContextDivertedToSteering(t *testing.T) {
	w, fs := newTestWriter()
	hash, err := agent.WriteContextFile("/proj", []*agent.Fragment{{Content: "PROJECT RULES"}}, agent.WithContextFS(fs))
	require.NoError(t, err)

	hooks := &wire.HooksConfig{Unified: wire.UnifiedHooks{
		SessionStart: []wire.Hook{{ContextHash: hash}},
	}}
	require.NoError(t, w.WriteSettings(hooks, nil, nil, "/proj"))

	steering, err := afero.ReadFile(fs, "/proj/.kiro/steering/ctxloom-context.md")
	require.NoError(t, err)
	assert.Contains(t, string(steering), "inclusion: always")
	assert.Contains(t, string(steering), "PROJECT RULES")

	// the context hash must NOT be registered as an agentSpawn hook.
	a := readAgent(t, fs, "/proj")
	assert.Nil(t, a.Hooks)
}

func TestKiroWriter_MCPPreservesUserAndLedgers(t *testing.T) {
	w, fs := newTestWriter()
	require.NoError(t, afero.WriteFile(fs, "/proj/.kiro/settings/mcp.json",
		[]byte(`{"mcpServers":{"user-srv":{"command":"user","args":["x"]}}}`), 0644))

	mcp := &wire.MCPConfig{Servers: map[string]wire.MCPServer{
		"weather": {Command: "npx", Args: []string{"-y", "weather-mcp"}},
	}}
	require.NoError(t, w.WriteSettings(nil, mcp, nil, "/proj"))

	data, err := afero.ReadFile(fs, "/proj/.kiro/settings/mcp.json")
	require.NoError(t, err)
	var f struct {
		MCPServers map[string]json.RawMessage `json:"mcpServers"`
	}
	require.NoError(t, json.Unmarshal(data, &f))
	assert.Contains(t, f.MCPServers, "user-srv")             // user entry preserved
	assert.Contains(t, f.MCPServers, "weather")              // managed server added
	assert.Contains(t, f.MCPServers, agent.MCPServerName)    // ctxloom auto-registered

	ledger, err := afero.ReadFile(fs, "/proj/.kiro/settings/"+kiroMCPLedger)
	require.NoError(t, err)
	assert.Contains(t, string(ledger), "weather")
	assert.Contains(t, string(ledger), agent.MCPServerName)
	assert.NotContains(t, string(ledger), "user-srv") // user server is not managed
}

func TestKiroWriter_RemoveSettings(t *testing.T) {
	w, fs := newTestWriter()
	hash, _ := agent.WriteContextFile("/proj", []*agent.Fragment{{Content: "ctx"}}, agent.WithContextFS(fs))
	hooks := &wire.HooksConfig{Unified: wire.UnifiedHooks{
		SessionStart: []wire.Hook{{ContextHash: hash}},
		PreTool:      []wire.Hook{{Command: "x"}},
	}}
	mcp := &wire.MCPConfig{Servers: map[string]wire.MCPServer{"weather": {Command: "npx"}}}
	require.NoError(t, w.WriteSettings(hooks, mcp, nil, "/proj"))

	require.NoError(t, w.RemoveSettings("/proj"))

	for _, p := range []string{
		"/proj/.kiro/agents/ctxloom.json",
		"/proj/.kiro/steering/ctxloom-context.md",
	} {
		exists, _ := afero.Exists(fs, p)
		assert.Falsef(t, exists, "expected %s removed", p)
	}
	data, _ := afero.ReadFile(fs, "/proj/.kiro/settings/mcp.json")
	assert.NotContains(t, string(data), "weather")
	assert.NotContains(t, string(data), agent.MCPServerName)
}

func TestKiroWriter_Status(t *testing.T) {
	w, fs := newTestWriter()

	st, err := w.Status("/proj")
	require.NoError(t, err)
	assert.False(t, st.Wired())

	hooks := &wire.HooksConfig{Unified: wire.UnifiedHooks{PreTool: []wire.Hook{{Command: "x"}}}}
	require.NoError(t, w.WriteSettings(hooks, &wire.MCPConfig{}, nil, "/proj"))

	st, err = w.Status("/proj")
	require.NoError(t, err)
	assert.True(t, st.SettingsExists)
	assert.True(t, st.HooksPresent)
	assert.True(t, st.MCPPresent) // ctxloom server auto-registered
	assert.True(t, st.Wired())
	_ = fs
}
