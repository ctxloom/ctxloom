package main

import (
	"bytes"
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

// Having no backend to uninstall from is a legitimate empty state, so it
// stays exit 0 — but it must SAY so. Printing nothing at all is
// indistinguishable from a successful removal, and `manage install` in the
// identical situation is loud.
func TestManageUninstall_NoBackendsSaysSoAndSucceeds(t *testing.T) {
	fakeHome(t)
	var errOut bytes.Buffer
	assert.NoError(t, manageUninstall("", ".", true, &errOut),
		"nothing to remove is not a failure")
	assert.Contains(t, errOut.String(), "no agent backends detected",
		"a silent exit 0 reads as a successful removal")
}

// An engine returning empty bytes must never be made durable over the user's
// real backend config: iox.WriteFileAtomic would faithfully commit the
// truncation.
func TestWriteConfig_RefusesToTruncateToZeroBytes(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "mcp.json")
	original := `{"mcpServers":{"other":{"command":"x"}}}`
	require.NoError(t, os.WriteFile(path, []byte(original), 0o644))

	for _, empty := range [][]byte{nil, {}} {
		require.Error(t, writeConfig(path, empty), "an empty payload must be refused")
		got, err := os.ReadFile(path)
		require.NoError(t, err)
		assert.Equal(t, original, string(got), "the user's config must survive intact")
	}
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

// Uninstalling from a config that never carried the taskloom entry is a
// no-op, and must not be reported as a removal — nor rewrite the user's
// config file. "removed MCP server from claude-code" for a backend that was
// never registered is a success message for work that did not happen, and the
// rewrite reformats a file the user never asked us to touch.
func TestManageUninstall_NotRegisteredIsNotReportedAsRemoved(t *testing.T) {
	fakeHome(t)
	proj := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(proj, ".kiro", "settings"), 0o755))
	path := filepath.Join(proj, ".kiro", "settings", "mcp.json")
	// A real config carrying somebody else's server, deliberately formatted
	// unlike our writer's output so a rewrite is visible byte-for-byte.
	original := "{\n  \"mcpServers\": {\n    \"other\": {\"command\": \"x\"}\n  }\n}\n"
	require.NoError(t, os.WriteFile(path, []byte(original), 0o644))

	var errOut bytes.Buffer
	require.NoError(t, manageUninstall("kiro", proj, false, &errOut))

	assert.NotContains(t, errOut.String(), "removed MCP server",
		"reporting a removal that never happened is a success message for a no-op")
	assert.Contains(t, errOut.String(), "not registered",
		"the honest empty state must be stated, not left silent")
	got, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Equal(t, original, string(got),
		"a config with no taskloom entry must not be rewritten at all")
}

// A config `manage check` cannot READ is not the same as one that is absent,
// and reporting neither is the worst answer: the user asks "where am I
// registered?" and a permission-denied or wrong-type config drops out of the
// table with no trace, reading exactly like "this backend has no config".
func TestManageCheck_UnreadableConfigIsReportedNotSkipped(t *testing.T) {
	fakeHome(t)
	proj := t.TempDir()
	// A directory where the config file belongs: os.ReadFile fails with a
	// real error that is not fs.ErrNotExist, on every platform and every uid.
	require.NoError(t, os.MkdirAll(filepath.Join(proj, ".agents", "mcp_config.json"), 0o755))

	var out bytes.Buffer
	require.NoError(t, manageCheck(proj, &out))

	assert.Contains(t, out.String(), "unreadable",
		"a config that cannot be read must be reported, not silently skipped")
	assert.Contains(t, out.String(), filepath.Join(proj, ".agents", "mcp_config.json"),
		"the report must name the path that could not be read")
}
