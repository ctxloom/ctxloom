package cmd

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/benjaminabbitt/scm/internal/config"
	"github.com/benjaminabbitt/scm/internal/operations"
	"github.com/benjaminabbitt/scm/resources"
)

var initCmd = &cobra.Command{
	Use:   "init",
	Short: "Initialize a new .scm directory",
	Long: `Initialize a new .scm directory in the current working directory.

This creates a marker directory that SCM uses to identify a project root.
All SCM data (profiles, bundles, fragments, prompts) will be stored here.

If no .scm directory exists when running SCM commands, the user home ~/.scm
is used as a fallback.

Examples:
  scm init              # Create .scm in current directory
  scm init --home       # Create/ensure ~/.scm exists`,
	RunE: runInit,
}

var initHome bool

func init() {
	rootCmd.AddCommand(initCmd)
	initCmd.Flags().BoolVar(&initHome, "home", false, "Initialize in user home directory instead of current directory")
}

func runInit(cmd *cobra.Command, args []string) error {
	var scmDir string

	if initHome {
		home, err := os.UserHomeDir()
		if err != nil {
			return fmt.Errorf("failed to get home directory: %w", err)
		}
		scmDir = filepath.Join(home, config.SCMDirName)
	} else {
		pwd, err := os.Getwd()
		if err != nil {
			return fmt.Errorf("failed to get current directory: %w", err)
		}
		scmDir = filepath.Join(pwd, config.SCMDirName)
	}

	// Check if already exists
	if info, err := os.Stat(scmDir); err == nil && info.IsDir() {
		fmt.Printf("SCM directory already exists: %s\n", scmDir)
		return nil
	}

	// Create the directory structure
	dirs := []string{
		scmDir,
		filepath.Join(scmDir, "profiles"),
		filepath.Join(scmDir, "bundles"),
	}

	for _, dir := range dirs {
		if err := os.MkdirAll(dir, 0755); err != nil {
			return fmt.Errorf("failed to create directory %s: %w", dir, err)
		}
	}

	// Create config.yaml from embedded example
	configPath := filepath.Join(scmDir, "config.yaml")
	configContent, err := resources.GetExampleConfig()
	if err != nil {
		return fmt.Errorf("failed to read example config: %w", err)
	}
	if err := os.WriteFile(configPath, configContent, 0644); err != nil {
		return fmt.Errorf("failed to create config.yaml: %w", err)
	}

	// Create remotes.yaml with default remote (scm-main)
	remotesPath := filepath.Join(scmDir, "remotes.yaml")
	remotesContent, err := resources.GetDefaultRemotes()
	if err != nil {
		return fmt.Errorf("failed to read default remotes: %w", err)
	}
	if err := os.WriteFile(remotesPath, remotesContent, 0644); err != nil {
		return fmt.Errorf("failed to create remotes.yaml: %w", err)
	}

	fmt.Printf("Initialized SCM directory: %s\n", scmDir)

	// Apply hooks to register MCP server
	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("failed to load config: %w", err)
	}

	result, err := operations.ApplyHooks(context.Background(), cfg, operations.ApplyHooksRequest{
		Backend:           "all",
		RegenerateContext: false,
	})
	if err != nil {
		return fmt.Errorf("failed to apply hooks: %w", err)
	}

	fmt.Printf("Applied hooks for: %v\n", result.Backends)
	return nil
}
