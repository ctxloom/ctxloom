package cmd

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/cbroglie/mustache"
	"github.com/spf13/cobra"

	"github.com/benjaminabbitt/scm/internal/bundles"
	"github.com/benjaminabbitt/scm/internal/config"
	"github.com/benjaminabbitt/scm/internal/gitutil"
	"github.com/benjaminabbitt/scm/internal/lm/backends"
	pb "github.com/benjaminabbitt/scm/internal/lm/grpc"
	"github.com/benjaminabbitt/scm/internal/profiles"
)

var (
	runPlugin           string
	runPrompt           string
	runFragments        []string
	runTags             []string
	runProfile          string
	runSavedPrompt      string
	runDryRun           bool
	runSuppressWarnings bool
	runPrint            bool
	runVerbosity        int
)

var runCmd = &cobra.Command{
	Use:   "run [flags] [prompt...]",
	Short: "Assemble context and run AI",
	Long: `Assemble context from fragments and execute the configured AI plugin.

Fragments are loaded from bundles in .scm/bundles/.

Use --profile/-p to load a predefined set of fragments and variables.
Use --tag/-t to include all fragments with a specific tag.
Additional -f flags will be appended to the profile's fragments.

The AI plugin runs in isolation, ignoring default context files like Claude.md.

Verbosity levels (-v can be repeated):
  -v      Show plugin commands being executed
  -vv     Show command arguments
  -vvv    Show debug output

Examples:
  scm run -f coding-standards "review this code"
  scm run -p developer "explain the architecture"
  scm run -p reviewer -f extra-rules "review this PR"
  scm run -t security "check for vulnerabilities"
  scm run -vv -p developer "debug mode"`,
	RunE: func(cmd *cobra.Command, args []string) error {
		// Load configuration
		cfg, err := config.Load()
		if err != nil {
			return fmt.Errorf("failed to load config: %w", err)
		}

		// Create bundle loader
		bundleLoader := bundles.NewLoader(cfg.GetBundleDirs(), cfg.Defaults.ShouldUseDistilled())

		// Determine which plugin to use
		pluginName := runPlugin
		if pluginName == "" {
			pluginName = cfg.GetDefaultLLMPlugin()
		}

		// Verify the backend exists
		if !backends.Exists(pluginName) {
			return fmt.Errorf("unknown plugin: %s (available: %v)", pluginName, backends.List())
		}

		// Get plugin configuration from config
		pluginCfg := cfg.LM.Plugins[pluginName]

		// Build the prompt - from saved prompt, flag, or remaining args
		// Empty prompt is allowed (starts interactive mode)
		prompt := runPrompt
		if prompt == "" && runSavedPrompt != "" {
			savedPrompt, err := LoadPrompt(cfg, runSavedPrompt)
			if err != nil {
				return fmt.Errorf("failed to load prompt: %w", err)
			}
			prompt = savedPrompt
		}
		if prompt == "" && len(args) > 0 {
			prompt = strings.Join(args, " ")
		}

		// Collect content references from profile + flags
		var allRefs []string
		profileVars := make(map[string]string)

		// Inject built-in SCM variables (available in all templates)
		profileVars["SCM_ROOT"] = cfg.SCMRoot // Project root (parent of .scm)
		profileVars["SCM_DIR"] = cfg.SCMDir   // Full path to .scm directory

		// Determine which profiles to use: explicit flag > default from config
		var profileNames []string
		if runProfile != "" {
			profileNames = []string{runProfile}
		} else if len(runFragments) == 0 && len(runTags) == 0 {
			// No explicit profile, fragments, or tags - use defaults
			profileNames = cfg.GetDefaultProfiles()
		}

		// Create profile loader for .scm/profiles/ directory
		profileDirs := profiles.GetProfileDirs(cfg.SCMPaths)
		profileLoader := profiles.NewLoader(profileDirs)

		// Process all profiles (supports multiple default profiles)
		for _, profileName := range profileNames {
			var profileTags []string
			var profileBundles []string

			// First try to load from .scm/profiles/ directory
			if fileProfile, err := profileLoader.Load(profileName); err == nil {
				// Profile found in .scm/profiles/
				profileTags = fileProfile.Tags
				profileBundles = fileProfile.Bundles
				for k, v := range fileProfile.Variables {
					profileVars[k] = v
				}
			} else {
				// Fall back to config.yaml profiles
				configProfile, err := config.ResolveProfile(cfg.Profiles, profileName)
				if err != nil {
					return fmt.Errorf("failed to resolve profile %q: %w", profileName, err)
				}

				profileTags = configProfile.Tags
				profileBundles = append(configProfile.Bundles, configProfile.BundleItems...)
				// Also include legacy fragments field
				profileBundles = append(profileBundles, configProfile.Fragments...)

				// Collect variables from profile
				for k, v := range configProfile.Variables {
					profileVars[k] = v
				}
			}

			// Include fragments matching profile tags
			if len(profileTags) > 0 {
				taggedInfos, err := bundleLoader.ListByTags(profileTags)
				if err != nil {
					return fmt.Errorf("failed to list fragments by profile tags: %w", err)
				}
				for _, info := range taggedInfos {
					if info.Bundle != "" {
						// Bundle fragment - use bundle#fragments/name syntax
						allRefs = append(allRefs, fmt.Sprintf("%s#fragments/%s", info.Bundle, info.Name))
					} else {
						// Legacy fragment - use simple name
						allRefs = append(allRefs, info.Name)
					}
				}
			}

			// Process bundle references (already in correct format)
			for _, ref := range profileBundles {
				contentRef := profiles.ParseContentRef(ref)
				if contentRef.IsFragment() {
					// Specific fragment reference (bundle#fragments/name) - use as-is
					allRefs = append(allRefs, ref)
				} else if contentRef.IsBundle() {
					// Could be a full bundle or a simple fragment name
					// Try to load as bundle first, using local path for URLs
					bundlePath := contentRef.LocalBundlePath()
					bundle, err := bundleLoader.Load(bundlePath)
					if err == nil {
						// It's a bundle - expand to all fragments
						// Use the full bundle path for fragment references so they can be found
						for fragName := range bundle.Fragments {
							allRefs = append(allRefs, fmt.Sprintf("%s#fragments/%s", bundlePath, fragName))
						}
					} else {
						// Bundle not found - treat as simple fragment name
						// GetFragment will search bundles and legacy dirs
						allRefs = append(allRefs, ref)
					}
				}
			}
		}

		// Append additional refs from -f flags (support # syntax)
		allRefs = append(allRefs, runFragments...)

		// Append fragments matching specified tags
		if len(runTags) > 0 {
			taggedInfos, err := bundleLoader.ListByTags(runTags)
			if err != nil {
				return fmt.Errorf("failed to list fragments by tags: %w", err)
			}
			for _, info := range taggedInfos {
				if info.Bundle != "" {
					allRefs = append(allRefs, fmt.Sprintf("%s#fragments/%s", info.Bundle, info.Name))
				} else {
					allRefs = append(allRefs, info.Name)
				}
			}
		}

		// Dedupe refs
		allRefs = config.DedupeStrings(allRefs)

		// Warn function for reporting non-fatal issues
		warnFunc := func(msg string) {
			if !runSuppressWarnings {
				fmt.Fprintf(os.Stderr, "warning: %s\n", msg)
			}
		}

		// Load all referenced content from bundles
		var protoFragments []*pb.Fragment
		for _, ref := range allRefs {
			content, err := bundleLoader.GetFragment(ref)
			if err != nil {
				return fmt.Errorf("fragment not found: %s", ref)
			}
			// Apply variable substitution
			renderedContent := substituteVariables(content.Content, profileVars, warnFunc)
			protoFragments = append(protoFragments, &pb.Fragment{
				Name:        content.Name,
				Version:     content.Version,
				Tags:        content.Tags,
				Content:     renderedContent,
				IsDistilled: content.IsDistilled,
				DistilledBy: content.DistilledBy,
			})
		}

		// Determine execution mode
		mode := pb.ExecutionMode_INTERACTIVE
		if runPrint {
			mode = pb.ExecutionMode_ONESHOT
		}

		// Build prompt fragment
		var promptFragment *pb.Fragment
		if prompt != "" {
			promptFragment = &pb.Fragment{
				Content: prompt,
			}
		}

		// Determine work directory: git root if in repo, home directory otherwise
		workDir := ""
		if root, err := gitutil.FindRoot("."); err == nil {
			workDir = root
		} else if home, err := os.UserHomeDir(); err == nil {
			workDir = home
		}

		// Build request
		req := &pb.RunRequest{
			Fragments: protoFragments,
			Prompt:    promptFragment,
			Options: &pb.RunOptions{
				WorkDir:     workDir,
				AutoApprove: true,
				Mode:        mode,
				Env:         pluginCfg.Env,
				Verbosity:   uint32(runVerbosity * 16), // Each -v adds 16 to verbosity level
			},
		}

		// Dry run mode - show the assembled context and prompt
		if runDryRun {
			fmt.Println("=== Plugin ===")
			fmt.Println(pluginName)
			fmt.Println("\n=== Fragments ===")
			if len(protoFragments) > 0 {
				for _, f := range protoFragments {
					fmt.Printf("\n--- %s", f.Name)
					if f.Version != "" {
						fmt.Printf(" (v%s)", f.Version)
					}
					if f.IsDistilled {
						fmt.Printf(" [distilled by %s]", f.DistilledBy)
					}
					if len(f.Tags) > 0 {
						fmt.Printf(" tags:%v", f.Tags)
					}
					fmt.Println(" ---")
					fmt.Println(f.Content)
				}
			} else {
				fmt.Println("(no fragments)")
			}
			fmt.Println("\n=== Prompt ===")
			if prompt != "" {
				fmt.Println(prompt)
			} else {
				fmt.Println("(interactive mode)")
			}
			// Show context file that would be written
			fmt.Println("\n=== Context File ===")
			fmt.Printf("Would write to: %s/[hash].md\n", filepath.Join(workDir, backends.SCMContextSubdir))
			if len(protoFragments) > 0 {
				var parts []string
				for _, f := range protoFragments {
					if f.Content != "" {
						parts = append(parts, strings.TrimSpace(f.Content))
					}
				}
				if len(parts) > 0 {
					contextContent := "<!-- DO NOT EDIT: This file is auto-generated by `scm run`. Edit source fragments instead. -->\n\n" +
						strings.Join(parts, "\n\n---\n\n")
					fmt.Println("\nContent:")
					fmt.Println(contextContent)
				}
			}
			return nil
		}

		// Create plugin client
		var client *pb.PluginClient
		if pluginCfg.BinaryPath != "" {
			// Use external plugin binary
			client, err = pb.NewPluginClient(pluginCfg.BinaryPath, pluginCfg.Args, runVerbosity)
		} else {
			// Use built-in plugin via self-invocation
			client, err = pb.NewSelfInvokingClient(pluginName, runVerbosity)
		}
		if err != nil {
			return fmt.Errorf("failed to start plugin: %w", err)
		}
		defer client.Kill()

		// Run the AI plugin
		exitCode, err := client.Run(context.Background(), req, os.Stdout, os.Stderr)
		if err != nil {
			return fmt.Errorf("AI plugin failed: %w", err)
		}

		if exitCode != 0 {
			return &ExitError{Code: int(exitCode)}
		}

		return nil
	},
}

func init() {
	rootCmd.AddCommand(runCmd)

	runCmd.Flags().StringVarP(&runPlugin, "plugin", "l", "", "LLM to use (default from config)")
	runCmd.Flags().StringVar(&runPrompt, "prompt", "", "Prompt to send to the AI (alternative to positional args)")
	runCmd.Flags().StringVarP(&runSavedPrompt, "run-prompt", "r", "", "Run a saved prompt by name")
	runCmd.Flags().StringSliceVarP(&runFragments, "fragment", "f", nil, "Context fragment(s) to include (can be repeated)")
	runCmd.Flags().StringSliceVarP(&runTags, "tag", "t", nil, "Include fragments with this tag (can be repeated)")
	runCmd.Flags().StringVarP(&runProfile, "profile", "p", "", "Profile to use (predefined fragment collection)")
	runCmd.Flags().BoolVarP(&runDryRun, "dry-run", "n", false, "Show command that would be executed")
	runCmd.Flags().BoolVarP(&runSuppressWarnings, "quiet", "q", false, "Suppress warnings (e.g., variable redefinition)")
	runCmd.Flags().BoolVar(&runPrint, "print", false, "Print response and exit (non-interactive mode)")
	runCmd.Flags().CountVarP(&runVerbosity, "verbose", "v", "Increase verbosity (can be repeated: -v, -vv, -vvv)")

	// Register completions
	_ = runCmd.RegisterFlagCompletionFunc("plugin", completePluginNames)
	_ = runCmd.RegisterFlagCompletionFunc("fragment", completeFragmentNames)
	_ = runCmd.RegisterFlagCompletionFunc("tag", completeTagNames)
	_ = runCmd.RegisterFlagCompletionFunc("profile", completeProfileNames)
	_ = runCmd.RegisterFlagCompletionFunc("run-prompt", completePromptNames)
}

// LoadPrompt loads a saved prompt from bundles by name.
func LoadPrompt(cfg *config.Config, name string) (string, error) {
	loader := bundles.NewLoader(cfg.GetBundleDirs(), cfg.Defaults.ShouldUseDistilled())
	prompt, err := loader.GetPrompt(name)
	if err != nil {
		return "", err
	}
	return prompt.Content, nil
}

// substituteVariables applies mustache templating to content using the provided variables.
// It warns about undefined variables referenced in the template.
// Undefined variables are replaced with empty strings.
func substituteVariables(content string, vars map[string]string, warnFunc func(string)) string {
	// Parse the template using the mustache library (handles delimiter changes correctly)
	tmpl, err := mustache.ParseString(content)
	if err != nil {
		warnFunc(fmt.Sprintf("failed to parse template: %v", err))
		return content
	}

	// Check for undefined variables by walking the parsed tags
	seen := make(map[string]bool)
	checkTags(tmpl.Tags(), vars, seen, warnFunc)

	data := make(map[string]interface{})
	for k, v := range vars {
		data[k] = v
	}

	rendered, err := tmpl.Render(data)
	if err != nil {
		warnFunc(fmt.Sprintf("failed to render template: %v", err))
		return content
	}

	return rendered
}

// checkTags recursively walks mustache tags to find undefined variables.
func checkTags(tags []mustache.Tag, vars map[string]string, seen map[string]bool, warnFunc func(string)) {
	for _, tag := range tags {
		name := tag.Name()
		tagType := tag.Type()

		// Only check variable tags (types 1 and 2 are Variable and RawVariable)
		// Section tags (3) and inverted sections (4) also reference variables
		if tagType == 1 || tagType == 2 || tagType == 3 || tagType == 4 {
			if !seen[name] {
				seen[name] = true
				if _, exists := vars[name]; !exists {
					warnFunc(fmt.Sprintf("undefined variable: {{%s}}", name))
				}
			}
		}

		// Recursively check child tags (for sections)
		if tagType == 3 || tagType == 4 { // Section or InvertedSection
			checkTags(tag.Tags(), vars, seen, warnFunc)
		}
	}
}
