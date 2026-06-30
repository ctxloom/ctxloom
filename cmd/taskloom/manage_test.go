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
