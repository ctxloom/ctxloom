package tui

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/ctxloom/ctxloom/internal/core/coord"
	"github.com/ctxloom/ctxloom/internal/shared/agebound"
)

// The agent list shows only rows active within its window (ui.list_window),
// so a project's old sessions do not bury the run's own tree. A row that
// matters now shows whatever its age (alwaysShown), and so does the parent
// of any row that shows: the tree never orphans a row.

// listWindowAll is the ui.list_window word for no window.
const listWindowAll = "all"

// ErrInvalidListWindow refuses a ui.list_window that is neither a duration
// nor "all".
var ErrInvalidListWindow = errors.New(`ui.list_window must be a duration (7d, 2w, 12h, 90m), or 0 or "all" to show every row`)

// ParseListWindow reads ui.list_window: a duration in the age grammar, or
// "0" or "all" for no window (0).
func ParseListWindow(raw string) (time.Duration, error) {
	s := strings.TrimSpace(raw)
	if s == listWindowAll {
		return 0, nil
	}
	d, ok := agebound.Offset(s)
	if !ok || d < 0 {
		return 0, fmt.Errorf("%w: got %q", ErrInvalidListWindow, raw)
	}
	return d, nil
}

// hiddenNote is the panel's line for n rows the window hides.
func hiddenNote(n int) string { return fmt.Sprintf("+%d older · ui.list_window", n) }

// alwaysShown reports whether r shows whatever its age: the run's own
// session, the selection, a running agent, one waiting on an approval or
// holding unread mail.
func alwaysShown(r RosterRow, self, sel string) bool {
	switch {
	case r.Harp == self, r.Harp == sel, r.Pending > 0, r.Unread > 0, r.Hold != nil:
		return true
	}
	switch r.State {
	case StateLive, coord.StateExecuting, coord.StateQueued:
		return true
	}
	return false
}

// inWindow is rows, lineage-ordered, less every row last active before
// since that alwaysShown does not keep and that has no kept descendant; and
// how many rows that left out.
func inWindow(rows []RosterRow, since time.Time, keep func(RosterRow) bool) ([]RosterRow, int) {
	kept := make([]bool, len(rows))
	for i := len(rows) - 1; i >= 0; i-- {
		if kept[i] || keep(rows[i]) || !rows[i].Activity().Before(since) {
			kept[i] = true
			// The nearest row above at a lesser depth is the parent.
			for j := i - 1; j >= 0; j-- {
				if rows[j].Depth < rows[i].Depth {
					kept[j] = true
					break
				}
			}
		}
	}
	out := make([]RosterRow, 0, len(rows))
	for i, r := range rows {
		if kept[i] {
			out = append(out, r)
		}
	}
	return out, len(rows) - len(out)
}
