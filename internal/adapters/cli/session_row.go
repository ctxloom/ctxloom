package cli

import (
	"io"
	"time"

	"github.com/ctxloom/ctxloom/internal/adapters/operations"
	"github.com/ctxloom/ctxloom/internal/shared/errwriter"
	"github.com/ctxloom/ctxloom/pkg/clifmt"
)

// sessionTime wraps time.Time so the SAME field marshals two different ways
// depending on the destination: a standard RFC3339 timestamp for
// json/yaml/toml (delegating to time.Time's own MarshalJSON — clifmt
// round-trips yaml/toml through encoding/json first, so this covers all
// three), but a compact "2006-01-02 15:04:05" line for text/markdown, where
// clifmt's reflective renderer treats any fmt.Stringer field as a scalar and
// calls String() (see clifmt's reflectmodel.go classifyField) rather than
// falling back to time.Time's own verbose default String() form. Without
// this wrapper the text/markdown tables session list/query render would
// show something like "2026-07-17 17:27:32.481 +0000 UTC m=+0.002" per cell.
type sessionTime time.Time

func (t sessionTime) String() string {
	return time.Time(t).Local().Format("2006-01-02 15:04:05")
}

func (t sessionTime) MarshalJSON() ([]byte, error) {
	return time.Time(t).MarshalJSON()
}

// SessionRow is the lightweight per-session projection `session list` and
// `session query` render by default: a single-line summary, the harp name,
// start/end timestamps, and — when the session has been distilled — the
// essence file's path, but never the essence BODY itself (that stays
// `session show`'s job, or `--full`'s — see session_full.go). Projected from
// operations.SessionView, the one read model, never from the store's entry.
// The label/col tags drive clifmt's text/markdown table headers (see
// renderSessionRows).
type SessionRow struct {
	Harp        string       `json:"harp" label:"Harp" col:"HARP"`
	Summary     string       `json:"summary" label:"Summary" col:"SUMMARY"`
	Start       sessionTime  `json:"start" label:"Start" col:"START"`
	End         *sessionTime `json:"end,omitempty" label:"End" col:"END"`
	EssencePath string       `json:"essence_path,omitempty" label:"Essence Path" col:"ESSENCE PATH"`
	// Purged mirrors SessionView.Purged: `ctxloom session purge` destroyed
	// this row's machine-written bulk. Structured consumers (a script
	// filtering `--format json`) get a plain boolean; the same fact also
	// rides the Summary badge below so it is visible in TEXT output too — a
	// purged session must never read, in a table a human is actually looking
	// at, as indistinguishable from one that was never purged.
	Purged bool `json:"purged,omitempty" label:"Purged" col:"PURGED"`
}

// newSessionRow projects the read model down to a SessionRow. Summary falls
// back to a placeholder for a session that was never distilled, and carries
// the "out of date" badge when the essence predates the live transcript
// (SessionView.Stale) — so the badge rides in the row itself and shows up in
// every format, not just text.
func newSessionRow(v operations.SessionView) SessionRow {
	summary := v.Summary
	if summary == "" {
		summary = "(no summary)"
	}
	if v.StaleKnown && v.Stale {
		summary += "  ⚠ out of date"
	}
	if v.Purged {
		summary += "  🗑 purged (transcript destroyed)"
	}
	row := SessionRow{
		Harp:        v.Harp,
		Summary:     summary,
		Start:       sessionTime(v.StartedAt),
		Purged:      v.Purged,
		EssencePath: v.EssencePath,
	}
	if v.EndedAt != nil {
		end := sessionTime(*v.EndedAt)
		row.End = &end
	}
	return row
}

// renderSessionRows is the shared text-format body for `session list` and
// `session query`: a friendly empty state, or clifmt's own reflective table
// (driven by SessionRow's col: tags) for everything else. No bespoke
// tabwriter code needed here — the entire point of decision 7's shared
// output filter is that a command hands over a tagged struct and writes
// almost no rendering code of its own.
func renderSessionRows(w io.Writer, rows []SessionRow) error {
	if len(rows) == 0 {
		ew := errwriter.New(w)
		ew.Println("(no sessions)")
		return ew.Err()
	}
	return clifmt.Render(w, rows, clifmt.FormatText)
}
