package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeHome points HOME at a temp dir and returns it, so manage never touches
// the real user configs.
func fakeHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	return home
}

func readServers(t *testing.T, path string) map[string]any {
	t.Helper()
	raw, err := os.ReadFile(path)
	require.NoError(t, err)
	var doc map[string]any
	require.NoError(t, json.Unmarshal(raw, &doc))
	servers, _ := doc["mcpServers"].(map[string]any)
	return servers
}

func TestManageInstall_AutoRegistersOnlyPresentBackends(t *testing.T) {
	home := fakeHome(t)
	// Only claude is "present" on this machine.
	require.NoError(t, os.MkdirAll(filepath.Join(home, ".claude"), 0o755))

	require.NoError(t, manageInstall("", ".", true, false, os.Stderr))

	servers := readServers(t, filepath.Join(home, ".claude.json"))
	require.Contains(t, servers, "taskloom")
	entry := servers["taskloom"].(map[string]any)
	assert.Equal(t, "taskloom", entry["command"])

	// Absent backends must not have configs conjured for them.
	assert.NoDirExists(t, filepath.Join(home, ".codex"))
	assert.NoDirExists(t, filepath.Join(home, ".kiro"))
}

// TestManageInstall_AutoRegistersKiroAlongsideOtherBackends is the
// silent-no-op regression test (task snowy-worst): before kiro shipped its
// own agent.MCPRegistrar, engine.All() omitted it entirely, so an
// auto-register install with kiro AND another backend present would
// register the taskloom MCP server for the other backend and silently say
// nothing about kiro — no error, zero bytes written to
// $KIRO_HOME/settings/mcp.json. This asserts the PAYLOAD kiro-cli actually
// reads (the "mcpServers" table in settings/mcp.json), not just an exit
// code, so a registrar that runs but writes the wrong file/key would still
// fail this test.
func TestManageInstall_AutoRegistersKiroAlongsideOtherBackends(t *testing.T) {
	home := fakeHome(t)
	// Both claude and kiro are "present" on this machine.
	require.NoError(t, os.MkdirAll(filepath.Join(home, ".claude"), 0o755))
	require.NoError(t, os.MkdirAll(filepath.Join(home, ".kiro"), 0o755))

	require.NoError(t, manageInstall("", ".", true, false, os.Stderr))

	claudeServers := readServers(t, filepath.Join(home, ".claude.json"))
	require.Contains(t, claudeServers, "taskloom", "claude must still be registered")

	kiroServers := readServers(t, filepath.Join(home, ".kiro", "settings", "mcp.json"))
	require.Contains(t, kiroServers, "taskloom", "kiro must be registered, not silently skipped")
	entry := kiroServers["taskloom"].(map[string]any)
	assert.Equal(t, "taskloom", entry["command"])
}

func TestManageInstall_ExplicitEngineCreatesConfig(t *testing.T) {
	home := fakeHome(t)
	// claude is not "present", but the user asked for it by name.
	require.NoError(t, manageInstall("claude", ".", true, false, os.Stderr))
	servers := readServers(t, filepath.Join(home, ".claude.json"))
	assert.Contains(t, servers, "taskloom")
}

func TestManageInstall_ProjectScope(t *testing.T) {
	fakeHome(t)
	proj := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(proj, ".claude"), 0o755))

	require.NoError(t, manageInstall("", proj, false, false, os.Stderr))

	servers := readServers(t, filepath.Join(proj, ".mcp.json"))
	assert.Contains(t, servers, "taskloom")
}

func TestManageInstall_KiroExplicitEngineCreatesConfig(t *testing.T) {
	home := fakeHome(t)
	// kiro is not "present", but the user asked for it by name.
	require.NoError(t, manageInstall("kiro", ".", true, false, os.Stderr))
	servers := readServers(t, filepath.Join(home, ".kiro", "settings", "mcp.json"))
	assert.Contains(t, servers, "taskloom")
}

func TestManageInstall_KiroProjectScope(t *testing.T) {
	fakeHome(t)
	proj := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(proj, ".kiro"), 0o755))

	require.NoError(t, manageInstall("", proj, false, false, os.Stderr))

	servers := readServers(t, filepath.Join(proj, ".kiro", "settings", "mcp.json"))
	assert.Contains(t, servers, "taskloom")
}

func TestManageInstall_KiroPreservesExistingServers(t *testing.T) {
	fakeHome(t)
	proj := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(proj, ".kiro", "settings"), 0o755))
	existing := `{"mcpServers": {"remote": {"serverUrl": "https://example.com/mcp"}}}`
	require.NoError(t, os.WriteFile(filepath.Join(proj, ".kiro", "settings", "mcp.json"), []byte(existing), 0o644))

	require.NoError(t, manageInstall("kiro", proj, false, false, os.Stderr))

	servers := readServers(t, filepath.Join(proj, ".kiro", "settings", "mcp.json"))
	assert.Contains(t, servers, "remote", "foreign servers must survive")
	assert.Contains(t, servers, "taskloom")
}

func TestManageInstall_PreservesExistingServers(t *testing.T) {
	fakeHome(t)
	proj := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(proj, ".agents"), 0o755))
	existing := `{"mcpServers": {"ctxloom": {"command": "ctxloom", "args": ["mcp"]}}}`
	require.NoError(t, os.WriteFile(filepath.Join(proj, ".agents", "mcp_config.json"), []byte(existing), 0o644))

	require.NoError(t, manageInstall("antigravity", proj, false, false, os.Stderr))

	servers := readServers(t, filepath.Join(proj, ".agents", "mcp_config.json"))
	assert.Contains(t, servers, "ctxloom", "foreign servers must survive")
	assert.Contains(t, servers, "taskloom")
}

func TestManageUninstall_RemovesEntry(t *testing.T) {
	fakeHome(t)
	proj := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(proj, ".agents"), 0o755))
	require.NoError(t, manageInstall("antigravity", proj, false, false, os.Stderr))

	require.NoError(t, manageUninstall("antigravity", proj, false, os.Stderr))

	servers := readServers(t, filepath.Join(proj, ".agents", "mcp_config.json"))
	assert.NotContains(t, servers, "taskloom")
}

func TestManageUninstall_NoConfigIsNoop(t *testing.T) {
	fakeHome(t)
	assert.NoError(t, manageUninstall("", ".", true, os.Stderr), "nothing installed → quiet no-op")
}

func TestManageUninstall_RemovesKiroEntry(t *testing.T) {
	fakeHome(t)
	proj := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(proj, ".kiro"), 0o755))
	require.NoError(t, manageInstall("kiro", proj, false, false, os.Stderr))

	require.NoError(t, manageUninstall("kiro", proj, false, os.Stderr))

	servers := readServers(t, filepath.Join(proj, ".kiro", "settings", "mcp.json"))
	assert.NotContains(t, servers, "taskloom")
}
