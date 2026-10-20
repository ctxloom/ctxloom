package cli

import (
	"context"
	"errors"
	"io"

	"github.com/spf13/afero"
	"github.com/spf13/cobra"

	"github.com/ctxloom/ctxloom/internal/adapters/operations"
	"github.com/ctxloom/ctxloom/internal/shared/errwriter"
	"github.com/ctxloom/ctxloom/internal/shared/safefs"
	"github.com/ctxloom/ctxloom/pkg/clifmt"
)

// session adopt re-indexes vendor transcripts that a rotation the index
// never recorded left ORPHANED — chiefly claude-code's /clear before the
// rotation-lineage fix: the index used to clobber the old
// binding on rebind instead of preserving it in Entry.Rotations, so every
// pre-fix /clear left a vendor file on disk with no index row, no rotation
// record, and no path FindBySessionID could ever walk back to. The restore
// that motivated this command was originally done by hand-editing
// the record; this is the command that does it through the store instead.
//
// SAME report-then-apply shape as the session purge family: without --yes
// this only reports, and the report says outright that nothing was applied.

var sessionAdoptYes bool

var sessionAdoptCmd = &cobra.Command{
	Use:   "adopt <session-name>",
	Short: "Re-index orphaned vendor transcripts into a session's rotation lineage",
	Long: `Scans the directory of the session's current transcript for transcripts of
the session's engine that no rotation the index ever recorded left
reachable — chiefly pre-rotation-lineage-fix '/clear's — and reports which
ones belong in this session's lineage.

A candidate already resolvable through the session store (the session's current
binding, an already-recorded rotation, or another session's binding/rotation) is
skipped as already known. Everything else is ordered and judged by its OWN
internal record timestamps — never file mtime, which is routinely rewritten
by tools outside ctxloom's control. A candidate whose span slots into a gap
in the session's existing lineage is ADOPTED; one whose span overlaps an
existing segment (a concurrent, unrelated session) is SKIPPED, never
adopted.

Without --yes this only reports; nothing on disk or in the session index
changes. --yes appends every adopted candidate to the session's Rotations
through the session store, oldest first — never a hand edit of the record —
and then rebuilds the session's canonical transcript from the full lineage,
so the recovered history is what every reader of the session sees.

A purged session is rebuilt even when nothing is adopted: --yes rebuilds its
canonical transcript from whatever engine log outlived the purge, and a
session it rebuilds is no longer marked purged. One with no surviving engine
log stays purged.

A --yes whose rebuild fails exits 1 after saying why: a rebuild that fails
or is refused, a purged session with no engine log left to rebuild from, and
one rebuilt but still marked purged. Adopting nothing into a session that is not
purged, and a report without --yes, exit 0.

The session's engine decides what is a transcript of it, and when each one's
records were written. An engine whose transcript readers cannot say that is
refused, naming the engine, rather than silently scanning nothing.`,
	Example: `  ctxloom session adopt amber-swift-owl        # report the plan
  ctxloom session adopt amber-swift-owl --yes`,
	Args: cobra.ExactArgs(1),
	RunE: runSessionAdopt,
}

func init() {
	sessionAdoptCmd.Flags().BoolVarP(&sessionAdoptYes, yesFlagName, "y", false,
		"apply the plan this invocation printed (default: report only)")
	sessionCmd.AddCommand(sessionAdoptCmd)
}

// sessionAdoptRow is the rendering projection of one operations.AdoptCandidate
// — SpanStart/SpanEnd/RotatedAt are pointers so a candidate this scan never
// got a span for (already resolved via the store lookup) renders as empty
// cells rather than a misleading zero-time.
type sessionAdoptRow struct {
	SessionID       string       `json:"session_id" clifmt:"label=Session,col=SESSION"`
	SpanStart       *sessionTime `json:"span_start,omitempty" clifmt:"label=Span Start,col=SPAN START"`
	SpanEnd         *sessionTime `json:"span_end,omitempty" clifmt:"label=Span End,col=SPAN END"`
	Verdict         string       `json:"verdict" clifmt:"label=Verdict,col=VERDICT"`
	Reason          string       `json:"reason,omitempty" clifmt:"label=Reason,col=REASON"`
	RotatedAt       *sessionTime `json:"rotated_at,omitempty" clifmt:"label=Rotated At,col=ROTATED AT"`
	RotatedAtSource string       `json:"rotated_at_source,omitempty" clifmt:"label=Rotated At Source,col=SOURCE"`
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
// --yes — the SAME shape either way, so a caller diffing the two sees
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
	scan, err := operations.ScanAdoptCandidates(afero.NewOsFs(), App().Engines(), harp)
	if err != nil {
		return err
	}

	rows := make([]sessionAdoptRow, 0, len(scan.Candidates))
	for _, c := range scan.Candidates {
		rows = append(rows, newSessionAdoptRow(c))
	}
	wouldAdopt := countAdoptVerdictRows(rows)

	applied := 0
	if sessionAdoptYes {
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
		Applied:    sessionAdoptYes,
		Adopted:    applied,
		Candidates: rows,
	}
	if err := emit(cmd, result, func() error {
		return renderSessionAdopt(cmd.OutOrStdout(), result, wouldAdopt)
	}); err != nil {
		return err
	}

	if !sessionAdoptYes {
		return reportAdoptPlanOnly(cmd, harp, wouldAdopt, scan.Purged)
	}
	return reportAdoptHeal(cmd, harp, applied, scan.Purged, healAdopted)
}

// healAdopted rebuilds harp's canonical transcript from its whole rotation
// lineage (operations.ResolveAndHeal), the heal every reader of the session
// trusts, and reports whether it rebuilt one. A var so a test can stand in
// for the vendor readers.
var healAdopted = func(ctx context.Context, harp string) (rebuilt bool, err error) {
	src, err := operations.ResolveAndHeal(ctx, safefs.New(), App().Engines(), harp)
	return src.Healed, errors.Join(err, src.HealErr)
}

// What `session adopt --yes` says on the diagnostic channel when it adopted
// nothing: about a session that is not purged, and about a purged one, whose
// transcript it rebuilds anyway.
const (
	adoptNothing                  = "ctxloom adopted nothing for %s: every candidate was already known or overlapped the existing lineage.\n"
	adoptPurgedRebuilt            = "ctxloom adopted nothing for %s, which was purged: rebuilt its canonical transcript from the engine log that survives; it is no longer marked purged.\n"
	adoptPurgedNothingToRebuild   = "ctxloom adopted nothing for %s, which was purged: no engine log survives to rebuild its canonical transcript from; it stays marked purged.\n"
	adoptPurgedNotRebuilt         = "ctxloom adopted nothing for %s, which was purged: its canonical transcript was NOT rebuilt: %v. It stays marked purged.\n"
	adoptPurgedRebuiltStillMarked = "ctxloom adopted nothing for %s, which was purged: rebuilt its canonical transcript from the engine log that survives, but it is STILL marked purged: %v\n"
)

// adoptPlanPurged is the report-only run's note that --yes rebuilds a purged
// session's transcript whatever it adopts.
const adoptPlanPurged = "%s is purged: --yes also rebuilds its canonical transcript from whatever engine log survives.\n"

// renderSessionAdopt is the text-format body: a one-line summary (verb tense
// matching whether this was applied, matching the purge family's convention)
// followed by a table naming every candidate this scan looked at — adopted
// or would-adopt rows AND skip rows with their reason, so a skip is never a
// file nobody will ever be told about.
func renderSessionAdopt(w io.Writer, res sessionAdoptResult, wouldAdopt int) error {
	ew := errwriter.New(w)
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
		ew2 := errwriter.New(w)
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
func reportAdoptPlanOnly(cmd *cobra.Command, harp string, wouldAdopt int, purged bool) error {
	w := errwriter.New(cmd.ErrOrStderr())
	w.Printf("ctxloom adopted nothing — this was a report, not an adoption. %d candidate(s) would be adopted. To apply exactly this plan:\n  ctxloom session adopt %s --%s\n", wouldAdopt, harp, yesFlagName)
	if purged {
		w.Printf(adoptPlanPurged, harp)
	}
	return w.Err()
}

// reportAdoptHeal is the LOUD part of a successful --yes: seeding
// Rotations changes the index, not the canonical transcript a session reader
// actually loads, so the adoption is followed by the heal that rebuilds it
// (heal), and the outcome is said outright. A heal that fails leaves the
// adoption in place — the lineage is recorded — says the transcript was
// not rebuilt, and fails the command (adoptRebuildFailed).
//
// A purged session is rebuilt even when nothing was adopted: its engine log
// may have outlived the purge, and adopt is an explicit act of re-capture
// (reportPurgedRebuild).
func reportAdoptHeal(cmd *cobra.Command, harp string, adopted int, purged bool, heal func(context.Context, string) (bool, error)) error {
	w := errwriter.New(cmd.ErrOrStderr())
	if adopted == 0 {
		if purged {
			return reportPurgedRebuild(cmd, harp, heal)
		}
		w.Printf(adoptNothing, harp)
		return w.Err()
	}
	if _, err := heal(cmd.Context(), harp); err != nil {
		w.Printf("ctxloom adopted %d rotation(s) into %s's lineage, but its canonical transcript was NOT rebuilt from it: %v. The lineage is recorded; the transcript still holds only what it held before.\n",
			adopted, harp, err)
		return adoptRebuildFailed(w)
	}
	w.Printf("ctxloom adopted %d rotation(s) into %s's lineage and rebuilt its canonical transcript from the full lineage.\n", adopted, harp)
	return w.Err()
}

// reportPurgedRebuild rebuilds a purged session that adopted nothing and says
// which way it went: rebuilt (and no longer purged), rebuilt but still
// marked purged, nothing to rebuild from, or failed. Only a rebuild clears
// the purge mark (operations.ResolveAndHeal). Every outcome but a rebuild
// that cleared the mark fails the command (adoptRebuildFailed).
func reportPurgedRebuild(cmd *cobra.Command, harp string, heal func(context.Context, string) (bool, error)) error {
	w := errwriter.New(cmd.ErrOrStderr())
	rebuilt, err := heal(cmd.Context(), harp)
	switch {
	case errors.Is(err, operations.ErrPurgeMarkNotCleared):
		w.Printf(adoptPurgedRebuiltStillMarked, harp, err)
	case err != nil:
		w.Printf(adoptPurgedNotRebuilt, harp, err)
	case rebuilt:
		w.Printf(adoptPurgedRebuilt, harp)
		return w.Err()
	default:
		w.Printf(adoptPurgedNothingToRebuild, harp)
	}
	return adoptRebuildFailed(w)
}

// adoptRebuildFailed ends an adopt whose rebuild failed: the message w carries
// is the whole report, so the command exits 1 and says nothing more. A
// failed write of that message is returned instead.
func adoptRebuildFailed(w *errwriter.Writer) error {
	if err := w.Err(); err != nil {
		return err
	}
	return clifmt.ExitStatus{Code: 1}
}
