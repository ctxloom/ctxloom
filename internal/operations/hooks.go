package operations

import (
	"context"
	"errors"
	"fmt"

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
	ExecPath          string           `json:"-"`                  // Optional executable path for testing
	ConfigLoader      ConfigLoaderFunc `json:"-"`                  // Optional config loader for testing (defaults to config.Load)
	WorkDir           string           `json:"-"`                  // Optional work directory for testing (defaults to git root)
	BundleLoaderFS    afero.Fs         `json:"-"`                  // Optional FS for bundle loader (for testing regenerateContext)
}

// ApplyHooksResult contains the result of applying hooks.
type ApplyHooksResult struct {
	Status      string   `json:"status"`
	Backends    []string `json:"backends"`
	ContextHash string   `json:"context_hash,omitempty"`
	Errors      []string `json:"errors,omitempty"` // per-backend failures; non-empty => partial success
}

// ApplyHooks applies ctxloom hooks to backend configuration files.
func ApplyHooks(ctx context.Context, cfg *config.Config, req ApplyHooksRequest) (*ApplyHooksResult, error) {
	backend := req.Backend
	if backend == "" {
		backend = "all"
	}

	fs := getFS(req.FS)
	contextOpts := []agent.ContextFileOption{agent.WithContextFS(fs)}

	// Set executable path for testing if provided
	if req.ExecPath != "" {
		agent.SetExecutablePathForTesting(req.ExecPath)
	}

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

	// Gate the executable surfaces about to be written to backend settings —
	// bundle MCP servers, bundle hooks, and prompt command-file exports (trust
	// rework, TR5). These bypass the content loader, so each is gated at its own
	// choke via this injected gate; a DENY omits the executable. Built once (runs
	// the migration baseline + opens the trust store, idempotent with the regen
	// content gate). Fault tolerant: the gate never errors (fail-closed) and
	// attaching it never blocks the write. Set before any resolve below so
	// ResolveBundleMCPServers / AssembleManagedHooks / LoadSkillExports all gate.
	execGate := NewExecutableTrustGate(freshCfg)
	freshCfg.SetExecutableTrustGate(execGate.Gate())

	contextHash := maybeRegenerateContext(req, freshCfg, workDir, contextOpts)

	// The native-context backends (antigravity/kiro) read context from their own
	// file, not the injection hook, so apply materializes it from the assembled
	// context STRING — the same content regenerateContext hashed into the cache the
	// hook backends (claude/codex) read, so the two paths agree. Assembled only when
	// context was regenerated this round (contextHash != ""); otherwise "" strips
	// their managed native-context section. Fault tolerant: a failed assembly leaves
	// it "" (the native surface then strips), never blocking the apply.
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
	prompts := backends.LoadSkillExports(freshCfg, nil, bundleLoaderOpts(req)...)

	applied, applyErrors, err := applyHooksToBackends(ctx, hookApplyParams{
		backendNames:     hookBackendNames(backend),
		freshCfg:         freshCfg,
		workDir:          workDir,
		contextHash:      contextHash,
		assembledContext: assembledContext,
		bundleMCP:        bundleMCP,
		prompts:          prompts,
		fs:               fs,
	})
	if err != nil {
		return nil, err
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
	status := "applied"
	if len(applyErrors) > 0 {
		status = "partial"
	}

	return &ApplyHooksResult{
		Status:      status,
		Backends:    applied,
		ContextHash: contextHash,
		Errors:      applyErrors,
	}, nil
}

// bundleLoaderOpts builds the bundle loader options from the request's optional
// test filesystem.
func bundleLoaderOpts(req ApplyHooksRequest) []bundles.LoaderOption {
	if req.BundleLoaderFS == nil {
		return nil
	}
	return []bundles.LoaderOption{bundles.WithFS(req.BundleLoaderFS)}
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

// maybeRegenerateContext regenerates the injected context when requested,
// returning its hash. A regen failure is fatal-class in strict mode (the
// SessionStart-injected context silently going stale/absent is exactly what
// fail-loudly exists to catch); in degraded mode it stays a warning and the
// injection hook is simply omitted this round.
func maybeRegenerateContext(req ApplyHooksRequest, freshCfg *config.Config, workDir string, contextOpts []agent.ContextFileOption) string {
	if !req.RegenerateContext {
		return ""
	}
	contextHash, err := regenerateContext(freshCfg, workDir, bundleLoaderOpts(req), contextOpts...)
	if err != nil {
		strictness.Fail(strictness.ClassApply, "fix the failure, then re-apply (ctxloom manage hooks install)",
			"regenerate context failed: %v", err)
		return ""
	}
	return contextHash
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
	bundleMCP        map[string]wire.MCPServer
	prompts          []*bundles.LoadedContent
	fs               afero.Fs
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
// the surfaces × cells seam: settings (hooks) + MCP + context + skills. The hook
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
// contextViaNativeFile bool. Skills are delivered only when there are prompts,
// preserving the prior guard (no prompts ⇒ command files left untouched).
func applyHooksToBackend(backendName string, p hookApplyParams) error {
	hooksCfg := backends.AssembleManagedHooks(p.freshCfg, p.workDir, p.contextHash, nil)

	set := backends.BuildSurfaces(backendName, agent.SurfaceInputs{
		// Empty when context was not regenerated this round (assembledContext ==
		// ""), which strips a native-file backend's managed context section —
		// matching the prior write. Ignored by the hook-context backends (claude
		// context via Hook writes no native file; codex's context surface reads
		// Fragments, not this string), so it is safe to set for every backend.
		Context:          p.assembledContext,
		MCP:              &p.freshCfg.MCP,
		BundleMCP:        p.bundleMCP,
		Hooks:            hooksCfg,
		ManageStatusline: p.freshCfg.Settings.ShouldManageStatusline(),
		Skills:           backends.CommandExportsFor(backendName, p.prompts),
	}, p.fs)

	sel := agent.Select(set).WithSettings(agent.SettingsWriteUnsafeFile).WithMCP(agent.MCPWriteUnsafeFile)
	if contextViaHook(set) {
		sel = sel.WithContext(agent.ContextWriteHook)
	} else {
		sel = sel.WithContext(agent.ContextWriteUnsafeFile)
	}
	if len(p.prompts) > 0 {
		sel = sel.WithSkills(agent.SkillsWriteUnsafeFile)
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
func regenerateContext(cfg *config.Config, workDir string, bundleOpts []bundles.LoaderOption, opts ...agent.ContextFileOption) (string, error) {
	// Load fragments from default profiles using bundles. This is an exposure
	// surface (the SessionStart-injected context file), so it gates content the
	// same way AssembleContext does (trust rework, TR5) — baseline-first, then
	// withhold anything the cascade denies.
	loader, gate := exposureLoaderGated(cfg, bundleOpts...)

	// Collect through the same path AssembleContext uses: collectProfileFragments
	// emits tag-matched fragments under their canonical qualified names (so
	// dedupeFragmentRefs actually collapses duplicates) and applies each
	// profile's exclude_fragments to them. This function's output MUST match
	// AssembleContext — any divergence ships a SessionStart-injected context
	// that disagrees with what `ctxloom run` assembles. The default set is the
	// default agent's composed profiles (resolveContextProfileNames reads the
	// same DefaultAgentProfiles; profiles.defaults was retired).
	allFragments, profileVars, _, err := collectProfileFragments(cfg, loader, cfg.DefaultAgentProfiles(), nil, true)
	if err != nil {
		return "", err
	}

	// Dedupe and sort using bookend strategy
	uniqueFragments := dedupeFragmentRefs(allFragments)
	orderedRefs := sortFragmentsByPriority(uniqueFragments)

	var backendFrags []*agent.Fragment
	for _, ref := range orderedRefs {
		content, err := loadFragmentRef(loader, ref)
		if err != nil {
			warnFragmentLoadFailure(ref, err)
			continue
		}
		backendFrags = append(backendFrags, &agent.Fragment{
			Name:         content.Name,
			Content:      substituteVariables(content.Content, profileVars, warnSubstitution),
			Installation: content.Installation,
		})
	}

	// Surface (content-free) any items the trust gate withheld while regenerating
	// the SessionStart context, mirroring AssembleContext.
	warnWithheld(loader, gate)

	// Built-in bundles inject their fragments unconditionally — the always-on
	// counterpart to their hooks/MCP — so the SessionStart-injected context file
	// matches AssembleContext. Skipped when the companion binary is absent.
	// Gated through the SAME content gate as loader-resolved fragments
	// (loader.Gate()) so a rejected builtin fragment is withheld here too.
	for _, bf := range cfg.ResolveBuiltinBundleFragments(loader.Gate()) {
		backendFrags = append(backendFrags, &agent.Fragment{
			Name:         bf.Name,
			Content:      bf.Content,
			Installation: bf.Installation,
		})
	}

	if len(backendFrags) == 0 {
		return "", nil
	}

	contextHash, err := agent.WriteContextFile(workDir, backendFrags, opts...)
	if err != nil {
		// Fatal-class in strict mode (context regen failure); degraded mode
		// degrades — the SessionStart injection hook is simply omitted, but
		// says so rather than leaving the user wondering where their context
		// went.
		strictness.Fail(strictness.ClassApply, "fix the write failure, then re-apply (ctxloom manage hooks install)",
			"context file write failed; SessionStart injection skipped: %v", err)
		return "", nil
	}
	return contextHash, nil
}
