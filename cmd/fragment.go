package cmd

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"github.com/spf13/cobra"

	"github.com/benjaminabbitt/mlcm/internal/config"
	"github.com/benjaminabbitt/mlcm/internal/editor"
	"github.com/benjaminabbitt/mlcm/internal/fragments"
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
		fragmentDirs, err := GetFragmentDirs()
		if err != nil {
			return fmt.Errorf("failed to get fragment directories: %w", err)
		}

		loader := fragments.NewLoader(fragmentDirs)
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
If the fragment doesn't exist, creates it in ~/.mlcm/context-fragments/ by default.

Examples:
  mlcm fragment edit coding-standards         # Creates/edits in ~/.mlcm
  mlcm fragment edit my-fragment --local      # Creates in project .mlcm`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		name := args[0]

		cfg, err := config.Load()
		if err != nil {
			return fmt.Errorf("failed to load config: %w", err)
		}

		fragmentDirs, err := GetFragmentDirs()
		if err != nil {
			return fmt.Errorf("failed to get fragment directories: %w", err)
		}

		// First, try to find existing fragment
		loader := fragments.NewLoader(fragmentDirs)
		if path, err := loader.Find(name); err == nil {
			editorCmd, editorArgs := cfg.GetEditorCommand()
			ed := editor.New(editorCmd, editorArgs)
			return ed.Edit(path)
		}

		// Fragment doesn't exist, determine where to create it
		var fragmentDir string
		if fragmentEditLocal {
			// Create in project-local .mlcm
			pwd, err := os.Getwd()
			if err != nil {
				return fmt.Errorf("failed to get working directory: %w", err)
			}
			fragmentDir = filepath.Join(pwd, ".mlcm", config.ContextFragmentsDir)
		} else {
			// Default: create in ~/.mlcm
			home, err := os.UserHomeDir()
			if err != nil {
				return fmt.Errorf("failed to get home directory: %w", err)
			}
			fragmentDir = filepath.Join(home, ".mlcm", config.ContextFragmentsDir)
		}

		if err := os.MkdirAll(fragmentDir, 0755); err != nil {
			return fmt.Errorf("failed to create fragment directory: %w", err)
		}

		fragmentPath := filepath.Join(fragmentDir, name+".yaml")
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

		fragmentDirs, err := GetFragmentDirs()
		if err != nil {
			return fmt.Errorf("failed to get fragment directories: %w", err)
		}

		loader := fragments.NewLoader(fragmentDirs)
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

		fragmentDirs, err := GetFragmentDirs()
		if err != nil {
			return fmt.Errorf("failed to get fragment directories: %w", err)
		}

		loader := fragments.NewLoader(fragmentDirs)
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
	return fmt.Sprintf(`version: "1.0"
tags: []
content: |-
    # %s

    Add your context content here.
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
