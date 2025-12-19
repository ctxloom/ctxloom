package cmd

import (
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/spf13/cobra"

	"mlcm/internal/config"
	"mlcm/resources"
)

var initNoFragments bool

var initCmd = &cobra.Command{
	Use:   "init",
	Short: "Initialize .mlcm in current directory",
	Long: `Initialize a .mlcm directory in the current directory with:
  - context-fragments/  (for context fragment files)
  - prompts/            (for prompt templates)
  - config.yaml         (for configuration)`,
	RunE: func(cmd *cobra.Command, args []string) error {
		pwd, err := os.Getwd()
		if err != nil {
			return fmt.Errorf("failed to get working directory: %w", err)
		}
		mlcmDir := filepath.Join(pwd, config.MLCMDirName)
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

	// Copy fragments unless --no-fragments was specified
	if !initNoFragments {
		// First, copy embedded fragments
		if err := resources.CopyFragments(fragmentsDir); err != nil {
			return fmt.Errorf("failed to copy embedded fragments: %w", err)
		}
		fmt.Println("Copied embedded context fragments")

		// Then, copy from ~/.mlcm (overwrites duplicates)
		home, err := os.UserHomeDir()
		if err != nil {
			return fmt.Errorf("failed to get home directory: %w", err)
		}
		homeFragments := filepath.Join(home, config.MLCMDirName, config.ContextFragmentsDir)
		if info, err := os.Stat(homeFragments); err == nil && info.IsDir() {
			if err := copyDir(homeFragments, fragmentsDir); err != nil {
				return fmt.Errorf("failed to copy fragments from %s: %w", homeFragments, err)
			}
			fmt.Printf("Copied fragments from %s\n", homeFragments)
		}
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

// copyDir recursively copies a directory tree.
func copyDir(src, dst string) error {
	return filepath.WalkDir(src, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}

		relPath, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		dstPath := filepath.Join(dst, relPath)

		if d.IsDir() {
			return os.MkdirAll(dstPath, 0755)
		}

		return copyFile(path, dstPath)
	})
}

// copyFile copies a single file.
func copyFile(src, dst string) error {
	srcFile, err := os.Open(src)
	if err != nil {
		return err
	}
	defer srcFile.Close()

	if err := os.MkdirAll(filepath.Dir(dst), 0755); err != nil {
		return err
	}

	dstFile, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer dstFile.Close()

	_, err = io.Copy(dstFile, srcFile)
	return err
}

func init() {
	rootCmd.AddCommand(initCmd)

	initCmd.Flags().BoolVar(&initNoFragments, "no-fragments", false, "Skip copying context fragments from ~/.mlcm")
}
