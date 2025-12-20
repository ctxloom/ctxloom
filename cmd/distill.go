package cmd

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"

	"mlcm/internal/ai"
	_ "mlcm/internal/ai/claudecode"
	_ "mlcm/internal/ai/gemini"
	"mlcm/internal/fragments"
	"mlcm/internal/fsys"
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
	distillPlugin       string
	distillPersona      string
	distillFragments    []string
	distillPromptNames  []string
	distillDryRun       bool
	distillForce        bool
	distillOnlyPrompts  bool
	distillSkipPrompts  bool
)

var distillCmd = &cobra.Command{
	Use:   "distill [flags]",
	Short: "Create minimal-token versions of fragments and prompts",
	Long: `Distill fragments and prompts to create minimal-token versions that preserve meaning.

This command processes each fragment and prompt through an AI to create a compressed
version that preserves all rules and behaviors while reducing verbosity.
Distilled files are saved alongside the original with a .distilled.yaml extension.

When loading fragments or prompts, the distilled version is preferred if it exists
(controlled by use_distilled config setting).

Use --persona/-p to distill only fragments associated with specific personas.
Use --fragment/-f to distill specific fragments by name.
Use --prompt/-P to distill specific prompts by name.
Use --prompts-only to distill only prompts (skip fragments).
Use --skip-prompts to distill only fragments (skip prompts).

Examples:
  mlcm distill                           # Distill all fragments and prompts
  mlcm distill -p go-developer           # Distill fragments for go-developer persona
  mlcm distill -f style/direct           # Distill specific fragments
  mlcm distill -P code-review            # Distill specific prompts
  mlcm distill --prompts-only            # Distill only prompts
  mlcm distill --dry-run                 # Preview what would be distilled`,
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

	// Load the distillation prompt from embedded resources
	distillPrompt, err := getDistillPrompt()
	if err != nil {
		return err
	}

	// Track overall results
	totalSuccess := 0
	totalSkipped := 0

	// Distill fragments unless --prompts-only is set
	if !distillOnlyPrompts {
		fragmentDirs, err := GetFragmentDirs()
		if err != nil {
			return fmt.Errorf("failed to get fragment directories: %w", err)
		}

		if len(fragmentDirs) > 0 {
			loader := fragments.NewLoader(fragmentDirs, fragments.WithPreferDistilled(false))

			var fragmentNames []string
			if len(distillFragments) > 0 {
				fragmentNames = distillFragments
			} else if distillPersona != "" {
				persona, exists := cfg.Personas[distillPersona]
				if !exists {
					return fmt.Errorf("persona %q not found", distillPersona)
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

				for _, name := range fragmentNames {
					sourcePath, err := loader.Find(name)
					if err != nil {
						fmt.Fprintf(os.Stderr, "  Warning: fragment not found: %s\n", name)
						continue
					}

					if strings.HasSuffix(sourcePath, ".distilled.yaml") || strings.HasSuffix(sourcePath, ".distilled.yml") {
						continue
					}

					frag, err := loader.Load(name)
					if err != nil {
						fmt.Fprintf(os.Stderr, "  Warning: failed to load %s: %v\n", name, err)
						continue
					}

					sourceHash := computeFragmentHash(frag)
					hashPath := computeHashPath(sourcePath)
					destPath := computeDistilledPath(sourcePath)

					// Check if source has changed by comparing hash
					if !distillForce {
						cachedHash := readCachedHash(hashPath)
						if cachedHash == sourceHash {
							fmt.Printf("  Skipping %s (unchanged)\n", name)
							totalSkipped++
							continue
						}
					}

					if distillDryRun {
						fmt.Printf("  Would distill: %s -> %s\n", name, destPath)
						continue
					}

					reDistilling := false
					if !distillForce {
						if _, err := os.Stat(destPath); err == nil {
							reDistilling = true
						}
					}

					if reDistilling {
						fmt.Printf("  Re-distilling %s (source changed)...", name)
					} else {
						fmt.Printf("  Distilling: %s...", name)
					}

					distilled, err := distillFragment(plugin, frag, distillPrompt)
					if err != nil {
						fmt.Printf(" FAILED: %v\n", err)
						continue
					}

					if err := fsys.WriteProtected(destPath, []byte(distilled)); err != nil {
						fmt.Printf(" FAILED: %v\n", err)
						continue
					}

					// Write hash file alongside source
					if err := writeCachedHash(hashPath, sourceHash); err != nil {
						fmt.Fprintf(os.Stderr, "  Warning: could not write hash file %s: %v\n", hashPath, err)
					}

					fmt.Printf(" OK\n")
					totalSuccess++
				}
			}
		}
	}

	// Distill prompts unless --skip-prompts is set
	// Prompts use the same YAML format as fragments
	if !distillSkipPrompts {
		promptDirs, err := GetPromptDirs()
		if err != nil {
			return fmt.Errorf("failed to get prompt directories: %w", err)
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
					sourcePath, err := promptLoader.Find(name)
					if err != nil {
						fmt.Fprintf(os.Stderr, "  Warning: prompt not found: %s\n", name)
						continue
					}

					if strings.HasSuffix(sourcePath, ".distilled.yaml") {
						continue
					}

					prompt, err := promptLoader.Load(name)
					if err != nil {
						fmt.Fprintf(os.Stderr, "  Warning: failed to load %s: %v\n", name, err)
						continue
					}

					sourceHash := computeFragmentHash(prompt)
					hashPath := computeHashPath(sourcePath)
					destPath := computeDistilledPath(sourcePath)

					// Check if source has changed by comparing hash
					if !distillForce {
						cachedHash := readCachedHash(hashPath)
						if cachedHash == sourceHash {
							fmt.Printf("  Skipping %s (unchanged)\n", name)
							totalSkipped++
							continue
						}
					}

					if distillDryRun {
						fmt.Printf("  Would distill: %s -> %s\n", name, destPath)
						continue
					}

					reDistilling := false
					if !distillForce {
						if _, err := os.Stat(destPath); err == nil {
							reDistilling = true
						}
					}

					if reDistilling {
						fmt.Printf("  Re-distilling %s (source changed)...", name)
					} else {
						fmt.Printf("  Distilling: %s...", name)
					}

					distilled, err := distillPromptContent(plugin, prompt, distillPrompt)
					if err != nil {
						fmt.Printf(" FAILED: %v\n", err)
						continue
					}

					if err := fsys.WriteProtected(destPath, []byte(distilled)); err != nil {
						fmt.Printf(" FAILED: %v\n", err)
						continue
					}

					// Write hash file alongside source
					if err := writeCachedHash(hashPath, sourceHash); err != nil {
						fmt.Fprintf(os.Stderr, "  Warning: could not write hash file %s: %v\n", hashPath, err)
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
		if totalSkipped > 0 {
			fmt.Printf("\nDistilled %d items, skipped %d unchanged (use --force to re-distill all)\n", totalSuccess, totalSkipped)
		} else if totalSuccess > 0 {
			fmt.Printf("\nDistilled %d items\n", totalSuccess)
		} else {
			fmt.Println("No items to distill.")
		}
	}

	return nil
}

// computeDistilledPath returns the path for a distilled version of a fragment.
// For example: style/direct.yaml -> style/direct.distilled.yaml
func computeDistilledPath(sourcePath string) string {
	dir := filepath.Dir(sourcePath)
	base := filepath.Base(sourcePath)

	// Remove extension
	ext := filepath.Ext(base)
	name := strings.TrimSuffix(base, ext)

	return filepath.Join(dir, name+".distilled.yaml")
}

// computeHashPath returns the path for a .sha256 file alongside the source.
// For example: style/direct.yaml -> style/direct.sha256
func computeHashPath(sourcePath string) string {
	dir := filepath.Dir(sourcePath)
	base := filepath.Base(sourcePath)

	ext := filepath.Ext(base)
	name := strings.TrimSuffix(base, ext)

	return filepath.Join(dir, name+".sha256")
}

// readCachedHash reads the cached hash from a .sha256 file.
// Returns empty string if file doesn't exist or can't be read.
func readCachedHash(hashPath string) string {
	data, err := os.ReadFile(hashPath)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(data))
}

// writeCachedHash writes the hash to a .sha256 file.
func writeCachedHash(hashPath, hash string) error {
	return os.WriteFile(hashPath, []byte(hash+"\n"), 0644)
}

// computeFragmentHash computes a SHA-256 hash of the fragment's content and metadata.
// Returns full 64-character hex-encoded hash.
func computeFragmentHash(frag *fragments.Fragment) string {
	h := sha256.New()
	h.Write([]byte(frag.Version))
	h.Write([]byte(frag.Author))
	h.Write([]byte(frag.Content))
	for _, tag := range frag.Tags {
		h.Write([]byte(tag))
	}
	for _, v := range frag.Variables {
		h.Write([]byte(v))
	}
	return hex.EncodeToString(h.Sum(nil))
}

// distilledFragment represents the YAML structure for distilled output.
type distilledFragment struct {
	Version   string   `yaml:"version,omitempty"`
	Author    string   `yaml:"author,omitempty"`
	Tags      []string `yaml:"tags,omitempty"`
	Variables []string `yaml:"variables,omitempty"`
	Content   string   `yaml:"content"`
}

// distillFragment sends a fragment through the AI for distillation.
// Returns YAML-formatted output preserving the original metadata.
func distillFragment(plugin ai.Plugin, frag *fragments.Fragment, distillPrompt string) (string, error) {
	// Build the content to distill
	var content strings.Builder
	content.WriteString("# Fragment: ")
	content.WriteString(frag.Name)
	content.WriteString("\n\n")

	if frag.Content != "" {
		content.WriteString(frag.Content)
	}

	// Build the request
	req := ai.Request{
		Prompt:  content.String(),
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

	// Create the distilled YAML structure with original metadata
	distilled := distilledFragment{
		Version:   frag.Version,
		Author:    frag.Author,
		Tags:      frag.Tags,
		Variables: frag.Variables,
		Content:   distilledContent,
	}

	// Marshal to YAML
	yamlData, err := yaml.Marshal(&distilled)
	if err != nil {
		return "", fmt.Errorf("failed to marshal distilled fragment: %w", err)
	}

	return string(yamlData), nil
}

// distillPromptContent sends a prompt through the AI for distillation.
// Takes a parsed prompt fragment and returns YAML-formatted output.
func distillPromptContent(plugin ai.Plugin, prompt *fragments.Fragment, distillPrompt string) (string, error) {
	// Build the content to distill
	var promptContent strings.Builder
	promptContent.WriteString("# Prompt: ")
	promptContent.WriteString(prompt.Name)
	promptContent.WriteString("\n\n")
	promptContent.WriteString(prompt.Content)

	// Build the request
	req := ai.Request{
		Prompt:  promptContent.String(),
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

	// Create the distilled YAML structure with original metadata
	distilled := distilledFragment{
		Version:   prompt.Version,
		Author:    prompt.Author,
		Tags:      prompt.Tags,
		Variables: prompt.Variables,
		Content:   distilledContent,
	}

	// Marshal to YAML
	yamlData, err := yaml.Marshal(&distilled)
	if err != nil {
		return "", fmt.Errorf("failed to marshal distilled prompt: %w", err)
	}

	return string(yamlData), nil
}

var distillCleanDryRun bool

var distillCleanCmd = &cobra.Command{
	Use:   "clean",
	Short: "Remove all distilled fragments, prompts, and hash files",
	Long: `Remove all .distilled.yaml and .sha256 files from fragment and prompt directories.

This command walks through all .mlcm directories in the search path and
deletes any files with .distilled.yaml or .sha256 extensions.

Examples:
  mlcm distill clean              # Remove all distilled and hash files
  mlcm distill clean --dry-run    # Preview what would be deleted`,
	RunE: runDistillClean,
}

func runDistillClean(cmd *cobra.Command, args []string) error {
	var deleted int
	var errors int

	// Clean fragment directories
	fragmentDirs, err := GetFragmentDirs()
	if err != nil {
		return fmt.Errorf("failed to get fragment directories: %w", err)
	}
	for _, dir := range fragmentDirs {
		d, e := cleanDistilledFiles(dir, distillCleanDryRun)
		deleted += d
		errors += e
	}

	// Clean prompt directories
	promptDirs, err := GetPromptDirs()
	if err != nil {
		return fmt.Errorf("failed to get prompt directories: %w", err)
	}
	for _, dir := range promptDirs {
		d, e := cleanDistilledFiles(dir, distillCleanDryRun)
		deleted += d
		errors += e
	}

	if distillCleanDryRun {
		fmt.Printf("Would delete %d distilled files\n", deleted)
	} else {
		fmt.Printf("Deleted %d distilled files", deleted)
		if errors > 0 {
			fmt.Printf(" (%d errors)", errors)
		}
		fmt.Println()
	}

	return nil
}

// cleanDistilledFiles removes all .distilled.yaml and .sha256 files from a directory.
// Returns the count of deleted files and errors.
func cleanDistilledFiles(dir string, dryRun bool) (deleted, errors int) {
	filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			return nil
		}

		name := d.Name()
		if strings.HasSuffix(name, ".distilled.yaml") || strings.HasSuffix(name, ".sha256") {
			if dryRun {
				fmt.Printf("  Would delete: %s\n", path)
				deleted++
			} else {
				// Make writable before deleting (may be read-only)
				fsys.MakeWritable(path)
				if err := os.Remove(path); err != nil {
					fmt.Fprintf(os.Stderr, "  Error deleting %s: %v\n", path, err)
					errors++
				} else {
					fmt.Printf("  Deleted: %s\n", path)
					deleted++
				}
			}
		}
		return nil
	})
	return
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

	distillCleanCmd.Flags().BoolVarP(&distillCleanDryRun, "dry-run", "n", false, "Show what would be deleted without doing it")
}
