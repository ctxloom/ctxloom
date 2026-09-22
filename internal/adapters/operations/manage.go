package operations

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/afero"

	"github.com/ctxloom/ctxloom/internal/adapters/projectroot"
	"github.com/ctxloom/ctxloom/internal/core/agent"
	"github.com/ctxloom/ctxloom/internal/core/config"
	"github.com/ctxloom/ctxloom/internal/core/delivery"
	"github.com/ctxloom/ctxloom/internal/core/engine"
	"github.com/ctxloom/ctxloom/internal/core/present"
	"github.com/ctxloom/ctxloom/internal/engines"
	"github.com/ctxloom/ctxloom/internal/lm/backends"
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
		if err := removeBackendHarness(ctx, name, workDir, fs); err != nil {
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

// removeBackendHarness delivers the EMPTY plan against the project target:
// the project writer's record says what ctxloom put there and only that is
// removed — the user's own hooks, servers, commands and context stay.
func removeBackendHarness(ctx context.Context, name, workDir string, fs afero.Fs) error {
	kind, ok := engines.Registry().Lookup(engine.Name(name))
	if !ok {
		return fmt.Errorf("failed to remove %s: no engine kind is composed for it", name)
	}
	if err := RemoveProject(ctx, fs, kind, workDir); err != nil {
		return fmt.Errorf("failed to remove %s: %w", name, err)
	}
	return nil
}

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
	// Surfaces is the read half: whether a context file the project
	// writer's record owns still carries what the project's default
	// profiles currently compose. Wiring alone (Backends, above) cannot
	// answer that — "hooks present" says nothing about a stale materialized
	// file. A file the record does not own is absent from this list — see
	// surfaceCurrencies.
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
	opts := []backends.SettingsOption{backends.WithSettingsFS(fs), agent.WithSettingsReporter(terminalReporter().Sink)}

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

// surfaceCurrencies is the read half `manage check` walks: for every
// shipped engine whose context approach writes a file at the project root,
// whether the file the project writer's record OWNS still carries the
// context the current configuration composes. A file the record does not
// own is not reported: the hook-delivered default materializes nothing,
// and an absent file is no finding then.
func surfaceCurrencies(ctx context.Context, cfg *config.Config, fs afero.Fs, workDir string) (surfaces []SurfaceCurrency, errs []string) {
	records, err := OwnershipRecordsOn(fs)
	if err != nil {
		return nil, []string{err.Error()}
	}
	for _, name := range backends.BackendsWithSettings() {
		if IsTestOnlyEngine(name) {
			continue
		}
		kind, ok := engines.Registry().Lookup(engine.Name(name))
		if !ok {
			continue
		}
		rel, ok := contextFileOf(kind)
		if !ok {
			continue
		}
		intended, err := intendedContextFile(ctx, cfg, name)
		if err != nil {
			errs = append(errs, fmt.Sprintf("failed to compose the current context to compare materialized surfaces against: %v", err))
			return surfaces, errs
		}
		cur, owned, err := contextFileCurrency(fs, records, workDir, rel, intended)
		if err != nil {
			errs = append(errs, fmt.Sprintf("failed to read %s's materialized context surface: %v", name, err))
			continue
		}
		if !owned {
			continue
		}
		surfaces = append(surfaces, SurfaceCurrency{Backend: name, Route: rel, Status: string(cur.Status), Detail: cur.Detail})
	}
	return surfaces, errs
}

// contextFileOf is the file an engine's context approach writes at the
// project root, relative to it (a materialize's context file); false for
// an engine whose context approach offers no project root.
func contextFileOf(kind engine.Engine) (string, bool) {
	a, ok := kind.Root().Surfaces()[present.Context]
	if !ok || !a.Traits().Offers(present.RootProjectRoot) {
		return "", false
	}
	c, ok := a.(engine.ContextApproach)
	if !ok {
		return "", false
	}
	const probe = "/probe"
	d, err := c.DeliverContext(present.ProjectOnHost(probe), present.RootProjectRoot, engine.ContextInputs{Text: []byte("probe")}, afero.NewMemMapFs())
	if err != nil || d.Presented.HostPath == "" {
		return "", false
	}
	rel, err := filepath.Rel(probe, d.Presented.HostPath)
	if err != nil {
		return "", false
	}
	return filepath.ToSlash(rel), true
}

// intendedContextFile composes the context the current configuration
// would deliver to backend's file, from the default agent's profile set.
func intendedContextFile(ctx context.Context, cfg *config.Config, backend string) (string, error) {
	materialized, err := AssembleContext(ctx, cfg, AssembleContextRequest{
		Profiles: cfg.DefaultAgentProfiles(),
		Consumer: MaterializedFor(backend),
	})
	if err != nil {
		return "", err
	}
	return materialized.Context, nil
}

// contextFileCurrency is the verdict on one context file the project
// writer's record owns: delivered when it carries the composed context,
// stale when it does not, missing when it is gone. owned is false when the
// record does not own it, and the verdict is then nobody's business.
func contextFileCurrency(fs afero.Fs, records delivery.Ownership, workDir, rel, intended string) (cur agent.Currency, owned bool, err error) {
	path := filepath.Join(workDir, filepath.FromSlash(rel))
	entries, err := records.Owned(path, delivery.ProjectWriter)
	if err != nil || len(entries) == 0 {
		return agent.Currency{}, false, err
	}
	raw, err := afero.ReadFile(fs, path)
	switch {
	case os.IsNotExist(err):
		return agent.Currency{Status: agent.StatusMissing, Detail: fmt.Sprintf("%s does not exist", rel)}, true, nil
	case err != nil:
		return agent.Currency{}, true, err
	case strings.Contains(string(raw), strings.TrimSpace(intended)):
		return agent.Currency{Status: agent.StatusDelivered}, true, nil
	default:
		return agent.Currency{Status: agent.StatusStale, Detail: fmt.Sprintf("%s carries ctxloom-written context that no longer matches the composed context", rel)}, true, nil
	}
}

type SetStatuslineRequest struct {
	Enabled bool `json:"enabled"`
}

// SetStatuslineResult reports the resulting statusline preference.
type SetStatuslineResult struct {
	Status     string `json:"status"`
	Statusline bool   `json:"statusline"`
}

// SetStatusline persists whether ctxloom manages its HUD statusline, inside
// one Owner.Update transaction. The change takes effect on the next hook
// apply (`manage hooks install` / `ctxloom run`).
func SetStatusline(ctx context.Context, app *App, req SetStatuslineRequest) (*SetStatuslineResult, error) {
	if app == nil {
		return nil, fmt.Errorf("app is required")
	}
	enabled := req.Enabled
	if _, err := app.Update(ctx, func(d *config.Draft) error {
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
