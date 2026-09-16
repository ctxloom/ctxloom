package claude

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/afero"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/confpatch"
	"github.com/ctxloom/ctxloom/internal/shared/wire"
	"github.com/ctxloom/ctxloom/internal/testsupport"
)

func TestMCPRegistrar_Name(t *testing.T) {
	assert.Equal(t, "claude-code", MCPRegistrar{}.Name())
}

func TestMCPRegistrar_ConfigPath(t *testing.T) {
	p, err := (MCPRegistrar{}).ConfigPath("/proj", false)
	require.NoError(t, err)
	assert.Equal(t, filepath.Join("/proj", ".mcp.json"), p)

	g, err := (MCPRegistrar{}).ConfigPath("/proj", true)
	require.NoError(t, err)
	assert.True(t, strings.HasSuffix(g, ".claude.json"), g)
	assert.NotContains(t, g, "/proj", "global path is home-rooted")
}

// Register patches the named member in place: the foreign entry's members keep
// their spelling, order and one-line layout, and the uninstall hands back the
// original bytes exactly.
func TestMCPRegistrar_RegisterPreservesForeignBytesAndUninstallRestoresThem(t *testing.T) {
	const existing = "{\n  \"mcpServers\": {\n    \"ctxloom\": {\"_ctxloom\": \"ctxloom-auto\", \"command\": \"ctxloom\", \"cwd\": \"${CLAUDE_PROJECT_DIR}\"}\n  }\n}\n"
	fs := afero.NewMemMapFs()
	store, err := confpatch.NewStore(fs, "/home/u/.ctxloom/records/taskloom", "taskloom")
	require.NoError(t, err)
	const path = "/proj/.mcp.json"
	testsupport.WriteFileString(t, fs, path, existing, 0o644)
	r := MCPRegistrar{}
	server := wire.MCPServer{Command: "taskloom", Args: []string{"mcp"}}

	res, err := r.Register(fs, store, path, "taskloom", &server)
	require.NoError(t, err)
	assert.True(t, res.Changed)
	assert.NotEmpty(t, res.RecordPath, "the write is recorded")

	out, err := afero.ReadFile(fs, path)
	require.NoError(t, err)
	var doc map[string]map[string]map[string]any
	require.NoError(t, json.Unmarshal(out, &doc))
	assert.Equal(t, "taskloom", doc["mcpServers"]["taskloom"]["command"])
	assert.Contains(t, string(out), `"ctxloom": {"_ctxloom": "ctxloom-auto", "command": "ctxloom", "cwd": "${CLAUDE_PROJECT_DIR}"}`,
		"the foreign entry survives byte for byte")

	res, err = r.Register(fs, store, path, "taskloom", nil)
	require.NoError(t, err)
	assert.True(t, res.Changed)
	out, err = afero.ReadFile(fs, path)
	require.NoError(t, err)
	assert.Equal(t, existing, string(out), "uninstall restores the user's bytes exactly")
}
