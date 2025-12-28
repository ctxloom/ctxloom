package cmd

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/benjaminabbitt/scm/internal/config"
	"github.com/benjaminabbitt/scm/internal/fragments"
	"github.com/benjaminabbitt/scm/internal/lm/backends"
	pb "github.com/benjaminabbitt/scm/internal/lm/grpc"
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
)

var runCmd = &cobra.Command{
	Use:   "run [flags] [prompt...]",
	Short: "Assemble context and run AI",
	Long: `Assemble context from fragments and execute the configured AI plugin.

Fragments are loaded from a single source (first found):
  1. <git-root>/.scm/context-fragments/ (project)
  2. ~/.scm/context-fragments/ (home)
  3. Embedded resources (fallback)

Use --profile/-p to load a predefined set of fragments, variables, and generators.
Use --tag/-t to include all fragments with a specific tag.
Additional -f flags will be appended to the profile's fragments.

The AI plugin runs in isolation, ignoring default context files like Claude.md.

Examples:
  scm run -f coding-standards "review this code"
  scm run -p developer "explain the architecture"
  scm run -p reviewer -f extra-rules "review this PR"
  scm run -t security "check for vulnerabilities"
  scm run -t review -t style "comprehensive code review"`,
	RunE: func(cmd *cobra.Command, args []string) error {
		// Load configuration
		cfg, err := config.Load()
		if err != nil {
			return fmt.Errorf("failed to load config: %w", err)
		}

		// Create fragment loader with appropriate source
		loaderOpts := []fragments.LoaderOption{
			fragments.WithSuppressWarnings(runSuppressWarnings),
			fragments.WithPreferDistilled(cfg.Defaults.ShouldUseDistilled()),
			fragments.WithFailOnMissing(true),
		}
		if cfg.IsEmbedded() {
			loaderOpts = append(loaderOpts, fragments.WithFS(cfg.GetFragmentFS()))
		}
		loader := fragments.NewLoader(cfg.GetFragmentDirs(), loaderOpts...)

		// Determine which plugin to use
		pluginName := runPlugin
		if pluginName == "" {
			pluginName = cfg.LM.GetDefaultPlugin()
		}

		// Verify the backend exists
		if !backends.Exists(pluginName) {
			return fmt.Errorf("unknown plugin: %s (available: %v)", pluginName, backends.List())
		}

		// Get plugin configuration from config
		pluginCfg := cfg.LM.Plugins[pluginName]
		_ = pluginCfg // Will be used when we configure the backend

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

		// Collect fragments and variables from profile + flags
		var allFragments []string
		profileVars := make(map[string]string)
		var generators []string

		// Determine which profiles to use: explicit flag > default from config
		var profileNames []string
		if runProfile != "" {
			profileNames = []string{runProfile}
		} else if len(runFragments) == 0 && len(runTags) == 0 {
			// No explicit profile, fragments, or tags - use defaults
			profileNames = cfg.GetDefaultProfiles()
			generators = append(generators, cfg.Defaults.Generators...)
		}

		// Process all profiles (supports multiple default profiles)
		for _, profileName := range profileNames {
			// Resolve profile with inheritance
			profile, err := config.ResolveProfile(cfg.Profiles, profileName)
			if err != nil {
				return fmt.Errorf("failed to resolve profile %q: %w", profileName, err)
			}

			// Include fragments matching profile tags
			if len(profile.Tags) > 0 {
				taggedInfos, err := loader.ListByTags(profile.Tags)
				if err != nil {
					return fmt.Errorf("failed to list fragments by profile tags: %w", err)
				}
				for _, info := range taggedInfos {
					allFragments = append(allFragments, info.Name)
				}
			}

			// Include explicit fragments
			allFragments = append(allFragments, profile.Fragments...)
			for k, v := range profile.Variables {
				profileVars[k] = v
			}
			generators = append(generators, profile.Generators...)
		}

		// Append additional fragments from -f flags
		allFragments = append(allFragments, runFragments...)

		// Append fragments matching specified tags
		if len(runTags) > 0 {
			taggedInfos, err := loader.ListByTags(runTags)
			if err != nil {
				return fmt.Errorf("failed to list fragments by tags: %w", err)
			}
			for _, info := range taggedInfos {
				allFragments = append(allFragments, info.Name)
			}
		}

		// Dedupe fragments and generators before processing.
		// This handles the diamond problem when multiple profiles share common fragments.
		allFragments = config.DedupeStrings(allFragments)
		generators = config.DedupeStrings(generators)

		// Warn function for reporting non-fatal issues
		warnFunc := func(msg string) {
			if !runSuppressWarnings {
				fmt.Fprintf(os.Stderr, "warning: %s\n", msg)
			}
		}

		// Run generators and collect their fragments
		var generatorFrags []*fragments.Fragment
		if len(generators) > 0 {
			generatorFrags, err = RunGenerators(cfg, generators, warnFunc)
			if err != nil {
				return fmt.Errorf("failed to run generators: %w", err)
			}
			// Merge generator exports into profile vars (generators take precedence)
			for _, frag := range generatorFrags {
				for k, v := range frag.Exports {
					profileVars[k] = v
				}
			}
		}

		// Load fragments with metadata
		var loadedFrags []*fragments.LoadedFragment
		if len(allFragments) > 0 {
			loadedFrags, err = loader.LoadMultipleAsFragments(allFragments, profileVars)
			if err != nil {
				return fmt.Errorf("failed to load fragments: %w", err)
			}
		}

		// Convert loaded fragments to proto format
		var protoFragments []*pb.Fragment
		for _, lf := range loadedFrags {
			protoFragments = append(protoFragments, &pb.Fragment{
				Name:        lf.Name,
				Version:     lf.Version,
				Tags:        lf.Tags,
				Content:     lf.Content,
				IsDistilled: lf.IsDistilled,
				DistilledBy: lf.DistilledBy,
			})
		}

		// Append generator outputs as fragments
		for _, frag := range generatorFrags {
			if frag.Content != "" {
				protoFragments = append(protoFragments, &pb.Fragment{
					Name:    frag.Name,
					Tags:    frag.Tags,
					Content: frag.Content,
				})
			}
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

		// Build request
		req := &pb.RunRequest{
			Fragments: protoFragments,
			Prompt:    promptFragment,
			Options: &pb.RunOptions{
				WorkDir:     "",
				AutoApprove: true,
				Mode:        mode,
				Env:         pluginCfg.Env,
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
			return nil
		}

		// Create plugin client
		var client *pb.PluginClient
		if pluginCfg.BinaryPath != "" {
			// Use external plugin binary
			client, err = pb.NewPluginClient(pluginCfg.BinaryPath, pluginCfg.Args)
		} else {
			// Use built-in plugin via self-invocation
			client, err = pb.NewSelfInvokingClient(pluginName)
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
}
