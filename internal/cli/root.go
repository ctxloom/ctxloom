package cli

import (
	"errors"
	"fmt"
	"io"
	"os"

	"github.com/spf13/cobra"

	"github.com/ctxloom/ctxloom/internal/config"
	"github.com/ctxloom/ctxloom/internal/lm/isolation"
	"github.com/ctxloom/ctxloom/internal/shared/clidiag"
	"github.com/ctxloom/ctxloom/internal/shared/cliemit"
	"github.com/ctxloom/ctxloom/internal/shared/confload"
	"github.com/ctxloom/ctxloom/internal/shared/strictness"
	"github.com/ctxloom/ctxloom/pkg/clifmt"
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
// PATH, so a run's commands/hooks/MCP/context otherwise vary with what the machine
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
	return loadWithWarnings(config.Load)
}

// GetConfigForUpdate returns a config a command may MUTATE before saving. It is
// GetConfig's read/write twin: GetConfig hands back the shared ambient config
// (memoized, so ~35 call sites share one parse), and mutating that instance
// would let a change abandoned on an error path — validation failure, a Save
// that errors — leak into every later reader in the same process (an MCP/ACP
// server, the coordinator). Commands that write config (agent set/remove, llm
// default, mcp add/remove) take their own instance instead.
func GetConfigForUpdate() (*config.Config, error) {
	return loadWithWarnings(config.LoadFresh)
}

// loadWithWarnings runs one of config's loaders and echoes the findings it
// downgraded from hard errors, so no command silently operates on a partial
// config. Which loader is the ONLY difference between the two entry points
// above; the warning echo is a property of loading, not of read-vs-write.
func loadWithWarnings(load func(...config.LoadOption) (*config.Config, error)) (*config.Config, error) {
	cfg, err := load()
	if err != nil {
		return nil, err
	}
	printConfigWarnings(os.Stderr, cfg.GetWarnings())
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
		// U104-F01's runtime guard (format.go): clear the per-invocation
		// "did this command honor --format" tracker before anything can set
		// it, so checkFormatWasHonored's PersistentPostRunE below checks
		// exactly this invocation.
		resetFormatGuard()
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
		// Capture the env/CLI config-override chain (CTXLOOM_CONFIG_* vars,
		// plus any flag this INVOKED command changed) exactly ONCE per process,
		// right here — cmd.Flags() is the invoked command's fully-parsed flag
		// set (its own local flags plus every inherited persistent flag) at the
		// earliest point every subcommand passes through. Every config.Load
		// from here on resolves it via the funnel (see loadUncached); a bind
		// failure degrades to a warning rather than aborting startup, matching
		// this codebase's fault-tolerance convention (an ambiguous individual
		// override is caught and warned about later, per-Load, once there is a
		// config base to resolve it against).
		if err := config.InstallOverridesFromFlags(cmd.Flags()); err != nil {
			clidiag.Warn("ctxloom", "config overrides: %v", err)
		}
		// Flip clidiag's structured-diagnostics channel on for json/yaml/toml
		// --format, off (today's plain "<prog>: warning: <msg>" stderr) for
		// text/markdown or an unresolvable value — an invalid --format is
		// reported by the command's own emit()/cliemit.Resolve call, not here, so
		// this just falls back to the safe default rather than erroring twice.
		format, ferr := cliemit.Resolve(cmd)
		clidiag.SetStructured(ferr == nil && format.Structured())
	},
	// U104-F01's runtime guard (format.go): fires only after a successful
	// RunE (cobra skips it on error, and skips it entirely for
	// --help/--version/completion), turning a non-text --format that no
	// command ever honored into a loud error instead of a silent exit 0.
	// No subcommand defines its own PersistentPostRun(E), so — symmetrically
	// with PersistentPreRun above — this runs for all of them.
	PersistentPostRunE: func(cmd *cobra.Command, args []string) error {
		return checkFormatWasHonored(cmd)
	},
	Long: `ctxloom manages context for AI coding assistants.

QUICK START
  ctxloom run -p developer "explain this code"    Run with a profile
  ctxloom fragment edit core#fragments/coding     Edit a fragment

CONTENT COMMANDS
  fragment      Manage fragments (list, show, create, delete, edit, search)
  command       Manage commands (list, show, create, delete, edit)
  profile       Manage profiles (list, show, create, delete, edit, default)

INFRASTRUCTURE
  manage        Install/manage ctxloom's project harness (init, hooks, mcp, config)
  remote        Manage remotes (add, remove, list, default, pull, update, upgrade)
  mcp           Run ctxloom as an MCP server

WORKFLOW
  run           Assemble context and run AI

KEY CONCEPTS
  Fragments   Reusable context snippets (coding standards, patterns, etc.)
  Commands    Saved prompt templates, exported as slash commands
  Profiles    Named configurations combining bundles and variables
  Bundles     YAML files containing fragments/commands (internal format)
  Remotes     Git repositories for sharing content (GitHub or generic git)

REFERENCE SYNTAX
  bundle#fragments/name           Specific fragment from bundle
  bundle#commands/name            Specific command from bundle
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
		format, ferr := cliemit.Resolve(rootCmd)
		if ferr != nil {
			// The --format value itself was rejected; report err (the ORIGINAL
			// failure) rather than the format error, in the one shape every
			// terminal can read.
			format = clifmt.FormatText
		}
		reportExecuteError(os.Stderr, err, format)
		os.Exit(1)
	}
}

// reportExecuteError renders a top-level command failure to w, in the
// selected --format. text keeps the plain "Error: msg" line the CLI has
// always printed on failure; json/yaml/toml/markdown get clifmt's structured
// {"error": "..."} envelope instead — the one stderr surface that used to
// stay human-text-only no matter what --format a machine caller requested.
func reportExecuteError(w io.Writer, err error, format clifmt.Format) {
	// RenderError only fails if the underlying writer fails (os.Stderr in
	// production never does); there's no further fallback if it did.
	_ = clifmt.RenderError(w, err, format)
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
		"skip companion loadout discovery: do not execute companion binaries (ltk, taskloom, ...) or contribute their commands, hooks, MCP servers and context")

	// The isolation layer bakes this stamp into agent images (ctxloom.version
	// label) and compares it against present images to rebuild stale ones; it
	// cannot import this package to read Version itself.
	isolation.SetBinaryVersion(Version)

	// --config-set is the ONLY source of CLI-layer config overrides (see
	// confload.ConfigSetFlagName's doc): a dedicated, repeatable, PERSISTENT flag
	// (so it works identically for every subcommand) rather than treating
	// every command's OWN flags as candidate config paths by name — that
	// approach silently collided with real flags that happen to share a name
	// with a top-level config key (--runtime, --workspace, --version,
	// --hooks, --agents, --mcp, --profiles, --llm) and broke structured
	// --format output with stray warnings on totally unrelated flags
	// (--bundle, --sig, ...). Registered here, once, on the root command;
	// config.InstallOverridesFromFlags (called from PersistentPreRun below)
	// reads it via confload.Product.ReadOverrides.
	rootCmd.PersistentFlags().StringArray(confload.ConfigSetFlagName, nil,
		"override a config value for this invocation: --config-set <dotted.path>=<value> (repeatable; e.g. --config-set llm.defaults.primary=big, --config-set agents.MyCoder.runtime=container)")

	// Config is loaded via internal/config.Load() which handles the hierarchy:
	// 1. Project .ctxloom/config.yaml
	// 2. Embedded resources
}
