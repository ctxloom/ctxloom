package engine

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/spf13/afero"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/claude"
	"github.com/ctxloom/ctxloom/internal/confpatch"
)

// Fixtures modeled on real backend configs: each holds a foreign server and
// (for claude) foreign provenance keys that a merge must not disturb.
const claudeFixture = `{
  "mcpServers": {
    "ctxloom": {
      "_ctxloom": "ctxloom-auto",
      "args": ["mcp"],
      "command": "ctxloom",
      "cwd": "${CLAUDE_PROJECT_DIR}"
    }
  }
}`

func jsonServers(t *testing.T, config []byte) map[string]any {
	t.Helper()
	var doc map[string]any
	require.NoError(t, json.Unmarshal(config, &doc))
	servers, _ := doc["mcpServers"].(map[string]any)
	return servers
}

// registrar is one engine with a fresh record store and an in-memory config
// at path, so each test starts from the bytes it names.
func registrar(t *testing.T, e Engine, config string) (afero.Fs, *confpatch.Store, string) {
	t.Helper()
	fs := afero.NewMemMapFs()
	store, err := confpatch.NewStore(fs, "/home/u/.ctxloom/records/taskloom", TaskloomCommand)
	require.NoError(t, err)
	path := "/proj/" + strings.TrimPrefix(e.Name(), "claude-code") + ".mcp.json"
	if config != "" {
		require.NoError(t, afero.WriteFile(fs, path, []byte(config), 0o644))
	}
	return fs, store, path
}

func TestEngines_RegisterIntoAbsentConfig_CreatesEntry(t *testing.T) {
	server := TaskloomServer()
	for _, e := range All() {
		t.Run(e.Name(), func(t *testing.T) {
			fs, store, path := registrar(t, e, "")
			res, err := e.Register(fs, store, path, TaskloomName, &server)
			require.NoError(t, err)
			assert.True(t, res.Changed)
			out, err := afero.ReadFile(fs, path)
			require.NoError(t, err)
			ok, err := e.Installed(out, TaskloomName)
			require.NoError(t, err)
			assert.True(t, ok, "fresh install must register the server")
		})
	}
}

func TestEngines_Register_PreservesForeignContent(t *testing.T) {
	fixtures := map[string]string{
		"claude-code": claudeFixture,
	}
	server := TaskloomServer()
	for _, e := range All() {
		t.Run(e.Name(), func(t *testing.T) {
			fs, store, path := registrar(t, e, fixtures[e.Name()])
			_, err := e.Register(fs, store, path, TaskloomName, &server)
			require.NoError(t, err)
			out, err := afero.ReadFile(fs, path)
			require.NoError(t, err)
			ok, err := e.Installed(out, TaskloomName)
			require.NoError(t, err)
			assert.True(t, ok)
			foreign, err := e.Installed(out, "ctxloom")
			require.NoError(t, err)
			assert.True(t, foreign, "foreign server must be preserved")
		})
	}
}

func TestClaudeCode_Register_PreservesProvenanceKeysByteForByte(t *testing.T) {
	e := claude.MCPRegistrar{}
	server := TaskloomServer()
	fs, store, path := registrar(t, e, claudeFixture)
	_, err := e.Register(fs, store, path, TaskloomName, &server)
	require.NoError(t, err)
	out, err := afero.ReadFile(fs, path)
	require.NoError(t, err)
	servers := jsonServers(t, out)
	ctx, ok := servers["ctxloom"].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, "ctxloom-auto", ctx["_ctxloom"], "foreign provenance keys must survive")
	assert.Equal(t, "${CLAUDE_PROJECT_DIR}", ctx["cwd"])
	// Not merely present but UNTOUCHED: the foreign entry's own lines, in
	// the fixture's order and layout, are still in the file.
	assert.Contains(t, string(out), "\"_ctxloom\": \"ctxloom-auto\",\n      \"args\": [\"mcp\"],\n      \"command\": \"ctxloom\",\n      \"cwd\": \"${CLAUDE_PROJECT_DIR}\"")
}

func TestEngines_Register_Idempotent(t *testing.T) {
	server := TaskloomServer()
	for _, e := range All() {
		t.Run(e.Name(), func(t *testing.T) {
			fs, store, path := registrar(t, e, "")
			_, err := e.Register(fs, store, path, TaskloomName, &server)
			require.NoError(t, err)
			once, err := afero.ReadFile(fs, path)
			require.NoError(t, err)
			res, err := e.Register(fs, store, path, TaskloomName, &server)
			require.NoError(t, err)
			assert.False(t, res.Changed, "re-install must be a no-op")
			twice, err := afero.ReadFile(fs, path)
			require.NoError(t, err)
			assert.Equal(t, string(once), string(twice))
		})
	}
}

func TestEngines_Uninstall_RestoresTheFixtureExactly(t *testing.T) {
	fixtures := map[string]string{
		"claude-code": claudeFixture,
	}
	server := TaskloomServer()
	for _, e := range All() {
		t.Run(e.Name(), func(t *testing.T) {
			fs, store, path := registrar(t, e, fixtures[e.Name()])
			_, err := e.Register(fs, store, path, TaskloomName, &server)
			require.NoError(t, err)
			res, err := e.Register(fs, store, path, TaskloomName, nil)
			require.NoError(t, err)
			assert.True(t, res.Changed, "the entry was there to remove")
			out, err := afero.ReadFile(fs, path)
			require.NoError(t, err)
			assert.Equal(t, fixtures[e.Name()], string(out),
				"uninstall must hand back the user's bytes, not a re-encoding with the entry removed")
		})
	}
}

func TestEngines_Uninstall_AbsentIsNoop(t *testing.T) {
	for _, e := range All() {
		t.Run(e.Name(), func(t *testing.T) {
			fs, store, path := registrar(t, e, "")
			res, err := e.Register(fs, store, path, TaskloomName, nil)
			require.NoError(t, err)
			assert.False(t, res.Changed)
			exists, err := afero.Exists(fs, path)
			require.NoError(t, err)
			assert.False(t, exists, "uninstalling from an absent config must not conjure one")
		})
	}
}

// A PRESENT "mcpServers" of the wrong type (a string, an array) is the user's,
// however it got there. Writing members into it would destroy it, so the
// registrar refuses, names the member, and leaves the file untouched — the
// same refusal a file that will not parse gets, backup included.
func TestEngines_Register_WrongTypeMcpServersRefuses(t *testing.T) {
	const original = `{"mcpServers": "not an object"}`
	server := TaskloomServer()
	for _, e := range All() {
		t.Run(e.Name(), func(t *testing.T) {
			fs, store, path := registrar(t, e, original)
			_, err := e.Register(fs, store, path, TaskloomName, &server)
			require.Error(t, err, "a present-but-wrong-type mcpServers value must be reported, not silently replaced")
			assert.Contains(t, err.Error(), "mcpServers", "the refusal names the member that is wrong")
			out, err := afero.ReadFile(fs, path)
			require.NoError(t, err)
			assert.Equal(t, original, string(out))
		})
	}
}

func TestGet_RegisteredNameOnly(t *testing.T) {
	e, err := Get("claude-code")
	require.NoError(t, err)
	assert.Equal(t, "claude-code", e.Name())

	// An engine has one name: the retired short spellings and case variants
	// are unknown engines, as a typo is — nothing guesses.
	for _, spelling := range []string{"claude", "claudecode", "CLAUDE", "Claude-Code", "cluade"} {
		_, err := Get(spelling)
		assert.Error(t, err, "%q must not resolve", spelling)
	}
}

func TestConfigPath_Scopes(t *testing.T) {
	tests := []struct {
		engine string
		global bool
		suffix string
	}{
		{"claude-code", false, ".mcp.json"},
		{"claude-code", true, ".claude.json"},
	}
	for _, tt := range tests {
		e, err := Get(tt.engine)
		require.NoError(t, err)
		p, err := e.ConfigPath("/proj", tt.global)
		require.NoError(t, err)
		if tt.global {
			assert.NotContains(t, p, "/proj", "%s global path must be home-rooted", tt.engine)
		} else {
			assert.Contains(t, p, "/proj", "%s project path must live under dir", tt.engine)
		}
		assert.Contains(t, p, tt.suffix)
	}
}
