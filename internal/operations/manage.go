package operations

import (
	"context"
	"errors"
	"fmt"
	"slices"
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
// AgentSurfaceLoss pairs one configured agent with what the engine it resolves
// to has no structural place for. The agent NAME travels with the loss because
// a roster-wide report is otherwise unactionable: "hooks are being dropped" is
// not something a user with four agents can go and fix.
type AgentSurfaceLoss struct {
	Agent   string              `json:"agent"`
	Backend string              `json:"backend"`
	Losses  []agent.SurfaceLoss `json:"losses"`
}

type HarnessStatusResult struct {
	WorkDir          string          `json:"work_dir"`
	ManageStatusline bool            `json:"manage_statusline"`
	Backends         []BackendWiring `json:"backends"`
	// RootFallback reports that WorkDir is the bare cwd fallback — no
	// CTXLOOM_ROOT override and not inside a git repository — so tasks, plans,
	// and sessions are keyed off the launch directory rather than a stable repo
	// root. The single source of truth for the not-a-stable-root warning, shared
	// by `ctxloom run` and the VSCode companion's title-bar warning.
	RootFallback bool `json:"root_fallback"`
	// CapabilityLoss names, per configured agent, what its resolved engine has
	// no structural place for. Omitted when nothing is lost, matching the
	// "only when it costs something" rule the text renderer already follows.
	//
	// It is on the wire because this report's machine-readable form is now the
	// DEFAULT for every scripted caller: off a terminal the resolved format is
	// json, so a report that carried the loss only in its text rendering would
	// state it to a human and withhold it from every script, CI job and agent.
	CapabilityLoss []AgentSurfaceLoss `json:"capability_loss,omitempty"`
	// Surfaces reports delivery currency for the native context files
	// (CLAUDE.md and its per-backend analogues) under WorkDir — see
	// SurfaceCurrency. This is the read half of the
	// engine-delivery seam (docs/design/engine-delivery-seam.design.md): the
	// hop wiring alone (Backends, above) cannot answer, because "hooks
	// present" says nothing about whether a materialized native file's
	// CONTENT still matches what the project's default profiles currently
	// compose (J001900's B6 finding — this command used to report on WIRING
	// only, never on DELIVERY, so a stale `profile materialize` output was
	// invisible to it). A backend with no read half yet, or with nothing
	// materialized here AND no engine-declared expectation of one, is simply
	// absent from this list. A missing verdict appears only where the engine
	// itself declares the native file its default context route AND the
	// composed context has something to put in it — see
	// surfaceCurrencies/contextFileExpected.
	Surfaces []SurfaceCurrency `json:"surfaces,omitempty"`
	// Errors records per-backend status-read failures; non-empty means the
	// report is partial. One backend's corrupt/unreadable settings.json no
	// longer blacks out the status of every other backend.
	Errors []string `json:"errors,omitempty"`
}

// SurfaceCurrency reports one backend's native context-surface delivery
// currency: whether the file (CLAUDE.md, AGENTS.md, .kiro/steering/
// ctxloom-context.md, MOCK_CONTEXT.md, …) still carries what the project's
// default profiles currently compose — or, where the engine declares that file
// its default context route and there is context to deliver, that it is not
// there at all. Route/Status/Detail mirror agent.DeliveryState's
// Route()/Currency() verbatim — this is that read half rendered for a report,
// not a second judgment about what the surface holds.
type SurfaceCurrency struct {
	Backend string `json:"backend"`
	Route   string `json:"route"`
	Status  string `json:"status"`
	Detail  string `json:"detail,omitempty"`
}

// HarnessStatus reports which ctxloom-managed artifacts are wired into each
// settings-supporting backend.
func HarnessStatus(ctx context.Context, cfg *config.Config, req HarnessStatusRequest) (*HarnessStatusResult, error) {
	fs := getFS(req.FS)
	workDir := manageWorkDir(req.WorkDir)
	opts := []backends.SettingsOption{backends.WithSettingsFS(fs)}

	settings := cfg.GetSettings()
	result := &HarnessStatusResult{
		WorkDir:          workDir,
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

// surfaceCurrencies walks every registered backend's NATIVE-FILE context
// surface and reports its delivery currency.
//
// It resolves agent.ApproachUnsafeFile deliberately, not the backend's default
// approach: "materialized native file" IS that approach, and asking for the
// default gets codex's hook route — a content-addressed <hash>.md whose name a
// harpless caller cannot even derive. A backend that declares no file approach
// for context (or no context surface at all) is skipped; so is one whose file
// route offers no read half (agent.StateReader). Either way the backend is
// structurally ABSENT from the report rather than reported as unreadable.
//
// It walks backends.BackendsWithSettings, the SAME set the wiring half above
// enumerates, rather than backends.List. The difference is the hermetic `mock`
// engine, which has no settings surface and is absent from every other line of
// this report — before the missing verdict existed it surfaced here only in the
// hermetic tests that materialize a MOCK_CONTEXT.md, but a verdict that fires on
// an ABSENT file would put a test engine in front of every real user. One
// report, one set of engines.
//
// A materialized file that is present reports delivered or stale. A file that
// is ABSENT reports missing only when materialization was actually EXPECTED —
// see contextFileExpected. That predicate is the whole of the "no false alarms"
// rule here: without it every hook-delivered project would be told its CLAUDE.md
// is gone, which is the fastest way to teach a user to skip this section.
//
// The composed ("intended") context is assembled AT MOST ONCE, lazily, and only
// once some backend actually has a readable file route to answer for — every
// verdict now depends on it, missing included, because "does this loadout carry
// anything for the file surface" cannot be answered without composing it. It
// reads via the existing AssembleContext, never regenerateContext:
// this is the read half the design doc calls out — "a status command that
// rewrites the surface it inspects is its own bug" — so it must never write.
func surfaceCurrencies(ctx context.Context, cfg *config.Config, fs afero.Fs, workDir string) (surfaces []SurfaceCurrency, errs []string) {
	var intended string
	var composed, composeFailed bool
	compose := func() (string, bool) {
		if composeFailed {
			return "", false
		}
		if !composed {
			asm, err := AssembleContext(ctx, cfg, AssembleContextRequest{Profiles: cfg.DefaultAgentProfiles()})
			if err != nil {
				errs = append(errs, fmt.Sprintf("failed to compose the current context to compare materialized surfaces against: %v", err))
				composeFailed = true
				return "", false
			}
			intended = asm.Context
			composed = true
		}
		return intended, true
	}

	for _, name := range backends.BackendsWithSettings() {
		set := backends.BuildSurfaces(name, agent.SurfaceInputs{}, fs)
		reader, ok := contextFileReader(set)
		if !ok {
			continue
		}
		state, err := reader.State(workDir)
		if err != nil {
			errs = append(errs, fmt.Sprintf("failed to read %s's materialized context surface: %v", name, err))
			continue
		}
		current, ok := compose()
		if !ok {
			return surfaces, errs
		}
		cur, report := reportableContextCurrency(state, current, contextFileExpected(set))
		if !report {
			continue
		}
		surfaces = append(surfaces, SurfaceCurrency{
			Backend: name,
			Route:   state.Route(),
			Status:  string(cur.Status),
			Detail:  cur.Detail,
		})
	}
	return surfaces, errs
}

// reportableContextCurrency is the whole "report it or stay quiet" rule, in one
// place so the two halves cannot drift apart.
//
// A file that EXISTS is always reported — delivered or stale — for any engine
// that can read it, expectation or not: content sitting on disk that nobody
// composes any more is exactly the drift this report is for, and the engine
// clearly did materialize there at some point.
//
// A file that is ABSENT is reported only when BOTH halves of the ruling hold:
// the engine declares that file its context route (expected — see
// contextFileExpected), AND the composed loadout actually carries context to
// put in it. The second half is the rule backends.UncarriedSurfaces already
// states — "A capability gap nobody asked to use costs nothing and stays
// quiet" — read against agent.SurfaceInputs.Context, the field the native-file
// route is fed from. Either half false is silence.
func reportableContextCurrency(state agent.DeliveryState, intended string, expected bool) (agent.Currency, bool) {
	cur := state.Currency(intended)
	if cur.Status != agent.StatusMissing {
		return cur, true
	}
	if !expected || strings.TrimSpace(intended) == "" {
		return agent.Currency{}, false
	}
	return cur, true
}

// contextFileReader resolves a backend's materialized-file context route to its
// read half, or reports false when it has none to read.
//
// It asks for agent.ApproachUnsafeFile by name. An empty SupportedApproaches
// for the kind means the backend has no distinct context surface at all — a
// FOLD, not a loss (backends.UncarriedSurfaces' doc: "reporting a folded
// surface as lost would be a false alarm"), so it is skipped in silence rather
// than reported as an unreadable route.
func contextFileReader(set agent.SurfaceSet) (agent.StateReader, bool) {
	if !slices.Contains(set.SupportedApproaches(agent.SurfaceContext), agent.ApproachUnsafeFile) {
		return nil, false
	}
	delivery, err := set.SurfaceFor(agent.SurfaceContext, agent.ApproachUnsafeFile)
	if err != nil {
		return nil, false
	}
	reader, ok := delivery.(agent.StateReader)
	return reader, ok
}

// contextFileExpected reports whether an ABSENT native context file is worth a
// missing verdict for this backend — the capability half of the rule
// backends.UncarriedSurfaces already states: "A capability gap nobody asked to
// use costs nothing and stays quiet."
//
// The expectation is a property of the ENGINE, read from what the engine itself
// declares, never from a user-facing mode key and never inferred from
// wire.HooksConfig. An engine whose DEFAULT context approach is the native file
// (claude-code, kiro, opencode, mock) delivers context by materializing that
// file, so its absence is a real finding. An engine whose default is some other
// route does not: codex's default is agent.ApproachHook — a per-run
// content-addressed cache file plus a SessionStart hook, with AGENTS.md as the
// second, opt-in route — so a harpless caller like `manage check` has no
// grounds to expect a file and must stay quiet. That is the same fact
// backends.LaunchOnlySurfaces encodes for codex's OTHER surfaces, and its doc
// is the reason this is not a "does the engine HAVE a file route" test: codex
// HAS one, and reporting it missing would be the false alarm that gets the real
// line ignored.
//
// LaunchOnlySurfaces itself is deliberately NOT called here. It answers about
// hooks, MCP, commands and skills — never about a context surface — so a call
// would return nil for every backend and every input, and a guard that can
// never fire is worse than none: it reads as a check and is not one. The
// declared default approach is where the same fact about codex is legible for
// the context kind, and TestSurfaceCurrencies_StaysSilentForCodex is what keeps
// it honest.
func contextFileExpected(set agent.SurfaceSet) bool {
	def, ok := set.DefaultApproach(agent.SurfaceContext)
	return ok && def == agent.ApproachUnsafeFile
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
