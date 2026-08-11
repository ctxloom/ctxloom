package operations

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/spf13/afero"

	"github.com/ctxloom/ctxloom/internal/config"
	"github.com/ctxloom/ctxloom/internal/lm/backends"
	"github.com/ctxloom/ctxloom/internal/projectroot"
	"github.com/ctxloom/ctxloom/internal/shared/agent"
	"github.com/ctxloom/ctxloom/internal/shared/clidiag"
)

// RemoveHooksRequest contains parameters for stripping ctxloom's harness from
// backend config files.
type RemoveHooksRequest struct {
	Backend string   `json:"backend"` // claude-code, codex, or all
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

	names, err := manageBackendNames(req.Backend)
	if err != nil {
		return nil, err
	}
	removed := []string{}
	var errs []string
	for _, name := range names {
		if ctx.Err() != nil {
			return &RemoveHooksResult{Status: "partial", Backends: removed, Errors: errs}, ctx.Err()
		}
		if err := removeBackendHarness(name, workDir, fs, settingsOpts); err != nil {
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

// removeBackendHarness strips one backend's ctxloom harness: RemoveSettings
// reverts the per-backend hooks/MCP (unchanged), and the commands surface — built
// from an EMPTY export set and delivered — clears ONLY ctxloom-managed command
// files. That clear routes through the same manifest-scoped writer the old
// WriteCommandFilesFor(nil) used: it removes exactly the .ctxloom-manifest-tracked
// files ctxloom wrote and leaves user-authored commands untouched (never a blanket
// wipe of the commands dir). Context is deliberately NOT selected, so CLAUDE.md and
// the other native context files are left in place.
func removeBackendHarness(name, workDir string, fs afero.Fs, settingsOpts []backends.SettingsOption) error {
	if err := backends.RemoveSettings(name, workDir, settingsOpts...); err != nil {
		return fmt.Errorf("failed to remove %s settings: %w", name, err)
	}
	set := backends.BuildSurfaces(name, agent.SurfaceInputs{}, fs)
	if _, _, errs := agent.Select(set).WithCommands(agent.CommandsWriteUnsafeFile).DeliverUnder(workDir); len(errs) > 0 {
		return fmt.Errorf("failed to remove %s commands: %w", name, errors.Join(errs...))
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

	mcpCfg := cfg.GetMCPConfig()
	settings := cfg.GetSettings()
	result := &HarnessStatusResult{
		WorkDir:          workDir,
		AutoRegisterMCP:  mcpCfg.ShouldAutoRegisterCtxloom(),
		ManageStatusline: settings.ShouldManageStatusline(),
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
}

// SetStatuslineResult reports the resulting statusline preference.
type SetStatuslineResult struct {
	Status     string `json:"status"`
	Statusline bool   `json:"statusline"`
}

// SetStatusline persists whether ctxloom manages its HUD statusline, inside
// one Manager.Update transaction. The change takes effect on the next hook
// apply (`manage hooks install` / `ctxloom run`).
func SetStatusline(_ context.Context, mgr *config.Manager, req SetStatuslineRequest) (*SetStatuslineResult, error) {
	if mgr == nil {
		return nil, fmt.Errorf("manager is required")
	}
	enabled := req.Enabled
	if err := mgr.Update(func(d *config.Draft) error {
		d.Settings.Statusline = &enabled
		return nil
	}); err != nil {
		return nil, err
	}

	return &SetStatuslineResult{
		Status:     "updated",
		Statusline: enabled,
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
//
// An unknown name is an ERROR, not a one-element list. Passing it through made
// `manage hooks uninstall --backend <typo>` report Status "removed" listing the
// typo while removing nothing: every layer below reads an unregistered backend
// as a permitted no-op (RemoveSettings returns nil with no settings writer;
// BuildSurfaces returns an EmptySurfaceSet whose nil SupportedApproaches makes
// Select skip the kind), so no error ever surfaced and the name was appended to
// `removed`. The user's harness was still installed and they had been told it
// was gone. MaterializeProfile in this same package already guards with
// backends.Exists — this is that guard, at the other door.
func manageBackendNames(backend string) ([]string, error) {
	if backend == "" || backend == "all" {
		return backends.BackendsWithSettings(), nil
	}
	if !backends.Exists(backend) {
		return nil, fmt.Errorf("unknown backend %q (supported: %s)", backend, strings.Join(backends.BackendsWithSettings(), ", "))
	}
	return []string{backend}, nil
}
