package cli

import (
	"io"

	"github.com/spf13/cobra"

	"github.com/ctxloom/ctxloom/internal/operations"
	"github.com/ctxloom/ctxloom/internal/shared/iox"
	"github.com/ctxloom/ctxloom/pkg/clifmt"
)

// session adopt re-indexes vendor transcripts that a rotation the index
// never recorded left ORPHANED — chiefly claude-code's /clear before the
// rotation-lineage fix: the index used to clobber the old
// binding on rebind instead of preserving it in Entry.Rotations, so every
// pre-fix /clear left a vendor file on disk with no index row, no rotation
// record, and no path FindBySessionID could ever walk back to. The restore
// that motivated this command was originally done by hand-editing
// index.yaml; this is the command that does it through the store instead.
//
// SAME report-then-apply shape as the session purge family: without --apply
// this only reports, and the report says outright that nothing was applied.

var sessionAdoptApply bool

var sessionAdoptCmd = &cobra.Command{
	Use:   "adopt <harp-name>",
	Short: "Re-index orphaned vendor transcripts into a harp's rotation lineage",
	Long: `Scans the same claude-code project directory as the harp's current
transcript for vendor .jsonl files no rotation the index ever recorded left
reachable — chiefly pre-rotation-lineage-fix '/clear's — and reports which
ones belong in this harp's lineage.

A candidate already resolvable through the session store (the harp's current
binding, an already-recorded rotation, or another harp's binding/rotation) is
skipped as already known. Everything else is ordered and judged by its OWN
internal record timestamps — never file mtime, which is routinely rewritten
by tools outside ctxloom's control. A candidate whose span slots into a gap
in the harp's existing lineage is ADOPTED; one whose span overlaps an
existing segment (a concurrent, unrelated session) is SKIPPED, never
adopted.

Without --apply this only reports; nothing on disk or in the session index
changes. --apply appends every adopted candidate to the harp's Rotations
through the session store, oldest first — never a hand edit of index.yaml —
and prints the next step (distill or recover) to actually materialize the
recovered history; it does not run that step itself.

Only claude-code harps are supported today; every other backend refuses by
name rather than silently scanning nothing.`,
	Args: cobra.ExactArgs(1),
	RunE: runSessionAdopt,
}

func init() {
	sessionAdoptCmd.Flags().BoolVar(&sessionAdoptApply, "apply", false,
		"apply the plan this invocation printed (default: report only)")
	sessionCmd.AddCommand(sessionAdoptCmd)
}

// sessionAdoptRow is the rendering projection of one operations.AdoptCandidate
// — SpanStart/SpanEnd/RotatedAt are pointers so a candidate this scan never
// got a span for (already resolved via the store lookup) renders as empty
// cells rather than a misleading zero-time.
type sessionAdoptRow struct {
	SessionID       string       `json:"session_id" label:"Session" col:"SESSION"`
	SpanStart       *sessionTime `json:"span_start,omitempty" label:"Span Start" col:"SPAN START"`
	SpanEnd         *sessionTime `json:"span_end,omitempty" label:"Span End" col:"SPAN END"`
	Verdict         string       `json:"verdict" label:"Verdict" col:"VERDICT"`
	Reason          string       `json:"reason,omitempty" label:"Reason" col:"REASON"`
	RotatedAt       *sessionTime `json:"rotated_at,omitempty" label:"Rotated At" col:"ROTATED AT"`
	RotatedAtSource string       `json:"rotated_at_source,omitempty" label:"Rotated At Source" col:"SOURCE"`
}

func newSessionAdoptRow(c operations.AdoptCandidate) sessionAdoptRow {
	row := sessionAdoptRow{
		SessionID: c.SessionID,
		Verdict:   string(c.Verdict),
		Reason:    c.Reason,
	}
	if c.HasSpan {
		start, end := sessionTime(c.SpanStart), sessionTime(c.SpanEnd)
		row.SpanStart, row.SpanEnd = &start, &end
	}
	if c.Verdict == operations.AdoptVerdictAdopt {
		rotatedAt := sessionTime(c.RotatedAt)
		row.RotatedAt = &rotatedAt
		row.RotatedAtSource = c.RotatedAtSource
	}
	return row
}

// countAdoptVerdictRows counts the ADOPT-verdict rows in a rendered set —
// shared by the summary line and the report-only stderr message so "how many
// would be adopted" is computed once and cannot drift between the two.
func countAdoptVerdictRows(rows []sessionAdoptRow) int {
	n := 0
	for _, r := range rows {
		if r.Verdict == string(operations.AdoptVerdictAdopt) {
			n++
		}
	}
	return n
}

// sessionAdoptResult is `session adopt`'s payload for both dry-run and
// --apply — the SAME shape either way, so a caller diffing the two sees
// exactly what changed (Applied flips, Adopted counts up) rather than two
// differently-shaped responses.
type sessionAdoptResult struct {
	Harp       string            `json:"harp"`
	Backend    string            `json:"backend"`
	ScanDir    string            `json:"scan_dir"`
	Applied    bool              `json:"applied"`
	Adopted    int               `json:"adopted"`
	Candidates []sessionAdoptRow `json:"candidates"`
}

func runSessionAdopt(cmd *cobra.Command, args []string) error {
	harp := args[0]
	scan, err := operations.ScanAdoptCandidates(harp)
	if err != nil {
		return err
	}

	rows := make([]sessionAdoptRow, 0, len(scan.Candidates))
	for _, c := range scan.Candidates {
		rows = append(rows, newSessionAdoptRow(c))
	}
	wouldAdopt := countAdoptVerdictRows(rows)

	applied := 0
	if sessionAdoptApply {
		n, aerr := operations.ApplyAdopt(harp, scan.Candidates)
		if aerr != nil {
			return aerr
		}
		applied = n
	}

	result := sessionAdoptResult{
		Harp:       harp,
		Backend:    scan.Backend,
		ScanDir:    scan.ScanDir,
		Applied:    sessionAdoptApply,
		Adopted:    applied,
		Candidates: rows,
	}
	if err := emit(cmd, result, func() error {
		return renderSessionAdopt(cmd.OutOrStdout(), result, wouldAdopt)
	}); err != nil {
		return err
	}

	if !sessionAdoptApply {
		return reportAdoptPlanOnly(cmd, harp, wouldAdopt)
	}
	return reportAdoptNextStep(cmd, harp, applied)
}

// renderSessionAdopt is the text-format body: a one-line summary (verb tense
// matching whether this was applied, matching the purge family's convention)
// followed by a table naming every candidate this scan looked at — adopted
// or would-adopt rows AND skip rows with their reason, so a skip is never a
// file nobody will ever be told about.
func renderSessionAdopt(w io.Writer, res sessionAdoptResult, wouldAdopt int) error {
	ew := iox.NewErrWriter(w)
	verb := "would adopt"
	count := wouldAdopt
	if res.Applied {
		verb = "adopted"
		count = res.Adopted
	}
	ew.Printf("session %s (%s) — %s %d of %d candidate(s)\n", res.Harp, res.ScanDir, verb, count, len(res.Candidates))
	if err := ew.Err(); err != nil {
		return err
	}
	if len(res.Candidates) == 0 {
		ew2 := iox.NewErrWriter(w)
		ew2.Println("(no candidate vendor transcripts found)")
		return ew2.Err()
	}
	return clifmt.Render(w, res.Candidates, clifmt.FormatText)
}

// reportAdoptPlanOnly is adopt's report-by-default backstop, matching the
// purge family's reportPlanOnly: a dry-run table can look exactly like an
// outcome, so the difference between "would adopt" and "adopted" is said
// outright on the diagnostic channel, every run, not left to verb tense in a
// table a caller might only skim.
func reportAdoptPlanOnly(cmd *cobra.Command, harp string, wouldAdopt int) error {
	w := iox.NewErrWriter(cmd.ErrOrStderr())
	w.Printf("ctxloom adopted nothing — this was a report, not an adoption. %d candidate(s) would be adopted. To apply exactly this plan:\n  ctxloom session adopt %s --apply\n", wouldAdopt, harp)
	return w.Err()
}

// reportAdoptNextStep is the LOUD part of a successful --apply: seeding
// Rotations changes the index, not the canonical transcript a session reader
// actually loads — this command deliberately never distills or refreshes
// that on its own (a caller may want to review the lineage first), so it
// names the next step outright rather than leaving a caller to assume
// adoption alone made the recovered history readable.
func reportAdoptNextStep(cmd *cobra.Command, harp string, adopted int) error {
	w := iox.NewErrWriter(cmd.ErrOrStderr())
	if adopted == 0 {
		w.Printf("ctxloom adopted nothing for %s: every candidate was already known or overlapped the existing lineage.\n", harp)
		return w.Err()
	}
	w.Printf("ctxloom adopted %d rotation(s) into %s's lineage. Nothing else changed yet — run `ctxloom session distill %s` next (or, from inside that session, /recover) to rebuild its canonical transcript from the full lineage.\n",
		adopted, harp, harp)
	return w.Err()
}
