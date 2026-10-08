package cli

import (
	"fmt"
	"io"
	"os"
	"sort"

	"github.com/spf13/cobra"

	"github.com/ctxloom/ctxloom/internal/adapters/operations"
	"github.com/ctxloom/ctxloom/internal/shared/errwriter"
)

// doctorDepsOnlyFlag backs --deps (operations.DoctorRequest.DepsOnly).
var doctorDepsOnlyFlag bool

// doctorAllFlag backs --all: the text report lists every check rather than
// the warnings alone. Structured output always carries every check.
var doctorAllFlag bool

// doctorFixFlag backs --fix: remove what the fixable checks find before
// reporting (operations.DoctorFix).
var doctorFixFlag bool

const (
	// doctorAllClearLine closes a text report that found nothing to fix.
	doctorAllClearLine = "No warnings. `ctxloom doctor --all` lists every check."
	// doctorNoFixNamed ends the summary when no warning carries a structured
	// remedy; most rows state their fix inside the detail instead.
	doctorNoFixNamed = "see each row above for its fix"
)

// doctorCmd is the init-as-skill setup skill's Phase 6 postcondition check
// (init-as-skill.plan.md §8.2). Its DOCTOR-CHECK-* markers are the vocabulary
// the "ctxloom-doctor" Agent Skill uses, so a human and an LLM reading either
// surface see one language. --deps is the mode init's PRIME and the setup
// skill's phase 1 use, before there is anything else to check. The
// transcript-reader rows exist because reading a vendor's own transcript store
// refuses an unvalidated format rather than guessing; the local-state rows
// cover every paths.TierLocal path a fresh clone has no way to learn it lacks.
// Version currency has no check: it is best-effort and skill-guided.
var doctorCmd = &cobra.Command{
	Use:   "doctor",
	Short: "Run deterministic setup checks (deps, agents, hooks, MCP, companions)",
	Long: `Check this project's setup and say what to fix: the .ctxloom marker and
config, required binaries (git, each configured engine's client, a container
runtime for container agents), whether every agent resolves, the lockfile and
context assembly, hooks and MCP registration, companions,
transcript readers for each engine's installed version, and local-only state
a fresh clone lacks. It also states the one thing no check can confirm:
ctxloom writes the context onto the engine's surface, but whether the engine
reads it happens in a process ctxloom does not own.

The text report lists the warnings, then one line counting them and naming
the first fix; --all lists every check. Each row starts with a DOCTOR-CHECK-*
marker. --deps checks only what this machine needs (binaries and git
identity), so it reads clean before a project is set up.

A warning is the signal: doctor exits 0 whatever it finds, and without
--fix changes nothing (the container-runtime probe may create the runtime's
own storage directories). A usage error, such as an unknown --format, still
fails.

--fix changes one thing before reporting: it removes every hook entry in an
engine's project or user settings file that runs a ` + "`ctxloom hook`" + ` subcommand
this ctxloom does not have (left by an older ctxloom; the engine runs it at
its event and it fails every time). Only those entries leave the file; each
removal is listed on stderr.`,
	Example: `  ctxloom doctor                      # the warnings, and the first fix
  ctxloom doctor --all                # every check
  ctxloom doctor --deps               # only what this machine needs
  ctxloom doctor --fix                # remove stale ctxloom hook entries, then report`,
	Args: cobra.NoArgs,
	RunE: runDoctorCmd,
}

// runDoctorCmd is the frontend over operations.Doctor: it reads the config
// once here so the load warnings reach stderr the way every GetConfig-based
// command surfaces them (the service re-reads the same memoized generation
// and reports a failed load as a finding, never as an error), hands the
// service the home it stands in for the composition root on, and renders.
func runDoctorCmd(cmd *cobra.Command, args []string) error {
	_, _ = GetConfig()
	verbs := hookVerbsOf(hookCmd)
	if doctorFixFlag {
		if err := runDoctorFix(cmd, verbs); err != nil {
			return err
		}
	}
	report, err := operations.Doctor(cmd.Context(), App(), operations.DoctorRequest{
		DepsOnly:  doctorDepsOnlyFlag,
		Home:      doctorHome(),
		HookVerbs: verbs,
	})
	if err != nil {
		return err
	}
	return emit(cmd, report, func() error {
		if doctorAllFlag {
			return operations.WriteDoctorReport(cmd.OutOrStdout(), report)
		}
		return renderDoctorSummary(cmd.OutOrStdout(), report)
	})
}

// runDoctorFix applies the fixes and lists each removal on stderr, so the
// report on stdout keeps its format.
func runDoctorFix(cmd *cobra.Command, verbs []string) error {
	res, err := operations.DoctorFix(cmd.Context(), App(), operations.DoctorFixRequest{HookVerbs: verbs})
	w := errwriter.New(cmd.ErrOrStderr())
	for _, e := range res.Removed {
		w.Println("removed stale hook entry: " + e.String())
	}
	if err != nil {
		return fmt.Errorf("doctor --fix: %w", err)
	}
	return w.Err()
}

// hookVerbsOf is every subcommand name and alias directly under hook, read
// off the command tree at run time — never a hand list, so a renamed hook
// subcommand makes the old spelling's settings entries stale with no other
// edit. Sorted for a stable report.
func hookVerbsOf(hook *cobra.Command) []string {
	var verbs []string
	for _, c := range hook.Commands() {
		verbs = append(verbs, c.Name())
		verbs = append(verbs, c.Aliases...)
	}
	sort.Strings(verbs)
	return verbs
}

// doctorHome is the user's home as the doctor's home-rooted checks see it —
// the CLI standing in for the composition root (launch.HostFacts carries
// home from cmd/*). Best-effort: "" when it cannot be resolved, and the
// home-rooted rows are then skipped rather than failing a check.
func doctorHome() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return home
}

// renderDoctorSummary is the default text report: the warn rows alone, then
// one line counting them and naming the first remedy, or the all-clear line.
func renderDoctorSummary(out io.Writer, report operations.DoctorReport) error {
	w := errwriter.New(out)
	w.Println("ctxloom doctor")
	warnings, firstFix := 0, ""
	for _, c := range report.Checks {
		if c.Status != operations.DoctorWarn {
			continue
		}
		warnings++
		if firstFix == "" {
			firstFix = inertBody(c.Remedy, 0, false).Text
		}
		operations.WriteDoctorRow(w, c)
	}
	if warnings == 0 {
		w.Println(doctorAllClearLine)
	} else {
		w.Println(doctorWarningsSummary(warnings, firstFix))
	}
	return w.Err()
}

// doctorWarningsSummary is the text report's closing line for n warnings,
// naming fix (the first warning's remedy) when there is one.
func doctorWarningsSummary(n int, fix string) string {
	noun := "warnings"
	if n == 1 {
		noun = "warning"
	}
	if fix == "" {
		return fmt.Sprintf("%d %s; %s", n, noun, doctorNoFixNamed)
	}
	return fmt.Sprintf("%d %s; first fix: %s", n, noun, fix)
}

func init() {
	doctorCmd.Flags().BoolVar(&doctorAllFlag, "all", false,
		"list every check in the text report, not only the warnings")
	doctorCmd.Flags().BoolVar(&doctorDepsOnlyFlag, "deps", false,
		"check ONLY machine-capability dependencies (git/ssh/container runtime/configured engines' clients/git identity) — skips agents/profiles/hooks/MCP, for use before a project has been set up")
	doctorCmd.Flags().BoolVar(&doctorFixFlag, "fix", false,
		"remove settings hook entries that run a 'ctxloom hook' subcommand this ctxloom does not have, then report")
	rootCmd.AddCommand(doctorCmd)
}
