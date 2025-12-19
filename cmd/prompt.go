package cmd

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/spf13/cobra"

	"mlcm/internal/config"
	"mlcm/internal/editor"
)

var promptCmd = &cobra.Command{
	Use:   "prompt",
	Short: "Manage saved prompts",
	Long: `Manage saved prompts - reusable prompt templates.

Prompts are markdown files stored in .mlcm/prompts/ directories.
They can include {{variables}} that are substituted from fragments.`,
}

// promptListCmd lists all available prompts
var promptListCmd = &cobra.Command{
	Use:   "list",
	Short: "List available prompts",
	RunE: func(cmd *cobra.Command, args []string) error {
		cfg, err := config.Load()
		if err != nil {
			return fmt.Errorf("failed to load config: %w", err)
		}

		prompts, err := listPrompts(cfg)
		if err != nil {
			return err
		}

		if len(prompts) == 0 {
			fmt.Println("No prompts found.")
			return nil
		}

		// Group by source directory
		bySource := make(map[string][]promptInfo)
		for _, p := range prompts {
			bySource[p.Source] = append(bySource[p.Source], p)
		}

		// Sort sources
		var sources []string
		for s := range bySource {
			sources = append(sources, s)
		}
		sort.Strings(sources)

		for _, source := range sources {
			fmt.Printf("\n%s:\n", source)
			for _, p := range bySource[source] {
				fmt.Printf("  %s\n", p.Name)
			}
		}

		return nil
	},
}

var promptEditLocal bool

// promptEditCmd edits or creates a prompt
var promptEditCmd = &cobra.Command{
	Use:   "edit <name>",
	Short: "Edit or create a prompt",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		name := args[0]

		cfg, err := config.Load()
		if err != nil {
			return fmt.Errorf("failed to load config: %w", err)
		}

		var promptDir string
		if promptEditLocal {
			pwd, err := os.Getwd()
			if err != nil {
				return fmt.Errorf("failed to get working directory: %w", err)
			}
			promptDir = filepath.Join(pwd, ".mlcm", config.PromptsDir)
		} else {
			// Try to find existing prompt first
			promptPath, err := findPrompt(cfg, name)
			if err == nil {
				editorCmd, editorArgs := cfg.GetEditorCommand()
				ed := editor.New(editorCmd, editorArgs)
				return ed.Edit(promptPath)
			}

			// Prompt doesn't exist, use first available directory or home
			dirs := cfg.GetPromptDirs()
			if len(dirs) > 0 {
				promptDir = dirs[0]
			} else {
				homeMLCM, err := config.HomeMLCMDir()
				if err != nil {
					return fmt.Errorf("failed to get home .mlcm directory: %w", err)
				}
				promptDir = filepath.Join(homeMLCM, config.PromptsDir)
			}
		}

		if err := os.MkdirAll(promptDir, 0755); err != nil {
			return fmt.Errorf("failed to create prompt directory: %w", err)
		}

		promptPath := filepath.Join(promptDir, name+".md")
		editorCmd, editorArgs := cfg.GetEditorCommand()
		ed := editor.New(editorCmd, editorArgs)

		template := newPromptTemplate(name)
		if err := ed.EditWithTemplate(promptPath, template); err != nil {
			return err
		}

		fmt.Printf("Edited prompt: %s\n", promptPath)
		return nil
	},
}

// promptDeleteCmd deletes a prompt
var promptDeleteCmd = &cobra.Command{
	Use:     "delete <name>",
	Aliases: []string{"rm"},
	Short:   "Delete a prompt",
	Args:    cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		name := args[0]

		cfg, err := config.Load()
		if err != nil {
			return fmt.Errorf("failed to load config: %w", err)
		}

		promptPath, err := findPrompt(cfg, name)
		if err != nil {
			return err
		}

		if err := os.Remove(promptPath); err != nil {
			return fmt.Errorf("failed to delete prompt: %w", err)
		}

		fmt.Printf("Deleted prompt: %s\n", promptPath)
		return nil
	},
}

// promptShowCmd shows a prompt's content
var promptShowCmd = &cobra.Command{
	Use:   "show <name>",
	Short: "Show a prompt's content",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		name := args[0]

		cfg, err := config.Load()
		if err != nil {
			return fmt.Errorf("failed to load config: %w", err)
		}

		promptPath, err := findPrompt(cfg, name)
		if err != nil {
			return err
		}

		content, err := os.ReadFile(promptPath)
		if err != nil {
			return fmt.Errorf("failed to read prompt: %w", err)
		}

		fmt.Println(string(content))
		return nil
	},
}

type promptInfo struct {
	Name   string
	Path   string
	Source string
}

// listPrompts returns all available prompts across all prompt directories.
func listPrompts(cfg *config.Config) ([]promptInfo, error) {
	var prompts []promptInfo
	seen := make(map[string]bool)

	for _, dir := range cfg.GetPromptDirs() {
		err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				if os.IsNotExist(err) {
					return nil
				}
				return err
			}

			if d.IsDir() {
				if strings.HasPrefix(d.Name(), ".") && path != dir {
					return filepath.SkipDir
				}
				return nil
			}

			name := d.Name()
			if !strings.HasSuffix(name, ".md") {
				return nil
			}
			if strings.HasPrefix(name, ".") {
				return nil
			}

			relPath, err := filepath.Rel(dir, path)
			if err != nil {
				return nil
			}

			promptName := strings.TrimSuffix(relPath, ".md")

			if !seen[promptName] {
				seen[promptName] = true
				prompts = append(prompts, promptInfo{
					Name:   promptName,
					Path:   path,
					Source: dir,
				})
			}
			return nil
		})
		if err != nil && !os.IsNotExist(err) {
			return nil, fmt.Errorf("failed to walk directory %s: %w", dir, err)
		}
	}

	return prompts, nil
}

// findPrompt locates a prompt by name across all prompt directories.
//
// Naming conventions supported:
//   - Slash paths: "reviews/security" finds "reviews/security.md" (forward or back slashes)
//   - Basename only: "security" finds it in any subdirectory (first match wins)
func findPrompt(cfg *config.Config, name string) (string, error) {
	// Normalize path separators for cross-platform support
	name = filepath.FromSlash(name)

	candidates := []string{name, name + ".md"}

	// First try direct path lookup (including subdirectory paths)
	for _, dir := range cfg.GetPromptDirs() {
		for _, candidate := range candidates {
			path := filepath.Join(dir, candidate)
			if info, err := os.Stat(path); err == nil && !info.IsDir() {
				return path, nil
			}
		}
	}

	// If not found directly, walk directories to find by basename
	baseName := filepath.Base(name)
	baseNameMd := baseName + ".md"

	for _, dir := range cfg.GetPromptDirs() {
		var found string
		filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() {
				return nil
			}
			fileName := d.Name()
			if fileName == baseName || fileName == baseNameMd {
				found = path
				return filepath.SkipDir
			}
			return nil
		})
		if found != "" {
			return found, nil
		}
	}

	return "", fmt.Errorf("prompt not found: %s", name)
}

// toTitle converts a string to title case (replaces deprecated strings.Title).
func toTitle(s string) string {
	words := strings.Fields(s)
	for i, word := range words {
		if len(word) > 0 {
			words[i] = strings.ToUpper(string(word[0])) + strings.ToLower(word[1:])
		}
	}
	return strings.Join(words, " ")
}

// LoadPrompt loads a prompt by name and returns its content.
func LoadPrompt(cfg *config.Config, name string) (string, error) {
	promptPath, err := findPrompt(cfg, name)
	if err != nil {
		return "", err
	}

	content, err := os.ReadFile(promptPath)
	if err != nil {
		return "", fmt.Errorf("failed to read prompt: %w", err)
	}

	return strings.TrimSpace(string(content)), nil
}

func newPromptTemplate(name string) string {
	title := strings.ReplaceAll(name, "-", " ")
	title = toTitle(title)

	return fmt.Sprintf(`# %s

<!--
Prompt: %s
Write your prompt here. You can use {{variables}} from context fragments.
-->

`, title, name)
}

func init() {
	rootCmd.AddCommand(promptCmd)

	promptCmd.AddCommand(promptListCmd)
	promptCmd.AddCommand(promptEditCmd)
	promptCmd.AddCommand(promptDeleteCmd)
	promptCmd.AddCommand(promptShowCmd)

	promptEditCmd.Flags().BoolVarP(&promptEditLocal, "local", "l", false, "Create prompt in local .mlcm directory")
}
