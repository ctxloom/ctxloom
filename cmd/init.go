package cmd

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/spf13/cobra"

	"mlcm/internal/config"
	"mlcm/resources"
)

var (
	initLocal       bool
	initNoFragments bool
)

var initCmd = &cobra.Command{
	Use:   "init",
	Short: "Initialize the .mlcm directory",
	Long: `Initialize the .mlcm directory structure with default directories.

By default, initializes ~/.mlcm as a git repository with:
  - context-fragments/  (for context fragment files)
  - prompts/            (for prompt templates)
  - config.yaml         (for configuration)

Use --local to initialize a .mlcm directory in the current directory instead.`,
	RunE: func(cmd *cobra.Command, args []string) error {
		var mlcmDir string
		var err error

		if initLocal {
			pwd, err := os.Getwd()
			if err != nil {
				return fmt.Errorf("failed to get working directory: %w", err)
			}
			mlcmDir = filepath.Join(pwd, config.MLCMDirName)
		} else {
			mlcmDir, err = config.HomeMLCMDir()
			if err != nil {
				return err
			}
		}

		return initMLCMDirectory(mlcmDir)
	},
}

func initMLCMDirectory(mlcmDir string) error {
	// Check if already exists
	if info, err := os.Stat(mlcmDir); err == nil && info.IsDir() {
		fmt.Printf(".mlcm directory already exists at %s\n", mlcmDir)
		return nil
	}

	fmt.Printf("Initializing .mlcm directory at %s\n", mlcmDir)

	// Create main directory
	if err := os.MkdirAll(mlcmDir, 0755); err != nil {
		return fmt.Errorf("failed to create .mlcm directory: %w", err)
	}

	// Create subdirectories
	fragmentsDir := filepath.Join(mlcmDir, config.ContextFragmentsDir)
	if err := os.MkdirAll(fragmentsDir, 0755); err != nil {
		return fmt.Errorf("failed to create context-fragments directory: %w", err)
	}

	promptsDir := filepath.Join(mlcmDir, config.PromptsDir)
	if err := os.MkdirAll(promptsDir, 0755); err != nil {
		return fmt.Errorf("failed to create prompts directory: %w", err)
	}

	// Copy embedded fragments unless --no-fragments was specified
	if !initNoFragments {
		if err := resources.CopyFragments(fragmentsDir); err != nil {
			return fmt.Errorf("failed to copy fragments: %w", err)
		}
		fmt.Println("Copied default context fragments")
	}

	// Create default config file
	configPath := filepath.Join(mlcmDir, config.ConfigFileName+".yaml")
	defaultConfig := `# MLCM Configuration
ai:
  default_plugin: claude-code
  plugins:
    claude-code:
      args:
        - "--print"
        - "--dangerously-skip-permissions"
`
	if err := os.WriteFile(configPath, []byte(defaultConfig), 0644); err != nil {
		return fmt.Errorf("failed to create config file: %w", err)
	}

	// Create .gitkeep for prompts directory (fragments has content)
	gitkeepPrompts := filepath.Join(promptsDir, ".gitkeep")
	if err := os.WriteFile(gitkeepPrompts, []byte(""), 0644); err != nil {
		return fmt.Errorf("failed to create .gitkeep: %w", err)
	}

	// Initialize git repository
	gitCmd := exec.Command("git", "init")
	gitCmd.Dir = mlcmDir
	if output, err := gitCmd.CombinedOutput(); err != nil {
		fmt.Printf("Warning: failed to initialize git repository: %s\n", string(output))
	} else {
		fmt.Println("Initialized git repository")

		// Initial commit
		addCmd := exec.Command("git", "add", ".")
		addCmd.Dir = mlcmDir
		addCmd.Run()

		commitCmd := exec.Command("git", "commit", "-m", "Initial MLCM setup")
		commitCmd.Dir = mlcmDir
		commitCmd.Run()
		fmt.Println("Created initial commit")
	}

	fmt.Printf("\nMLCM initialized successfully!\n")
	fmt.Printf("  Context fragments: %s\n", fragmentsDir)
	fmt.Printf("  Prompts:           %s\n", promptsDir)
	fmt.Printf("  Config:            %s\n", configPath)

	return nil
}

func init() {
	rootCmd.AddCommand(initCmd)

	initCmd.Flags().BoolVarP(&initLocal, "local", "l", false, "Initialize in current directory instead of home")
	initCmd.Flags().BoolVar(&initNoFragments, "no-fragments", false, "Skip copying default context fragments")
}
