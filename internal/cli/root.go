package cli

import (
	"errors"
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/ctxloom/ctxloom/internal/config"
	"github.com/ctxloom/ctxloom/internal/lm/isolation"
	"github.com/ctxloom/ctxloom/internal/shared/strictness"
)

// Version is set at build time via ldflags
// Example: go build -ldflags "-X ctxloom/cmd.Version=v1.0.0"
var Version = "dev"

// degradedFlag backs the persistent --degraded flag: the fail-loudly escape
// hatch ("things may be broken, get me an agent"). Strict is the default;
// this flag (or CTXLOOM_DEGRADED=1, which the flag beats when both are set)
// downgrades fatal startup findings back to warn-and-continue.
var degradedFlag bool

// noCompanionsFlag backs the persistent --no-companions flag: skip companion
// loadout discovery entirely. Discovery EXECUTES the companion binaries found on
// PATH, so a run's skills/hooks/MCP/context otherwise vary with what the machine
// has installed; this makes a run reproducible (and is what CI and hermetic tests
// want). Env fallback: CTXLOOM_NO_COMPANIONS=1.
var noCompanionsFlag bool

// ExitError is returned when a command needs to exit with a specific code.
// This allows deferred cleanup to run before the process exits.
type ExitError struct {
	Code int
}

func (e *ExitError) Error() string {
	return fmt.Sprintf("exit code %d", e.Code)
}

// GetConfig returns the project configuration. Warnings that config.Load
// downgraded from hard errors (unreadable/malformed/schema-invalid files —
// CLAUDE.md fault tolerance) are echoed to stderr here so every GetConfig-based
// command surfaces them instead of silently operating on a partial config.
func GetConfig() (*config.Config, error) {
	cfg, err := config.Load()
	if err != nil {
		return nil, err
	}
	printConfigWarnings(os.Stderr, cfg.Warnings)
	return cfg, nil
}

// GetConfigForUpdate returns a config a command may MUTATE before saving. It is
// GetConfig's read/write twin: GetConfig hands back the shared ambient config
// (memoized, so ~35 call sites share one parse), and mutating that instance
// would let a change abandoned on an error path — validation failure, a Save
// that errors — leak into every later reader in the same process (an MCP/ACP
// server, the coordinator). Commands that write config (agent set/remove, llm
// default, mcp add/remove) take their own instance instead.
func GetConfigForUpdate() (*config.Config, error) {
	cfg, err := config.LoadFresh()
	if err != nil {
		return nil, err
	}
	printConfigWarnings(os.Stderr, cfg.Warnings)
	return cfg, nil
}

var rootCmd = &cobra.Command{
	Use:   "ctxloom",
	Short: "Sophisticated Context Management",
	// Execute owns error printing: without these, cobra prints every RunE
	// error twice ("Error: x" + Execute's own print) and dumps the full
	// usage text — including for a wrapped LLM's ordinary nonzero exit.
	SilenceUsage:  true,
	SilenceErrors: true,
	// Apply the parsed --degraded flag before any subcommand runs. The
	// CTXLOOM_DEGRADED env was already applied pre-dispatch (cmd/ctxloom/
	// main.go); an explicitly set flag wins over it in either direction.
	// No subcommand defines its own PersistentPreRun, so this runs for all.
	PersistentPreRun: func(cmd *cobra.Command, args []string) {
		if cmd.Root().PersistentFlags().Changed("degraded") {
			strictness.SetDegraded(degradedFlag)
		}
		// Same shape as --degraded: CTXLOOM_NO_COMPANIONS was already applied
		// pre-dispatch, and an explicitly set flag wins over it in either
		// direction. Applied here (not after GetConfig) because config.Load is
		// called from ~10 sites across the CLI — a per-Config toggle would only
		// take effect on whichever one happened to be wired.
		if cmd.Root().PersistentFlags().Changed("no-companions") {
			config.SetCompanionsDisabled(noCompanionsFlag)
		}
	},
	Long: `ctxloom manages context for AI coding assistants.

QUICK START
  ctxloom run -p developer "explain this code"    Run with a profile
  ctxloom fragment edit core#fragments/coding     Edit a fragment

CONTENT COMMANDS
  fragment      Manage fragments (list, show, create, delete, edit, search)
  skill         Manage skills (list, show, create, delete, edit)
  profile       Manage profiles (list, show, create, delete, edit, default)

INFRASTRUCTURE
  manage        Install/manage ctxloom's project harness (init, hooks, mcp, config)
  remote        Manage remotes (add, remove, list, default, pull, update, upgrade)
  mcp           Run ctxloom as an MCP server

WORKFLOW
  run           Assemble context and run AI

KEY CONCEPTS
  Fragments   Reusable context snippets (coding standards, patterns, etc.)
  Skills      Saved prompt templates, exported as slash commands
  Profiles    Named configurations combining bundles and variables
  Bundles     YAML files containing fragments/skills (internal format)
  Remotes     Git repositories for sharing content (GitHub or generic git)

REFERENCE SYNTAX
  bundle#fragments/name           Specific fragment from bundle
  bundle#skills/name              Specific skill from bundle
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

	// The fail-loudly escape hatch, on every command (startup chokes gate on
	// it; management commands simply ignore it). Env fallback: CTXLOOM_DEGRADED=1.
	rootCmd.PersistentFlags().BoolVar(&degradedFlag, "degraded", false,
		"degrade instead of failing: downgrade fatal startup findings (broken config, unresolvable profiles/bundles, failed hook applies) to warnings and launch anyway")

	// Companion loadouts are discovered by EXECUTING the companion binaries on
	// PATH (ltk, taskloom, ...), so what a run sees depends on what the machine
	// has installed. This turns that off for a reproducible run.
	// Env fallback: CTXLOOM_NO_COMPANIONS=1.
	rootCmd.PersistentFlags().BoolVar(&noCompanionsFlag, "no-companions", false,
		"skip companion loadout discovery: do not execute companion binaries (ltk, taskloom, ...) or contribute their skills, hooks, MCP servers and context")

	// The isolation layer bakes this stamp into agent images (ctxloom.version
	// label) and compares it against present images to rebuild stale ones; it
	// cannot import this package to read Version itself.
	isolation.SetBinaryVersion(Version)

	// Config is loaded via internal/config.Load() which handles the hierarchy:
	// 1. Project .ctxloom/config.yaml
	// 2. Embedded resources
}
