package cmd

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/ctxloom/ctxloom/internal/config"
)

// Version is set at build time via ldflags
// Example: go build -ldflags "-X ctxloom/cmd.Version=v1.0.0"
var Version = "dev"

// ExitError is returned when a command needs to exit with a specific code.
// This allows deferred cleanup to run before the process exits.
type ExitError struct {
	Code int
}

func (e *ExitError) Error() string {
	return fmt.Sprintf("exit code %d", e.Code)
}

// GetAppDirs returns the .ctxloom directories from project config.
func GetAppDirs() ([]string, error) {
	cfg, err := config.Load()
	if err != nil {
		return nil, err
	}
	return cfg.AppPaths, nil
}

// GetConfig returns the project configuration.
func GetConfig() (*config.Config, error) {
	return config.Load()
}

var rootCmd = &cobra.Command{
	Use:   "ctxloom",
	Short: "Sophisticated Context Management",
	Long: `ctxloom manages context for AI coding assistants.

QUICK START
  ctxloom run -p developer "explain this code"    Run with a profile
  ctxloom fragment install ctxloom-default/core        Install a fragment bundle
  ctxloom fragment edit core#fragments/coding     Edit a fragment

CONTENT COMMANDS
  fragment      Manage fragments (list, show, create, delete, edit, install)
  prompt        Manage prompts (list, show, create, delete, edit, install)
  profile       Manage profiles (list, show, create, delete, edit, install)

INFRASTRUCTURE
  remote        Manage remotes (add, remove, list, sync, lock, update)
  mcp           Manage MCP server configs (list, add, remove, show)

WORKFLOW
  run           Assemble context and run AI

KEY CONCEPTS
  Fragments   Reusable context snippets (coding standards, patterns, etc.)
  Prompts     Saved prompts for common tasks
  Profiles    Named configurations combining bundles and variables
  Bundles     YAML files containing fragments/prompts (internal format)
  Remotes     Git repositories for sharing content (GitHub/GitLab)

REFERENCE SYNTAX
  bundle#fragments/name           Specific fragment from bundle
  bundle#prompts/name             Specific prompt from bundle
  remote/bundle                   Bundle from a remote repository

Run 'ctxloom <command> --help' for details on any command.`,
}

// GetRootCmd returns the root command for documentation generation.
func GetRootCmd() *cobra.Command {
	return rootCmd
}

func Execute() {
	if err := rootCmd.Execute(); err != nil {
		// Check for ExitError to preserve specific exit codes
		if exitErr, ok := err.(*ExitError); ok {
			os.Exit(exitErr.Code)
		}
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func init() {
	// Enable --version flag
	rootCmd.Version = Version

	// Config is loaded via internal/config.Load() which handles the hierarchy:
	// 1. Project .ctxloom/config.yaml
	// 2. Embedded resources
}
