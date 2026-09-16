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
}

func TestManageInstall_ExplicitEngineCreatesConfig(t *testing.T) {
	home := fakeHome(t)
	// claude is not "present", but the user asked for it by name.
	require.NoError(t, manageInstall("claude-code", ".", true, false, os.Stderr))
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

// Uninstalling from a config that never carried the taskloom entry is a
// no-op, and must not be reported as a removal — nor rewrite the user's
// config file. "removed MCP server from claude-code" for a backend that was
// never registered is a success message for work that did not happen, and the
// rewrite reformats a file the user never asked us to touch.
func TestManageUninstall_NotRegisteredIsNotReportedAsRemoved(t *testing.T) {
	fakeHome(t)
	proj := t.TempDir()
	path := filepath.Join(proj, ".mcp.json")
	// A real config carrying somebody else's server, deliberately formatted
	// unlike our writer's output so a rewrite is visible byte-for-byte.
	original := "{\n  \"mcpServers\": {\n    \"other\": {\"command\": \"x\"}\n  }\n}\n"
	require.NoError(t, os.WriteFile(path, []byte(original), 0o644))

	var errOut bytes.Buffer
	require.NoError(t, manageUninstall("claude-code", proj, false, &errOut))

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
	require.NoError(t, os.MkdirAll(filepath.Join(proj, ".mcp.json"), 0o755))

	var out bytes.Buffer
	require.NoError(t, manageCheck(proj, &out))

	assert.Contains(t, out.String(), "unreadable",
		"a config that cannot be read must be reported, not silently skipped")
	assert.Contains(t, out.String(), filepath.Join(proj, ".mcp.json"),
		"the report must name the path that could not be read")
}
