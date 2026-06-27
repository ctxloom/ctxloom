package operations

import (
	"context"
	"fmt"

	"github.com/ctxloom/ctxloom/internal/bundles"
	"github.com/ctxloom/ctxloom/internal/config"
	"github.com/ctxloom/ctxloom/internal/gitignore"
	"github.com/ctxloom/ctxloom/internal/lm/backends"
	"github.com/ctxloom/ctxloom/internal/projectroot"
	"github.com/ctxloom/shared/agent"
	"github.com/ctxloom/shared/clidiag"
	"github.com/ctxloom/shared/wire"
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
	settingsOpts := []backends.SettingsOption{backends.WithSettingsFS(fs)}
	contextOpts := []agent.ContextFileOption{agent.WithContextFS(fs)}

	// Set executable path for testing if provided
	if req.ExecPath != "" {
		agent.SetExecutablePathForTesting(req.ExecPath)
	}

	freshCfg, err := resolveHookConfig(req)
	if err != nil {
		return nil, err
	}

	// Honor the statusline opt-out (config: settings.statusline: false).
	settingsOpts = append(settingsOpts, backends.WithStatusLineDisabled(!freshCfg.Settings.ShouldManageStatusline()))

	workDir := resolveHookWorkDir(req)
	contextHash := maybeRegenerateContext(req, freshCfg, workDir, contextOpts)

	// MCP servers from profile bundles + prompts for command files, shared
	// across backends.
	bundleMCP := freshCfg.ResolveBundleMCPServers()
	prompts := backends.LoadPromptExports(freshCfg, bundleLoaderOpts(req)...)

	applied, applyErrors, err := applyHooksToBackends(ctx, hookApplyParams{
		backendNames: hookBackendNames(backend),
		freshCfg:     freshCfg,
		workDir:      workDir,
		contextHash:  contextHash,
		bundleMCP:    bundleMCP,
		prompts:      prompts,
		fs:           fs,
		settingsOpts: settingsOpts,
	})
	if err != nil {
		return nil, err
	}

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
// returning its hash. Fault tolerance: a regen failure must not block hook
// application — warn and return an empty hash so the SessionStart injection
// hook is simply omitted this round.
func maybeRegenerateContext(req ApplyHooksRequest, freshCfg *config.Config, workDir string, contextOpts []agent.ContextFileOption) string {
	if !req.RegenerateContext {
		return ""
	}
	contextHash, err := regenerateContext(freshCfg, workDir, bundleLoaderOpts(req), contextOpts...)
	if err != nil {
		clidiag.Warn("ctxloom", "regenerate context failed: %v", err)
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
	backendNames []string
	freshCfg     *config.Config
	workDir      string
	contextHash  string
	bundleMCP    map[string]wire.MCPServer
	prompts      []*bundles.LoadedContent
	fs           afero.Fs
	settingsOpts []backends.SettingsOption
}

// applyHooksToBackends applies hooks to each backend, returning the backends
// that took and the per-backend failures. Fault tolerance: a single backend's
// failure is recorded and warned, then the rest still run — a partially applied
// set beats none. Context cancellation aborts the whole loop.
func applyHooksToBackends(ctx context.Context, p hookApplyParams) (applied, applyErrors []string, err error) {
	applied = []string{}
	for _, backendName := range p.backendNames {
		if ctx.Err() != nil {
			return applied, applyErrors, ctx.Err()
		}
		if e := applyHooksToBackend(backendName, p); e != nil {
			clidiag.Warn("ctxloom", "%s", e)
			applyErrors = append(applyErrors, e.Error())
			continue
		}
		applied = append(applied, backendName)
	}
	return applied, applyErrors, nil
}

// applyHooksToBackend writes one backend's settings and command files. The hook
// set is assembled fresh per backend (config + default-profile + bundle-shipped
// + context-injection, via AssembleManagedHooks) so this write matches the
// `ctxloom run` Setup path and avoids duplicate-hook accumulation from aliasing
// freshCfg.Hooks across the loop.
func applyHooksToBackend(backendName string, p hookApplyParams) error {
	hooksCfg := backends.AssembleManagedHooks(p.freshCfg, p.workDir, p.contextHash)
	if err := backends.WriteSettings(backendName, hooksCfg, &p.freshCfg.MCP, p.bundleMCP, p.workDir, p.settingsOpts...); err != nil {
		return fmt.Errorf("failed to apply %s settings: %w", backendName, err)
	}

	if len(p.prompts) > 0 {
		cmdOpts := []agent.CommandFileOption{agent.WithCommandFS(p.fs)}
		if err := backends.WriteCommandFilesFor(backendName, p.workDir, p.prompts, cmdOpts...); err != nil {
			return fmt.Errorf("failed to write %s commands: %w", backendName, err)
		}
	}
	return nil
}

// regenerateContext loads fragments from default profiles and writes the context file.
func regenerateContext(cfg *config.Config, workDir string, bundleOpts []bundles.LoaderOption, opts ...agent.ContextFileOption) (string, error) {
	// Load fragments from default profiles using bundles
	loader := cfg.SeededBundleLoader(cfg.ShouldUseDistilled(), bundleOpts...)

	// Resolve defaults the way AssembleContext does (resolveContextProfileNames):
	// ApplyHooks reloads a fresh config, so without EnsureDefaultProfiles a
	// project inheriting its defaults from the home config would regenerate an
	// EMPTY context file here while assembly injects a full one.
	EnsureDefaultProfiles(cfg)

	// Collect through the same path AssembleContext uses: collectProfileFragments
	// emits tag-matched fragments under their canonical qualified names (so
	// dedupeFragmentRefs actually collapses duplicates) and applies each
	// profile's exclude_fragments to them. This function's output MUST match
	// AssembleContext — any divergence ships a SessionStart-injected context
	// that disagrees with what `ctxloom run` assembles.
	allFragments, profileVars, _, err := collectProfileFragments(cfg, loader, cfg.GetDefaultProfiles(), nil, true)
	if err != nil {
		return "", err
	}

	// Dedupe and sort using bookend strategy
	uniqueFragments := dedupeFragmentRefs(allFragments)
	allFragmentNames := sortFragmentsByPriority(uniqueFragments)

	var backendFrags []*agent.Fragment
	for _, name := range allFragmentNames {
		content, err := loader.GetFragment(name)
		if err != nil {
			continue
		}
		backendFrags = append(backendFrags, &agent.Fragment{
			Name:         content.Name,
			Content:      substituteVariables(content.Content, profileVars, func(string) {}),
			Installation: content.Installation,
		})
	}

	// Built-in bundles inject their fragments unconditionally — the always-on
	// counterpart to their hooks/MCP — so the SessionStart-injected context file
	// matches AssembleContext. Skipped when the companion binary is absent.
	for _, bf := range cfg.ResolveBuiltinBundleFragments() {
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
		// Degrade (the SessionStart injection hook is simply omitted), but
		// say so — a silent skip leaves the user wondering where their
		// context went.
		clidiag.Warn("ctxloom", "context file write failed; SessionStart injection skipped: %v", err)
		return "", nil
	}
	return contextHash, nil
}
