package cmd

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/spf13/cobra"
	"golang.org/x/text/cases"
	"golang.org/x/text/language"

	"github.com/benjaminabbitt/scm/internal/config"
	"github.com/benjaminabbitt/scm/internal/editor"
	"github.com/benjaminabbitt/scm/internal/fragments"
)

var promptCmd = &cobra.Command{
	Use:   "prompt",
	Short: "Manage saved prompts",
	Long: `Manage saved prompts - reusable prompt templates.

Prompts are YAML files stored in .scm/prompts/ directories.
They use the same format as context fragments and can include {{variables}}.`,
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

		loaderOpts := []fragments.LoaderOption{fragments.WithPreferDistilled(false)}
		if cfg.IsEmbedded() {
			loaderOpts = append(loaderOpts, fragments.WithFS(cfg.GetPromptFS()))
		}
		loader := fragments.NewLoader(cfg.GetPromptDirs(), loaderOpts...)
		prompts, err := loader.List()
		if err != nil {
			return err
		}

		if len(prompts) == 0 {
			fmt.Println("No prompts found.")
			return nil
		}

		// Group by source directory
		bySource := make(map[string][]fragments.FragmentInfo)
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
			promptDir = filepath.Join(pwd, ".scm", config.PromptsDir)
		} else {
			// Try to find existing prompt first (can't edit embedded prompts)
			if !cfg.IsEmbedded() {
				loader := fragments.NewLoader(cfg.GetPromptDirs(), fragments.WithPreferDistilled(false))
				if promptPath, err := loader.Find(name); err == nil {
					editorCmd, editorArgs := cfg.GetEditorCommand()
					ed := editor.New(editorCmd, editorArgs)
					return ed.Edit(promptPath)
				}
			}

			// Prompt doesn't exist or using embedded, create in home
			home, err := os.UserHomeDir()
			if err != nil {
				return fmt.Errorf("failed to get home directory: %w", err)
			}
			promptDir = filepath.Join(home, ".scm", config.PromptsDir)
		}

		if err := os.MkdirAll(promptDir, 0755); err != nil {
			return fmt.Errorf("failed to create prompt directory: %w", err)
		}

		promptPath := filepath.Join(promptDir, name+".yaml")
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

		if cfg.IsEmbedded() {
			return fmt.Errorf("cannot delete embedded prompts; use 'scm copy' to create a local copy first")
		}

		// Don't prefer distilled when deleting - delete the source
		loader := fragments.NewLoader(cfg.GetPromptDirs(), fragments.WithPreferDistilled(false))
		promptPath, err := loader.Find(name)
		if err != nil {
			return fmt.Errorf("prompt not found: %s", name)
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

		loaderOpts := []fragments.LoaderOption{
			fragments.WithPreferDistilled(cfg.Defaults.ShouldUseDistilled()),
		}
		if cfg.IsEmbedded() {
			loaderOpts = append(loaderOpts, fragments.WithFS(cfg.GetPromptFS()))
		}
		loader := fragments.NewLoader(cfg.GetPromptDirs(), loaderOpts...)
		prompt, err := loader.Load(name)
		if err != nil {
			return fmt.Errorf("prompt not found: %s", name)
		}

		fmt.Println(prompt.Content)
		return nil
	},
}

// toTitle converts a string to title case using proper unicode handling.
func toTitle(s string) string {
	caser := cases.Title(language.English)
	return caser.String(s)
}

// LoadPrompt loads a prompt by name and returns its content.
func LoadPrompt(cfg *config.Config, name string) (string, error) {
	loaderOpts := []fragments.LoaderOption{
		fragments.WithPreferDistilled(cfg.Defaults.ShouldUseDistilled()),
	}
	if cfg.IsEmbedded() {
		loaderOpts = append(loaderOpts, fragments.WithFS(cfg.GetPromptFS()))
	}
	loader := fragments.NewLoader(cfg.GetPromptDirs(), loaderOpts...)
	prompt, err := loader.Load(name)
	if err != nil {
		return "", err
	}
	return prompt.Content, nil
}

func newPromptTemplate(name string) string {
	title := strings.ReplaceAll(name, "-", " ")
	title = toTitle(title)

	return fmt.Sprintf(`# Prompt: %s
# Write your prompt here. You can use {{variables}} from context fragments.

version: "1.0"
tags:
  - prompt
content: |
  # %s

  Your prompt content here.
`, name, title)
}

func init() {
	rootCmd.AddCommand(promptCmd)

	promptCmd.AddCommand(promptListCmd)
	promptCmd.AddCommand(promptEditCmd)
	promptCmd.AddCommand(promptDeleteCmd)
	promptCmd.AddCommand(promptShowCmd)

	promptEditCmd.Flags().BoolVarP(&promptEditLocal, "local", "l", false, "Create prompt in local .scm directory")
}
