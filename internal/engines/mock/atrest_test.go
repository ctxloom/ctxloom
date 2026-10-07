package mock

import (
	"path/filepath"
	"testing"

	"github.com/spf13/afero"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/core/composite"
	"github.com/ctxloom/ctxloom/internal/core/wire"
	"github.com/ctxloom/ctxloom/internal/shared/safefs"
	"github.com/ctxloom/ctxloom/internal/testsupport/atrest"
)

// These cover the surfaces that made mock a COMPLETE engine — MCP, settings
// and commands. Every assertion reads the BYTES the delivery wrote, never the
// handle it returned: a surface that reports success and writes nothing is
// this project's characteristic failure, and it is the exact failure a test
// double is supposed to expose in others.

// TestMockSettingsReader_InstallThenRemove_ReportsWhatIsWired: the status
// read reports what the at-rest delivery actually wired — hooks and servers
// present after an install, nothing after the uninstall.
func TestMockSettingsReader_InstallThenRemove_ReportsWhatIsWired(t *testing.T) {
	fs := afero.NewMemMapFs()
	p := atrest.New(t, safefs.NewMem(fs), New(), "/proj")
	require.NoError(t, p.Install(composite.Package{
		Hooks: wire.HooksConfig{Unified: wire.UnifiedHooks{SessionStart: []wire.Hook{{Command: "ctxloom hook session-start"}}}},
		MCP:   map[string]wire.MCPServer{"ctxloom": {Command: "ctxloom"}},
	}))

	r := NewMockSettingsReader(p.Settings())
	status, err := r.Status("/proj")
	require.NoError(t, err)
	assert.True(t, status.HooksPresent, "an installed hook must be REPORTED as present, not merely written")
	assert.True(t, status.MCPPresent, "an installed server must be reported present")
	assert.False(t, status.StatusLine, "mock models no statusline and must not claim one")

	require.NoError(t, p.Uninstall())

	after, err := r.Status("/proj")
	require.NoError(t, err)
	assert.False(t, after.Wired(), "nothing managed may remain after removal")
}

// TestMockUninstall_NeverCreatesAFile pins the contract every engine holds:
// uninstall is not allowed to bring a settings file into existence.
// Absence-satisfies-absence would make this vacuous, so the assertion is
// that the path is STILL absent after a removal that ran.
func TestMockUninstall_NeverCreatesAFile(t *testing.T) {
	fs := afero.NewMemMapFs()
	require.NoError(t, atrest.New(t, safefs.NewMem(fs), New(), "/proj").Uninstall(), "removing from a project with no settings file is not an error")

	_, err := fs.Stat(filepath.Join("/proj", settingsRel))
	assert.Error(t, err, "uninstall must never create the file it was asked to clean")
}
