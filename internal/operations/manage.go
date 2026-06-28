package operations

import (
	"context"
	"fmt"

	"github.com/spf13/afero"

	"github.com/ctxloom/ctxloom/internal/config"
	"github.com/ctxloom/ctxloom/internal/lm/backends"
	"github.com/ctxloom/ctxloom/internal/projectroot"
	"github.com/ctxloom/shared/agent"
	"github.com/ctxloom/shared/clidiag"
)

// RemoveHooksRequest contains parameters for stripping ctxloom's harness from
// backend config files.
type RemoveHooksRequest struct {
	Backend string   `json:"backend"` // claude-code, antigravity, or all
	FS      afero.Fs `json:"-"`       // Optional filesystem for testing
	WorkDir string   `json:"-"`       // Optional work directory (defaults to git root)
}

// RemoveHooksResult reports which backends were cleaned and any per-backend
// failures.
type RemoveHooksResult struct {
	Status   string   `json:"status"`
	Backends []string `json:"backends"`
	Errors   []string `json:"errors,omitempty"`
}

// RemoveHooks strips ctxloom-managed hooks, statusline, MCP servers, and
// generated command files from the requested backends. Fault tolerant: a
// single backend's failure is recorded and the rest still run.
func RemoveHooks(ctx context.Context, _ *config.Config, req RemoveHooksRequest) (*RemoveHooksResult, error) {
	fs := getFS(req.FS)
	workDir := manageWorkDir(req.WorkDir)
	settingsOpts := []backends.SettingsOption{backends.WithSettingsFS(fs)}
	cmdOpts := []agent.CommandFileOption{agent.WithCommandFS(fs)}

	removed := []string{}
	var errs []string
	for _, name := range manageBackendNames(req.Backend) {
		if ctx.Err() != nil {
			return &RemoveHooksResult{Status: "partial", Backends: removed, Errors: errs}, ctx.Err()
		}
		if err := removeBackendHarness(name, workDir, settingsOpts, cmdOpts); err != nil {
			clidiag.Warn("ctxloom", "%s", err)
			errs = append(errs, err.Error())
			continue
		}
		removed = append(removed, name)
	}

	status := "removed"
	if len(errs) > 0 {
		status = "partial"
	}
	return &RemoveHooksResult{Status: status, Backends: removed, Errors: errs}, nil
}

// removeBackendHarness strips one backend's settings and clears the command
// files it generated (writing an empty prompt set triggers manifest cleanup).
func removeBackendHarness(name, workDir string, settingsOpts []backends.SettingsOption, cmdOpts []agent.CommandFileOption) error {
	if err := backends.RemoveSettings(name, workDir, settingsOpts...); err != nil {
		return fmt.Errorf("failed to remove %s settings: %w", name, err)
	}
	if err := backends.WriteCommandFilesFor(name, workDir, nil, cmdOpts...); err != nil {
		return fmt.Errorf("failed to remove %s commands: %w", name, err)
	}
	return nil
}

// HarnessStatusRequest contains parameters for inspecting ctxloom's wiring.
type HarnessStatusRequest struct {
	FS      afero.Fs `json:"-"`
	WorkDir string   `json:"-"`
}

// BackendWiring reports a single backend's ctxloom wiring.
type BackendWiring struct {
	Backend        string `json:"backend"`
	SettingsExists bool   `json:"settings_exists"`
	HooksPresent   bool   `json:"hooks_present"`
	StatusLine     bool   `json:"status_line"`
	MCPPresent     bool   `json:"mcp_present"`
}

// HarnessStatusResult reports ctxloom's project wiring across backends.
type HarnessStatusResult struct {
	WorkDir          string          `json:"work_dir"`
	AutoRegisterMCP  bool            `json:"auto_register_mcp"`
	ManageStatusline bool            `json:"manage_statusline"`
	Backends         []BackendWiring `json:"backends"`
	// RootFallback reports that WorkDir is the bare cwd fallback — no
	// CTXLOOM_ROOT override and not inside a git repository — so tasks, plans,
	// and sessions are keyed off the launch directory rather than a stable repo
	// root. The single source of truth for the not-a-stable-root warning, shared
	// by `ctxloom run` and the VSCode companion's title-bar warning.
	RootFallback bool `json:"root_fallback"`
	// Errors records per-backend status-read failures; non-empty means the
	// report is partial. One backend's corrupt/unreadable settings.json no
	// longer blacks out the status of every other backend.
	Errors []string `json:"errors,omitempty"`
}

// HarnessStatus reports which ctxloom-managed artifacts are wired into each
// settings-supporting backend, plus the MCP auto-registration setting.
func HarnessStatus(_ context.Context, cfg *config.Config, req HarnessStatusRequest) (*HarnessStatusResult, error) {
	fs := getFS(req.FS)
	workDir := manageWorkDir(req.WorkDir)
	opts := []backends.SettingsOption{backends.WithSettingsFS(fs)}

	result := &HarnessStatusResult{
		WorkDir:          workDir,
		AutoRegisterMCP:  cfg.MCP.ShouldAutoRegisterCtxloom(),
		ManageStatusline: cfg.Settings.ShouldManageStatusline(),
		Backends:         []BackendWiring{},
		RootFallback:     projectroot.RootFromFallback(),
	}
	for _, name := range backends.BackendsWithSettings() {
		status, err := backends.BackendStatus(name, workDir, opts...)
		if err != nil {
			// Warn-and-continue like the sibling RemoveHooks: one backend's
			// unreadable settings.json must not abort the whole read-only status
			// report and hide every other backend's wiring.
			clidiag.Warn("ctxloom", "failed to read %s status: %v", name, err)
			result.Errors = append(result.Errors, fmt.Sprintf("failed to read %s status: %v", name, err))
			continue
		}
		result.Backends = append(result.Backends, BackendWiring{
			Backend:        name,
			SettingsExists: status.SettingsExists,
			HooksPresent:   status.HooksPresent,
			StatusLine:     status.StatusLine,
			MCPPresent:     status.MCPPresent,
		})
	}
	return result, nil
}

// SetStatuslineRequest contains parameters for toggling the ctxloom HUD statusline.
type SetStatuslineRequest struct {
	Enabled bool `json:"enabled"`

	// TestConfig, FS, AppDir mirror the MCP toggle for hermetic testing.
	TestConfig *config.Config `json:"-"`
	FS         afero.Fs       `json:"-"`
	AppDir     string         `json:"-"`
}

// SetStatuslineResult reports the resulting statusline preference.
type SetStatuslineResult struct {
	Status     string         `json:"status"`
	Statusline bool           `json:"statusline"`
	Config     *config.Config `json:"-"`
}

// SetStatusline persists whether ctxloom manages its HUD statusline. The change
// takes effect on the next hook apply (`manage hooks install` / `ctxloom run`).
func SetStatusline(_ context.Context, _ *config.Config, req SetStatuslineRequest) (*SetStatuslineResult, error) {
	freshCfg, err := loadFreshConfig(req.FS, req.AppDir, req.TestConfig)
	if err != nil {
		return nil, err
	}

	enabled := req.Enabled
	freshCfg.Settings.Statusline = &enabled

	if err := saveMCPConfig(freshCfg, req.TestConfig); err != nil {
		return nil, err
	}

	return &SetStatuslineResult{
		Status:     "updated",
		Statusline: enabled,
		Config:     freshCfg,
	}, nil
}

// manageWorkDir returns the injected work dir, else the resolved project root.
func manageWorkDir(workDir string) string {
	if workDir != "" {
		return workDir
	}
	return projectroot.WorkDir()
}

// manageBackendNames resolves the backend filter to the backends to operate on.
func manageBackendNames(backend string) []string {
	if backend == "" || backend == "all" {
		return backends.BackendsWithSettings()
	}
	return []string{backend}
}
