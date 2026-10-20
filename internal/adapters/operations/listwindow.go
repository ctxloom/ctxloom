package operations

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/ctxloom/ctxloom/internal/shared/agebound"
)

// ui.list_window is the agent list's age window (frontend.RosterQuery.Window)
// as configured: every frontend reads it with this one parser.

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
