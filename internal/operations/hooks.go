package operations

import (
	"context"
	"fmt"
	"strings"

	"github.com/ctxloom/ctxloom/internal/bundles"
	"github.com/ctxloom/ctxloom/internal/config"
	"github.com/ctxloom/ctxloom/internal/gitutil"
	"github.com/ctxloom/ctxloom/internal/lm/backends"
	"github.com/ctxloom/ctxloom/resources"
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
		// Check for cancellation
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}

		hooksCfg := &freshCfg.Hooks
		if contextHash != "" {
			hooksCfg.Unified.SessionStart = append(hooksCfg.Unified.SessionStart, backends.NewContextInjectionHook(contextHash, workDir))
		}

		if err := backends.WriteSettings(backendName, hooksCfg, &freshCfg.MCP, bundleMCP, workDir, settingsOpts...); err != nil {
			return nil, fmt.Errorf("failed to apply %s settings: %w", backendName, err)
		}

		// Write command files (each backend handles its own format)
		if len(prompts) > 0 {
			cmdOpts := []backends.CommandFileOption{backends.WithCommandFS(fs)}
			if err := backends.WriteCommandFilesFor(backendName, workDir, prompts, cmdOpts...); err != nil {
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
// These are embedded in the SCM binary and always available.
func getBuiltinPrompts(_ *config.Config) []*bundles.LoadedContent {
	names, err := resources.ListBuiltinCommands()
	if err != nil {
		return nil
	}

	var prompts []*bundles.LoadedContent
	for _, name := range names {
		content, err := resources.GetBuiltinCommand(name)
		if err != nil {
			continue
		}

		description, body := parseMarkdownFrontmatter(string(content))
		prompts = append(prompts, &bundles.LoadedContent{
			Name:    name,
			Content: body,
			Plugins: bundles.PluginsConfig{
				LM: bundles.LMPluginConfig{
					ClaudeCode: bundles.ClaudeCodeConfig{
						Description: description,
					},
					Gemini: bundles.GeminiConfig{
						Description: description,
					},
				},
			},
		})
	}
	return prompts
}

// parseMarkdownFrontmatter extracts description from YAML frontmatter and returns body.
// Expects format: ---\ndescription: ...\n---\nbody
func parseMarkdownFrontmatter(content string) (description, body string) {
	if !strings.HasPrefix(content, "---\n") {
		return "", content
	}

	// Find the closing ---
	rest := content[4:] // Skip opening "---\n"
	endIdx := strings.Index(rest, "\n---")
	if endIdx == -1 {
		return "", content
	}

	frontmatter := rest[:endIdx]
	body = strings.TrimPrefix(rest[endIdx+4:], "\n")

	// Parse description from frontmatter (simple key: value parsing)
	for _, line := range strings.Split(frontmatter, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "description:") {
			description = strings.TrimSpace(strings.TrimPrefix(line, "description:"))
			// Remove surrounding quotes if present
			if len(description) >= 2 && description[0] == '"' && description[len(description)-1] == '"' {
				description = description[1 : len(description)-1]
			}
			break
		}
	}

	return description, body
}

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
