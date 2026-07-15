package kiro

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/shared/wire"
)

func TestMCPRegistrar_Name(t *testing.T) {
	assert.Equal(t, "kiro", MCPRegistrar{}.Name())
}

func TestMCPRegistrar_ConfigPath(t *testing.T) {
	p, err := (MCPRegistrar{}).ConfigPath("/proj", false)
	require.NoError(t, err)
	assert.Equal(t, filepath.Join("/proj", ".kiro", "settings", "mcp.json"), p)

	// Global scope honors KIRO_HOME.
	t.Setenv("KIRO_HOME", "/custom/kiro-home")
	g, err := (MCPRegistrar{}).ConfigPath("/proj", true)
	require.NoError(t, err)
	assert.Equal(t, filepath.Join("/custom/kiro-home", "settings", "mcp.json"), g)
}

func TestMCPRegistrar_ConfigPath_GlobalDefaultsToHomeDotKiro(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("KIRO_HOME", "")

	g, err := (MCPRegistrar{}).ConfigPath("/proj", true)
	require.NoError(t, err)
	assert.Equal(t, filepath.Join(home, ".kiro", "settings", "mcp.json"), g)
}

func TestMCPRegistrar_Present(t *testing.T) {
	dir := t.TempDir()
	r := MCPRegistrar{}
	assert.False(t, r.Present(dir, false), "no .kiro dir → not present")
	require.NoError(t, os.MkdirAll(filepath.Join(dir, ".kiro"), 0o755))
	assert.True(t, r.Present(dir, false))

	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("KIRO_HOME", "")
	assert.False(t, r.Present(dir, true), "no home .kiro dir → not present globally")
	require.NoError(t, os.MkdirAll(filepath.Join(home, ".kiro"), 0o755))
	assert.True(t, r.Present(dir, true))
}

func TestMCPRegistrar_InstallUninstallRoundTrip(t *testing.T) {
	r := MCPRegistrar{}
	existing := `{"mcpServers": {"remote": {"serverUrl": "https://example.com/mcp"}}}`

	out, err := r.Install([]byte(existing), "taskloom", wire.MCPServer{Command: "taskloom", Args: []string{"mcp"}})
	require.NoError(t, err)
	ok, err := r.Installed(out, "taskloom")
	require.NoError(t, err)
	assert.True(t, ok)

	// Idempotent.
	again, err := r.Install(out, "taskloom", wire.MCPServer{Command: "taskloom", Args: []string{"mcp"}})
	require.NoError(t, err)
	assert.Equal(t, string(out), string(again))

	// Foreign remote server (serverUrl shape) survives install and uninstall —
	// the same "mcpServers" table shape kiro-cli itself reads from
	// .kiro/settings/mcp.json.
	removed, err := r.Uninstall(out, "taskloom")
	require.NoError(t, err)
	var doc map[string]map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(removed, &doc))
	assert.Contains(t, doc["mcpServers"], "remote")
	assert.NotContains(t, doc["mcpServers"], "taskloom")
}
