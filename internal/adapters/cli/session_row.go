package cli

import (
	"fmt"
	"io"
	"slices"
	"strings"
	"time"

	"github.com/spf13/cobra"

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
// `session search` render: a single-line summary (the session's title, its
// first user prompt), the harp name, the session's state now (from its
// liveness lock), and start/end timestamps. Projected from
// operations.SessionView, the one read model, never from the store's entry.
// The clifmt label/col hints drive clifmt's text/markdown table headers (see
// renderSessionRows).
type SessionRow struct {
	Harp    string `json:"harp" clifmt:"label=Harp,col=HARP"`
	Summary string `json:"summary" clifmt:"label=Summary,col=SUMMARY"`
	// State is SessionView.State: live, ended or unknown, from the session's
	// liveness lock — never from End.
	State string      `json:"state" clifmt:"label=State,col=STATE"`
	Start sessionTime `json:"start" clifmt:"label=Start,col=START"`
	// End is when the session's last run ended, blank while it is live: a
	// resumed session keeps its previous run's stamp, which is not an end
	// of the run in progress.
	End *sessionTime `json:"end,omitempty" clifmt:"label=End,col=END"`
	// Purged mirrors SessionView.Purged: `ctxloom session purge` destroyed
	// this row's machine-written bulk. Structured consumers (a script
	// filtering `--format json`) get a plain boolean; the same fact also
	// rides the Summary badge below so it is visible in TEXT output too — a
	// purged session must never read, in a table a human is actually looking
	// at, as indistinguishable from one that was never purged.
	Purged bool `json:"purged,omitempty" clifmt:"label=Purged,col=PURGED"`
	// Kept mirrors SessionView.Kept (`ctxloom session keep`): a plain
	// boolean for a script, and a badge on Summary for a human, as Purged.
	Kept bool `json:"kept,omitempty" clifmt:"label=Kept,col=KEPT"`
}

// newSessionRow projects the read model down to a SessionRow. Summary is the
// session's title, falling back to a placeholder for a session whose
// transcript holds no user prompt, and carries the purged and kept badges —
// so a badge rides in the row itself and shows up in every format, not just
// text.
func newSessionRow(v operations.SessionView) SessionRow {
	summary := v.Title
	if summary == "" {
		summary = "(no summary)"
	}
	if v.Purged {
		summary += "  🗑 purged (transcript destroyed)"
	}
	if v.EndReason != nil {
		summary += fmt.Sprintf("  ✗ ended before launch: %s (exit %d)", v.EndReason.Class, v.EndReason.Exit)
	}
	if v.Kept {
		summary += "  📌 kept (transcript kept at any age)"
	}
	row := SessionRow{
		Harp:    v.Harp,
		Summary: summary,
		State:   v.State,
		Start:   sessionTime(v.StartedAt),
		Purged:  v.Purged,
		Kept:    v.Kept,
	}
	if v.EndedAt != nil && v.State != operations.SessionLive {
		end := sessionTime(*v.EndedAt)
		row.End = &end
	}
	return row
}

// emitSessionRows is the shared render tail for `session list` and `session
// search`: the SessionRow projection, through emit() in every format.
func emitSessionRows(cmd *cobra.Command, views []operations.SessionView) error {
	rows := make([]SessionRow, len(views))
	for i, v := range views {
		rows[i] = newSessionRow(v)
	}
	return emit(cmd, rows, func() error {
		return renderSessionRows(cmd.OutOrStdout(), rows)
	})
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

// sessionStateOf is the probe `session list` and `session search` ask each
// row's state through: the session's liveness lock. Both commands run in
// their own process, so no listed session's lock is this process's; a test
// swaps the probe to force the states it needs.
var sessionStateOf operations.SessionStateProbe = operations.LockSessionState

// addSessionStateFlag registers --state on a session listing, bound to dst.
func addSessionStateFlag(cmd *cobra.Command, dst *string) {
	cmd.Flags().StringVar(dst, "state", "",
		"Only sessions in this state, from the session's liveness lock: "+strings.Join(operations.SessionStates, ", "))
	_ = cmd.RegisterFlagCompletionFunc("state", func(*cobra.Command, []string, string) ([]string, cobra.ShellCompDirective) {
		return operations.SessionStates, cobra.ShellCompDirectiveNoFileComp
	})
}

// checkSessionState refuses a --state that is not one of the three states;
// "" (no filter) passes.
func checkSessionState(state string) error {
	if state == "" || slices.Contains(operations.SessionStates, state) {
		return nil
	}
	return fmt.Errorf("--state %q: a session's state is one of %s", state, strings.Join(operations.SessionStates, ", "))
}

// filterSessionState keeps the views in state, in order; "" keeps them all.
func filterSessionState(views []operations.SessionView, state string) []operations.SessionView {
	if state == "" {
		return views
	}
	kept := make([]operations.SessionView, 0, len(views))
	for _, v := range views {
		if v.State == state {
			kept = append(kept, v)
		}
	}
	return kept
}
