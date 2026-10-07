package cli

import (
	"fmt"
	"io"

	"github.com/spf13/cobra"

	"github.com/ctxloom/ctxloom/internal/adapters/operations"
	"github.com/ctxloom/ctxloom/internal/core/paths"
)

// The companion registration CLI. A companion runs only when its NAME is
// registered here; nothing on PATH is used by being there.
//
// Deliberately NO MCP tools for any of this: handing the agent the ability to
// register the binaries that run alongside it defeats the property the
// registration exists to provide.

const companionLong = `Register which companion programs ctxloom runs.

A companion is a program that CONTRIBUTES context — hooks, MCP servers,
fragments — by answering ` + "`<binary> loadout`" + `. ctxloom runs exactly the companions
you registered, each resolved on your PATH by name when it is used. Nothing on
PATH is used just because it is there: a dependency that drops a
ctxloom-companion-* binary into ./node_modules/.bin earns nothing.

A name maps to a binary: the shipped ltk, taskloom and reprise are their own
binaries; any other name <n> is the binary ctxloom-companion-<n>.

The registration is the NAME only, kept in your home config
(~/.ctxloom/config.yaml, key ` + "`companions`" + `), never a path — so the same
registration works wherever the binary is installed, inside an agent
container included. A binary of a registered name placed EARLIER on PATH is
the one that runs.

  ctxloom companion add <name>           check it answers, then register it
  ctxloom companion remove <name> --yes  unregister it
  ctxloom companion list                 registered names, and whether each resolves`

// Bare `ctxloom companion` lists the registered companions.
var companionCmd = groupNodeDefault(&cobra.Command{
	Use:   "companion",
	Short: "Register which companion programs ctxloom runs",
	Long:  companionLong,
}, "list")

var companionAddCmd = &cobra.Command{
	Use:   "add <name>",
	Short: "Register a companion by name, after checking it answers",
	Long:  companionLong,
	Example: `  ctxloom companion add ltk
  ctxloom companion add acme      # resolves ctxloom-companion-acme`,
	Args: cobra.ExactArgs(1),
	RunE: runCompanionAddCmd,
}

// runCompanionAddCmd resolves the companion on PATH, runs its loadout probe,
// and records the name in the HOME config — the one registration every
// project and every container launched from this machine reads.
func runCompanionAddCmd(cmd *cobra.Command, args []string) error {
	if err := pinHomeConfig(cmd); err != nil {
		return err
	}
	res, err := operations.AddCompanion(cmd.Context(), App(), args[0])
	if err != nil {
		return err
	}
	return emit(cmd, res, func() error {
		w := cmd.OutOrStdout()
		if !res.Added {
			_, err := fmt.Fprintf(w, "Companion '%s' is already registered (%s)\n", res.Name, res.Path)
			return err
		}
		_, err := fmt.Fprintf(w, "Registered companion '%s' (%s answers at %s)\n", res.Name, res.Bin, res.Path)
		return err
	})
}

var companionRemoveYes bool

var companionRemoveCmd = &cobra.Command{
	Use:     "remove <name>",
	Aliases: []string{"rm"},
	Short:   "Unregister a companion",
	Long: `Unregister a companion: ctxloom stops running it.

Bare invocation reports what would be removed and removes nothing (exit 0).
Pass --yes to apply it. The binary itself is not touched.`,
	Example: `  ctxloom companion remove acme --yes`,
	Args:    cobra.ExactArgs(1),
	RunE:    runCompanionRemoveCmd,
}

func runCompanionRemoveCmd(cmd *cobra.Command, args []string) error {
	name := args[0]
	if err := pinHomeConfig(cmd); err != nil {
		return err
	}
	applyCmd := fmt.Sprintf("ctxloom companion remove %s --yes", name)
	target := fmt.Sprintf("companion %q", name)
	if !companionRemoveYes {
		cfg, err := GetConfig()
		if err != nil {
			return err
		}
		if !registered(cfg.GetCompanions(), name) {
			return fmt.Errorf("%w: %q (see `ctxloom companion list`)", operations.ErrCompanionNotRegistered, name)
		}
		return emit(cmd, newRemovePreviewResult(target, nil, applyCmd), func() error {
			printRemovePreview(cmd.OutOrStdout(), target, nil, applyCmd)
			return nil
		})
	}
	if err := operations.RemoveCompanion(cmd.Context(), App(), name); err != nil {
		return err
	}
	return emit(cmd, companionRemoved{Name: name, Removed: true}, func() error {
		_, err := fmt.Fprintf(cmd.OutOrStdout(), "Removed companion '%s'\n", name)
		return err
	})
}

// companionRemoved is the emitted shape of an applied `companion remove`.
type companionRemoved struct {
	Name    string `json:"name" yaml:"name" toml:"name"`
	Removed bool   `json:"removed" yaml:"removed" toml:"removed"`
}

func registered(names []string, name string) bool {
	for _, n := range names {
		if n == name {
			return true
		}
	}
	return false
}

// registeredCompanions is the isolation layer's view of this machine's
// registered companion names (isolation.SetRegisteredCompanions). A
// configuration that cannot be read registers nothing; its load error is
// reported where the configuration is loaded.
func registeredCompanions() []string {
	cfg, err := GetConfig()
	if err != nil {
		return nil
	}
	return cfg.GetCompanions()
}

// pinHomeConfig points this invocation's configuration at the HOME .ctxloom,
// where a companion registration lives (as `init --home` does).
func pinHomeConfig(cmd *cobra.Command) error {
	home, err := paths.HomeConfigDir()
	if err != nil {
		return err
	}
	return pinAppDir(cmd, home)
}

var companionListCmd = &cobra.Command{
	Use:     "list",
	Aliases: []string{"ls"},
	Short:   "List registered companions and whether each resolves on PATH",
	Long:    companionLong,
	Example: `  ctxloom companion list`,
	Args:    cobra.NoArgs,
	RunE:    runCompanionListCmd,
}

// runCompanionListCmd lists the registered names of the effective
// configuration — the set a session here would run — and resolves each on
// PATH. Looking runs nothing.
func runCompanionListCmd(cmd *cobra.Command, _ []string) error {
	cfg, err := GetConfig()
	if err != nil {
		return err
	}
	out := operations.ListCompanions(cfg.GetCompanions())
	return emit(cmd, out, func() error {
		return renderCompanionList(cmd.OutOrStdout(), out)
	})
}

func renderCompanionList(w io.Writer, out []operations.CompanionListing) error {
	if len(out) == 0 {
		_, err := fmt.Fprintln(w, "No companions registered.\nUse 'ctxloom companion add <name>' to register one.")
		return err
	}
	for _, l := range out {
		where := l.Path
		if !l.Resolves {
			where = "NOT ON PATH — install it, or: ctxloom companion remove " + l.Name + " --yes"
		}
		if _, err := fmt.Fprintf(w, "%-12s %-28s %s\n", l.Name, l.Bin, where); err != nil {
			return err
		}
	}
	return nil
}

func init() {
	rootCmd.AddCommand(companionCmd)
	companionCmd.AddCommand(companionAddCmd)
	companionCmd.AddCommand(companionRemoveCmd)
	companionCmd.AddCommand(companionListCmd)
	companionRemoveCmd.Flags().BoolVarP(&companionRemoveYes, yesFlagName, "y", false, "Apply the removal this invocation would report (default: report only)")
}
