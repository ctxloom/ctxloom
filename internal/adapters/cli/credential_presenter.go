package cli

import (
	"cmp"
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/ctxloom/ctxloom/internal/adapters/termui"
	"github.com/ctxloom/ctxloom/internal/core/agent"
	"github.com/ctxloom/ctxloom/internal/core/coord"
)

// The root's credential notice: while the coordinator holds runs on their
// credential, the bar says so, names the carrier, and says when the runs
// resume on their own. The finding the coordinator raises is the other half.

const (
	// credentialNoteRefresh re-sets an unchanged notice before it can lapse;
	// it is not re-set every tick, which would clobber every other bar note.
	credentialNoteRefresh = time.Minute
	// credentialNoteFor outlasts a refresh, so the notice never lapses while
	// a hold is in force.
	credentialNoteFor = 2 * credentialNoteRefresh
)

// credentialPresenter keeps the credential notice true on the bar.
type credentialPresenter struct {
	holds   func() []coord.CredentialHold
	noteBar func(text string, d time.Duration)
	clock   termui.Clock
	shown   string
	shownAt time.Time
}

// refresh polls the holds once and sets, re-sets or clears the notice.
func (p *credentialPresenter) refresh() {
	text := credentialNoticeText(p.holds())
	now := p.clock.Now()
	switch {
	case text == p.shown && (text == "" || now.Sub(p.shownAt) < credentialNoteRefresh):
		return
	case text == "":
		p.noteBar("", 0)
	default:
		p.noteBar(text, credentialNoteFor)
	}
	p.shown, p.shownAt = text, now
}

// presentCredentialHolds refreshes the notice on the presenter's tick until
// ctx ends.
func presentCredentialHolds(ctx context.Context, holds func() []coord.CredentialHold, noteBar func(string, time.Duration), clock termui.Clock) {
	p := &credentialPresenter{holds: holds, noteBar: noteBar, clock: clock}
	wake := make(chan struct{}, 1)
	for {
		p.refresh()
		stop := clock.AfterFunc(presenterTick, func() { nudge(wake) })
		select {
		case <-ctx.Done():
			stop()
			return
		case <-wake:
		}
	}
}

// credentialNoticeText is the bar's notice for holds ("" for none): the
// oldest hold in full, and how many more there are. The time is absolute: a
// countdown would change the note every tick.
func credentialNoticeText(holds []coord.CredentialHold) string {
	if len(holds) == 0 {
		return ""
	}
	h := holds[0]
	who, at := cmp.Or(string(h.Engine), "the engine"), h.Until.Local().Format("15:04:05")
	var text string
	if h.Kind == agent.FailureOverloaded {
		// An overload hold is one run's own backoff: no credential is spent.
		text = fmt.Sprintf("OVERLOADED: %s: %s backs off — it resumes on its own at %s", who, strings.Join(h.Harps, ", "), at)
	} else {
		carrier := cmp.Or(strings.Join(h.Source.EnvVars, ", "), strings.Join(h.Source.Stores, ", "))
		text = fmt.Sprintf("RATE LIMITED: %s (%s): %d waiting — they resume on their own at %s", who, carrier, len(h.Harps), at)
	}
	if more := len(holds) - 1; more > 0 {
		text += fmt.Sprintf(" (+%d more)", more)
	}
	return text
}
