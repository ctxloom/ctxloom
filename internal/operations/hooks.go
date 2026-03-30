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
	contextOpts := []backends.ContextFileOption{backends.WithContextFS(fs)}

	// Set executable path for testing if provided
	if req.ExecPath != "" {
		backends.SetExecutablePathForTesting(req.ExecPath)
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

	// Load prompts for command files (shared across backends)
	var bundleOpts []bundles.LoaderOption
	if req.BundleLoaderFS != nil {
		bundleOpts = append(bundleOpts, bundles.WithFS(req.BundleLoaderFS))
	}
	prompts := loadPromptsForCommands(freshCfg, bundleOpts)

	// Determine which backends to apply
	var backendNames []string
	if backend == "all" {
		backendNames = backends.BackendsWithSettings()
	} else {
		backendNames = []string{backend}
	}

	// Apply to each backend
	for _, backendName := range backendNames {
		hooksCfg := &freshCfg.Hooks
		if contextHash != "" {
			hooksCfg.Unified.SessionStart = append(hooksCfg.Unified.SessionStart, backends.NewContextInjectionHook(contextHash, workDir))
		}

		if err := backends.WriteSettings(backendName, hooksCfg, &freshCfg.MCP, bundleMCP, workDir, settingsOpts...); err != nil {
			return nil, fmt.Errorf("failed to apply %s settings: %w", backendName, err)
		}

		// Write command files (each backend handles its own format)
		if len(prompts) > 0 {
			if err := backends.WriteCommandFilesFor(backendName, workDir, prompts); err != nil {
				return nil, fmt.Errorf("failed to write %s commands: %w", backendName, err)
			}
		}

		applied = append(applied, backendName)
	}

	return &ApplyHooksResult{
		Status:      "applied",
		Backends:    applied,
		ContextHash: contextHash,
	}, nil
}

// loadPromptsForCommands loads all prompts from bundles for slash command export.
// Also includes built-in SCM prompts like saveandclear.
func loadPromptsForCommands(cfg *config.Config, opts []bundles.LoaderOption) []*bundles.LoadedContent {
	// Start with built-in prompts (always included)
	prompts := getBuiltinPrompts(cfg)

	bundleDirs := cfg.GetBundleDirs()
	if len(bundleDirs) == 0 {
		return prompts
	}

	loader := bundles.NewLoader(bundleDirs, cfg.Defaults.ShouldUseDistilled(), opts...)
	infos, err := loader.ListAllPrompts()
	if err != nil {
		return prompts
	}

	for _, info := range infos {
		content, err := loader.GetPrompt(info.Name)
		if err != nil {
			continue
		}
		prompts = append(prompts, content)
	}

	return prompts
}

// getBuiltinPrompts returns SCM's built-in slash command prompts.
func getBuiltinPrompts(cfg *config.Config) []*bundles.LoadedContent {
	var prompts []*bundles.LoadedContent

	// /save - compact session to memory
	// Only include if memory is enabled
	if cfg.Memory.Enabled {
		enabled := true

		// Choose prompt based on mode
		var promptContent string
		if cfg.Memory.IsEager() {
			promptContent = saveEagerPrompt
		} else {
			promptContent = saveLazyPrompt
		}

		save := &bundles.LoadedContent{
			Name:    "save",
			Version: "1.0.0",
			Tags:    []string{"memory", "session"},
			Content: promptContent,
		}
		save.Plugins.LM.ClaudeCode.Enabled = &enabled
		save.Plugins.LM.ClaudeCode.Description = "Compact current session to memory"
		prompts = append(prompts, save)
	}

	return prompts
}

// saveEagerPrompt is used when memory.mode is "eager".
// Distilled content is auto-loaded on next session start.
const saveEagerPrompt = `Save the current session context to memory.

Use the SCM MCP tool "compact_session" to save the current session:
- This distills the conversation into key decisions, context, and learnings
- The distilled content will be automatically loaded on your next session

After compaction completes, inform the user:
"Session saved to memory. The distilled context will be loaded automatically on your next session. Run /clear if you want to start fresh now."`

// saveLazyPrompt is used when memory.mode is "lazy".
// Uses vector DB for retrieval instead of auto-loading.
const saveLazyPrompt = `Save the current session context to memory.

Execute these steps in order:

1. Use the SCM MCP tool "compact_session" to save the current session:
   - This distills the conversation into key decisions, context, and learnings

2. Use the SCM MCP tool "index_session" to add the distilled content to the vector database:
   - This enables semantic search across your session history

After indexing completes, inform the user:
"Session saved to memory. Run /clear if you want to start fresh. To continue where you left off in a new session, just ask and I'll search my memory for relevant context using the query_memory tool."

The user can then ask questions like "What were we working on?" or "Continue from before" and you should use the "query_memory" MCP tool to retrieve relevant context from previous sessions.`

// regenerateContext loads fragments from default profiles and writes the context file.
func regenerateContext(cfg *config.Config, workDir string, bundleOpts []bundles.LoaderOption, opts ...backends.ContextFileOption) (string, error) {
	// Load fragments from default profiles using bundles
	loader := bundles.NewLoader(cfg.GetBundleDirs(), cfg.Defaults.ShouldUseDistilled(), bundleOpts...)
	var allFragments []config.FragmentRef

	for _, profileName := range cfg.GetDefaultProfiles() {
		profile, err := config.ResolveProfile(cfg.Profiles, profileName)
		if err != nil {
			continue
		}

		// Add fragments from tags (priority 0)
		if len(profile.Tags) > 0 {
			taggedInfos, _ := loader.ListByTags(profile.Tags)
			for _, info := range taggedInfos {
				allFragments = append(allFragments, config.FragmentRef{Name: info.Name, Priority: 0})
			}
		}

		// Add explicit fragments with their priorities
		allFragments = append(allFragments, profile.Fragments...)
	}

	// Dedupe and sort using bookend strategy
	uniqueFragments := dedupeFragmentRefs(allFragments)
	allFragmentNames := sortFragmentsByPriority(uniqueFragments)

	// Load and write context
	if len(allFragmentNames) == 0 {
		return "", nil
	}

	var backendFrags []*backends.Fragment
	for _, name := range allFragmentNames {
		content, err := loader.GetFragment(name)
		if err != nil {
			continue
		}
		backendFrags = append(backendFrags, &backends.Fragment{
			Name:         content.Name,
			Content:      content.Content,
			Installation: content.Installation,
		})
	}

	if len(backendFrags) == 0 {
		return "", nil
	}

	contextHash, _ := backends.WriteContextFile(workDir, backendFrags, opts...)
	return contextHash, nil
}
