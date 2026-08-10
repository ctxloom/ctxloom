package cli

import (
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/spf13/cobra"

	"github.com/ctxloom/ctxloom/internal/operations"
	"github.com/ctxloom/ctxloom/internal/shared/iox"
	"github.com/ctxloom/ctxloom/pkg/clifmt"
)

// The destroyers under `session`, and the one rule they all obey.
//
// EACH POPULATION OWNS ITS DESTROYER. A harp directory holds three separable
// populations, and each is destroyed by the sub-noun that understands it:
//
//	session transcript purge <harp>   the machine-written conversation
//	session artifacts purge <harp>    the derived essence
//	session worktrees purge <harp>    the scratch git checkouts
//	session purge <harp>              sweeps all three
//
// Selection flags live on the leaf that understands them — --undistilled on
// transcript — and never on the sweep. A flag on the parent would have to
// mean something for populations it was never about.
//
// ABSENCE OF --yes MEANS REPORT ONLY, always, on a TTY or not. There is no
// per-item interactive confirmation anywhere here, and no config key may
// pre-answer it. `--yes` applies exactly the plan this invocation printed.
//
// AND THE REPORT SAYS SO OUT LOUD. A plan that reads like an outcome is this
// project's characteristic failure with the polarity reversed: the caller
// believes the work is done and it never was. Every report-only run ends with
// a line stating that nothing was removed and naming the exact command that
// would remove it.

var (
	sessionPurgeYes                   bool
	sessionTranscriptPurgeYes         bool
	sessionTranscriptPurgeUndistilled bool
	sessionArtifactsPurgeYes          bool
)

// --- session transcript purge -----------------------------------------------

var sessionTranscriptPurgeCmd = &cobra.Command{
	Use:   "purge <harp-name>",
	Short: "Destroy a finished session's recorded conversation, keeping its essence",
	Long: `Destroys the machine-written bulk under a harp's directory —
transcript.jsonl and everything under persist/transcripts/ — and nothing
else. The distilled essence, the index entry and every authored file stay.

Without --yes this only reports; nothing on disk or in the session index
changes, on a TTY or not.

A session that was never distilled is REFUSED: with no essence, the
transcript is the only record of what happened. Pass --undistilled to
destroy it anyway.`,
	Args: cobra.ExactArgs(1),
	RunE: runSessionTranscriptPurge,
}

func runSessionTranscriptPurge(cmd *cobra.Command, args []string) error {
	return runHarpFilePurge(cmd, args[0], harpFilePurge{
		populations: []operations.PurgePopulation{operations.PurgePopulationTranscript},
		apply:       sessionTranscriptPurgeYes,
		undistilled: sessionTranscriptPurgeUndistilled,
		commandPath: "ctxloom session transcript purge",
	})
}

// --- session artifacts purge ------------------------------------------------

var sessionArtifactsPurgeCmd = &cobra.Command{
	Use:   "purge <harp-name>",
	Short: "Destroy a finished session's derived essence, keeping its transcript",
	Long: `Destroys what distillation produced — essence.md — and nothing else. The
transcript stays, which is what makes this reversible: while the transcript
is on disk the essence can be produced again with 'ctxloom session distill'.

Without --yes this only reports; nothing on disk or in the session index
changes, on a TTY or not.`,
	Args: cobra.ExactArgs(1),
	RunE: runSessionArtifactsPurge,
}

func runSessionArtifactsPurge(cmd *cobra.Command, args []string) error {
	return runHarpFilePurge(cmd, args[0], harpFilePurge{
		populations: []operations.PurgePopulation{operations.PurgePopulationArtifacts},
		apply:       sessionArtifactsPurgeYes,
		commandPath: "ctxloom session artifacts purge",
	})
}

// --- session purge (the sweep) ----------------------------------------------

var sessionPurgeCmd = &cobra.Command{
	Use:   "purge <harp-name>",
	Short: "Empty a finished session: its transcript, its artifacts and its scratch worktrees",
	Long: `Sweeps all three of a session's destroyable populations at once —
the recorded conversation, the derived essence, and the scratch git
worktrees the session left in its ephemeral directory. Authored files are
never destroyed; they are named in the report instead.

Without --yes this only reports; nothing on disk, in git, or in the session
index changes, on a TTY or not.

The index entry SURVIVES. Purge empties a session, it does not unlist it —
'ctxloom session remove' is what removes a session entirely.

A session that was never distilled is refused, because sweeping it would
destroy the only record of what happened. The refusal names the leaf that
can do it deliberately: 'ctxloom session transcript purge <harp>
--undistilled'.`,
	Args: cobra.ExactArgs(1),
	RunE: runSessionPurge,
}

func init() {
	sessionTranscriptPurgeCmd.Flags().BoolVarP(&sessionTranscriptPurgeYes, "yes", "y", false,
		"apply the plan this invocation printed (default: report only)")
	sessionTranscriptPurgeCmd.Flags().BoolVar(&sessionTranscriptPurgeUndistilled, "undistilled", false,
		"permit destroying the transcript of a session that has no essence")
	sessionTranscriptCmd.AddCommand(sessionTranscriptPurgeCmd)

	sessionArtifactsPurgeCmd.Flags().BoolVarP(&sessionArtifactsPurgeYes, "yes", "y", false,
		"apply the plan this invocation printed (default: report only)")
	sessionArtifactsCmd.AddCommand(sessionArtifactsPurgeCmd)

	sessionPurgeCmd.Flags().BoolVarP(&sessionPurgeYes, "yes", "y", false,
		"apply the plan this invocation printed (default: report only)")
	sessionCmd.AddCommand(sessionPurgeCmd)
}

// sessionSweepReport is `session purge`'s payload: one report per population,
// under one harp. The two halves are kept separate rather than flattened
// because they answer different questions and fail independently — a git
// refusal on a worktree says nothing about whether the transcript went.
type sessionSweepReport struct {
	Harp      string                         `json:"harp"`
	Applied   bool                           `json:"applied"`
	Files     *operations.PurgeSessionResult `json:"files"`
	Worktrees sessionWorktreeReport          `json:"worktrees"`
}

func runSessionPurge(cmd *cobra.Command, args []string) error {
	harp := args[0]
	apply := sessionPurgeYes

	res, purgeErr := operations.PurgeSession(harp, operations.PurgeSessionRequest{
		Harp: harp,
		Populations: []operations.PurgePopulation{
			operations.PurgePopulationTranscript,
			operations.PurgePopulationArtifacts,
		},
		Apply: apply,
	})
	refusal := harpPurgeRefusal(harp, purgeErr, "ctxloom session purge")
	if refusal == "" && purgeErr != nil {
		return purgeErr
	}

	// The worktree sweep runs even when the file purge refused: the two
	// populations are independent, and a caller who asked to empty a session
	// is owed the whole picture rather than the half that happened to come
	// first. Nothing is destroyed on a refusal — apply is false below unless
	// the file half also went through.
	wt, wtErr := sweepHarpWorktrees(cmd.Context(), harp, apply && refusal == "")
	if wtErr != nil {
		return wtErr
	}

	rep := sessionSweepReport{Harp: harp, Applied: apply && refusal == "", Files: res, Worktrees: wt}
	if err := emit(cmd, rep, func() error {
		return renderSessionSweep(cmd.OutOrStdout(), rep)
	}); err != nil {
		return err
	}

	if refusal != "" {
		return reportRefusal(cmd, refusal)
	}
	if !apply {
		return reportPlanOnly(cmd, fmt.Sprintf("ctxloom session purge %s --yes", harp))
	}
	return nil
}

// --- the shared body --------------------------------------------------------

// harpFilePurge is one file-population destroyer's parameters. The three
// leaves differ only in which populations they name and what their --yes
// command line reads as, so they share one body: three near-copies would
// drift the first time the report wording changed under one of them.
type harpFilePurge struct {
	populations []operations.PurgePopulation
	apply       bool
	undistilled bool
	commandPath string
}

func runHarpFilePurge(cmd *cobra.Command, harp string, p harpFilePurge) error {
	res, purgeErr := operations.PurgeSession(harp, operations.PurgeSessionRequest{
		Harp:        harp,
		Populations: p.populations,
		Undistilled: p.undistilled,
		Apply:       p.apply,
	})
	refusal := harpPurgeRefusal(harp, purgeErr, p.commandPath)
	if refusal == "" && purgeErr != nil {
		return purgeErr
	}

	if err := emit(cmd, res, func() error {
		return renderSessionPurgePlan(cmd.OutOrStdout(), res)
	}); err != nil {
		return err
	}

	if refusal != "" {
		return reportRefusal(cmd, refusal)
	}
	if !p.apply {
		return reportPlanOnly(cmd, fmt.Sprintf("%s %s --yes", p.commandPath, harp))
	}
	return nil
}

// harpPurgeRefusal turns a purge error ctxloom has a considered answer for
// into that answer, and returns "" for anything else — which the caller then
// returns unchanged rather than dressing an unknown failure in a friendly
// sentence.
//
// Every refusal ends with what was NOT done, because the whole point of
// refusing is that the caller must not walk away believing it happened.
func harpPurgeRefusal(harp string, err error, commandPath string) string {
	switch {
	case errors.Is(err, operations.ErrPurgeLiveSession):
		return fmt.Sprintf("ctxloom refuses to purge %s: the session is still live (no ended_at yet); nothing was removed", harp)
	case errors.Is(err, operations.ErrPurgeUndistilled):
		return fmt.Sprintf("ctxloom refuses: %s was never distilled — its transcript is the only record of what happened. "+
			"Nothing was removed. To destroy it anyway, deliberately: `ctxloom session transcript purge %s --undistilled --yes`", harp, harp)
	case errors.Is(err, operations.ErrPurgeNothingToDo):
		return fmt.Sprintf("ctxloom removed nothing for %s: no file in this population matched (`%s %s`)", harp, commandPath, harp)
	}
	return ""
}

// reportRefusal writes a refusal to the diagnostic channel and exits refused
// (2) — an action verb that was asked for something and did not do it.
func reportRefusal(cmd *cobra.Command, refusal string) error {
	w := iox.NewErrWriter(cmd.ErrOrStderr())
	w.Println(refusal)
	if werr := w.Err(); werr != nil {
		return werr
	}
	return refusedExit()
}

// reportPlanOnly is the LOUD part of report-by-default. A plan rendered
// without it reads exactly like an outcome: the same table, the same paths,
// the same byte counts. The difference is one sentence, and leaving it to the
// reader to infer from a verb tense is how a caller ends up believing a
// session was emptied when every byte is still there.
func reportPlanOnly(cmd *cobra.Command, applyCommand string) error {
	w := iox.NewErrWriter(cmd.ErrOrStderr())
	w.Printf("ctxloom removed nothing — this was a report, not a removal. To apply exactly this plan:\n  %s\n", applyCommand)
	return w.Err()
}

// sessionPurgeRow is the destroyers' rendering projection, matching
// cli.SessionRow's convention: never the domain type (operations.PurgeItem),
// tagged for clifmt's reflective text table.
type sessionPurgeRow struct {
	Rel    string `json:"rel"    label:"Path"   col:"PATH"`
	Class  string `json:"class"  label:"Class"  col:"CLASS"`
	Action string `json:"action" label:"Action" col:"ACTION"`
	Bytes  int64  `json:"bytes"  label:"Bytes"  col:"BYTES"`
	Reason string `json:"reason,omitempty" label:"Reason" col:"REASON"`
}

// renderSessionPurgePlan is the text-format body for a file-population
// destroyer: a one-line summary (what would be / was destroyed, bytes freed)
// followed by a table naming every file it looked at — destroy rows first,
// then every kept file with its reason. A kept file that is never named is a
// file nobody will ever file, so this never omits the keep rows.
func renderSessionPurgePlan(w io.Writer, res *operations.PurgeSessionResult) error {
	if res == nil {
		return nil
	}
	ew := iox.NewErrWriter(w)
	verb := "would destroy"
	if res.Applied {
		verb = "destroyed"
	}
	ew.Printf("session %s (%s) — %s %d file(s) (%d byte(s) freed)\n",
		res.Harp, populationNames(res.Populations), verb, len(res.Destroy), res.BytesFreed)
	if err := ew.Err(); err != nil {
		return err
	}

	rows := make([]sessionPurgeRow, 0, len(res.Destroy)+len(res.Keep))
	for _, it := range res.Destroy {
		rows = append(rows, sessionPurgeRow{Rel: it.Rel, Class: string(it.Class), Action: it.Action, Bytes: it.Bytes, Reason: it.Reason})
	}
	for _, it := range res.Keep {
		rows = append(rows, sessionPurgeRow{Rel: it.Rel, Class: string(it.Class), Action: it.Action, Bytes: it.Bytes, Reason: it.Reason})
	}
	if len(rows) == 0 {
		ew2 := iox.NewErrWriter(w)
		ew2.Println("(nothing found in this harp's directory)")
		return ew2.Err()
	}
	return clifmt.Render(w, rows, clifmt.FormatText)
}

// renderSessionSweep is `session purge`'s human render: the file half, then
// the worktree half, each labelled. The two are never merged into one table —
// they have different columns and different safety rules, and a single table
// would have to drop whichever ones did not fit.
func renderSessionSweep(w io.Writer, rep sessionSweepReport) error {
	if err := renderSessionPurgePlan(w, rep.Files); err != nil {
		return err
	}
	ew := iox.NewErrWriter(w)
	ew.Println("")
	if err := ew.Err(); err != nil {
		return err
	}
	return renderSessionWorktrees(w, rep.Worktrees)
}

// populationNames renders the populations a report covers, for the summary
// line. A report that does not say which populations it covered cannot be
// told from one that covered them all.
func populationNames(ps []operations.PurgePopulation) string {
	if len(ps) == 0 {
		return "no population"
	}
	names := make([]string, 0, len(ps))
	for _, p := range ps {
		names = append(names, string(p))
	}
	return strings.Join(names, "+")
}
