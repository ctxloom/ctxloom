package cmd

import (
	"errors"
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

// GetConfig returns the project configuration.
func GetConfig() (*config.Config, error) {
	return config.Load()
}

var rootCmd = &cobra.Command{
	Use:   "ctxloom",
	Short: "Sophisticated Context Management",
	// Execute owns error printing: without these, cobra prints every RunE
	// error twice ("Error: x" + Execute's own print) and dumps the full
	// usage text — including for a wrapped LLM's ordinary nonzero exit.
	SilenceUsage:  true,
	SilenceErrors: true,
	Long: `ctxloom manages context for AI coding assistants.

QUICK START
  ctxloom run -p developer "explain this code"    Run with a profile
  ctxloom fragment edit core#fragments/coding     Edit a fragment

CONTENT COMMANDS
  fragment      Manage fragments (list, show, create, delete, edit, search)
  prompt        Manage prompts (list, show, create, delete, edit)
  profile       Manage profiles (list, show, create, delete, edit, default)

INFRASTRUCTURE
  manage        Install/manage ctxloom's project harness (init, hooks, mcp, config)
  remote        Manage remotes (add, remove, list, default, pull, update, upgrade)
  mcp           Run ctxloom as an MCP server

WORKFLOW
  run           Assemble context and run AI

KEY CONCEPTS
  Fragments   Reusable context snippets (coding standards, patterns, etc.)
  Prompts     Saved prompts for common tasks
  Profiles    Named configurations combining bundles and variables
  Bundles     YAML files containing fragments/prompts (internal format)
  Remotes     Git repositories for sharing content (GitHub or generic git)

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
		// An ExitError carries the wrapped LLM's own exit code — an ordinary
		// outcome, not an error to report.
		var exitErr *ExitError
		if errors.As(err, &exitErr) {
			os.Exit(exitErr.Code)
		}
		fmt.Fprintf(os.Stderr, "Error: %s\n", err)
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
