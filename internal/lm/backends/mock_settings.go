package backends

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/spf13/afero"

	"github.com/ctxloom/ctxloom/internal/shared/agent"
	"github.com/ctxloom/ctxloom/internal/shared/wire"
)

// MockSettingsWriter is mock's agent.SettingsWriter — the install/uninstall/
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
type MockSettingsWriter struct {
	FS afero.Fs
}

// NewMockSettingsWriter builds mock's settings writer from resolved options.
// It reads only FS: mock has no statusline and no deny-tool policy of its own
// to honour, and inventing handling for options it does not model would make
// the double claim a capability the engines it stands in for would then be
// compared against.
func NewMockSettingsWriter(opts agent.SettingsOptions) agent.SettingsWriter {
	return &MockSettingsWriter{FS: opts.FS}
}

// mockSettingsMCPKey is the settings document key mock's managed MCP servers
// live under. Named beside mockSettingsHooksKey so install and removal cannot
// disagree about which keys ctxloom owns.
const mockSettingsMCPKey = "mcpServers"

// managedMockSettingsKeys is the complete set of top-level keys ctxloom owns
// in a mock settings document. It is the ONE list removal walks, so a key
// added to the write path without being added here would survive an uninstall
// and leave managed state behind claiming to be the user's.
var managedMockSettingsKeys = []string{mockSettingsHooksKey, mockSettingsMCPKey}

// WriteSettings merges hooks and bundleMCP into .mock/settings.json,
// preserving every key ctxloom does not own.
func (w *MockSettingsWriter) WriteSettings(hooks *wire.HooksConfig, bundleMCP map[string]wire.MCPServer, projectDir string) error {
	fs := agent.GetFS(w.FS)
	path := mockSettingsPath(projectDir)

	doc, err := readMockSettings(fs, path)
	if err != nil {
		return err
	}

	if hooks == nil {
		delete(doc, mockSettingsHooksKey)
	} else {
		raw, merr := json.Marshal(hooks)
		if merr != nil {
			return fmt.Errorf("mock: marshal hooks: %w", merr)
		}
		doc[mockSettingsHooksKey] = raw
	}

	if len(bundleMCP) == 0 {
		delete(doc, mockSettingsMCPKey)
	} else {
		raw, merr := json.Marshal(bundleMCP)
		if merr != nil {
			return fmt.Errorf("mock: marshal mcp servers: %w", merr)
		}
		doc[mockSettingsMCPKey] = raw
	}

	return writeMockSettings(fs, path, doc)
}

// RemoveSettings strips every managed key, preserving user-defined ones. An
// ABSENT file stays absent: uninstall never creates a file, which is the
// contract every engine's writer holds and the one a conformance suite checks.
func (w *MockSettingsWriter) RemoveSettings(projectDir string) error {
	fs := agent.GetFS(w.FS)
	path := mockSettingsPath(projectDir)

	if _, err := fs.Stat(path); err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("mock: stat %s: %w", path, err)
	}

	doc, err := readMockSettings(fs, path)
	if err != nil {
		return err
	}
	for _, k := range managedMockSettingsKeys {
		delete(doc, k)
	}

	// Nothing of ours left and nothing of theirs: remove the file rather than
	// leave an empty object, so "nothing managed remains" is observable as an
	// absent file and not merely as an empty one.
	if len(doc) == 0 {
		if err := fs.Remove(path); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("mock: remove %s: %w", path, err)
		}
		return nil
	}
	return writeMockSettings(fs, path, doc)
}

// Status reports which managed artifacts are currently wired in. StatusLine is
// always false: mock models no statusline, and reporting one it never writes
// would be the silent-no-op inversion — a status that claims a capability the
// delivery does not have.
func (w *MockSettingsWriter) Status(projectDir string) (agent.SettingsStatus, error) {
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
var _ agent.SettingsWriter = (*MockSettingsWriter)(nil)
