package cli

import (
	"cmp"
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/ctxloom/ctxloom/internal/adapters/cli/tui"
	"github.com/ctxloom/ctxloom/internal/core/agent"
	"github.com/ctxloom/ctxloom/internal/core/coord"
)

// The root's credential notice: while the coordinator holds runs on their
// credential, the panel says so, names the carrier, and says when the runs
// resume on their own — or, for a refused credential, which nothing releases
// on its own, what the human must do, with ONE bell as that hold opens. The
// finding the coordinator raises is the other half.

const (
	// credentialNoteRefresh re-sets an unchanged notice before it can lapse;
	// it is not re-set every tick, which would clobber every other panel note.
	credentialNoteRefresh = time.Minute
	// credentialNoteFor outlasts a refresh, so the notice never lapses while
	// a hold is in force.
	credentialNoteFor = 2 * credentialNoteRefresh
)

// credentialPresenter keeps the credential notice true on the panel.
type credentialPresenter struct {
	holds   func() []coord.CredentialHold
	noteBar func(text string, d time.Duration)
	// ring rings the bell once (tui.SplitView.Ring).
	ring func() bool
	// session is the root harp a refused credential's remedy restarts.
	session string
	clock   tui.Clock
	shown   string
	shownAt time.Time
	// known are the holds seen at the last poll, by holdID; nil until the
	// first poll, which only seeds it — a hold in force then was rebuilt by a
	// restart, which re-raises its finding instead of ringing again.
	known map[string]coord.CredentialHold
}

// holdID tells holds apart across polls: one hold opens at one instant (an
// upgraded hold keeps the instant it opened as a limit), and an endpoint
// failure's (coord.RunScopedFailure) is one run's own.
func holdID(h coord.CredentialHold) string {
	id := h.Source.Key + "@" + h.Since.UTC().Format(time.RFC3339Nano)
	if coord.RunScopedFailure(h.Kind) && len(h.Harps) > 0 {
		id += "/" + h.Harps[0]
	}
	return id
}

// track rings once for a refusal that opened since the last poll (a hold
// upgraded to one included).
func (p *credentialPresenter) track(holds []coord.CredentialHold) {
	now := make(map[string]coord.CredentialHold, len(holds))
	for _, h := range holds {
		now[holdID(h)] = h
	}
	if p.known != nil && p.refusalOpened(now) {
		p.ring()
	}
	p.known = now
}

// refusalOpened reports whether now holds a refused-credential hold the last
// poll did not, or held as another kind.
func (p *credentialPresenter) refusalOpened(now map[string]coord.CredentialHold) bool {
	for id, h := range now {
		if prev, seen := p.known[id]; h.Kind == agent.FailureCredentialRejected && (!seen || prev.Kind != h.Kind) {
			return true
		}
	}
	return false
}

// refresh polls the holds once, rings for what changed (track),
// and sets, re-sets or clears the notice.
func (p *credentialPresenter) refresh() {
	holds := p.holds()
	p.track(holds)
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

// credentialNoticeText is the panel's notice for holds ("" for none) in the
// session whose root harp is session: the oldest hold in full, and how many
// more there are.
func credentialNoticeText(holds []coord.CredentialHold, session string) string {
	if len(holds) == 0 {
		return ""
	}
	text := holdNotice(holds[0], session)
	if more := len(holds) - 1; more > 0 {
		text += fmt.Sprintf(" (+%d more)", more)
	}
	return text
}

// holdNotice is one hold's notice. The time is absolute: a countdown would
// change the note every tick.
func holdNotice(h coord.CredentialHold, session string) string {
	who, at := cmp.Or(string(h.Engine), "the engine"), h.Until.Local().Format("15:04:05")
	switch h.Kind {
	case agent.FailureCredentialRejected:
		return fmt.Sprintf("CREDENTIAL REFUSED: %s (%s): %d parked — %s",
			who, h.Source.Carrier(), len(h.Harps), coord.RefusedCredentialRemedy(h.Source, session))
	case agent.FailureOverloaded:
		// An overload hold is one run's own backoff: no credential is spent.
		return fmt.Sprintf("OVERLOADED: %s: %s backs off — it resumes on its own at %s", who, strings.Join(h.Harps, ", "), at)
	case agent.FailureEndpointUnreachable:
		// Likewise one run's own backoff: the endpoint, not the credential.
		return fmt.Sprintf("ENDPOINT UNREACHABLE: %s: %s backs off — it resumes on its own at %s", who, strings.Join(h.Harps, ", "), at)
	}
	return fmt.Sprintf("RATE LIMITED: %s (%s): %d waiting — they resume on their own at %s", who, h.Source.Carrier(), len(h.Harps), at)
}
