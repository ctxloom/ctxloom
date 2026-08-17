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
	// Surfaces reports delivery currency for the native context files
	// (CLAUDE.md and its per-backend analogues) that are ACTUALLY materialized
	// on disk under WorkDir — see SurfaceCurrency. This is the read half of the
	// engine-delivery seam (docs/design/engine-delivery-seam.design.md): the
	// hop wiring alone (Backends, above) cannot answer, because "hooks
	// present" says nothing about whether a materialized native file's
	// CONTENT still matches what the project's default profiles currently
	// compose (J001900's B6 finding — this command used to report on WIRING
	// only, never on DELIVERY, so a stale `profile materialize` output was
	// invisible to it). A backend with no read half yet, or with nothing
	// materialized here, is simply absent from this list — never reported as
	// an implicit "missing", which would be a false alarm for the (default)
	// hook-delivered case.
	Surfaces []SurfaceCurrency `json:"surfaces,omitempty"`
	// Errors records per-backend status-read failures; non-empty means the
	// report is partial. One backend's corrupt/unreadable settings.json no
	// longer blacks out the status of every other backend.
	Errors []string `json:"errors,omitempty"`
}

// SurfaceCurrency reports one backend's native context-surface delivery
// currency: whether a file already materialized on disk (CLAUDE.md,
// MOCK_CONTEXT.md, …) still carries what the project's default profiles
// currently compose. Route/Status/Detail mirror agent.DeliveryState's
// Route()/Currency() verbatim — this is that read half rendered for a report,
// not a second judgment about what the surface holds.
type SurfaceCurrency struct {
	Backend string `json:"backend"`
	Route   string `json:"route"`
	Status  string `json:"status"`
	Detail  string `json:"detail,omitempty"`
}

// HarnessStatus reports which ctxloom-managed artifacts are wired into each
// settings-supporting backend, plus the MCP auto-registration setting.
func HarnessStatus(ctx context.Context, cfg *config.Config, req HarnessStatusRequest) (*HarnessStatusResult, error) {
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

	surfaces, surfaceErrs := surfaceCurrencies(ctx, cfg, fs, workDir)
	result.Surfaces = surfaces
	for _, e := range surfaceErrs {
		clidiag.Warn("ctxloom", "%s", e)
	}
	result.Errors = append(result.Errors, surfaceErrs...)

	return result, nil
}

// surfaceCurrencies walks every registered backend's context surface and
// reports delivery currency for the ones that (a) offer a read half
// (agent.StateReader — today: claude-code and mock, see
// docs/design/engine-delivery-seam.design.md step 3) and (b) actually have
// something materialized under workDir. A backend with no read half yet is
// structurally absent from the result rather than reported as unreadable; a
// backend whose surface was never materialized (Currency == StatusMissing) is
// likewise omitted — a project that delivers context purely through the
// SessionStart hook (the default) has nothing here to report, and reporting
// it anyway would be exactly the false-alarm noise that trains a user to
// ignore this command.
//
// The composed ("intended") context is assembled AT MOST ONCE, lazily, and
// only once a materialized surface is actually found on disk — a read-only
// status command has no business running full context assembly (with its
// withheld-content advisories) when nothing on disk needs comparing against
// it. It reads via the existing AssembleContext, never regenerateContext:
// this is the read half the design doc calls out — "a status command that
// rewrites the surface it inspects is its own bug" — so it must never write.
func surfaceCurrencies(ctx context.Context, cfg *config.Config, fs afero.Fs, workDir string) (surfaces []SurfaceCurrency, errs []string) {
	var intended string
	var composed bool

	for _, name := range backends.List() {
		set := backends.BuildSurfaces(name, agent.SurfaceInputs{}, fs)
		approach, ok := set.DefaultApproach(agent.SurfaceContext)
		if !ok {
			continue
		}
		delivery, err := set.SurfaceFor(agent.SurfaceContext, approach)
		if err != nil {
			continue
		}
		reader, ok := delivery.(agent.StateReader)
		if !ok {
			continue
		}
		state, err := reader.State(workDir)
		if err != nil {
			errs = append(errs, fmt.Sprintf("failed to read %s's materialized context surface: %v", name, err))
			continue
		}
		// Cheap existence probe: Currency's not-found/no-managed-section cases
		// ignore the argument entirely, so this costs no extra I/O (State
		// already read the file) and, critically, no AssembleContext call for
		// the common case where this backend has nothing materialized here.
		if state.Currency("").Status == agent.StatusMissing {
			continue
		}
		if !composed {
			asm, err := AssembleContext(ctx, cfg, AssembleContextRequest{Profiles: cfg.DefaultAgentProfiles()})
			if err != nil {
				errs = append(errs, fmt.Sprintf("failed to compose the current context to compare materialized surfaces against: %v", err))
				return surfaces, errs
			}
			intended = asm.Context
			composed = true
		}
		cur := state.Currency(intended)
		surfaces = append(surfaces, SurfaceCurrency{
			Backend: name,
			Route:   state.Route(),
			Status:  string(cur.Status),
			Detail:  cur.Detail,
		})
	}
	return surfaces, errs
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
