package kiro

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/spf13/afero"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/shared/agent"
	"github.com/ctxloom/ctxloom/internal/shared/ledger"
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
	assert.Contains(t, f.MCPServers, "user-srv")          // user entry preserved
	assert.Contains(t, f.MCPServers, "weather")           // managed server added
	assert.Contains(t, f.MCPServers, agent.MCPServerName) // ctxloom auto-registered

	ledger, err := afero.ReadFile(fs, filepath.Join("/proj/.kiro/settings", ledger.Name))
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

// TestKiroWriter_Status_CorruptAgentJSONIsAnError pins the invariant codex's and
// antigravity's Status already state in prose ("an unreadable config is an
// error, not a 'not configured' report" / "reported status must never be a
// guess"): a materialized-but-corrupt .kiro/agents/<name>.json means the answer
// is UNKNOWN, which is not the same fact as "no managed hooks are wired". Kiro
// used to swallow the unmarshal error and report the file as present-but-
// hookless, so `ctxloom status` said "not wired" about a project whose agent
// JSON it could not read at all.
func TestKiroWriter_Status_CorruptAgentJSONIsAnError(t *testing.T) {
	w, fs := newTestWriter()
	require.NoError(t, afero.WriteFile(fs, "/proj/.kiro/agents/ctxloom.json", []byte("{not json"), 0o644))

	_, err := w.Status("/proj")
	require.Error(t, err, "a corrupt agent JSON must be reported as broken, not silently as unwired")
	assert.Contains(t, err.Error(), "ctxloom.json")
}

// TestKiroWriter_Status_UnreadableAgentJSONIsAnError covers the other swallowed
// arm: a read failure that is NOT "file absent". Absence is the legitimate
// not-configured answer; any other error means Status cannot know.
func TestKiroWriter_Status_UnreadableAgentJSONIsAnError(t *testing.T) {
	base := afero.NewMemMapFs()
	require.NoError(t, afero.WriteFile(base, "/proj/.kiro/agents/ctxloom.json", []byte("{}"), 0o644))
	w := &KiroWriter{FS: &readFailFs{Fs: base, failOn: "/proj/.kiro/agents/ctxloom.json"}}

	_, err := w.Status("/proj")
	require.Error(t, err, "an unreadable agent JSON must not be reported as an unconfigured project")
}

// TestKiroWriter_Status_UnreadableSteeringIsAnError covers the third swallowed
// arm: the steering file is kiro's stand-in for the SessionStart injection hook,
// so failing to determine whether it exists is failing to determine wired-ness.
func TestKiroWriter_Status_UnreadableSteeringIsAnError(t *testing.T) {
	base := afero.NewMemMapFs()
	require.NoError(t, afero.WriteFile(base, "/proj/.kiro/steering/ctxloom-context.md", []byte("x"), 0o644))
	w := &KiroWriter{FS: &statFailFs{Fs: base, failOn: "/proj/.kiro/steering/ctxloom-context.md"}}

	_, err := w.Status("/proj")
	require.Error(t, err, "an undeterminable steering file must not silently read as 'no managed hook'")
}

// TestKiroWriter_RemoveSettings_UndeterminableAgentFileIsAnError is the other
// half of the same bug: RemoveSettings drops afero.Exists' error exactly as
// Present drops os.Stat's, so an agent config that exists but cannot be stat'ed
// is silently skipped and uninstall reports success with the ctxloom-owned file
// still on disk — exit 0, success message, nothing removed.
func TestKiroWriter_RemoveSettings_UndeterminableAgentFileIsAnError(t *testing.T) {
	base := afero.NewMemMapFs()
	require.NoError(t, afero.WriteFile(base, "/proj/.kiro/agents/ctxloom.json", []byte("{}"), 0o644))
	w := &KiroWriter{FS: &statFailFs{Fs: base, failOn: "/proj/.kiro/agents/ctxloom.json"}}

	err := w.RemoveSettings("/proj")
	require.Error(t, err, "uninstall must not report success over a file it could not even look at")

	exists, _ := afero.Exists(base, "/proj/.kiro/agents/ctxloom.json")
	assert.True(t, exists, "the file really is still there — the success report was the lie")
}

// TestKiroWriter_WriteContext_UndeterminableSteeringIsAnError covers the third
// site of the same swallow, and the loudest one: writeSteering's empty-content
// arm skipped the removal when Exists failed and STILL returned
// ContextReport{Removed: ...}. It reported having removed a file it had not
// touched — the payload lie, not merely a missing diagnostic.
func TestKiroWriter_WriteContext_UndeterminableSteeringIsAnError(t *testing.T) {
	base := afero.NewMemMapFs()
	require.NoError(t, afero.WriteFile(base, "/proj/.kiro/steering/ctxloom-context.md", []byte("old"), 0o644))
	w := &KiroWriter{FS: &statFailFs{Fs: base, failOn: "/proj/.kiro/steering/ctxloom-context.md"}}

	report, err := w.WriteContext(agent.ContextWriteRequest{ProjectDir: "/proj"})
	require.Error(t, err)
	assert.Empty(t, report.Removed, "nothing may be reported removed when removal never happened")

	data, rerr := afero.ReadFile(base, "/proj/.kiro/steering/ctxloom-context.md")
	require.NoError(t, rerr)
	assert.Equal(t, "old", string(data), "the previous delivery is still on disk")
}

// readFailFs fails Open for one exact path with a non-NotExist error, modelling
// EACCES on an otherwise-present file.
type readFailFs struct {
	afero.Fs
	failOn string
}

func (f *readFailFs) Open(name string) (afero.File, error) {
	if name == f.failOn {
		return nil, os.ErrPermission
	}
	return f.Fs.Open(name)
}

// statFailFs fails Stat for one exact path with a non-NotExist error.
type statFailFs struct {
	afero.Fs
	failOn string
}

func (f *statFailFs) Stat(name string) (os.FileInfo, error) {
	if name == f.failOn {
		return nil, os.ErrPermission
	}
	return f.Fs.Stat(name)
}
