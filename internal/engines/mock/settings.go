package mock

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/spf13/afero"

	"github.com/ctxloom/ctxloom/internal/core/agent"
	"github.com/ctxloom/ctxloom/internal/core/present"
	"github.com/ctxloom/ctxloom/internal/core/wire"
)

// MockSettingsReader is mock's agent.SettingsReader: what `ctxloom manage
// status` and `ctxloom doctor` reach by backend name to ask what the at-rest
// delivery wired into a project. It reads the files mock's own approaches
// write (surfaces.go), so the report and the delivery cannot disagree about
// where things live.
type MockSettingsReader struct {
	FS afero.Fs
}

// NewMockSettingsReader builds mock's settings reader from resolved options.
// It reads only FS: mock's files are its own whole-file writes, so there are
// no claims to consult, and it models no statusline.
func NewMockSettingsReader(opts agent.SettingsOptions) agent.SettingsReader {
	return &MockSettingsReader{FS: opts.FS}
}

// Status reports which managed artifacts are currently wired in. StatusLine is
// always false: mock models no statusline, and reporting one it never writes
// would be the silent-no-op inversion — a status that claims a capability the
// delivery does not have.
func (r *MockSettingsReader) Status(projectDir string) (agent.SettingsStatus, error) {
	fs := agent.GetFS(r.FS)
	at := func(rel string) string {
		return present.ProjectOnHost(projectDir).UnderProjectRoot(rel).Build().HostPath
	}

	var status agent.SettingsStatus
	settings, err := afero.Exists(fs, at(settingsRel))
	if err != nil {
		return status, fmt.Errorf("mock: stat %s: %w", at(settingsRel), err)
	}
	status.SettingsExists = settings

	var hooks wire.UnifiedHooks // the hooks file is the unified set (DeliveredHooksFile)
	if err := readJSON(fs, at(hooksRel), &hooks); err != nil {
		return status, err
	}
	status.HooksPresent = len(hooks.All()) > 0

	var mcp struct {
		MCPServers map[string]json.RawMessage `json:"mcpServers"`
	}
	if err := readJSON(fs, at(mcpRel), &mcp); err != nil {
		return status, err
	}
	status.MCPPresent = len(mcp.MCPServers) > 0
	return status, nil
}

// readJSON decodes path into v; an absent file leaves v as it was.
func readJSON(fs afero.Fs, path string, v any) error {
	data, err := afero.ReadFile(fs, path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("mock: read %s: %w", path, err)
	}
	if err := json.Unmarshal(data, v); err != nil {
		return fmt.Errorf("mock: parse %s: %w", path, err)
	}
	return nil
}

// Compile-time capability contract.
var _ agent.SettingsReader = (*MockSettingsReader)(nil)
