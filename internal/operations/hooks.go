package operations

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/ctxloom/ctxloom/internal/bundles"
	"github.com/ctxloom/ctxloom/internal/config"
	"github.com/ctxloom/ctxloom/internal/gitignore"
	"github.com/ctxloom/ctxloom/internal/lm/backends"
	"github.com/ctxloom/ctxloom/internal/projectroot"
	"github.com/ctxloom/ctxloom/internal/shared/agent"
	"github.com/ctxloom/ctxloom/internal/shared/clidiag"
	"github.com/ctxloom/ctxloom/internal/shared/strictness"
	"github.com/ctxloom/ctxloom/internal/shared/wire"
	"github.com/spf13/afero"
)

// ConfigLoaderFunc is a function that loads configuration.
type ConfigLoaderFunc func() (*config.Config, error)

// ApplyHooksRequest contains parameters for applying hooks.
type ApplyHooksRequest struct {
	Backend           string           `json:"backend"`            // claude-code, antigravity, or all
	RegenerateContext bool             `json:"regenerate_context"` // Also regenerate context file
	FS                afero.Fs         `json:"-"`                  // Optional filesystem for testing
	ConfigLoader      ConfigLoaderFunc `json:"-"`                  // Optional config loader for testing (defaults to config.Load)
	WorkDir           string           `json:"-"`                  // Optional work directory for testing (defaults to git root)
	BundleLoaderFS    afero.Fs         `json:"-"`                  // Optional FS for bundle loader (for testing regenerateContext)
	// Force overrides the refusal in checkHookTargetScope when the resolved
	// workDir would write Claude Code's user-global settings.json (the $HOME
	// collision). Without it, that collision aborts the
	// whole apply; with it, the collision is downgraded to a loud warning and
	// the apply proceeds — the escape hatch for a genuine intentional global
	// install.
	Force bool `json:"force"`
}

// ApplyHooksResult contains the result of applying hooks.
type ApplyHooksResult struct {
	Status      string   `json:"status"`
	Backends    []string `json:"backends"`
	ContextHash string   `json:"context_hash,omitempty"`
	// Errors holds per-backend failures. Non-empty alongside a non-empty
	// Backends means partial success; non-empty with an EMPTY Backends means
	// nothing was configured at all, which ApplyHooks reports as
	// Status "failed" and a non-nil error.
	Errors []string `json:"errors,omitempty"`
}

// ApplyHooks applies ctxloom hooks to backend configuration files.
//
// It deliberately takes NO *config.Config. It used to accept one and
// never read it — every caller handed over a Config it had just built and had
// it silently discarded, because ApplyHooks reloads from disk via
// resolveHookConfig. That reload is correct and load-bearing (`manage install`
// writes config.yaml immediately before calling, so a Config captured earlier
// is stale by construction), so the parameter was the thing that was wrong,
// not the reload. Tests and any caller needing a different config inject it
// through ApplyHooksRequest.ConfigLoader, which is the one honoured seam.
func ApplyHooks(ctx context.Context, req ApplyHooksRequest) (*ApplyHooksResult, error) {
	backend := req.Backend
	if backend == "" {
		backend = "all"
	}

	fs := getFS(req.FS)
	contextOpts := []agent.ContextFileOption{agent.WithContextFS(fs)}

	freshCfg, err := resolveHookConfig(req)
	if err != nil {
		return nil, err
	}

	// The default profile set (Config.DefaultAgentProfiles, from the default
	// agent) is read directly off freshCfg by two later steps regardless of
	// RegenerateContext — ResolveBundleMCPServers (below) and the per-backend
	// AssembleManagedHooks — so a default agent's bundles' MCP servers and hooks
	// land in the written settings with no run-only resolution step needed
	// (profiles.defaults + its home-inheritance were retired).

	// The statusline opt-out (config: settings.statusline: false) rides
	// SurfaceInputs.ManageStatusline into the settings surface, read per backend in
	// applyHooksToBackend from freshCfg.Settings.ShouldManageStatusline().

	workDir := resolveHookWorkDir(req)

	// Refuse (or, with --force, loudly warn) when workDir resolves onto a
	// TARGET backend's user-global scope — see checkHookTargetScope. Scoped to
	// `backend` (not every backend unconditionally) so a codex-only apply is
	// never blocked on a claude collision neither of them is asking about,
	// and vice versa.
	if err := checkHookTargetScope(workDir, backend, req.Force); err != nil {
		return nil, err
	}

	// The general "not in a project" advisory: only meaningful when THIS call
	// resolved workDir itself via the cwd/CTXLOOM_ROOT fallback chain
	// (req.WorkDir empty) — an explicitly injected WorkDir (tests, or a future
	// caller with its own override) didn't come from that resolution, so
	// checking the real process cwd against it would be a non sequitur.
	if req.WorkDir == "" && projectroot.RootFromFallback() {
		clidiag.Warn("ctxloom", "not in a git repository — using %s as the project root; its tasks, plans, and sessions live under ~/.ctxloom keyed to this path, so re-launch from here to resume them.", workDir)
	}

	// Gate the executable surfaces about to be written to backend settings —
	// bundle MCP servers, bundle hooks, and prompt command-file exports (trust
	// rework, TR5). These bypass the content loader, so each is gated at its own
	// choke via this injected gate; a DENY omits the executable. Built once (runs
	// the migration baseline + opens the trust store, idempotent with the regen
	// content gate). Fault tolerant: the gate never errors (fail-closed) and
	// attaching it never blocks the write. Set before any resolve below so
	// ResolveBundleMCPServers / AssembleManagedHooks / LoadCommandExports all gate.
	execGate := NewExecutableTrustGate(freshCfg)
	freshCfg.SetExecutableTrustGate(execGate.Authorizer())

	contextHash, regenFailed := maybeRegenerateContext(req, freshCfg, workDir, contextOpts)

	// skipContext is true whenever this round must NOT touch a
	// native-file backend's managed context surface at all — covering BOTH
	// the legitimate "don't regenerate" request (req.RegenerateContext ==
	// false, which deliberately means leave existing context alone) AND a
	// genuine regeneration FAILURE (regenFailed). Neither case has fresh
	// content to write, and unlike a genuinely-empty fragment set (handled
	// inside regenerateContext itself, which still returns "" with
	// regenFailed == false — a real, if unwelcome, current state worth
	// reflecting), a failure or a no-op request must never be indistinguishable
	// from "the context is now empty" at the write layer: WriteManagedContext
	// (antigravity) and writeSteering (kiro) both treat Context: "" as "clear
	// this," which is exactly right for a real empty state and exactly wrong
	// for "we don't know" or "we couldn't tell you." Applied below by
	// omitting WithContext(...) from the surface selection entirely, which is
	// a true skip (no write, nothing stripped) — see cells.go's Select doc
	// ("opt-in selection... with NOTHING selected").
	skipContext := !req.RegenerateContext || regenFailed

	// The native-context backends (antigravity/kiro) read context from their own
	// file, not the injection hook, so apply materializes it from the assembled
	// context STRING — the same content regenerateContext hashed into the cache the
	// hook backends (claude/codex) read, so the two paths agree. Assembled only when
	// context was regenerated this round (contextHash != ""); otherwise "" would
	// strip their managed native-context section, which skipContext now prevents
	// whenever the emptiness is not a genuine, error-free current state.
	var assembledContext string
	if contextHash != "" {
		if asm, aerr := AssembleContext(ctx, freshCfg, AssembleContextRequest{Profiles: freshCfg.DefaultAgentProfiles()}); aerr == nil {
			assembledContext = asm.Context
		}
	}

	// MCP servers from profile bundles + prompts for command files, shared
	// across backends. ApplyHooks writes the project's STATIC managed config
	// (the `manage hooks install` path) for the configured DEFAULT profiles —
	// there is no per-run `-p` selection here — so nil scopes to the defaults.
	bundleMCP := freshCfg.ResolveBundleMCPServers(nil)
	prompts := backends.LoadCommandExports(freshCfg, nil, bundleLoaderOpts(req)...)

	applied, applyErrors, err := applyHooksToBackends(ctx, hookApplyParams{
		backendNames:     hookBackendNames(backend),
		freshCfg:         freshCfg,
		workDir:          workDir,
		contextHash:      contextHash,
		assembledContext: assembledContext,
		skipContext:      skipContext,
		bundleMCP:        bundleMCP,
		prompts:          prompts,
		fs:               fs,
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

	// Advisory: tell the user if a bundle executable (MCP server / hook / prompt
	// export) was withheld by the trust gate (content-free).
	execGate.WarnWithheld()

	// Self-heal the transient-artifact ignores (settings backups, generated
	// .agents/) so they stay covered after any hook apply, not just at init.
	// Skipped when a test FS is injected — the os-based writer would miss it.
	if req.FS == nil {
		if gitErr := gitignore.Ensure(workDir, gitignore.Comment, gitignore.TransientArtifactPatterns...); gitErr != nil {
			clidiag.Warn("ctxloom", "failed to update .gitignore: %v", gitErr)
		}
	}

	// Partial success is success: report which backends took and which
	// failed rather than collapsing the whole call to an error.
	//
	// But TOTAL failure is not partial success. When every backend
	// the request asked for failed, `applied` is empty and nothing at all was
	// written — yet the old code still answered Status "partial", Backends []
	// and a nil error, so `ctxloom manage hooks install` printed
	// "Hooks partial for: []" and exited 0. That is exactly the silent-no-op
	// shape (exit 0, success-ish message, zero bytes written). The word
	// "partial" has to have something on both sides of it.
	status := "applied"
	if len(applyErrors) > 0 {
		status = "partial"
	}
	result := &ApplyHooksResult{
		Status:      status,
		Backends:    applied,
		ContextHash: contextHash,
		Errors:      applyErrors,
	}
	if len(applied) == 0 && len(applyErrors) > 0 {
		result.Status = "failed"
		// The result is returned alongside the error so a caller that wants
		// the per-backend detail still has it; every current caller checks
		// err first and warns or aborts.
		return result, fmt.Errorf("no backend could be configured: %s", strings.Join(applyErrors, "; "))
	}

	return result, nil
}

// bundleLoaderOpts builds the bundle loader options from the request's optional
// test filesystem.
func bundleLoaderOpts(req ApplyHooksRequest) []config.BundleLoaderOption {
	if req.BundleLoaderFS == nil {
		return nil
	}
	return []config.BundleLoaderOption{config.WithBundleLoaderFS(req.BundleLoaderFS)}
}

// resolveHookConfig reloads config for freshness, using the injected loader when
// provided.
func resolveHookConfig(req ApplyHooksRequest) (*config.Config, error) {
	loader := req.ConfigLoader
	if loader == nil {
		loader = func() (*config.Config, error) { return config.Load() }
	}
	freshCfg, err := loader()
	if err != nil {
		return nil, fmt.Errorf("failed to load config: %w", err)
	}
	return freshCfg, nil
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
// write a TARGET backend's user-GLOBAL scope instead of a project's
// per-PROJECT scope — claude's settings.json, codex's whole
// config.toml/prompts/skills home, and kiro's whole
// agents/settings/steering home — all the SAME collision
// class: each backend's project-scoped home is a workDir join, so it
// collapses onto the bare global exactly when workDir == $HOME too. Scoped
// to backend ("all" runs every backend with a settings writer; a named
// backend runs only itself) so a single-backend apply is never blocked on a
// collision for a DIFFERENT backend it never touches.
//
// The per-backend collision paths and messages live in
// internal/lm/backends (registry.go's hookGlobalScopePaths /
// hookGlobalScopeLabel descriptor fields, read by backends.
// CheckHookTargetScope) rather than here — this used to be a hardcoded
// claude/codex/kiro if/else that imported those three backend packages
// directly, a literal ADR-0026 violation (operations, the core, branching on
// backend identity and reaching past the injected backends seam). Routing
// through the descriptor table means a backend that later needs this guard
// registers hookGlobalScopePaths ONCE, in its own descriptor, and this loop
// (and any other caller of backends.CheckHookTargetScope) picks it up with no
// operations-side edit.
//
// antigravity and opencode are AUDITED, not guarded (nil hookGlobalScopePaths
// in their descriptors), because neither can hit this collision class:
// antigravity has no usable global location at all in agy v1.0.7 (see
// AntigravityHookWriter.SettingsPath's doc — there is nothing for the
// project write to collapse onto), and opencode's actual global config lives
// at a DIFFERENT path (~/.config/opencode/opencode.json, per opencode's own
// docs) than its project file (workDir/opencode.json — see
// OpencodeWriter.SettingsPath), so workDir==$HOME never makes the two paths
// equal.
//
// force downgrades every collision to a loud warning and proceeds — the
// deliberate escape hatch for a genuine intentional global install.
//
// Found live 2026-07-14: `manage hooks install` run from
// $HOME silently went global, injecting context into every project and
// duplicating the /clear banner; home entries were removed by hand as a
// stopgap. The codex/kiro guards above are completions of the
// same audit.
func checkHookTargetScope(workDir, backend string, force bool) error {
	for _, name := range hookBackendNames(backend) {
		if err := backends.CheckHookTargetScope(name, workDir, force); err != nil {
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
func maybeRegenerateContext(req ApplyHooksRequest, freshCfg *config.Config, workDir string, contextOpts []agent.ContextFileOption) (hash string, regenFailed bool) {
	if !req.RegenerateContext {
		return "", false
	}
	contextHash, err := regenerateContext(freshCfg, workDir, bundleLoaderOpts(req), contextOpts...)
	if err != nil {
		strictness.Fail(strictness.ClassApply, "fix the failure, then re-apply (ctxloom manage hooks install)",
			"regenerate context failed: %v", err)
		return "", true
	}
	return contextHash, false
}

// hookBackendNames resolves the backend filter to the list of backends to apply.
func hookBackendNames(backend string) []string {
	if backend == "all" {
		return backends.BackendsWithSettings()
	}
	return []string{backend}
}

// hookApplyParams bundles the per-backend apply inputs (shared across the loop).
type hookApplyParams struct {
	backendNames     []string
	freshCfg         *config.Config
	workDir          string
	contextHash      string
	assembledContext string
	// skipContext: true whenever this apply must not touch the
	// context surface at all — see ApplyHooks' skipContext doc for the two
	// cases this covers (no-op request, genuine regen failure).
	skipContext bool
	bundleMCP   map[string]wire.MCPServer
	prompts     []*bundles.LoadedContent
	fs          afero.Fs
}

// applyHooksToBackends applies hooks to each backend, returning the backends
// that took and the per-backend failures. A single backend's failure is
// recorded and the rest still run — a partially applied set beats none — but
// partial is no longer success in strict mode: each per-backend failure is a
// fatal-class finding the startup choke owner aborts on. Degraded mode keeps
// the warn-and-continue. Context cancellation aborts the whole loop.
func applyHooksToBackends(ctx context.Context, p hookApplyParams) (applied, applyErrors []string, err error) {
	applied = []string{}
	for _, backendName := range p.backendNames {
		if ctx.Err() != nil {
			return applied, applyErrors, ctx.Err()
		}
		if e := applyHooksToBackend(backendName, p); e != nil {
			strictness.Fail(strictness.ClassApply, "fix the failure, then re-apply (ctxloom manage hooks install)", "%s", e)
			applyErrors = append(applyErrors, e.Error())
			continue
		}
		applied = append(applied, backendName)
	}
	return applied, applyErrors, nil
}

// applyHooksToBackend writes one backend's managed config into the project via
// the surfaces × cells seam: settings (hooks) + MCP + context + commands. The hook
// set is assembled fresh per backend (config + default-profile + bundle-shipped +
// context-injection, via AssembleManagedHooks) so this write matches the
// `ctxloom run` Setup path and avoids duplicate-hook accumulation from aliasing
// freshCfg.Hooks across the loop.
//
// Apply INSTALLS runtime context injection for the HOOK backends (claude/codex):
// the regenerated contextHash keys the SessionStart inject-context hook INTO their
// settings/config surface (the crucial difference from materialize, which passes ""
// so context stays static). Their context reaches a launched agent via that hook +
// the regenerated cache file, NOT a native file, so the selection names
// WithContext(Hook) for them — resolving (for claude) to a documented no-op that
// writes nothing: a static CLAUDE.md alongside the hook would double the context.
//
// The NATIVE-file backends (antigravity/kiro) read context from .agents/AGENTS.md /
// .kiro/steering and DIVERT the injection hook, so for them apply names
// WithContext(UnsafeFile), materializing the context surface with the assembled
// context. contextViaHook (descriptor-keyed via SupportedApproaches) picks the
// right approach per backend — the enum-driven replacement for the retired
// contextViaNativeFile bool. Commands are delivered only when there are prompts,
// preserving the prior guard (no prompts ⇒ command files left untouched).
func applyHooksToBackend(backendName string, p hookApplyParams) error {
	// The resolved model is what AssembleManagedHooks returns; a settings writer
	// takes its Wire() projection. Ordering and provenance stay in the model, so
	// what `manage hooks list` reports and what lands in this backend's settings
	// file are the same resolution, read twice.
	hooksCfg := backends.AssembleManagedHooks(p.freshCfg, p.workDir, p.contextHash, nil).Wire()
	mcpCfg := p.freshCfg.GetMCPConfig()
	settings := p.freshCfg.GetSettings()

	set := backends.BuildSurfaces(backendName, agent.SurfaceInputs{
		// Empty when context was not regenerated this round (assembledContext ==
		// ""), which strips a native-file backend's managed context section —
		// matching the prior write. Ignored by the hook-context backends (claude
		// context via Hook writes no native file; codex's context surface reads
		// Fragments, not this string), so it is safe to set for every backend.
		Context:          p.assembledContext,
		MCP:              &mcpCfg,
		BundleMCP:        p.bundleMCP,
		Hooks:            hooksCfg,
		ManageStatusline: settings.ShouldManageStatusline(),
		Commands:         backends.CommandExportsFor(backendName, p.prompts),
		DenyTools:        backends.AssembleManagedDenyTools(p.freshCfg, nil),
	}, p.fs)

	sel := agent.Select(set).WithSettings(agent.SettingsWriteUnsafeFile).WithMCP(agent.MCPWriteUnsafeFile)
	// skipContext omits WithContext entirely rather than selecting
	// it with empty content — Select's opt-in model means an unselected
	// surface is never delivered at all (cells.go), so this is a true no-op:
	// nothing is written, nothing is stripped. Selecting ContextWriteUnsafeFile
	// with p.assembledContext == "" is what used to reach the native-file
	// writers and get interpreted as "clear the managed section" regardless of
	// WHY it was empty.
	if !p.skipContext {
		if contextViaHook(set) {
			sel = sel.WithContext(agent.ContextWriteHook)
		} else {
			sel = sel.WithContext(agent.ContextWriteUnsafeFile)
		}
	}
	if len(p.prompts) > 0 {
		sel = sel.WithCommands(agent.CommandsWriteUnsafeFile)
	}
	if _, _, errs := sel.DeliverUnder(p.workDir); len(errs) > 0 {
		return fmt.Errorf("failed to apply %s: %w", backendName, errors.Join(errs...))
	}
	return nil
}

// contextViaHook reports whether the backend delivers context through the
// SessionStart inject-context hook (claude/codex — their context surface
// advertises agent.ApproachHook) rather than a native context file
// (antigravity/kiro, UnsafeFile only). It is the descriptor-keyed replacement for
// the retired backends.ContextViaNativeFile: apply reads it to pick
// WithContext(Hook) vs WithContext(UnsafeFile), so a static native file is never
// written alongside the hook.
func contextViaHook(set agent.SurfaceSet) bool {
	for _, a := range set.SupportedApproaches(agent.SurfaceContext) {
		if a == agent.ApproachHook {
			return true
		}
	}
	return false
}

// regenerateContext loads fragments from default profiles and writes the context file.
func regenerateContext(cfg *config.Config, workDir string, bundleOpts []config.BundleLoaderOption, opts ...agent.ContextFileOption) (string, error) {
	// Load fragments from default profiles using bundles. This is an exposure
	// surface (the SessionStart-injected context file), so it gates content the
	// same way AssembleContext does (trust rework, TR5) — baseline-first, then
	// withhold anything the cascade denies.
	pipe, gate := exposurePipelineGated(cfg, bundleOpts...)

	// Collect through the same path AssembleContext uses: collectProfileFragments
	// emits tag-matched fragments under their canonical qualified names (so
	// dedupeFragmentRefs actually collapses duplicates) and applies each
	// profile's exclude_fragments to them. This function's output MUST match
	// AssembleContext — any divergence ships a SessionStart-injected context
	// that disagrees with what `ctxloom run` assembles. The default set is the
	// default agent's composed profiles (resolveContextProfileNames reads the
	// same DefaultAgentProfiles; profiles.defaults was retired).
	allFragments, profileVars, _, _, err := collectProfileFragments(cfg, pipe.Loader(), cfg.DefaultAgentProfiles(), nil, true)
	if err != nil {
		return "", err
	}

	// Dedupe and sort using bookend strategy
	uniqueFragments := dedupeFragmentRefs(allFragments)
	orderedRefs := sortFragmentsByPriority(uniqueFragments)

	// The SAME ingest accumulator AssembleContext uses, for the same reason:
	// this function has two routes into one context (loader-resolved here,
	// injected builtins below) and only one of them may deliver a given piece
	// of content. See contextIngest for the identity rule and the order/silence
	// decisions.
	ingest := newContextIngest()
	for _, ref := range orderedRefs {
		content, err := loadFragmentRef(pipe, ref)
		if err != nil {
			warnFragmentLoadFailure(ref, err)
			continue
		}
		// Ref is the canonical item ref (identity); Name is the reporting name
		// this path has always written into the context file. They differ here
		// and contextIngest keeps them apart on purpose.
		ingest.add(ingestedFragment{
			Ref:     ref.Name,
			Name:    content.Name,
			Content: substituteVariables(content.Content, profileVars, warnSubstitutionFor(content.Name)),
		})
	}

	// Surface (content-free) any items the trust gate withheld while regenerating
	// the SessionStart context, mirroring AssembleContext.
	warnWithheld(gate)

	// Built-in bundles inject their fragments unconditionally — the always-on
	// counterpart to their hooks/MCP — so the SessionStart-injected context file
	// matches AssembleContext. Skipped when the companion binary is absent.
	// Gated through the SAME content gate as loader-resolved fragments
	// (pipe.Authorizer()) so a rejected builtin fragment is withheld here too.
	// Ingested AFTER the loader-resolved fragments so a builtin that was also
	// selected by ref collapses into the selection, not the reverse.
	for _, bf := range cfg.ResolveBuiltinBundleFragments(pipe.Authorizer()) {
		ingest.add(ingestedFragment{Ref: bf.Name, Name: bf.Name, Content: bf.Content})
	}

	var backendFrags []*agent.Fragment
	for _, f := range ingest.fragments() {
		backendFrags = append(backendFrags, &agent.Fragment{Name: f.Name, Content: f.Content})
	}

	if len(backendFrags) == 0 {
		// This is NOT an error — regenerateContext
		// legitimately produced nothing (an empty default profile set is a
		// valid configuration) — but it silently reached the exact same
		// downstream effect as a real failure (native-file backends strip
		// their managed context to match) with zero diagnostic at all. Warn
		// so a user is not left wondering why their AGENTS.md/steering file
		// went empty; still return ("", nil) — a genuinely-empty context IS
		// the honest current state (matching what `ctxloom run` would also
		// assemble), so stripping to match it is correct, just no longer
		// silent.
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
