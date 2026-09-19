package backends

import (
	"encoding/json"
	"testing"

	"github.com/spf13/afero"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/core/agent"
	"github.com/ctxloom/ctxloom/internal/core/config"
	"github.com/ctxloom/ctxloom/internal/core/present"
	"github.com/ctxloom/ctxloom/internal/core/wire"
)

// These cover the surfaces that made mock a COMPLETE engine — MCP, settings
// and commands. Every assertion reads the BYTES the delivery wrote, never the
// handle it returned: a surface that reports success and writes nothing is
// this project's characteristic failure, and it is the exact failure a test
// double is supposed to expose in others.

func TestMockMCPSurface_WritesTheServersItWasGiven(t *testing.T) {
	fs := afero.NewMemMapFs()
	set := newMockSurfaces(agent.SurfaceInputs{
		BundleMCP: map[string]wire.MCPServer{
			"postgres": {Command: "mcp-postgres", Args: []string{"--readonly"}},
		},
	}, fs)

	handle, err := set.MCP.Deliver(present.ProjectOnHost("/proj"))
	require.NoError(t, err)
	require.NotNil(t, handle)

	data, err := afero.ReadFile(fs, mockMCPPath("/proj"))
	require.NoError(t, err)
	require.NotEmpty(t, data, "a delivered MCP surface that wrote zero bytes is the silent no-op this double exists to catch")
	assert.Contains(t, string(data), "postgres", "the server the caller registered must appear in the delivered file")
	assert.Contains(t, string(data), "mcp-postgres", "the server's COMMAND must survive, not just its name")

	require.NoError(t, handle.Cleanup())
	_, err = fs.Stat(mockMCPPath("/proj"))
	assert.Error(t, err, "cleanup must remove what delivery wrote")
}

func TestMockSettingsSurface_PreservesKeysCtxloomDoesNotOwn(t *testing.T) {
	fs := afero.NewMemMapFs()
	path := mockSettingsPath("/proj")
	require.NoError(t, afero.WriteFile(fs, path, []byte(`{"theme":"dark"}`), 0o600))

	set := newMockSurfaces(agent.SurfaceInputs{
		Hooks: &wire.HooksConfig{},
	}, fs)

	handle, err := set.Settings.Deliver(present.ProjectOnHost("/proj"))
	require.NoError(t, err)

	doc := readSettingsDoc(t, fs, path)
	assert.Contains(t, doc, "hooks", "the managed hooks must land")
	assert.JSONEq(t, `"dark"`, string(doc["theme"]),
		"a key ctxloom does not own must survive delivery byte-for-byte — clobbering it is the opposite of what a managed writer promises")

	// Cleanup strips ONLY what ctxloom wrote.
	require.NoError(t, handle.Cleanup())
	after := readSettingsDoc(t, fs, path)
	assert.NotContains(t, after, "hooks", "cleanup must remove the managed key")
	assert.Contains(t, after, "theme", "cleanup must NOT remove the user's own key")
}

func TestMockSettingsWriter_InstallThenRemove_LeavesNothingManaged(t *testing.T) {
	fs := afero.NewMemMapFs()
	w := NewMockSettingsWriter(agent.SettingsOptions{FS: fs})

	require.NoError(t, w.WriteSettings(&wire.HooksConfig{}, map[string]wire.MCPServer{
		"ctxloom": {Command: "ctxloom"},
	}, "/proj"))

	status, err := w.Status("/proj")
	require.NoError(t, err)
	assert.True(t, status.SettingsExists)
	assert.True(t, status.HooksPresent, "an installed hook must be REPORTED as present, not merely written")
	assert.True(t, status.MCPPresent)
	assert.False(t, status.StatusLine, "mock models no statusline and must not claim one")

	require.NoError(t, w.RemoveSettings("/proj"))

	after, err := w.Status("/proj")
	require.NoError(t, err)
	assert.False(t, after.Wired(), "nothing managed may remain after removal")
}

// TestMockSettingsWriter_RemoveNeverCreatesAFile pins the contract every
// engine's writer holds: uninstall is not allowed to bring a settings file
// into existence. Absence-satisfies-absence would make this vacuous, so the
// assertion is that the path is STILL absent after a removal that ran.
func TestMockSettingsWriter_RemoveNeverCreatesAFile(t *testing.T) {
	fs := afero.NewMemMapFs()
	w := NewMockSettingsWriter(agent.SettingsOptions{FS: fs})

	require.NoError(t, w.RemoveSettings("/proj"), "removing from a project with no settings file is not an error")

	_, err := fs.Stat(mockSettingsPath("/proj"))
	assert.Error(t, err, "uninstall must never create the file it was asked to clean")
}

func TestMockCommandsSurface_WritesEnabledCommands(t *testing.T) {
	fs := afero.NewMemMapFs()
	set := newMockSurfaces(agent.SurfaceInputs{
		Commands: []agent.CommandExport{
			{Name: "review", Content: "Review the diff.", Enabled: true},
			{Name: "skipped", Content: "Never written.", Enabled: false},
		},
	}, fs)

	_, err := set.Commands.Deliver(present.ProjectOnHost("/proj"))
	require.NoError(t, err)

	data, err := afero.ReadFile(fs, mockCommandsPath("/proj")+"/review.md")
	require.NoError(t, err)
	assert.Contains(t, string(data), "Review the diff.", "an enabled command's BODY must land, not just its file")

	_, err = fs.Stat(mockCommandsPath("/proj") + "/skipped.md")
	assert.Error(t, err, "a disabled command must not be written — enablement that changes nothing is not enablement")
}

// readSettingsDoc reads the settings file as a key->raw map so a test can
// assert on individual keys without depending on formatting.
func readSettingsDoc(t *testing.T, fs afero.Fs, path string) map[string]json.RawMessage {
	t.Helper()
	data, err := afero.ReadFile(fs, path)
	require.NoError(t, err)
	doc := map[string]json.RawMessage{}
	require.NoError(t, json.Unmarshal(data, &doc))
	return doc
}

// mockBuilt holds one constructed instance of each mock approach, so a test
// can drive a surface directly rather than through the builder.
type mockBuilt struct {
	Context, Skills, MCP, Settings, Commands agent.Approach
}

// newMockSurfaces constructs every mock approach from in through the
// Declaration — the same path Build takes.
func newMockSurfaces(in agent.SurfaceInputs, fs afero.Fs) mockBuilt {
	decl := mockDeclaration(config.BackendMock)
	must := func(kind agent.SurfaceKind) agent.Approach {
		a, ok := decl.Construct(kind, agent.ApproachUnsafeFile, in, fs)
		if !ok {
			panic("mock does not declare " + kind.String())
		}
		return a
	}
	return mockBuilt{
		Context:  must(agent.SurfaceContext),
		Skills:   must(agent.SurfaceSkills),
		MCP:      must(agent.SurfaceMCP),
		Settings: must(agent.SurfaceSettings),
		Commands: must(agent.SurfaceCommands),
	}
}
