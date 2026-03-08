package operations

import (
	"context"
	"fmt"

	"github.com/SophisticatedContextManager/scm/internal/bundles"
	"github.com/SophisticatedContextManager/scm/internal/config"
	"github.com/SophisticatedContextManager/scm/internal/gitutil"
	"github.com/SophisticatedContextManager/scm/internal/lm/backends"
	"github.com/spf13/afero"
)

// ConfigLoaderFunc is a function that loads configuration.
type ConfigLoaderFunc func() (*config.Config, error)

// ApplyHooksRequest contains parameters for applying hooks.
type ApplyHooksRequest struct {
	Backend           string           `json:"backend"`            // claude-code, gemini, or all
	RegenerateContext bool             `json:"regenerate_context"` // Also regenerate context file
	FS                afero.Fs         `json:"-"`                  // Optional filesystem for testing
	ExecPath          string           `json:"-"`                  // Optional executable path for testing
	ConfigLoader      ConfigLoaderFunc `json:"-"`                  // Optional config loader for testing (defaults to config.Load)
	WorkDir           string           `json:"-"`                  // Optional work directory for testing (defaults to git root)
	SkipSymlink       bool             `json:"-"`                  // Skip symlink creation for testing with MemMapFs
	BundleLoaderFS    afero.Fs         `json:"-"`                  // Optional FS for bundle loader (for testing regenerateContext)
}

// ApplyHooksResult contains the result of applying hooks.
type ApplyHooksResult struct {
	Status      string   `json:"status"`
	Backends    []string `json:"backends"`
	ContextHash string   `json:"context_hash,omitempty"`
}

// ApplyHooks applies SCM hooks to backend configuration files.
func ApplyHooks(ctx context.Context, cfg *config.Config, req ApplyHooksRequest) (*ApplyHooksResult, error) {
	// Set defaults
	backend := req.Backend
	if backend == "" {
		backend = "all"
	}

	// Default filesystem
	fs := req.FS
	if fs == nil {
		fs = afero.NewOsFs()
	}

	// Build options for FS injection
	settingsOpts := []backends.SettingsOption{backends.WithSettingsFS(fs)}
	symlinkOpts := []backends.SymlinkOption{backends.WithSymlinkFS(fs)}
	contextOpts := []backends.ContextFileOption{backends.WithContextFS(fs)}

	if req.ExecPath != "" {
		symlinkOpts = append(symlinkOpts, backends.WithExecPath(req.ExecPath))
	}

	// Reload config to ensure freshness (use injected loader if provided)
	configLoader := req.ConfigLoader
	if configLoader == nil {
		configLoader = func() (*config.Config, error) {
			return config.Load()
		}
	}
	freshCfg, err := configLoader()
	if err != nil {
		return nil, fmt.Errorf("failed to load config: %w", err)
	}

	// Determine work directory (use injected workDir if provided)
	workDir := req.WorkDir
	if workDir == "" {
		workDir = "."
		if root, err := gitutil.FindRoot("."); err == nil {
			workDir = root
		}
	}

	// Ensure SCM symlink exists (skip in tests with MemMapFs which doesn't support symlinks)
	if !req.SkipSymlink {
		if _, err := backends.EnsureSCMSymlink(workDir, symlinkOpts...); err != nil {
			return nil, fmt.Errorf("failed to create scm symlink: %w", err)
		}
	}

	var contextHash string
	if req.RegenerateContext {
		var bundleOpts []bundles.LoaderOption
		if req.BundleLoaderFS != nil {
			bundleOpts = append(bundleOpts, bundles.WithFS(req.BundleLoaderFS))
		}
		contextHash, err = regenerateContext(freshCfg, workDir, bundleOpts, contextOpts...)
		if err != nil {
			return nil, err
		}
	}

	applied := []string{}

	// Load MCP servers from profile bundles
	bundleMCP := freshCfg.ResolveBundleMCPServers()

	// Apply to backends
	if backend == "all" || backend == "claude-code" {
		hooksCfg := &freshCfg.Hooks
		if contextHash != "" {
			hooksCfg.Unified.SessionStart = append(hooksCfg.Unified.SessionStart, backends.NewContextInjectionHook(contextHash))
		}
		if err := backends.WriteSettings("claude-code", hooksCfg, &freshCfg.MCP, bundleMCP, workDir, settingsOpts...); err != nil {
			return nil, fmt.Errorf("failed to apply claude-code settings: %w", err)
		}
		applied = append(applied, "claude-code")
	}

	if backend == "all" || backend == "gemini" {
		hooksCfg := &freshCfg.Hooks
		if contextHash != "" {
			hooksCfg.Unified.SessionStart = append(hooksCfg.Unified.SessionStart, backends.NewContextInjectionHook(contextHash))
		}
		if err := backends.WriteSettings("gemini", hooksCfg, &freshCfg.MCP, bundleMCP, workDir, settingsOpts...); err != nil {
			return nil, fmt.Errorf("failed to apply gemini settings: %w", err)
		}
		applied = append(applied, "gemini")
	}

	return &ApplyHooksResult{
		Status:      "applied",
		Backends:    applied,
		ContextHash: contextHash,
	}, nil
}

// regenerateContext loads fragments from default profiles and writes the context file.
func regenerateContext(cfg *config.Config, workDir string, bundleOpts []bundles.LoaderOption, opts ...backends.ContextFileOption) (string, error) {
	// Load fragments from default profiles using bundles
	loader := bundles.NewLoader(cfg.GetBundleDirs(), cfg.Defaults.ShouldUseDistilled(), bundleOpts...)
	var allFragments []string

	for _, profileName := range cfg.GetDefaultProfiles() {
		profile, err := config.ResolveProfile(cfg.Profiles, profileName)
		if err != nil {
			continue
		}

		if len(profile.Tags) > 0 {
			taggedInfos, _ := loader.ListByTags(profile.Tags)
			for _, info := range taggedInfos {
				allFragments = append(allFragments, info.Name)
			}
		}

		allFragments = append(allFragments, profile.Fragments...)
	}

	// Dedupe
	allFragments = config.DedupeStrings(allFragments)

	// Load and write context
	if len(allFragments) == 0 {
		return "", nil
	}

	var backendFrags []*backends.Fragment
	for _, name := range allFragments {
		content, err := loader.GetFragment(name)
		if err != nil {
			continue
		}
		backendFrags = append(backendFrags, &backends.Fragment{
			Name:    content.Name,
			Content: content.Content,
		})
	}

	if len(backendFrags) == 0 {
		return "", nil
	}

	contextHash, _ := backends.WriteContextFile(workDir, backendFrags, opts...)
	return contextHash, nil
}
