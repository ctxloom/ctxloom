package cmd

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"github.com/spf13/cobra"

	"mlcm/internal/config"
	"mlcm/internal/editor"
	"mlcm/internal/fragments"
)

var fragmentCmd = &cobra.Command{
	Use:   "fragment",
	Short: "Manage context fragments",
	Long:  `Manage context fragments - markdown files that provide context to AI.`,
}

var fragmentListCmd = &cobra.Command{
	Use:     "list",
	Aliases: []string{"ls"},
	Short:   "List available fragments",
	RunE: func(cmd *cobra.Command, args []string) error {
		cfg, err := config.Load()
		if err != nil {
			return fmt.Errorf("failed to load config: %w", err)
		}

		loader := fragments.NewLoader(cfg.GetFragmentDirs())
		frags, err := loader.List()
		if err != nil {
			return err
		}

		if len(frags) == 0 {
			fmt.Println("No fragments found.")
			fmt.Println("Use 'mlcm fragment edit <name>' to create one.")
			return nil
		}

		// Group by directory
		byDir := make(map[string][]string)
		for _, f := range frags {
			dir := filepath.Dir(f.Path)
			byDir[dir] = append(byDir[dir], f.Name)
		}

		var dirs []string
		for d := range byDir {
			dirs = append(dirs, d)
		}
		sort.Strings(dirs)

		for _, dir := range dirs {
			fmt.Printf("\n%s:\n", dir)
			names := byDir[dir]
			sort.Strings(names)
			for _, name := range names {
				fmt.Printf("  %s\n", name)
			}
		}

		return nil
	},
}

var fragmentEditLocal bool

var fragmentEditCmd = &cobra.Command{
	Use:   "edit <name>",
	Short: "Edit or create a fragment",
	Long: `Edit an existing context fragment or create a new one.

Opens the fragment in your configured editor.
If the fragment doesn't exist, creates it with a template.

Examples:
  mlcm fragment edit coding-standards
  mlcm fragment edit my-fragment --local`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		name := args[0]

		cfg, err := config.Load()
		if err != nil {
			return fmt.Errorf("failed to load config: %w", err)
		}

		var fragmentDir string
		if fragmentEditLocal {
			pwd, err := os.Getwd()
			if err != nil {
				return fmt.Errorf("failed to get working directory: %w", err)
			}
			fragmentDir = filepath.Join(pwd, ".mlcm", config.ContextFragmentsDir)
		} else {
			loader := fragments.NewLoader(cfg.GetFragmentDirs())
			if path, err := loader.Find(name); err == nil {
				editorCmd, editorArgs := cfg.GetEditorCommand()
				ed := editor.New(editorCmd, editorArgs)
				return ed.Edit(path)
			}

			dirs := cfg.GetFragmentDirs()
			if len(dirs) > 0 {
				fragmentDir = dirs[0]
			} else {
				return fmt.Errorf("no .mlcm directory found; run 'mlcm init --local' first")
			}
		}

		if err := os.MkdirAll(fragmentDir, 0755); err != nil {
			return fmt.Errorf("failed to create fragment directory: %w", err)
		}

		fragmentPath := filepath.Join(fragmentDir, name+".md")
		editorCmd, editorArgs := cfg.GetEditorCommand()
		ed := editor.New(editorCmd, editorArgs)

		template := fragmentTemplate(name)
		if err := ed.EditWithTemplate(fragmentPath, template); err != nil {
			return err
		}

		fmt.Printf("Edited fragment: %s\n", fragmentPath)
		return nil
	},
}

var fragmentShowCmd = &cobra.Command{
	Use:   "show <name>",
	Short: "Show a fragment's content",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		name := args[0]

		cfg, err := config.Load()
		if err != nil {
			return fmt.Errorf("failed to load config: %w", err)
		}

		loader := fragments.NewLoader(cfg.GetFragmentDirs())
		path, err := loader.Find(name)
		if err != nil {
			return fmt.Errorf("fragment not found: %s", name)
		}

		content, err := os.ReadFile(path)
		if err != nil {
			return fmt.Errorf("failed to read fragment: %w", err)
		}

		fmt.Println(string(content))
		return nil
	},
}

var fragmentDeleteCmd = &cobra.Command{
	Use:     "delete <name>",
	Aliases: []string{"rm"},
	Short:   "Delete a fragment",
	Args:    cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		name := args[0]

		cfg, err := config.Load()
		if err != nil {
			return fmt.Errorf("failed to load config: %w", err)
		}

		loader := fragments.NewLoader(cfg.GetFragmentDirs())
		path, err := loader.Find(name)
		if err != nil {
			return fmt.Errorf("fragment not found: %s", name)
		}

		if err := os.Remove(path); err != nil {
			return fmt.Errorf("failed to delete fragment: %w", err)
		}

		fmt.Printf("Deleted fragment: %s\n", path)
		return nil
	},
}

func fragmentTemplate(name string) string {
	return fmt.Sprintf(`## Context

<!--
Fragment: %s
Add your context content here.
Use {{variable_name}} for template variables.
-->

## Variables

`+"```yaml"+`
# Define variables here
# example_var: value
`+"```"+`
`, name)
}

func init() {
	rootCmd.AddCommand(fragmentCmd)

	fragmentCmd.AddCommand(fragmentListCmd)
	fragmentCmd.AddCommand(fragmentEditCmd)
	fragmentCmd.AddCommand(fragmentShowCmd)
	fragmentCmd.AddCommand(fragmentDeleteCmd)

	fragmentEditCmd.Flags().BoolVarP(&fragmentEditLocal, "local", "l", false, "Create in local .mlcm directory")
}
