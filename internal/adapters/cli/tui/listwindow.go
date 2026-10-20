package tui

import (
	"fmt"

	"github.com/ctxloom/ctxloom/internal/core/frontend"
)

// The agent list shows only the rows its two filters pass
// (frontend.ArrangeRoster): the status filter, which f cycles, and the age
// window, ui.list_window to start, which w cycles. A row shown only for its
// place in the tree is drawn dim.

// filterLine is the panel's line for the filters: with every status shown,
// how many rows are older than the window; under a status filter, the
// filter, the window and how many rows the two hide. "" while they show
// every status and hide nothing.
func (s *Split) filterLine() string {
	if s.statusFilter == frontend.StatusAll {
		if s.hiddenRows == 0 {
			return ""
		}
		return fmt.Sprintf("+%d older than %s", s.hiddenRows, frontend.WindowLabel(s.listWindow))
	}
	line := s.statusFilter.String()
	if s.listWindow > 0 {
		line += " · " + frontend.WindowLabel(s.listWindow)
	}
	return line + fmt.Sprintf(" · +%d hidden", s.hiddenRows)
}
