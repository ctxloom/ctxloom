package opencode

import (
	"encoding/json"
	"testing"

	"github.com/spf13/afero"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/shared/agent"
	"github.com/ctxloom/ctxloom/internal/shared/wire"
)

// mcpObject decodes the `mcp` key of a written opencode.json into a name->entry map.
func mcpObject(t *testing.T, got map[string]any) map[string]any {
	t.Helper()
	m, ok := got["mcp"].(map[string]any)
	require.True(t, ok, "mcp key present and an object")
	return m
}

// TestWriteOpencodeConfig_MergesManagedKeysPreservingForeign proves the single
// merge engine writes mcp + permission + model while preserving every foreign key
// AND the earlier-written model key, all in one read-modify-write.
func TestWriteOpencodeConfig_MergesManagedKeysPreservingForeign(t *testing.T) {
	fs := afero.NewMemMapFs()
	require.NoError(t, fs.MkdirAll("/work", 0o755))
	existing := `{
  "$schema": "https://opencode.ai/config.json",
  "theme": "tokyonight",
  "model": "openrouter/OLD:free",
  "mcp": { "user-server": { "type": "local", "command": ["user-cmd"] } }
}`
	require.NoError(t, afero.WriteFile(fs, "/work/opencode.json", []byte(existing), 0o644))

	err := writeOpencodeConfig(fs, "/work", managedConfig{
		model: "openrouter/NEW:free",
		mcpServers: []agent.ChatMCPServer{
			{Name: "ctxloom", Command: "/abs/ctxloom", Args: []string{"mcp"}, Env: map[string]string{"K": "v"}},
		},
		readOnly: true,
	})
	require.NoError(t, err)

	got := readJSON(t, fs, "/work/opencode.json")
	// Foreign keys preserved.
	assert.Equal(t, "https://opencode.ai/config.json", got["$schema"])
	assert.Equal(t, "tokyonight", got["theme"])
	// Model updated.
	assert.Equal(t, "openrouter/NEW:free", got["model"])
	// Our MCP server added in opencode's local shape; the user's server preserved.
	mcp := mcpObject(t, got)
	assert.NotNil(t, mcp["user-server"], "foreign mcp entry preserved")
	ours, ok := mcp["ctxloom"].(map[string]any)
	require.True(t, ok, "ctxloom mcp entry present")
	assert.Equal(t, "local", ours["type"])
	assert.Equal(t, []any{"/abs/ctxloom", "mcp"}, ours["command"], "command is binary + args as one array")
	assert.Equal(t, map[string]any{"K": "v"}, ours["environment"], "env spelled `environment`")
	assert.Equal(t, true, ours["enabled"])
	// Read-only permission block present and denying the write/exec vectors.
	perm, ok := got["permission"].(map[string]any)
	require.True(t, ok, "permission block present")
	assert.Equal(t, "deny", perm["edit"], "edit denied (gates opencode's write tool)")
	assert.Equal(t, "deny", perm["bash"], "bash denied (closes the plan-agent shell hole)")
}

// TestWriteOpencodeConfig_ReadOnlyOverridesExistingPermission proves read-only is
// authoritative: an existing edit/bash "allow" is replaced with deny, while an
// unrelated user permission key survives.
func TestWriteOpencodeConfig_ReadOnlyOverridesExistingPermission(t *testing.T) {
	fs := afero.NewMemMapFs()
	require.NoError(t, fs.MkdirAll("/work", 0o755))
	existing := `{"permission": {"edit": "allow", "bash": "allow", "webfetch": "ask"}}`
	require.NoError(t, afero.WriteFile(fs, "/work/opencode.json", []byte(existing), 0o644))

	require.NoError(t, writeOpencodeConfig(fs, "/work", managedConfig{readOnly: true}))

	got := readJSON(t, fs, "/work/opencode.json")
	perm := got["permission"].(map[string]any)
	assert.Equal(t, "deny", perm["edit"], "user allow overridden to deny")
	assert.Equal(t, "deny", perm["bash"], "user allow overridden to deny")
	assert.Equal(t, "ask", perm["webfetch"], "unrelated user permission key preserved")
}

// TestWriteOpencodeConfig_MalformedErrors: a malformed existing file FAILS LOUDLY
// and leaves the original bytes intact.
func TestWriteOpencodeConfig_MalformedErrors(t *testing.T) {
	fs := afero.NewMemMapFs()
	require.NoError(t, fs.MkdirAll("/work", 0o755))
	garbage := `{ not valid json ]`
	require.NoError(t, afero.WriteFile(fs, "/work/opencode.json", []byte(garbage), 0o644))

	err := writeOpencodeConfig(fs, "/work", managedConfig{model: "openrouter/x:free"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "malformed")

	after, readErr := afero.ReadFile(fs, "/work/opencode.json")
	require.NoError(t, readErr)
	assert.Equal(t, garbage, string(after), "malformed bytes untouched")
}

// TestSnapshotRestore_RevertsTransientOverlay proves the chat-path snapshot
// restores opencode.json exactly (removing a plan run's read-only permission), and
// deletes a file it created when none existed.
func TestSnapshotRestore_RevertsTransientOverlay(t *testing.T) {
	t.Run("existing file restored verbatim", func(t *testing.T) {
		fs := afero.NewMemMapFs()
		require.NoError(t, fs.MkdirAll("/work", 0o755))
		orig := `{"model":"openrouter/mine:free"}`
		require.NoError(t, afero.WriteFile(fs, "/work/opencode.json", []byte(orig), 0o644))

		restore, err := snapshotOpencodeConfig(fs, "/work")
		require.NoError(t, err)
		require.NoError(t, writeOpencodeConfig(fs, "/work", managedConfig{readOnly: true}))
		// Overlay is present mid-run.
		mid := readJSON(t, fs, "/work/opencode.json")
		assert.NotNil(t, mid["permission"])

		require.NoError(t, restore())
		after, _ := afero.ReadFile(fs, "/work/opencode.json")
		assert.Equal(t, orig, string(after), "restored to original bytes; no permission left behind")
	})

	t.Run("created file removed on restore", func(t *testing.T) {
		fs := afero.NewMemMapFs()
		require.NoError(t, fs.MkdirAll("/work", 0o755))

		restore, err := snapshotOpencodeConfig(fs, "/work")
		require.NoError(t, err)
		require.NoError(t, writeOpencodeConfig(fs, "/work", managedConfig{model: "openrouter/x:free"}))
		exists, _ := afero.Exists(fs, "/work/opencode.json")
		require.True(t, exists)

		require.NoError(t, restore())
		exists, _ = afero.Exists(fs, "/work/opencode.json")
		assert.False(t, exists, "file we created is removed on restore")
	})
}

// TestWriteSettings_MCPAndRemoveOnlyOurs proves the settings writer adds the
// managed MCP servers, preserves a foreign server, and RemoveSettings drops ONLY
// the managed entries (via the ledger), leaving the foreign server and foreign keys.
func TestWriteSettings_MCPAndRemoveOnlyOurs(t *testing.T) {
	fs := afero.NewMemMapFs()
	w := &OpencodeWriter{FS: fs}
	require.NoError(t, fs.MkdirAll("/proj", 0o755))
	existing := `{
  "theme": "gruvbox",
  "mcp": { "user-server": { "type": "local", "command": ["user-cmd"] } }
}`
	require.NoError(t, afero.WriteFile(fs, "/proj/opencode.json", []byte(existing), 0o644))

	mcp := &wire.MCPConfig{Servers: map[string]wire.MCPServer{
		"proj-tool": {Command: "proj-cmd", Args: []string{"serve"}},
	}}
	require.NoError(t, w.WriteSettings(nil, mcp, nil, "/proj"))

	got := readJSON(t, fs, "/proj/opencode.json")
	servers := mcpObject(t, got)
	assert.NotNil(t, servers["ctxloom"], "auto-registered ctxloom server present")
	assert.NotNil(t, servers["proj-tool"], "config server present")
	assert.NotNil(t, servers["user-server"], "foreign server preserved")

	// Status sees a managed server.
	st, err := w.Status("/proj")
	require.NoError(t, err)
	assert.True(t, st.MCPPresent)

	require.NoError(t, w.RemoveSettings("/proj"))
	got = readJSON(t, fs, "/proj/opencode.json")
	servers = mcpObject(t, got)
	assert.Nil(t, servers["ctxloom"], "managed ctxloom server removed")
	assert.Nil(t, servers["proj-tool"], "managed config server removed")
	assert.NotNil(t, servers["user-server"], "foreign server survives removal")
	assert.Equal(t, "gruvbox", got["theme"], "foreign key survives removal")
}

// TestWriteContext_InstructionsAndFile proves context is delivered via the
// instructions key + a ctxloom-owned file, and cleanly removed, without touching a
// managed MCP entry in the same opencode.json.
func TestWriteContext_InstructionsAndFile(t *testing.T) {
	fs := afero.NewMemMapFs()
	w := &OpencodeWriter{FS: fs}
	require.NoError(t, fs.MkdirAll("/proj", 0o755))

	// Pre-seed an MCP entry via the settings writer (same file).
	require.NoError(t, w.WriteSettings(nil, nil, nil, "/proj"))

	_, err := w.WriteContext(agent.ContextWriteRequest{ProjectDir: "/proj", Context: "SENTINEL-CONTEXT"})
	require.NoError(t, err)

	// Context file written.
	body, err := afero.ReadFile(fs, "/proj/.opencode/ctxloom-context.md")
	require.NoError(t, err)
	assert.Contains(t, string(body), "SENTINEL-CONTEXT")

	got := readJSON(t, fs, "/proj/opencode.json")
	var instr []string
	require.NoError(t, json.Unmarshal(mustRaw(t, got, "instructions"), &instr))
	assert.Contains(t, instr, ".opencode/ctxloom-context.md")
	// MCP still present alongside instructions.
	assert.NotNil(t, mcpObject(t, got)["ctxloom"], "MCP entry untouched by context write")

	// Remove context: file gone, instructions gone, MCP still present.
	_, err = w.WriteContext(agent.ContextWriteRequest{ProjectDir: "/proj", Context: ""})
	require.NoError(t, err)
	exists, _ := afero.Exists(fs, "/proj/.opencode/ctxloom-context.md")
	assert.False(t, exists, "context file removed")
	got = readJSON(t, fs, "/proj/opencode.json")
	_, hasInstr := got["instructions"]
	assert.False(t, hasInstr, "instructions reference removed")
	assert.NotNil(t, mcpObject(t, got)["ctxloom"], "MCP entry preserved through context removal")
}

func mustRaw(t *testing.T, m map[string]any, key string) []byte {
	t.Helper()
	v, ok := m[key]
	require.True(t, ok, "key %q present", key)
	b, err := json.Marshal(v)
	require.NoError(t, err)
	return b
}
