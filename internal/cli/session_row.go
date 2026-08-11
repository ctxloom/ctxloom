package cli

import (
	"io"
	"time"

	"github.com/ctxloom/ctxloom/internal/operations"
	"github.com/ctxloom/ctxloom/internal/sessions"
	"github.com/ctxloom/ctxloom/internal/shared/iox"
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
// `session query` render by default (CLI-primary reorg plan, decision 13):
// a single-line summary, the harp name, start/end timestamps, and — when the
// session has been distilled — the essence file's path (backend-contract
// V4), but never the essence BODY itself (that stays `session show`'s job,
// or `--full`'s — see session_full.go). Deliberately a separate type from
// sessions.Entry, whose json/yaml wire posture stays untouched for its other
// consumers (the on-disk index, the MCP session tools) — this is a
// rendering-time view only. The label/col tags drive clifmt's text/markdown
// table headers (see renderSessionRows).
type SessionRow struct {
	Harp        string       `json:"harp" label:"Harp" col:"HARP"`
	Summary     string       `json:"summary" label:"Summary" col:"SUMMARY"`
	Start       sessionTime  `json:"start" label:"Start" col:"START"`
	End         *sessionTime `json:"end,omitempty" label:"End" col:"END"`
	EssencePath string       `json:"essence_path,omitempty" label:"Essence Path" col:"ESSENCE PATH"`
	// Purged mirrors sessions.Entry.PurgedAt != nil: `ctxloom session purge`
	// destroyed this row's machine-written bulk. Structured consumers (a
	// script filtering `--format json`) get a plain boolean; the same fact
	// also rides the Summary badge below so it is visible in TEXT output too
	// (the same "shows up in every format" posture SourceStale's "out of
	// date" badge already established here) — a purged session must never
	// read, in a table a human is actually looking at, as indistinguishable
	// from one that was never purged.
	Purged bool `json:"purged,omitempty" label:"Purged" col:"PURGED"`
}

// newSessionRow projects a full index entry down to a SessionRow. Summary
// falls back to a placeholder for a session that was never distilled, and
// carries the same "out of date" badge `session list` has always shown when
// the essence predates the live transcript (sessions.Entry.SourceStale) —
// so the badge that used to live in renderSessionTable's text-only path now
// rides in the row itself and shows up in every format, not just text.
// EssencePath is resolved the same way `session show` resolves it
// (operations.SessionEssenceInfo) and left "" when the
// session isn't distilled yet; appDir is only needed for that legacy
// <sessionsDir>/<sessionID>.md fallback lookup.
func newSessionRow(e sessions.Entry, appDir string) SessionRow {
	summary := e.Summary
	if summary == "" {
		summary = "(no summary)"
	}
	if stale, known := e.SourceStale(); known && stale {
		summary += "  ⚠ out of date"
	}
	if e.PurgedAt != nil {
		summary += "  🗑 purged (transcript destroyed)"
	}
	row := SessionRow{
		Harp:    e.HarpName,
		Summary: summary,
		Start:   sessionTime(e.StartedAt),
		Purged:  e.PurgedAt != nil,
	}
	if e.EndedAt != nil {
		end := sessionTime(*e.EndedAt)
		row.End = &end
	}
	if path, distilled := operations.SessionEssenceInfo(e.HarpName, &e, appDir); distilled {
		row.EssencePath = path
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
		ew := iox.NewErrWriter(w)
		ew.Println("(no sessions)")
		return ew.Err()
	}
	return clifmt.Render(w, rows, clifmt.FormatText)
}
