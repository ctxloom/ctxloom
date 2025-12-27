package cmd

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"regexp"
	"strings"

	"github.com/spf13/cobra"

	"mlcm/internal/config"
	"mlcm/internal/fragments"
	"mlcm/internal/ml"
	_ "mlcm/internal/ml/claudecode"
	_ "mlcm/internal/ml/gemini"
	"mlcm/internal/schema"
	"mlcm/resources"
)

// getDistillPrompt loads the distillation prompt from embedded resources.
func getDistillPrompt() (string, error) {
	data, err := resources.GetPrompt("distill")
	if err != nil {
		return "", fmt.Errorf("failed to load distill prompt: %w", err)
	}
	return strings.TrimSpace(string(data)), nil
}

var (
	distillPlugin      string
	distillPersona     string
	distillFragments   []string
	distillPromptNames []string
	distillDryRun      bool
	distillForce       bool
	distillOnlyPrompts bool
	distillSkipPrompts bool
	distillResources   bool
)

var distillCmd = &cobra.Command{
	Use:   "distill [flags]",
	Short: "Create minimal-token versions of fragments and prompts",
	Long: `Distill fragments and prompts to create minimal-token versions that preserve meaning.

This command processes each fragment and prompt through an AI to create a compressed
version that preserves all rules and behaviors while reducing verbosity.
Distilled content is stored in the fragment's 'distilled' field alongside content_hash
and distilled_by metadata.

When loading fragments, the distilled version is used if available
(controlled by use_distilled config setting).

Use --persona/-p to distill only fragments associated with specific personas.
Use --fragment/-f to distill specific fragments by name.
Use --prompt/-P to distill specific prompts by name.
Use --prompts-only to distill only prompts (skip fragments).
Use --skip-prompts to distill only fragments (skip prompts).
Use --resources to distill embedded resources (for packaging).

Examples:
  mlcm distill                           # Distill all fragments and prompts
  mlcm distill -p go-developer           # Distill fragments for go-developer persona
  mlcm distill -f style/direct           # Distill specific fragments
  mlcm distill -P code-review            # Distill specific prompts
  mlcm distill --prompts-only            # Distill only prompts
  mlcm distill --dry-run                 # Preview what would be distilled
  mlcm distill --resources               # Distill resources/ for packaging`,
	RunE: runDistill,
}

func runDistill(cmd *cobra.Command, args []string) error {
	// Load configuration (respects --home flag)
	cfg, err := GetConfig()
	if err != nil {
		return fmt.Errorf("failed to load config: %w", err)
	}

	// Determine which plugin to use
	pluginName := distillPlugin
	if pluginName == "" {
		pluginName = cfg.LM.DefaultPlugin
	}
	if pluginName == "" {
		pluginName = ml.Default()
	}

	// Get plugin configuration
	pluginCfg := cfg.LM.Plugins[pluginName]

	// Get the AI plugin with configuration
	plugin, err := ml.GetWithConfig(pluginName, pluginCfg)
	if err != nil {
		return fmt.Errorf("failed to get AI plugin: %w", err)
	}

	// Load the distillation prompt from embedded resources
	distillPrompt, err := getDistillPrompt()
	if err != nil {
		return err
	}

	// Create validator for schema checking before distilling
	validator, err := schema.NewValidator()
	if err != nil {
		return fmt.Errorf("failed to create validator: %w", err)
	}

	// Track overall results
	totalSuccess := 0
	totalSkipped := 0
	totalInvalid := 0

	// Distill fragments unless --prompts-only is set
	if !distillOnlyPrompts {
		var fragmentDirs []string
		if distillResources {
			// Use resources directory for packaging
			fragmentDirs = []string{"resources/context-fragments"}
		} else {
			fragmentDirs, err = GetFragmentDirs()
			if err != nil {
				return fmt.Errorf("failed to get fragment directories: %w", err)
			}
		}

		if len(fragmentDirs) > 0 {
			loader := fragments.NewLoader(fragmentDirs, fragments.WithPreferDistilled(false))

			var fragmentNames []string
			if len(distillFragments) > 0 {
				fragmentNames = distillFragments
			} else if distillPersona != "" {
				// Resolve persona with inheritance
				persona, err := config.ResolvePersona(cfg.Personas, distillPersona)
				if err != nil {
					return fmt.Errorf("failed to resolve persona %q: %w", distillPersona, err)
				}

				// Include fragments matching persona tags
				if len(persona.Tags) > 0 {
					taggedInfos, err := loader.ListByTags(persona.Tags)
					if err != nil {
						return fmt.Errorf("failed to list fragments by persona tags: %w", err)
					}
					for _, info := range taggedInfos {
						fragmentNames = append(fragmentNames, info.Name)
					}
				}

				// Include explicit fragments
				fragmentNames = append(fragmentNames, persona.Fragments...)
			} else {
				frags, err := loader.List()
				if err != nil {
					return fmt.Errorf("failed to list fragments: %w", err)
				}
				for _, f := range frags {
					fragmentNames = append(fragmentNames, f.Name)
				}
			}

			if len(fragmentNames) > 0 {
				fmt.Printf("Distilling %d fragments using %s...\n", len(fragmentNames), pluginName)

				var missingFragments []string
				for _, name := range fragmentNames {
					frag, err := loader.Load(name)
					if err != nil {
						fmt.Fprintf(os.Stderr, "  Error: fragment not found: %s\n", name)
						missingFragments = append(missingFragments, name)
						continue
					}

					// Skip fragments marked as no_distill
					if frag.NoDistill {
						fmt.Printf("  Skipping %s (no_distill)\n", name)
						totalSkipped++
						continue
					}

					// Validate against schema before distilling
					if frag.Path != "" {
						data, err := os.ReadFile(frag.Path)
						if err == nil {
							if err := validator.ValidateBytes(data); err != nil {
								fmt.Fprintf(os.Stderr, "  Skipping invalid fragment %s: %v\n", name, err)
								totalInvalid++
								continue
							}
						}
					}

					// Check if distillation is needed
					if !distillForce && !frag.NeedsDistill() {
						fmt.Printf("  Skipping %s (unchanged)\n", name)
						totalSkipped++
						continue
					}

					if distillDryRun {
						fmt.Printf("  Would distill: %s\n", name)
						continue
					}

					if frag.Distilled != "" {
						if frag.NeedsDistill() {
							fmt.Printf("  Re-distilling %s (source changed)...", name)
						} else {
							fmt.Printf("  Re-distilling %s (forced)...", name)
						}
					} else {
						fmt.Printf("  Distilling: %s...", name)
					}

					distilledContent, err := distillContent(plugin, frag.Name, frag.Content, distillPrompt)
					if err != nil {
						fmt.Printf(" FAILED: %v\n", err)
						continue
					}

					// Update the fragment with distilled content
					frag.Distilled = distilledContent
					frag.ContentHash = frag.ComputeContentHash()
					frag.DistilledBy = pluginName

					// Save back to the same file
					if err := frag.Save(); err != nil {
						fmt.Printf(" FAILED: %v\n", err)
						continue
					}

					fmt.Printf(" OK\n")
					totalSuccess++
				}

				// Fail if any fragments were not found
				if len(missingFragments) > 0 {
					return fmt.Errorf("fragments not found: %s", strings.Join(missingFragments, ", "))
				}
			}
		}
	}

	// Distill prompts unless --skip-prompts is set
	// Prompts use the same YAML format as fragments
	if !distillSkipPrompts {
		var promptDirs []string
		if distillResources {
			// Use resources directory for packaging
			promptDirs = []string{"resources/prompts"}
		} else {
			promptDirs, err = GetPromptDirs()
			if err != nil {
				return fmt.Errorf("failed to get prompt directories: %w", err)
			}
		}

		if len(promptDirs) > 0 {
			// Use fragment loader for prompts (same YAML format)
			promptLoader := fragments.NewLoader(promptDirs, fragments.WithPreferDistilled(false))

			var promptNames []string
			if len(distillPromptNames) > 0 {
				promptNames = distillPromptNames
			} else {
				// List all prompts from directories
				prompts, err := promptLoader.List()
				if err != nil {
					return fmt.Errorf("failed to list prompts: %w", err)
				}
				for _, p := range prompts {
					promptNames = append(promptNames, p.Name)
				}
			}

			if len(promptNames) > 0 {
				fmt.Printf("Distilling %d prompts using %s...\n", len(promptNames), pluginName)

				for _, name := range promptNames {
					prompt, err := promptLoader.Load(name)
					if err != nil {
						fmt.Fprintf(os.Stderr, "  Warning: prompt not found: %s\n", name)
						continue
					}

					// Skip prompts marked as no_distill
					if prompt.NoDistill {
						fmt.Printf("  Skipping %s (no_distill)\n", name)
						totalSkipped++
						continue
					}

					// Validate against schema before distilling
					if prompt.Path != "" {
						data, err := os.ReadFile(prompt.Path)
						if err == nil {
							if err := validator.ValidateBytes(data); err != nil {
								fmt.Fprintf(os.Stderr, "  Skipping invalid prompt %s: %v\n", name, err)
								totalInvalid++
								continue
							}
						}
					}

					// Check if distillation is needed
					if !distillForce && !prompt.NeedsDistill() {
						fmt.Printf("  Skipping %s (unchanged)\n", name)
						totalSkipped++
						continue
					}

					if distillDryRun {
						fmt.Printf("  Would distill: %s\n", name)
						continue
					}

					if prompt.Distilled != "" {
						if prompt.NeedsDistill() {
							fmt.Printf("  Re-distilling %s (source changed)...", name)
						} else {
							fmt.Printf("  Re-distilling %s (forced)...", name)
						}
					} else {
						fmt.Printf("  Distilling: %s...", name)
					}

					distilledContent, err := distillContent(plugin, prompt.Name, prompt.Content, distillPrompt)
					if err != nil {
						fmt.Printf(" FAILED: %v\n", err)
						continue
					}

					// Update the prompt with distilled content
					prompt.Distilled = distilledContent
					prompt.ContentHash = prompt.ComputeContentHash()
					prompt.DistilledBy = pluginName

					// Save back to the same file
					if err := prompt.Save(); err != nil {
						fmt.Printf(" FAILED: %v\n", err)
						continue
					}

					fmt.Printf(" OK\n")
					totalSuccess++
				}
			}
		}
	}

	if distillDryRun {
		fmt.Printf("\nDry run complete. Use without --dry-run to distill.\n")
	} else {
		var parts []string
		if totalSuccess > 0 {
			parts = append(parts, fmt.Sprintf("distilled %d", totalSuccess))
		}
		if totalSkipped > 0 {
			parts = append(parts, fmt.Sprintf("skipped %d unchanged", totalSkipped))
		}
		if totalInvalid > 0 {
			parts = append(parts, fmt.Sprintf("skipped %d invalid", totalInvalid))
		}
		if len(parts) > 0 {
			fmt.Printf("\n%s\n", strings.Join(parts, ", "))
		} else {
			fmt.Println("No items to distill.")
		}
	}

	return nil
}

// distillContent sends content through the AI for distillation.
// Returns just the distilled text content.
func distillContent(plugin ml.Plugin, name, content, distillPrompt string) (string, error) {
	// Build the content to distill
	var builder strings.Builder
	builder.WriteString("# ")
	builder.WriteString(name)
	builder.WriteString("\n\n")
	builder.WriteString(content)

	// Build the request
	req := ml.Request{
		Prompt:  builder.String(),
		Context: distillPrompt,
		Print:   true, // Non-interactive mode
	}

	// Run the AI
	var stdout, stderr bytes.Buffer
	resp, err := plugin.Run(context.Background(), req, &stdout, &stderr)
	if err != nil {
		return "", err
	}

	if resp.ExitCode != 0 {
		return "", fmt.Errorf("AI exited with code %d: %s", resp.ExitCode, stderr.String())
	}

	// Get the distilled content
	distilledContent := strings.TrimSpace(stdout.String())
	if distilledContent == "" {
		distilledContent = strings.TrimSpace(resp.Output)
	}

	// Strip any preamble before "---" that LLMs sometimes add despite instructions
	distilledContent = stripDistillPreamble(distilledContent)

	return distilledContent, nil
}

// preamblePattern matches a line with 3+ dashes (markdown horizontal rule/separator)
var preamblePattern = regexp.MustCompile(`(?m)^-{3,}\s*$`)

// stripDistillPreamble removes any content before the first "---+" separator.
// LLMs sometimes add explanatory text before the actual distilled content.
func stripDistillPreamble(content string) string {
	loc := preamblePattern.FindStringIndex(content)
	if loc == nil {
		return content
	}

	// Find the end of the separator line
	afterSep := content[loc[1]:]
	// Skip any leading newline after the separator
	if len(afterSep) > 0 && afterSep[0] == '\n' {
		afterSep = afterSep[1:]
	}

	return strings.TrimSpace(afterSep)
}

var distillCleanDryRun bool

var distillCleanCmd = &cobra.Command{
	Use:   "clean",
	Short: "Clear distilled content from all fragments and prompts",
	Long: `Clear distilled content from fragment and prompt YAML files.

This command walks through all .mlcm directories in the search path and
clears the distilled, content_hash, and distilled_by fields from each YAML file.

Examples:
  mlcm distill clean              # Clear distilled content from all files
  mlcm distill clean --dry-run    # Preview what would be cleaned`,
	RunE: runDistillClean,
}

func runDistillClean(cmd *cobra.Command, args []string) error {
	var cleaned int
	var skipped int
	var errors int

	// Clean fragment directories
	fragmentDirs, err := GetFragmentDirs()
	if err != nil {
		return fmt.Errorf("failed to get fragment directories: %w", err)
	}

	if len(fragmentDirs) > 0 {
		loader := fragments.NewLoader(fragmentDirs, fragments.WithPreferDistilled(false))
		frags, err := loader.List()
		if err != nil {
			return fmt.Errorf("failed to list fragments: %w", err)
		}

		for _, info := range frags {
			frag, err := loader.Load(info.Name)
			if err != nil {
				fmt.Fprintf(os.Stderr, "  Warning: failed to load %s: %v\n", info.Name, err)
				errors++
				continue
			}

			// Skip if nothing to clean
			if frag.Distilled == "" && frag.ContentHash == "" && frag.DistilledBy == "" {
				skipped++
				continue
			}

			if distillCleanDryRun {
				fmt.Printf("  Would clean: %s\n", info.Name)
				cleaned++
				continue
			}

			// Clear distilled fields
			frag.Distilled = ""
			frag.ContentHash = ""
			frag.DistilledBy = ""

			if err := frag.Save(); err != nil {
				fmt.Fprintf(os.Stderr, "  Error cleaning %s: %v\n", info.Name, err)
				errors++
				continue
			}

			fmt.Printf("  Cleaned: %s\n", info.Name)
			cleaned++
		}
	}

	// Clean prompt directories
	promptDirs, err := GetPromptDirs()
	if err != nil {
		return fmt.Errorf("failed to get prompt directories: %w", err)
	}

	if len(promptDirs) > 0 {
		promptLoader := fragments.NewLoader(promptDirs, fragments.WithPreferDistilled(false))
		prompts, err := promptLoader.List()
		if err != nil {
			return fmt.Errorf("failed to list prompts: %w", err)
		}

		for _, info := range prompts {
			prompt, err := promptLoader.Load(info.Name)
			if err != nil {
				fmt.Fprintf(os.Stderr, "  Warning: failed to load prompt %s: %v\n", info.Name, err)
				errors++
				continue
			}

			// Skip if nothing to clean
			if prompt.Distilled == "" && prompt.ContentHash == "" && prompt.DistilledBy == "" {
				skipped++
				continue
			}

			if distillCleanDryRun {
				fmt.Printf("  Would clean: %s\n", info.Name)
				cleaned++
				continue
			}

			// Clear distilled fields
			prompt.Distilled = ""
			prompt.ContentHash = ""
			prompt.DistilledBy = ""

			if err := prompt.Save(); err != nil {
				fmt.Fprintf(os.Stderr, "  Error cleaning prompt %s: %v\n", info.Name, err)
				errors++
				continue
			}

			fmt.Printf("  Cleaned: %s\n", info.Name)
			cleaned++
		}
	}

	if distillCleanDryRun {
		fmt.Printf("\nDry run: would clean %d files", cleaned)
	} else {
		fmt.Printf("\nCleaned %d files", cleaned)
	}
	if skipped > 0 {
		fmt.Printf(", skipped %d (already clean)", skipped)
	}
	if errors > 0 {
		fmt.Printf(", %d errors", errors)
	}
	fmt.Println()

	return nil
}

func init() {
	rootCmd.AddCommand(distillCmd)
	distillCmd.AddCommand(distillCleanCmd)

	distillCmd.Flags().StringVar(&distillPlugin, "plugin", "", "AI plugin to use (default from config)")
	distillCmd.Flags().StringVarP(&distillPersona, "persona", "p", "", "Distill only fragments for this persona")
	distillCmd.Flags().StringSliceVarP(&distillFragments, "fragment", "f", nil, "Specific fragment(s) to distill (can be repeated)")
	distillCmd.Flags().StringSliceVarP(&distillPromptNames, "prompt", "P", nil, "Specific prompt(s) to distill (can be repeated)")
	distillCmd.Flags().BoolVarP(&distillDryRun, "dry-run", "n", false, "Show what would be distilled without doing it")
	distillCmd.Flags().BoolVar(&distillForce, "force", false, "Re-distill even if unchanged")
	distillCmd.Flags().BoolVar(&distillOnlyPrompts, "prompts-only", false, "Distill only prompts (skip fragments)")
	distillCmd.Flags().BoolVar(&distillSkipPrompts, "skip-prompts", false, "Distill only fragments (skip prompts)")
	distillCmd.Flags().BoolVar(&distillResources, "resources", false, "Distill resources/ directory (for packaging)")

	distillCleanCmd.Flags().BoolVarP(&distillCleanDryRun, "dry-run", "n", false, "Show what would be deleted without doing it")
}
