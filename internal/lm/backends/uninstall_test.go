package backends

import (
	"encoding/json"
	"testing"

	"github.com/ctxloom/ctxloom/internal/shared/agent"
	"github.com/ctxloom/ctxloom/internal/shared/wire"
	"github.com/spf13/afero"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ctxloomManagedHooks is a hook set whose command is recognized as
// ctxloom-managed (executable token "ctxloom").
func ctxloomManagedHooks() *wire.HooksConfig {
	return &wire.HooksConfig{
		Unified: wire.UnifiedHooks{
			SessionStart: []wire.Hook{{Command: "ctxloom hook inject-context"}},
		},
	}
}

func TestClaudeCodeRemoveSettings_StripsManagedPreservesUser(t *testing.T) {
	fs := afero.NewMemMapFs()
	const dir = "/project"

	// Seed a user-owned hook and a user-owned MCP server that ctxloom must not touch.
	require.NoError(t, fs.MkdirAll(dir+"/.claude", 0755))
	userSettings := `{"hooks":{"PreToolUse":[{"hooks":[{"type":"command","command":"./user.sh"}]}]}}`
	require.NoError(t, afero.WriteFile(fs, dir+"/.claude/settings.json", []byte(userSettings), 0644))
	userMCP := `{"mcpServers":{"user-server":{"command":"./user-mcp"}}}`
	require.NoError(t, afero.WriteFile(fs, dir+"/.mcp.json", []byte(userMCP), 0644))

	// Wire ctxloom hooks, statusline (auto), and ctxloom's own MCP server.
	deliverManagedSettings(t, "claude-code", ctxloomManagedHooks(), map[string]wire.MCPServer{agent.MCPServerName: {Command: agent.CtxloomBinary, Args: []string{"mcp", "serve"}}}, true, dir, fs)

	// Sanity: ctxloom is wired before removal.
	before, err := BackendStatus("claude-code", dir, WithSettingsFS(fs))
	require.NoError(t, err)
	require.True(t, before.Wired(), "ctxloom should be wired after delivery")
	require.True(t, before.StatusLine)
	require.True(t, before.MCPPresent)

	require.NoError(t, RemoveSettings("claude-code", dir, WithSettingsFS(fs)))

	after, err := BackendStatus("claude-code", dir, WithSettingsFS(fs))
	require.NoError(t, err)
	assert.False(t, after.Wired(), "no ctxloom artifacts should remain")

	// The user's hook and MCP server survive.
	settings := readJSON(t, fs, dir+"/.claude/settings.json")
	assert.Contains(t, mustMarshal(t, settings), "./user.sh")
	mcp := readJSON(t, fs, dir+"/.mcp.json")
	assert.Contains(t, mustMarshal(t, mcp), "user-server")
}

func TestRemoveSettings_AbsentFilesAreNoOp(t *testing.T) {
	fs := afero.NewMemMapFs()

	require.NoError(t, RemoveSettings("claude-code", "/empty", WithSettingsFS(fs)))
	require.NoError(t, RemoveSettings("antigravity", "/empty", WithSettingsFS(fs)))

	// Uninstall must never create config files.
	exists, _ := afero.Exists(fs, "/empty/.claude/settings.json")
	assert.False(t, exists)
	exists, _ = afero.Exists(fs, "/empty/.agents/hooks.json")
	assert.False(t, exists)
	exists, _ = afero.Exists(fs, "/empty/.agents/mcp_config.json")
	assert.False(t, exists)
}

// A settings-writer failure surfaced through RemoveSettings must
// name the backend it came from — a caller looping over multiple backends
// (operations.RemoveHooks) cannot otherwise attribute the failure.
func TestRemoveSettings_FailureNamesBackend(t *testing.T) {
	fs := afero.NewMemMapFs()
	const dir = "/project"
	require.NoError(t, fs.MkdirAll(dir+"/.claude", 0755))
	// Malformed settings.json: the writer's loadSettings must fail.
	require.NoError(t, afero.WriteFile(fs, dir+"/.claude/settings.json", []byte("{not valid json"), 0644))

	err := RemoveSettings("claude-code", dir, WithSettingsFS(fs))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "claude-code",
		"a settings-writer failure must name the backend it came from: got %q", err.Error())
}

// BackendStatus must be able to tell "typo'd/unregistered name"
// apart from "registered backend that genuinely has no settings support"
// (acp, mock) — both used to return a zero SettingsStatus and a nil error,
// so a caller passing a typo got a clean, empty, successful-looking read.
// An UNREGISTERED name now errors; a registered-but-no-writer backend still
// reports an empty status with a nil error (that IS a legitimate "nothing to
// report" — the case TestBackendStatus_UnsupportedBackendIsUnwired covers is
// renamed to reflect this).
func TestBackendStatus_UnregisteredBackendErrors(t *testing.T) {
	_, err := BackendStatus("unknown-backend", "/project")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unknown-backend")
}

func TestBackendStatus_RegisteredNoWriterBackendIsUnwiredNoError(t *testing.T) {
	// "acp" is a REGISTERED backend that deliberately has no settings writer
	// (no native config format to materialize) — this must stay a clean,
	// error-free empty read, unlike an unregistered name.
	status, err := BackendStatus("acp", "/project")
	require.NoError(t, err)
	assert.False(t, status.Wired())
	assert.False(t, status.SettingsExists)
}

func readJSON(t *testing.T, fs afero.Fs, path string) map[string]any {
	t.Helper()
	data, err := afero.ReadFile(fs, path)
	require.NoError(t, err)
	var m map[string]any
	require.NoError(t, json.Unmarshal(data, &m))
	return m
}

func mustMarshal(t *testing.T, v any) string {
	t.Helper()
	data, err := json.Marshal(v)
	require.NoError(t, err)
	return string(data)
}
