package cmd

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/benjaminabbitt/scm/internal/config"
)

// Version is set at build time via ldflags
// Example: go build -ldflags "-X scm/cmd.Version=v1.0.0"
var Version = "dev"

// ExitError is returned when a command needs to exit with a specific code.
// This allows deferred cleanup to run before the process exits.
type ExitError struct {
	Code int
}

func (e *ExitError) Error() string {
	return fmt.Sprintf("exit code %d", e.Code)
}

// GetSCMDirs returns the .scm directories from project config.
func GetSCMDirs() ([]string, error) {
	cfg, err := config.Load()
	if err != nil {
		return nil, err
	}
	return cfg.SCMPaths, nil
}

// GetConfig returns the project configuration.
func GetConfig() (*config.Config, error) {
	return config.Load()
}

var rootCmd = &cobra.Command{
	Use:   "scm",
	Short: "Sophisticated Context Management",
	Long: `SCM manages context for AI coding assistants.

QUICK START
  scm run -p developer "explain this code"    Run with a profile
  scm run -f coding-standards "review"        Run with specific fragments
  scm bundle list                             List installed bundles
  scm profile list                            List available profiles

CORE COMMANDS
  run           Assemble context and run AI
  bundle        Manage bundles (fragments, prompts, MCP servers)
  profile       Manage profiles (named fragment collections)
  mcp           Run as MCP server for AI tool integration

REMOTE CONTENT
  remote add    Register a remote source (GitHub/GitLab)
  remote pull   Pull profiles or bundles from remotes
  remote list   List configured remote sources

CONFIGURATION
  mcp-servers   Manage MCP server configurations
  hook          Hook commands for AI tool integration
  plugin        Manage AI backend plugins

KEY CONCEPTS
  Bundles     Versioned collections of fragments, prompts, and MCP configs
  Fragments   Reusable context snippets (coding standards, patterns, etc.)
  Profiles    Named sets of bundles/fragments with variables
  Remotes     Git repositories for sharing content

CONTENT REFERENCE SYNTAX
  bundle-name                      Entire bundle (all fragments)
  bundle-name#fragments/name       Specific fragment from bundle
  bundle-name#prompts/name         Specific prompt from bundle
  remote/bundle#fragments/name     Fragment from remote bundle

Run 'scm <command> --help' for details on any command.`,
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
	// Config is loaded via internal/config.Load() which handles the hierarchy:
	// 1. Project .scm/config.yaml
	// 2. Embedded resources
}
