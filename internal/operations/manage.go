package operations

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/spf13/afero"

	"github.com/ctxloom/ctxloom/internal/core/agent"
	"github.com/ctxloom/ctxloom/internal/core/config"
	"github.com/ctxloom/ctxloom/internal/core/present"
	"github.com/ctxloom/ctxloom/internal/lm/backends"
	"github.com/ctxloom/ctxloom/internal/projectroot"
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
	sel := agent.Select(backends.Declared(name)).With(agent.SurfaceCommands, agent.ApproachUnsafeFile)
	if _, _, errs := sel.DeliverUnder(agent.SurfaceInputs{}, fs, present.ProjectOnHost(workDir)); len(errs) > 0 {
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
	// absent from this list. A missing verdict appears only where the install
	// itself would have written the file (installedThroughProjectFile) AND the
	// composed context has something to put in it — see surfaceCurrencies.
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
// is ABSENT reports missing only when the install would have WRITTEN it —
// installedThroughProjectFile, the same predicate the install writes and
// retracts by. That predicate is the whole of the "no false alarms" rule here:
// without it every hook-delivered project would be told its CLAUDE.md is
// gone, which is the fastest way to teach a user to skip this section.
//
// The composed ("intended") context is assembled lazily, at most once PER
// BACKEND, and only for a backend that actually has a readable file route to
// answer for — every verdict depends on it, missing included, because "does
// this loadout carry anything for the file surface" cannot be answered without
// composing it. Per backend rather than once, because what a materialized file
// holds is a property of the engine it was written for, and of which writer
// wrote it (intendedContextFiles).
// It reads via the existing AssembleContext, never regenerateContext: this is
// the read half the design doc calls out — "a status command that rewrites the
// surface it inspects is its own bug" — so it must never write.
func surfaceCurrencies(ctx context.Context, cfg *config.Config, fs afero.Fs, workDir string) (surfaces []SurfaceCurrency, errs []string) {
	intended := map[string][]string{}
	var composeFailed bool
	compose := func(backend string) ([]string, bool) {
		if composeFailed {
			return nil, false
		}
		if _, ok := intended[backend]; !ok {
			composed, err := intendedContextFiles(ctx, cfg, backend)
			if err != nil {
				errs = append(errs, fmt.Sprintf("failed to compose the current context to compare materialized surfaces against: %v", err))
				composeFailed = true
				return nil, false
			}
			intended[backend] = composed
		}
		return intended[backend], true
	}

	for _, name := range backends.BackendsWithSettings() {
		// A test double is not part of a user's wiring, so it never appears in
		// their report. Stated rather than implied: an exclusion that depends
		// on a backend staying incomplete stops holding when it is completed.
		if backends.IsTestOnly(name) {
			continue
		}
		decl := backends.Declared(name)
		reader, ok := contextFileReader(decl, fs)
		if !ok {
			continue
		}
		state, err := reader.State(workDir)
		if err != nil {
			errs = append(errs, fmt.Sprintf("failed to read %s's materialized context surface: %v", name, err))
			continue
		}
		current, ok := compose(name)
		if !ok {
			return surfaces, errs
		}
		cur, report := reportableContextCurrency(state, current, installedThroughProjectFile(decl, agent.SurfaceContext))
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

// intendedContextFiles composes every context a ctxloom writer states for
// backend's native context file, for the configured default profiles — the
// check's side of the currency comparison.
//
// Two writers reach that file and they state DIFFERENT subjects. `profile
// materialize` composes MaterializedFor(backend): a launch with no ctxloom
// behind it, so an engine without a skills surface gets its premised fragments
// written into the file. `manage hooks install` composes for a live session
// (installedContextFile) and withholds them for every engine — it is included
// only where ApplyHooks routes context through the file at all
// (installedThroughProjectFile), since for an engine that injects it never
// writes this file; it retracts it. The file records bytes, not its writer, and both writers leave the
// same hooks and MCP server beside it, so the check cannot know which one it
// is reading: it holds the file against each, and a file current under the
// writer that produced it is delivered. Composing one subject alone reports
// the other writer's correct file stale forever; the two are equal exactly
// when the engine has a skills surface, which is why the divergence is
// invisible until it is not.
func intendedContextFiles(ctx context.Context, cfg *config.Config, backend string) ([]string, error) {
	materialized, err := AssembleContext(ctx, cfg, AssembleContextRequest{
		Profiles: cfg.DefaultAgentProfiles(),
		Consumer: MaterializedFor(backend),
	})
	if err != nil {
		return nil, err
	}
	intended := []string{materialized.Context}
	if installedThroughProjectFile(backends.Declared(backend), agent.SurfaceContext) {
		installed, err := installedContextFile(ctx, cfg)
		if err != nil {
			return nil, err
		}
		intended = append(intended, installed)
	}
	return intended, nil
}

// reportableContextCurrency is the whole "report it or stay quiet" rule, in one
// place so the two halves cannot drift apart. intended is every composition a
// ctxloom writer states for the file (intendedContextFiles): the file is
// delivered when it matches any of them, and stale only when it matches none.
//
// A file that EXISTS is always reported — delivered or stale — for any engine
// that can read it, expectation or not: content sitting on disk that nobody
// composes any more is exactly the drift this report is for, and the engine
// clearly did materialize there at some point.
//
// A file that is ABSENT is reported only when BOTH halves of the ruling hold:
// the install routes context through that file (expected — see
// installedThroughProjectFile), AND the composed loadout actually carries context to
// put in it. The second half is the rule backends.UncarriedSurfaces already
// states — "A capability gap nobody asked to use costs nothing and stays
// quiet" — read against agent.SurfaceInputs.Context, the field the native-file
// route is fed from. Either half false is silence.
func reportableContextCurrency(state agent.DeliveryState, intended []string, expected bool) (agent.Currency, bool) {
	var cur agent.Currency
	carries := false
	for _, want := range intended {
		carries = carries || strings.TrimSpace(want) != ""
		cur = state.Currency(want)
		if cur.Status == agent.StatusDelivered {
			return cur, true
		}
	}
	if cur.Status != agent.StatusMissing {
		return cur, true
	}
	if !expected || !carries {
		return agent.Currency{}, false
	}
	return cur, true
}

// contextFileReader resolves a backend's materialized-file context route to its
// read half, or reports false when it has none to read.
//
// It asks for agent.ApproachUnsafeFile by name, constructed with no content
// (only its READ side is used). A backend that declares no context surface
// at all has a FOLD, not a loss (backends.UncarriedSurfaces' doc: "reporting
// a folded surface as lost would be a false alarm"), so it is skipped in
// silence rather than reported as an unreadable route.
func contextFileReader(decl agent.Declaration, fs afero.Fs) (agent.StateReader, bool) {
	approach, ok := decl.Construct(agent.SurfaceContext, agent.ApproachUnsafeFile, agent.SurfaceInputs{}, fs)
	if !ok {
		return nil, false
	}
	reader, ok := approach.(agent.StateReader)
	return reader, ok
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
// Declared returns an empty Declaration, so Select skips every kind), so no
// error ever surfaced and the name was appended to
// `removed`. The user's harness was still installed and they had been told it
// was gone. MaterializeProfile in this same package already guards with
// backends.Exists — this is that guard, at the other door.
// The empty default is EXHAUSTIVE here, and the asymmetry with the apply path
// (hookBackendNames, which defaults to the project's configured engines) is
// deliberate: removal must reach managed hooks in an engine the project has
// since stopped configuring. Write narrowly, clean widely; the reverse leaves
// litter nothing will collect.
//
// Exhaustiveness is a property of REMOVAL, not a value anyone types — there is
// no "all".
func manageBackendNames(backend string) ([]string, error) {
	if backend == "" {
		return backends.BackendsWithSettings(), nil
	}
	return namedBackend(backend)
}
