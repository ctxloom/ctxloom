package tui

import (
	"github.com/ctxloom/ctxloom/internal/core/coord"
	"github.com/ctxloom/ctxloom/internal/core/frontend"
)

// The three labels an index session can carry when the coordinator has no
// state for it. They are the session lock's three verdicts, one each: there
// is no fourth, and none is a guess. The same words `ctxloom session list`
// prints in its STATE column (the frontend port's session vocabulary), and
// the ended word is the coordinator's own.
const (
	StateLive    = frontend.SessionLive
	StateEnded   = coord.StateEnded
	StateUnknown = frontend.SessionUnknown
)
