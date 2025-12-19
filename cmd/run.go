package cmd

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"mlcm/internal/ai"
	_ "mlcm/internal/ai/claudecode" // Register Claude Code plugin
	_ "mlcm/internal/ai/gemini"     // Register Gemini plugin
	"mlcm/internal/config"
	"mlcm/internal/fragments"
)

var (
	runPlugin           string
	runPrompt           string
	runFragments        []string
	runPersona          string
	runSavedPrompt      string
	runDryRun           bool
	runSuppressWarnings bool
	runPrint            bool
)

var runCmd = &cobra.Command{
	Use:   "run [flags] [prompt...]",
	Short: "Assemble context and run AI",
	Long: `Assemble context from fragments and execute the configured AI plugin.

Fragments are searched in order:
  1. .mlcm/context-fragments/ directories walking up from current directory
  2. ~/.mlcm/context-fragments/

Use --persona/-p to load a predefined set of fragments, variables, and generators.
Additional -f flags will be appended to the persona's fragments.

The AI plugin runs in isolation, ignoring default context files like Claude.md.

Examples:
  mlcm run -f coding-standards "review this code"
  mlcm run -p developer "explain the architecture"
  mlcm run -p reviewer -f extra-rules "review this PR"`,
	RunE: func(cmd *cobra.Command, args []string) error {
		// Load configuration
		cfg, err := config.Load()
		if err != nil {
			return fmt.Errorf("failed to load config: %w", err)
		}

		// Create fragment loader
		loader := fragments.NewLoader(cfg.GetFragmentDirs(), fragments.WithSuppressWarnings(runSuppressWarnings))

		// Determine which plugin to use
		pluginName := runPlugin
		if pluginName == "" {
			pluginName = cfg.AI.DefaultPlugin
		}
		if pluginName == "" {
			pluginName = ai.Default()
		}

		// Get plugin configuration
		pluginCfg := cfg.AI.Plugins[pluginName]

		// Get the AI plugin with configuration
		plugin, err := ai.GetWithConfig(pluginName, pluginCfg)
		if err != nil {
			return fmt.Errorf("failed to get AI plugin: %w", err)
		}

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

		// Collect fragments and variables from persona + flags
		var allFragments []string
		personaVars := make(map[string]string)
		var generators []string

		// Determine which persona to use: explicit flag > default from config
		personaName := runPersona
		if personaName == "" && len(runFragments) == 0 {
			// No explicit persona or fragments - use defaults
			personaName = cfg.Defaults.Persona
			allFragments = append(allFragments, cfg.Defaults.Fragments...)
			generators = append(generators, cfg.Defaults.Generators...)
		}

		if personaName != "" {
			persona, exists := cfg.Personas[personaName]
			if !exists {
				return fmt.Errorf("persona %q not found", personaName)
			}
			allFragments = append(allFragments, persona.Fragments...)
			for k, v := range persona.Variables {
				personaVars[k] = v
			}
			generators = append(generators, persona.Generators...)
		}

		// Append additional fragments from -f flags
		allFragments = append(allFragments, runFragments...)

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
			// Merge generator variables into persona vars (generators take precedence)
			for _, frag := range generatorFrags {
				for k, v := range frag.Variables {
					personaVars[k] = v
				}
			}
		}

		// Load and concatenate fragments with persona + generator variables
		var contextContent string
		if len(allFragments) > 0 {
			contextContent, err = loader.LoadMultipleWithVars(allFragments, personaVars)
			if err != nil {
				return fmt.Errorf("failed to load fragments: %w", err)
			}
		}

		// Append generator context outputs
		if len(generatorFrags) > 0 {
			var genContexts []string
			for _, frag := range generatorFrags {
				if frag.Context != "" {
					genContexts = append(genContexts, frag.Context)
				}
			}
			if len(genContexts) > 0 {
				if contextContent != "" {
					contextContent += "\n\n---\n\n"
				}
				contextContent += strings.Join(genContexts, "\n\n---\n\n")
			}
		}

		// Build request
		req := ai.Request{
			Prompt:  prompt,
			Context: contextContent,
			WorkDir: "",
			Print:   runPrint,
		}

		// Dry run mode - show the command that would be executed
		if runDryRun {
			if previewPlugin, ok := plugin.(ai.CommandPreviewPlugin); ok {
				fmt.Println(previewPlugin.CommandPreview(req))
			} else {
				fmt.Println("=== Assembled Context ===")
				if contextContent != "" {
					fmt.Println(contextContent)
				} else {
					fmt.Println("(no context)")
				}
				fmt.Println("\n=== Prompt ===")
				fmt.Println(prompt)
			}
			return nil
		}

		// Run the AI plugin
		resp, err := plugin.Run(context.Background(), req, os.Stdout, os.Stderr)
		if err != nil {
			return fmt.Errorf("AI plugin failed: %w", err)
		}

		if resp.ExitCode != 0 {
			os.Exit(resp.ExitCode)
		}

		return nil
	},
}

func init() {
	rootCmd.AddCommand(runCmd)

	runCmd.Flags().StringVarP(&runPlugin, "plugin", "P", "", "AI plugin to use (default from config)")
	runCmd.Flags().StringVar(&runPrompt, "prompt", "", "Prompt to send to the AI (alternative to positional args)")
	runCmd.Flags().StringVarP(&runSavedPrompt, "run-prompt", "r", "", "Run a saved prompt by name")
	runCmd.Flags().StringSliceVarP(&runFragments, "fragment", "f", nil, "Context fragment(s) to include (can be repeated)")
	runCmd.Flags().StringVarP(&runPersona, "persona", "p", "", "Persona to use (predefined fragment collection)")
	runCmd.Flags().BoolVarP(&runDryRun, "dry-run", "n", false, "Show command that would be executed")
	runCmd.Flags().BoolVarP(&runSuppressWarnings, "quiet", "q", false, "Suppress warnings (e.g., variable redefinition)")
	runCmd.Flags().BoolVar(&runPrint, "print", false, "Print response and exit (non-interactive mode)")
}
