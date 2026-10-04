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
// resume on their own — or, for a refused credential, which nothing releases
// on its own, what the human must do, with ONE bell as that hold opens. The
// finding the coordinator raises is the other half.

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
	// ring rings the bell once (termui.Controller.Ring).
	ring func() bool
	// session is the root harp a refused credential's remedy restarts.
	session string
	clock   termui.Clock
	shown   string
	shownAt time.Time
	// refused are the refusal holds already seen, by key and opening; nil until the
	// first poll, which only seeds it — a hold in force then was rebuilt by a
	// restart, which re-raises its finding instead of ringing again.
	refused map[string]bool
}

// ringForNewRefusals rings once if a refused credential's hold appeared since
// the last poll.
func (p *credentialPresenter) ringForNewRefusals(holds []coord.CredentialHold) {
	seen := make(map[string]bool)
	fresh := false
	for _, h := range holds {
		if h.Kind != agent.FailureCredentialRejected {
			continue
		}
		// One credential's hold opens at one instant; an upgraded hold keeps
		// the instant it opened as a limit.
		id := h.Source.Key + "@" + h.Since.UTC().Format(time.RFC3339Nano)
		seen[id] = true
		fresh = fresh || (p.refused != nil && !p.refused[id])
	}
	if fresh {
		p.ring()
	}
	p.refused = seen
}

// refresh polls the holds once, rings for a refusal that just opened, and
// sets, re-sets or clears the notice.
func (p *credentialPresenter) refresh() {
	holds := p.holds()
	p.ringForNewRefusals(holds)
	text := credentialNoticeText(holds, p.session)
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

// presentCredentialHolds refreshes p's notice on the presenter's tick until
// ctx ends.
func presentCredentialHolds(ctx context.Context, p *credentialPresenter) {
	wake := make(chan struct{}, 1)
	for {
		p.refresh()
		stop := p.clock.AfterFunc(presenterTick, func() { nudge(wake) })
		select {
		case <-ctx.Done():
			stop()
			return
		case <-wake:
		}
	}
}

// credentialNoticeText is the bar's notice for holds ("" for none) in the
// session whose root harp is session: the oldest hold in full, and how many
// more there are. The time is absolute: a countdown would change the note
// every tick.
func credentialNoticeText(holds []coord.CredentialHold, session string) string {
	if len(holds) == 0 {
		return ""
	}
	h := holds[0]
	who, at := cmp.Or(string(h.Engine), "the engine"), h.Until.Local().Format("15:04:05")
	var text string
	switch h.Kind {
	case agent.FailureCredentialRejected:
		text = fmt.Sprintf("CREDENTIAL REFUSED: %s (%s): %d parked — %s",
			who, h.Source.Carrier(), len(h.Harps), coord.RefusedCredentialRemedy(h.Source, session))
	case agent.FailureOverloaded:
		// An overload hold is one run's own backoff: no credential is spent.
		text = fmt.Sprintf("OVERLOADED: %s: %s backs off — it resumes on its own at %s", who, strings.Join(h.Harps, ", "), at)
	default:
		text = fmt.Sprintf("RATE LIMITED: %s (%s): %d waiting — they resume on their own at %s", who, h.Source.Carrier(), len(h.Harps), at)
	}
	if more := len(holds) - 1; more > 0 {
		text += fmt.Sprintf(" (+%d more)", more)
	}
	return text
}
