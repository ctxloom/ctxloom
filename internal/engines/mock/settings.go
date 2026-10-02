package mock

import (
	"fmt"
	"os"

	"github.com/spf13/afero"

	"github.com/ctxloom/ctxloom/internal/core/agent"
)

// MockSettingsReader is mock's agent.SettingsReader — the install/uninstall/
// status half of an engine's settings capability, sibling to the delivery-seam
// mockSettingsSurface.
//
// BOTH exist because they answer different callers, not because one is a copy
// of the other: the SURFACE is what a run's delivery cell writes for a live
// session, while the WRITER is what `ctxloom manage hooks install|uninstall`
// and `ctxloom doctor` reach through by backend name. Every real engine
// carries both; mock carrying only one would make it unusable as the second
// engine in exactly the scenarios that exercise the management commands.
//
// It writes the SAME file, through the SAME merge helpers
// (readMockSettings/writeMockSettings), so the two paths cannot disagree about
// the document's shape — which is the failure a second implementation would
// invite.
type MockSettingsReader struct {
	FS afero.Fs
}

// NewMockSettingsReader builds mock's settings writer from resolved options.
// It reads only FS: mock has no statusline and no deny-tool policy of its own
// to honour, and inventing handling for options it does not model would make
// the double claim a capability the engines it stands in for would then be
// compared against.
func NewMockSettingsReader(opts agent.SettingsOptions) agent.SettingsReader {
	return &MockSettingsReader{FS: opts.FS}
}

// mockSettingsMCPKey is the settings document key mock's managed MCP servers
// live under. Named beside mockSettingsHooksKey so install and removal cannot
// disagree about which keys ctxloom owns.
const mockSettingsMCPKey = "mcpServers"

// Status reports which managed artifacts are currently wired in. StatusLine is
// always false: mock models no statusline, and reporting one it never writes
// would be the silent-no-op inversion — a status that claims a capability the
// delivery does not have.
func (w *MockSettingsReader) Status(projectDir string) (agent.SettingsStatus, error) {
	fs := agent.GetFS(w.FS)
	path := mockSettingsPath(projectDir)

	if _, err := fs.Stat(path); err != nil {
		if os.IsNotExist(err) {
			return agent.SettingsStatus{}, nil
		}
		return agent.SettingsStatus{}, fmt.Errorf("mock: stat %s: %w", path, err)
	}

	doc, err := readMockSettings(fs, path)
	if err != nil {
		return agent.SettingsStatus{}, err
	}
	_, hooks := doc[mockSettingsHooksKey]
	_, mcp := doc[mockSettingsMCPKey]
	return agent.SettingsStatus{
		SettingsExists: true,
		HooksPresent:   hooks,
		MCPPresent:     mcp,
	}, nil
}

// Compile-time capability contract.
var _ agent.SettingsReader = (*MockSettingsReader)(nil)
