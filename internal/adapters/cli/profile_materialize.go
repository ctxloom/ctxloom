package cli

import (
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/pmezard/go-difflib/difflib"
	"github.com/spf13/cobra"

	"github.com/ctxloom/ctxloom/internal/adapters/operations"
	"github.com/ctxloom/ctxloom/internal/core/config"
	"github.com/ctxloom/ctxloom/internal/shared/iox"
)

var (
	materializeTarget  string
	materializeBackend string
	materializeDiff    string
)

// profileMaterializeCmd writes a profile's ASSEMBLED, ready-to-run agent surface
// into a target dir (CLAUDE.md + .mcp.json + settings hooks + commands), so an
// externally-launched agent inherits it with ctxloom out of the loop. Distinct
// from `profile export`, which publishes the profile's YAML definition.
var profileMaterializeCmd = &cobra.Command{
	Use:   "materialize <profile>...",
	Short: "Write a profile's assembled context to a dir as a launchable agent surface",
	Long: `Materialize one or more profiles into --target as a backend's NATIVE on-disk
agent surface — CLAUDE.md (context) + .mcp.json (MCP) + .claude/settings.json
(hooks) + .claude/commands (commands) — so an externally-launched agent inherits
the profile with ctxloom out of the loop.

Each run OVERWRITES the target's ctxloom-managed surfaces (they are the source
of truth) while preserving foreign entries. Unlike 'ctxloom profile export'
(which publishes the profile YAML), this writes the assembled, ready-to-run
config a plain 'claude' / CI / human launch reads by default.

--diff <file> switches to a read-only comparison instead: it assembles the
named profile(s)' context exactly as materialize would, but rather than
writing it under --target, diffs it against an already-delivered context file
(e.g. another machine's materialized CLAUDE.md) and reports what differs — the
two-machine "it works on his machine, not hers" symptom. --target is not
required in this mode; nothing is written.

Examples:
  ctxloom profile materialize default --target ./out
  ctxloom profile materialize go-dev cr-correctness-go --target ../worktree
  ctxloom profile materialize default --diff ../bob-checkout/CLAUDE.md`,
	Args: cobra.MinimumNArgs(1),
	RunE: runProfileMaterialize,
}

func runProfileMaterialize(cmd *cobra.Command, args []string) error {
	cfg, err := GetConfig()
	if err != nil {
		return fmt.Errorf("failed to load config: %w", err)
	}
	if materializeDiff != "" {
		return runProfileMaterializeDiff(cmd, cfg, args)
	}
	if materializeTarget == "" {
		return fmt.Errorf(`required flag(s) "target" not set (or pass --diff to compare instead of writing)`)
	}
	// Fail-loudly choke owner (CLAUDE.md): checkpoint before materialize so every
	// fatal surface-write finding it records through strictness is caught here and
	// aborts the command (exit 3) unless --degraded downgrades them — mirroring
	// how `ctxloom run`/`mcp`/`acp` gate their own startup findings.
	gates := newPhaseGates(os.Stderr, App().Strictness)
	res, err := operations.MaterializeProfile(cmd.Context(), App().Engines(), cfg, operations.MaterializeProfileRequest{
		Profiles: args,
		Target:   materializeTarget,
		Backend:  materializeBackend,
	})
	if err != nil {
		return err
	}
	if ferr := gates.close(PhaseStartup); ferr != nil {
		return ferr
	}
	return emit(cmd, res, func() error {
		w := iox.NewErrWriter(cmd.OutOrStdout())
		w.Printf("Materialized %s → %s (%s)\n", strings.Join(res.Profiles, ", "), res.Target, res.Backend)
		for _, s := range res.Wrote {
			w.Printf("  wrote %s\n", s)
		}
		// The loss lines sit with the wrote lines, not in a separate pass at the
		// end: they are the same report. A reader who scans only the top of the
		// output must not come away with "wrote four things" as the whole story.
		for _, loss := range res.NotCarried {
			w.Printf("  NOT carried: %s\n", loss)
		}
		for _, warn := range res.Warnings {
			w.Printf("  warning: %s\n", warn)
		}
		return w.Err()
	})
}

// profileMaterializeDiffJSON is the --format json shape for `profile
// materialize --diff`: the two sides compared, whether they matched, and the
// unified diff text (empty when identical).
type profileMaterializeDiffJSON struct {
	Profiles   []string `json:"profiles"`
	ComparedTo string   `json:"compared_to"`
	Identical  bool     `json:"identical"`
	Diff       string   `json:"diff,omitempty"`
}

// runProfileMaterializeDiff answers M5, the two-machine symptom this journey
// opened on: "it is reaching her assistant and not his." Every other
// diagnostic ctxloom has is single-machine — this is the one surface that
// reads a SECOND, already-delivered context (materialized elsewhere, on
// another checkout) and reports what differs against the profile(s) resolved
// HERE, rather than merely asserting the two disagree.
//
// It reuses operations.AssembleContext — the context view of the same
// AssemblePackage assembly MaterializeProfile runs, stating the same subject
// (MaterializedFor --backend), so it composes what materialize would WRITE for
// that engine rather than what a live session would see — instead of writing a
// scratch target to disk and reading it back: --diff is read-only by design,
// so it never touches --target at all.
func runProfileMaterializeDiff(cmd *cobra.Command, cfg *config.Config, args []string) error {
	asm, err := operations.AssembleContext(cmd.Context(), cfg, operations.AssembleContextRequest{
		Profiles: args,
		Consumer: operations.MaterializedFor(App().Engines(), materializeBackend),
	})
	if err != nil {
		return fmt.Errorf("assemble context for %v: %w", args, err)
	}
	theirs, err := os.ReadFile(materializeDiff)
	if err != nil {
		return fmt.Errorf("read %s to compare against: %w", materializeDiff, err)
	}
	label := strings.Join(args, "+")
	diffText := ""
	if asm.Context != string(theirs) {
		diffText, err = difflib.GetUnifiedDiffString(difflib.UnifiedDiff{
			A:        difflib.SplitLines(string(theirs)),
			B:        difflib.SplitLines(asm.Context),
			FromFile: materializeDiff,
			ToFile:   label + " (materialized here)",
			Context:  3,
		})
		if err != nil {
			return fmt.Errorf("diff %s against %s's materialized context: %w", materializeDiff, label, err)
		}
	}
	result := profileMaterializeDiffJSON{
		Profiles:   args,
		ComparedTo: materializeDiff,
		Identical:  diffText == "",
		Diff:       diffText,
	}
	return emit(cmd, result, func() error {
		return renderMaterializeDiff(cmd.OutOrStdout(), label, result)
	})
}

// renderMaterializeDiff is `profile materialize --diff`'s text rendering. The
// diff carries assembled fragment bodies, publisher-authored and unbounded, so
// it goes through publisherBody — without the blank-line collapse, because a
// diff redirected to a file must still be the diff.
func renderMaterializeDiff(out io.Writer, label string, result profileMaterializeDiffJSON) error {
	w := iox.NewErrWriter(out)
	if result.Identical {
		w.Printf("%s's materialized context matches %s: no difference.\n", label, result.ComparedTo)
		return w.Err()
	}
	w.Printf("Comparing %s's materialized context with %s — content present on one side and absent on the other:\n", label, result.ComparedTo)
	_ = publisherBody("", "", false).Render(w, label+" diff", result.Diff)
	return w.Err()
}

func init() {
	profileCmd.AddCommand(profileMaterializeCmd)
	profileMaterializeCmd.Flags().StringVar(&materializeTarget, "target", "", "Target directory to write the agent surface into (required)")
	profileMaterializeCmd.Flags().StringVar(&materializeBackend, "backend", operations.DefaultMaterializeBackend, "Backend whose surface is written, or compared against with --diff (claude-code)")
	profileMaterializeCmd.Flags().StringVar(&materializeDiff, "diff", "",
		"Compare this profile's materialized context against an already-delivered context file instead of writing --target")
	// --target is required UNLESS --diff is given (read-only comparison mode
	// writes nothing) — that conditional can't be expressed via
	// MarkFlagRequired, so it is checked in runProfileMaterialize instead.
	profileMaterializeCmd.ValidArgsFunction = completeProfileNames

}
