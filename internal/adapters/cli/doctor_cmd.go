package cli

import (
	"io"
	"os"

	"github.com/spf13/cobra"

	"github.com/ctxloom/ctxloom/internal/adapters/operations"
	"github.com/ctxloom/ctxloom/internal/shared/iox"
	"github.com/ctxloom/ctxloom/internal/shared/termsafe"
)

// doctorDepsOnlyFlag backs --deps (operations.DoctorRequest.DepsOnly).
var doctorDepsOnlyFlag bool

var doctorCmd = &cobra.Command{
	Use:   "doctor",
	Short: "Run deterministic setup checks (deps, agents, hooks, MCP, companions, trust)",
	Long: `Run ctxloom's deterministic setup checks — this IS the init-as-skill setup
skill's Phase 6 postcondition check (init-as-skill.plan.md §8.2): the
.ctxloom marker + config validity; required binaries on PATH (git, each
configured engine's own client, a container runtime when this project runs
'runtime: container' agents, and — recommended, not required — ssh/ssh-keygen);
whether every configured agent resolves (profile composition +
engine/runtime) and the roster is non-empty; the seeded
dependency lockfile parses and a real context assembly succeeds; hooks AND
MCP registration per configured backend; the trust store's signers;
which version-scoped transcript reader each configured engine's INSTALLED
version selects, and the version ranges ctxloom carries readers for — what
you need when a transcript refuses to convert, since reading a vendor's own
transcript store refuses rather than guessing at an unvalidated format;
companion detection + loadout probing (taskloom/ltk/...); every
paths.TierLocal path (internal/core/paths.Layout) this checkout is missing — the
local-only state (the dirty-tree-commit acknowledgement, the task-log
project-id marker, distilled sessions, review's cached diff objects) that a
fresh clone has no way to learn it lacks anywhere else; and, always, a stated
reminder of the one boundary no check here crosses: ctxloom can confirm it
WROTE the assembled context onto the engine's own surface, never that the
engine actually READ it — that happens inside a process ctxloom does not own.
Each line is prefixed with a DOCTOR-CHECK-* marker — the SAME vocabulary the
"ctxloom-doctor" Agent Skill uses, so a human or an LLM reading either
surface sees one language.

Version currency has no dedicated check here (best-effort, skill-guided):
compare 'ctxloom version' against your remote's newest tag by hand, or ask
an assistant carrying the ctxloom-doctor skill to do it.

--deps scopes the report to ONLY the machine-capability probes (git/ssh/
ssh-keygen, a container runtime, any already-configured engine's client,
signing-key readiness, and git identity) —
no agents/profiles/hooks/trust checks, so it reads clean on a project that
hasn't been set up yet. This is the mode init's PRIME and the setup skill's
phase 1 use, before there's anything else to check.

Diagnostic only: no check outcome ever fails the command, and nothing is
blocked or changed. A "warn" status IS this command's fail-loud signal — read
the report, don't grep the exit code. A usage error is still an error (e.g. a
--format value this build cannot render).`,
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
	report, err := operations.Doctor(cmd.Context(), App(), operations.DoctorRequest{
		DepsOnly: doctorDepsOnlyFlag,
		Home:     doctorHome(),
	})
	if err != nil {
		return err
	}
	return emit(cmd, report, func() error { return renderDoctorReport(cmd.OutOrStdout(), report) })
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

// renderDoctorReport writes the human-readable check list, one
// "DOCTOR-CHECK-* [status] detail" line per check, in the fixed order the
// checks were run.
func renderDoctorReport(out io.Writer, report operations.DoctorReport) error {
	w := iox.NewErrWriter(out)
	w.Println("ctxloom doctor")
	for _, c := range report.Checks {
		// A detail is ctxloom's sentence with publisher values (bundle refs,
		// remote errors) spliced in. termsafe.Sanitize and not Field: Field's
		// line-sized cap would clip ctxloom's own longer sentences.
		w.Printf("  %s [%s] %s\n", c.Marker, c.Status, termsafe.Sanitize(c.Detail, 0, false).Text)
	}
	return w.Err()
}

func init() {
	doctorCmd.Flags().BoolVar(&doctorDepsOnlyFlag, "deps", false,
		"check ONLY machine-capability dependencies (git/ssh/ssh-keygen/container runtime/configured engines' clients/signing key/git identity) — skips agents/profiles/hooks/trust, for use before a project has been set up")
	rootCmd.AddCommand(doctorCmd)
}
