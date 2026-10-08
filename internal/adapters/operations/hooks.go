package operations

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/ctxloom/ctxloom/internal/adapters/projectroot"
	"github.com/ctxloom/ctxloom/internal/core/agent"
	"github.com/ctxloom/ctxloom/internal/core/composite"
	"github.com/ctxloom/ctxloom/internal/core/config"
	"github.com/ctxloom/ctxloom/internal/core/delivery"
	"github.com/ctxloom/ctxloom/internal/core/engine"
	"github.com/ctxloom/ctxloom/internal/core/present"
	"github.com/ctxloom/ctxloom/internal/shared/report"
	"github.com/ctxloom/ctxloom/internal/shared/safefs"
	"github.com/ctxloom/ctxloom/internal/shared/strictness"
	"github.com/ctxloom/ctxloom/pkg/clifmt/clidiag"
)

// ApplyHooksRequest contains parameters for applying hooks.
type ApplyHooksRequest struct {
	Backend           string         `json:"backend"`            // an engine name, or all
	RegenerateContext bool           `json:"regenerate_context"` // Also regenerate context file
	Root              safefs.Root    `json:"-"`                  // Optional Root for testing (zero = safefs.New())
	Cfg               *config.Config `json:"-"`                  // The generation to apply from; required
	WorkDir           string         `json:"-"`                  // Optional work directory for testing (defaults to git root)
	// Force overrides the refusal in checkHookTargetScope when the resolved
	// workDir would write Claude Code's user-global settings.json (the $HOME
	// collision). Without it, that collision aborts the
	// whole apply; with it, the collision is downgraded to a loud warning and
	// the apply proceeds — the escape hatch for a genuine intentional global
	// install.
	Force bool `json:"force"`
	// DryRun resolves the whole apply — config, profiles, hooks, MCP servers,
	// context — and then writes NOTHING. It exists because applying IS what
	// ctxloom does at startup: a command that merely starts the MCP server
	// still rewrites the project's settings, which is correct behaviour and
	// also the reason there has to be a way to ask "what would this change?"
	// without changing it. Resolution still runs in full, so findings a real
	// apply would report are reported here too.
	DryRun bool `json:"dry_run"`
}

// ApplyHooksResult contains the result of applying hooks.
type ApplyHooksResult struct {
	Status      string   `json:"status"`
	Backends    []string `json:"backends"`
	ContextHash string   `json:"context_hash,omitempty"`
	// Retracted names, one sentence per file, every native context file this
	// apply stripped ctxloom's managed section from — the file an earlier
	// `profile materialize` wrote for an engine whose installed context route
	// is NOT that file (installedThroughProjectFile). It is a destructive
	// edit, bounded by ctxloom's own markers, and it is reported for that
	// reason: the user sees what went, never merely finds it gone.
	Retracted []string `json:"retracted,omitempty"`
	// Errors holds per-backend failures. Non-empty alongside a non-empty
	// Backends means partial success; non-empty with an EMPTY Backends means
	// nothing was configured at all, which ApplyHooks reports as
	// Status "failed" and a non-nil error.
	Errors []string `json:"errors,omitempty"`
}

// ApplyHooks applies ctxloom hooks to backend configuration files IN THE
// PROJECT, from the generation req.Cfg carries. It never re-reads: a caller
// that has just written config did so through the config Owner's Update,
// which published the generation it then passes here. It is the explicit
// project-side writer; `manage install` and `init` write no engine file and never call it — a
// `ctxloom run` session delivers the same surfaces into its own home.
func ApplyHooks(ctx context.Context, reg engine.Registry, req ApplyHooksRequest) (*ApplyHooksResult, error) {
	// Each apply records its findings in its own FailOnce window.
	defer strictness.Close(strictness.Checkpoint())

	backend := req.Backend

	root := rootOf(req.Root)
	fs := root.Fs
	contextOpts := []agent.ContextFileOption{agent.WithContextFS(fs)}

	freshCfg, err := resolveHookConfig(req)
	if err != nil {
		return nil, err
	}

	// The default profile set (Config.DefaultAgentProfiles, from the default
	// agent) is resolved inside AssemblePackage regardless of
	// RegenerateContext — its MCP servers (Config.ResolveBundleMCPServersFor)
	// and hooks (managedhooks.AssembleFor) — so a default agent's bundles' MCP
	// servers and hooks land in the written settings with no run-only
	// resolution step needed.

	// The statusline opt-out (config: settings.statusline: false) rides the
	// assembled package's Statusline into the settings surface
	// (managedStatuslineEnabled, package.go).

	workDir := resolveHookWorkDir(req)

	// Refuse (or, with --force, loudly warn) when workDir resolves onto a
	// TARGET backend's user-global scope — see checkHookTargetScope. Scoped to
	// `backend` (not every backend unconditionally) so an apply aimed at one
	// backend is never blocked on another backend's collision it is not
	// asking about.
	if err := checkHookTargetScope(reg, freshCfg, workDir, backend, req.Force); err != nil {
		return nil, err
	}

	warnFallbackProjectRoot(req, workDir)

	// The ONE package, for the configured DEFAULT profiles: ApplyHooks writes
	// the project's STATIC managed config (the `manage hooks install` path)
	// and there is no per-run `-p` selection here. The regenerated context and
	// every backend below are written from it, so the default profile set is
	// resolved once per apply.
	pkg, err := AssemblePackage(ctx, freshCfg, PackageRequest{})
	if err != nil {
		return nil, err
	}

	contextHash, regenFailed := maybeRegenerateContext(req, pkg, workDir, contextOpts)

	// The context regenerated (or did not): a file-route engine gets the
	// package's context either way — the file and the cache are composed
	// from one package — and a hook-route engine gets the injection hook
	// only when there is a hash for it to read.

	backendNames, err := hookBackendNames(reg, freshCfg, backend)
	if err != nil {
		return nil, err
	}

	applied, retracted, applyErrors, err := applyHooksToBackends(ctx, reg, hookApplyParams{
		dryRun:       req.DryRun,
		backendNames: backendNames,
		workDir:      workDir,
		pkg:          pkg,
		root:         root,
	})
	if err != nil {
		return nil, err
	}

	// A genuine regen failure must never be reported as a clean
	// "applied" — fold it into the same partial-success accounting a
	// per-backend apply failure already uses, rather than inventing a
	// second status taxonomy. maybeRegenerateContext already recorded the
	// fatal-class strictness finding with the real underlying error; this is
	// the caller-visible echo of it.
	if regenFailed {
		applyErrors = append(applyErrors, "context regeneration failed; existing native-file managed context left untouched rather than cleared (see the warning above for the underlying error)")
	}

	warnRetractions(retracted)

	result := newApplyHooksResult(applied, retracted, applyErrors, contextHash)
	// The result is returned alongside a total-failure error so a caller that
	// wants the per-backend detail still has it; every current caller checks
	// err first and warns or aborts.
	return result, markTotalHookFailure(result)
}

// warnFallbackProjectRoot is the general "not in a project" advisory: only
// meaningful when ApplyHooks resolved workDir itself via the
// cwd/CTXLOOM_ROOT fallback chain (req.WorkDir empty) — an explicitly
// injected WorkDir (tests, or a caller with its own override) didn't come from
// that resolution, so checking the real process cwd against it would be a non
// sequitur.
func warnFallbackProjectRoot(req ApplyHooksRequest, workDir string) {
	if req.WorkDir == "" && projectroot.RootFromFallback() {
		clidiag.Warn("ctxloom", "not in a git repository — using %s as the project root; its tasks, plans, and sessions live under ~/.ctxloom keyed to this path, so re-launch from here to resume them.", workDir)
	}
}

// warnRetractions prints each retracted managed section. A retraction is
// printed as well as returned: the callers that run the apply at startup (the
// MCP server) discard the result, and a managed section that vanishes without
// a line saying so is the silent destructive edit the human accepted this
// behaviour on condition of never having.
func warnRetractions(retracted []string) {
	for _, line := range retracted {
		clidiag.Warn("ctxloom", "%s", line)
	}
}

// newApplyHooksResult reports which backends took and which failed. Partial
// success is success: a per-backend failure does not collapse the whole call
// to an error.
func newApplyHooksResult(applied, retracted, applyErrors []string, contextHash string) *ApplyHooksResult {
	status := "applied"
	if len(applyErrors) > 0 {
		status = "partial"
	}
	return &ApplyHooksResult{
		Status:      status,
		Backends:    applied,
		ContextHash: contextHash,
		Retracted:   retracted,
		Errors:      applyErrors,
	}
}

// markTotalHookFailure turns a result in which every requested backend failed
// into Status "failed" and an error. TOTAL failure is not partial success:
// with nothing applied, nothing at all was written, and answering "partial"
// with Backends [] and a nil error is the silent-no-op shape (exit 0,
// success-ish message, zero bytes written). The word "partial" has to have
// something on both sides of it.
func markTotalHookFailure(result *ApplyHooksResult) error {
	if len(result.Backends) > 0 || len(result.Errors) == 0 {
		return nil
	}
	result.Status = "failed"
	return fmt.Errorf("no backend could be configured: %s", strings.Join(result.Errors, "; "))
}

// resolveHookConfig is the one refusal for a request without a generation.
func resolveHookConfig(req ApplyHooksRequest) (*config.Config, error) {
	if req.Cfg == nil {
		return nil, fmt.Errorf("apply hooks: a config generation is required")
	}
	return req.Cfg, nil
}

// resolveHookWorkDir returns the injected work dir, else the CTXLOOM_ROOT
// override / git root / cwd as resolved by projectroot.WorkDir.
func resolveHookWorkDir(req ApplyHooksRequest) string {
	if req.WorkDir != "" {
		return req.WorkDir
	}
	return projectroot.WorkDir()
}

// checkHookTargetScope refuses to apply hooks when the resolved workDir would
// write a TARGET engine's user-GLOBAL scope instead of a project's
// per-PROJECT scope. Scoped to backend (empty targets the project's
// configured engines; a named backend runs only itself) so a single-backend
// apply is never blocked on a collision for a DIFFERENT backend it never
// touches. The collision class is each engine's own declaration
// (agent.Hosted.HookGlobalScope, read by checkHookTargetScopeOf): an engine
// declaring none is audited as unable to hit it, and this loop branches on
// no engine's identity.
//
// force downgrades every collision to a loud warning and proceeds — the
// deliberate escape hatch for a genuine intentional global install.
//
// Found live: `manage hooks install` run from $HOME silently went global,
// injecting context into every project and duplicating the /clear banner.
func checkHookTargetScope(reg engine.Registry, cfg *config.Config, workDir, backend string, force bool) error {
	// Deliberately UNVALIDATED: the scope guard also covers engines registered
	// through the guard's own table rather than the descriptor registry, and
	// rejecting those here would skip the very check they need. ApplyHooks
	// validates the name on its own path.
	for _, name := range hookBackendNamesUnchecked(reg, cfg, backend) {
		if err := checkHookTargetScopeOf(reg, name, workDir, force); err != nil {
			return err
		}
	}
	return nil
}

// maybeRegenerateContext regenerates the injected context when requested,
// returning its hash and whether the attempt genuinely failed. A regen
// failure is fatal-class in strict mode (the SessionStart-injected context
// silently going stale/absent is exactly what fail-loudly exists to catch);
// in degraded mode it stays a warning and the injection hook is simply
// omitted this round — but regenFailed still tells the caller
// this was a genuine FAILURE, not the legitimate "don't regenerate" request
// (req.RegenerateContext == false), so ApplyHooks can refuse to let a
// failure silently strip a native-file backend's existing managed context
// while still reporting success.
func maybeRegenerateContext(req ApplyHooksRequest, pkg composite.Package, workDir string, contextOpts []agent.ContextFileOption) (hash string, regenFailed bool) {
	if !req.RegenerateContext {
		return "", false
	}
	contextHash, err := regenerateContext(pkg, workDir, contextOpts...)
	if err != nil {
		strictness.Fail(report.KindApply, "fix the failure, then re-apply (ctxloom manage hooks install)",
			"regenerate context failed: %v", err)
		return "", true
	}
	return contextHash, false
}

// hookBackendNames resolves an APPLY's backend filter: a named backend is
// exactly that one, and the empty default — the common case — is every engine
// THE PROJECT CONFIGURES.
//
// There is no "all". Writing to engines the project does not use is not merely
// untidy: an apply that creates another engine's context file wins a race with
// that engine's own delivery, and safefs.WriteFileKeepMode REUSES an existing
// file's mode rather than widening it, so the delivery inherits whatever mode
// the apply chose.
//
// Uninstall does NOT share this default — see manageBackendNames, which is
// exhaustive so a narrow install cannot strand managed hooks in an engine
// nothing will clean up.
//
// The empty-default sweep is for EXPLICIT, whole-project operations only
// (`manage hooks install` with no --engine) — never for a call representing one engine's own
// initialisation. engaging-nutmeg (2026-09-10, ruled: "config/materialization
// should be on engine init") is about exactly that distinction: a project
// that configures several engines and starts (or otherwise materializes) ONE
// of them must write THAT engine's file and no other's, which here means
// passing backend explicitly rather than "". `ctxloom run`'s own per-engine
// write already does this correctly — it never reaches hookBackendNames at
// all, composing and delivering its ONE launched engine's config straight
// through the launch's plan and the runner's static writer — so this
// function's contract
// only needs to hold for callers that DO reach it, which today are the
// explicit sweeps listed above. See
// TestApplyHooks_NamedBackendLeavesOtherConfiguredEnginesUntouched for the
// regression this pins: a named apply must never also write a sibling
// configured engine's file.
//
// An unknown NAME is an error, never a one-element list: every layer below
// reads an unregistered backend as a permitted no-op, so a typo would report
// success having applied nothing. manageBackendNames guards the removal door
// the same way.
func hookBackendNames(reg engine.Registry, cfg *config.Config, backend string) ([]string, error) {
	return backendNames(reg, backend, ConfiguredEngines(reg, cfg))
}

// backendNames resolves a backend filter: the empty filter is the caller's
// default set; a name is ONE backend, refused when unregistered.
func backendNames(reg engine.Registry, backend string, defaults []string) ([]string, error) {
	if backend == "" {
		return defaults, nil
	}
	return namedBackend(reg, backend)
}

// namedBackend resolves ONE named backend, refusing an unregistered name.
//
// Shared by both resolvers because only their DEFAULTS differ (this path's is
// the project's configured engines, removal's is exhaustive) — the refusal is
// the same fact and must read the same either side, or one door gets a guard
// the other does not.
func namedBackend(reg engine.Registry, backend string) ([]string, error) {
	if !EngineExists(reg, backend) {
		return nil, fmt.Errorf("unknown backend %q (supported: %s)", backend, strings.Join(EngineNames(reg), ", "))
	}
	return []string{backend}, nil
}

// hookBackendNamesUnchecked is hookBackendNames without the registration
// guard, for the scope check (see checkHookTargetScope).
func hookBackendNamesUnchecked(reg engine.Registry, cfg *config.Config, backend string) []string {
	if backend == "" {
		return ConfiguredEngines(reg, cfg)
	}
	return []string{backend}
}

// ConfiguredEngines returns every backend THIS PROJECT configures, sorted and
// de-duplicated: the engines resolved from the configured agents' LLM labels.
// It is what an unqualified apply targets.
//
// A project that configures none gets an empty list, and an apply over nothing
// writes nothing — the correct outcome, not a reason to fall back to the whole
// registry.
func ConfiguredEngines(reg engine.Registry, cfg *config.Config) []string {
	if cfg == nil {
		return nil
	}
	seen := map[string]bool{}
	add := func(label string) {
		if backend, _ := ResolveBackend(reg, cfg, label); backend != "" {
			seen[backend] = true
		}
	}
	for _, a := range cfg.GetConfiguredAgents() {
		add(a.LLM)
	}
	// The DEFAULTS count too: a project with no explicit agent still runs on
	// its primary (and distills on its fast) engine, and an apply that skipped
	// them would write nothing for the most common setup of all.
	lm := cfg.GetLMConfig()
	add(lm.Defaults.Primary)
	add(lm.Defaults.Fast)
	// A project that names no engine anywhere still RUNS on one — the shipped
	// default — so it is what an unqualified apply targets. Returning nothing
	// here would silently write nothing for the simplest possible project.
	if len(seen) == 0 {
		if def := DefaultEngineName(reg); def != "" {
			seen[def] = true
		}
	}

	out := make([]string, 0, len(seen))
	for name := range seen {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

// hookApplyParams bundles the per-backend apply inputs (shared across the loop).
type hookApplyParams struct {
	backendNames []string
	workDir      string
	// pkg is the one package the surfaces are written from.
	pkg  composite.Package
	root safefs.Root
	// dryRun stops short of the single write, see ApplyHooksRequest.DryRun.
	dryRun bool
}

// applyHooksToBackends applies hooks to each backend, returning the backends
// that took and the per-backend failures. A single backend's failure is
// recorded and the rest still run — a partially applied set beats none — but
// partial is no longer success in strict mode: each per-backend failure is a
// fatal-class finding the startup choke owner aborts on. Degraded mode keeps
// the warn-and-continue. Context cancellation aborts the whole loop.
func applyHooksToBackends(ctx context.Context, reg engine.Registry, p hookApplyParams) (applied, retracted, applyErrors []string, err error) {
	applied = []string{}
	for _, backendName := range p.backendNames {
		if ctx.Err() != nil {
			return applied, retracted, applyErrors, ctx.Err()
		}
		took, e := applyHooksToBackend(ctx, reg, backendName, p)
		if e != nil {
			strictness.Fail(report.KindApply, "fix the failure, then re-apply (ctxloom manage hooks install)", "%s", e)
			applyErrors = append(applyErrors, e.Error())
			continue
		}
		applied = append(applied, backendName)
		retracted = append(retracted, took...)
	}
	return applied, retracted, applyErrors, nil
}

// contextRidesTheLaunch decides, from the engine's declaration alone, how
// the context reaches it with ctxloom IN the loop: an engine whose context
// approach is told on argv (a launch-time channel, nothing it opens at rest)
// takes the context at launch, so nothing at rest carries it; an engine that
// opens a context file reads the file written at rest.
func contextRidesTheLaunch(root engine.Base) bool {
	a, ok := root.Surfaces()[present.Context]
	return ok && a.Traits().Channel == present.ChannelArgv
}

// applyHooksToBackend delivers the package AT REST into the project root
// for one engine — the placement core (Deliver) at the project root, under
// the engine's per-kind project writers, every kind. An engine that takes its context at launch gets
// none here (a CLAUDE.md beside the system prompt would double it), and a
// section a prior delivery claimed is withdrawn; any other engine gets its
// native context file at the project root. A dry run stops before the write.
func applyHooksToBackend(ctx context.Context, reg engine.Registry, backendName string, p hookApplyParams) (retracted []string, err error) {
	kind, ok := reg.Lookup(engine.Name(backendName))
	if !ok {
		return nil, fmt.Errorf("failed to apply %s: no engine kind is composed for it", backendName)
	}
	pkg := p.pkg
	if contextRidesTheLaunch(kind.Root()) {
		// The context kind is built from the fragments, so leaving them out
		// is what leaves the context out of this delivery's items.
		pkg.Fragments, pkg.Premised = nil, nil
	}
	if p.dryRun {
		return nil, nil
	}
	if _, _, err := Deliver(ctx, p.root, kind, pkg, delivery.Loadout{}, atRestPlacement(p.workDir, kind.Root().Name, delivery.AllKinds())); err != nil {
		return nil, fmt.Errorf("failed to apply %s: %w", backendName, err)
	}
	return nil, nil
}

// regenerateContext writes the SessionStart-injected context file from pkg,
// the ONE package the apply assembled for the default agent's profiles: its
// fragments (AssemblePackage, the same assembly `ctxloom run` delivers — a premised fragment held back
// there is held back here, and the file carries no body the run would not),
// written through the context-file writer. The name each fragment is written
// under is its ref.
func regenerateContext(pkg composite.Package, workDir string, opts ...agent.ContextFileOption) (string, error) {
	var backendFrags []*agent.Fragment
	for _, f := range pkg.Fragments {
		backendFrags = append(backendFrags, &agent.Fragment{Name: f.Value.Name, Content: f.Value.Body})
	}

	if len(backendFrags) == 0 {
		// This is NOT an error — the default profile set legitimately
		// produced nothing — but it reaches the exact same downstream effect
		// as a real failure (native-file backends strip their managed context
		// to match), so warn rather than leave a user wondering why their
		// AGENTS.md went empty; a genuinely-empty context IS the honest
		// current state (matching what `ctxloom run` would also assemble).
		clidiag.Warn("ctxloom", "context regeneration produced no fragments (default profiles resolved zero content) — any existing native-file managed context will be cleared to match; check your default profiles' fragment set if this is unexpected")
		return "", nil
	}

	contextHash, err := agent.WriteContextFile(workDir, backendFrags, opts...)
	if err != nil {
		// This used to be swallowed here (strictness.Fail + return
		// "", nil), collapsing a genuine write failure into the SAME shape
		// maybeRegenerateContext sees for a legitimately-empty fragment set —
		// nil error either way, so its caller could not tell "nothing to
		// write" from "tried to write and broke." Propagate the real error
		// instead; maybeRegenerateContext is now the single place that both
		// records the fatal-class strictness finding and reports regenFailed
		// to ApplyHooks, so a write failure can no longer be reported as a
		// clean "applied" while silently stripping a native-file backend's
		// existing managed context.
		return "", fmt.Errorf("context file write failed: %w", err)
	}
	return contextHash, nil
}
