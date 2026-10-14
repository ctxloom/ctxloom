package runner

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"slices"
	"strings"
	"time"

	agentcoordpb "github.com/ctxloom/ctxloom/internal/adapters/coordgrpc/pb"
)

// MID-TURN MAIL.
//
// A child's mail is handed to it as a NEW turn (turnPump → sink →
// handOffTurn), and a new turn waits for the turn in flight: a message that
// arrives during one long turn sits unread until that turn ends. The
// mail-peek hook (`ctxloom hook mail-peek`, a PostToolUse hook) closes that
// gap: at each of the engine's tool calls it asks this runner for whatever
// waits, writes it into the engine's context, and confirms.
//
// The runner stays the ONE reader of the spool. The hook never touches it: it
// asks over the session endpoint (MailPath), and what it is handed comes from
// the same pending set the turn pump delivers from. The exchange is two
// steps, and the order is the guarantee:
//
//  1. PeekMail LEASES what waits — what is queued for a turn or waiting
//     inside a hand-off for the turn in flight to end — so a second hook
//     firing concurrently (parallel tool calls) is not handed the same
//     message.
//  2. ConfirmMail, sent only once the hook has WRITTEN the context, is the
//     delivery: the message is consumed and acknowledged (spool.Deliver)
//     exactly as an accepted turn's is.
//
// Anything leased and not confirmed stays queued for normal turn delivery:
// the hand-off that starts its turn takes it (claimTurnMail), and a confirm
// that arrives after that confirms nothing. A message
// confirmed while its hand-off waits is skipped by that hand-off — the
// double delivery this whole seam exists not to cause.

// MailPath is where the session's endpoint serves the mail-peek hook, beside
// HookPath on the same listener and behind the same bearer.
const MailPath = "/mail"

// mailLeaseTTL bounds how long a peek's lease holds messages back from a
// second peek. A hook confirms within its own timeout; a lease past this is
// one whose hook died, and its messages are free again.
const mailLeaseTTL = 30 * time.Second

// MailPeekRequest is the mail-peek hook's request body. Op "fetch" asks for
// what waits, in at most MaxChars of context; op "confirm" reports which of a
// lease's messages the hook wrote.
type MailPeekRequest struct {
	Op       string   `json:"op"`
	MaxChars int      `json:"max_chars,omitempty"`
	Lease    string   `json:"lease,omitempty"`
	IDs      []string `json:"ids,omitempty"`
}

// The mail-peek ops.
const (
	MailOpFetch   = "fetch"
	MailOpConfirm = "confirm"
)

// MailPeek is a fetch's answer: the context to write (empty when nothing
// waits), the lease and message ids it carries, and how many waiting
// messages did not fit.
type MailPeek struct {
	Lease   string   `json:"lease,omitempty"`
	Context string   `json:"context,omitempty"`
	IDs     []string `json:"ids,omitempty"`
	Carried int      `json:"carried,omitempty"`
}

// MailConfirmed is a confirm's answer: how many messages it delivered.
type MailConfirmed struct {
	Confirmed int `json:"confirmed"`
}

// mailLease is one peek's hold on the messages it handed out.
type mailLease struct {
	ids []string
	at  time.Time
}

// midTurnMailLead opens mid-turn mail: the engine is told the mail arrived
// DURING its turn, which is what makes "act on it now" the reading.
const midTurnMailLead = "ctxloom: mail arrived for you during this turn. Read it now and take it into account before you continue:"

// MidTurnMailContext renders mid-turn mail — frames in arrival order — as one
// hook's context of at most maxChars bytes (bytes bound characters from above,
// so the engine's character cap holds): as many frames as fit, in order, and
// a line saying how many more wait. n is how many frames it carries. When not
// even the first fits, the context is that line alone (n 0), so the engine
// still learns that mail waits; when not even that fits, it is empty.
func MidTurnMailContext(frames []string, maxChars int) (text string, n int) {
	if len(frames) == 0 {
		return "", 0
	}
	compose := func(k int) string {
		var b strings.Builder
		if k > 0 {
			b.WriteString(midTurnMailLead)
			for _, f := range frames[:k] {
				b.WriteString("\n\n")
				b.WriteString(f)
			}
		}
		if rest := len(frames) - k; rest > 0 {
			if k > 0 {
				b.WriteString("\n\n")
			}
			fmt.Fprintf(&b, "ctxloom: %d more message(s) wait for you and did not fit here; they arrive at your next tool call or turn boundary.", rest)
		}
		return b.String()
	}
	// The most frames that fit, always a PREFIX: frames are never reordered,
	// so a long message is never overtaken by shorter ones behind it.
	for k := len(frames); k >= 0; k-- {
		if t := compose(k); len(t) <= maxChars {
			return t, k
		}
	}
	return "", 0
}

// PeekMail leases what waits for this run's engine — mail queued for a turn,
// or waiting in a hand-off for the turn in flight — rendered as at most
// maxChars of context (MidTurnMailContext), oldest first. A message a turn
// has already taken, one another live peek holds, and one already delivered
// are not handed out.
func (h *Home) PeekMail(maxChars int) MailPeek {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.ensureMailMapsLocked()
	h.expireMailLeasesLocked(time.Now())
	var waiting []string
	for _, id := range h.pendingOrder {
		if _, ok := h.pendingMail[id]; !ok || h.consumed[id] || h.turnTaken[id] || h.leasedBy[id] != "" {
			continue
		}
		waiting = append(waiting, id)
	}
	if len(waiting) == 0 {
		return MailPeek{}
	}
	frames := make([]string, len(waiting))
	for i, id := range waiting {
		frames[i] = FrameCoordinatorMessage(h.pendingMail[id])
	}
	text, n := MidTurnMailContext(frames, maxChars)
	peek := MailPeek{Context: text, Carried: len(waiting) - n}
	if n == 0 {
		return peek
	}
	peek.Lease = newMailLease()
	peek.IDs = slices.Clone(waiting[:n])
	h.mailLeases[peek.Lease] = mailLease{ids: peek.IDs, at: time.Now()}
	for _, id := range peek.IDs {
		h.leasedBy[id] = peek.Lease
	}
	return peek
}

// ConfirmMail delivers the messages of lease that the hook WROTE: each is
// consumed and acknowledged (ackMailConsumed), exactly as an accepted turn's
// message is, and its waiting hand-off, if any, starts no turn for it. An
// expired or revoked lease, or an id it does not hold, confirms nothing — the
// message went (or goes) to its turn instead. The rest of the lease is
// released. It returns how many it delivered.
func (h *Home) ConfirmMail(lease string, ids []string) int {
	h.mu.Lock()
	l, ok := h.mailLeases[lease]
	if !ok {
		h.mu.Unlock()
		return 0
	}
	delete(h.mailLeases, lease)
	var delivered []string
	for _, id := range ids {
		if h.leasedBy[id] != lease || h.consumed[id] || h.turnTaken[id] {
			continue
		}
		h.consumed[id] = true
		delete(h.pendingMail, id)
		delivered = append(delivered, id)
	}
	for _, id := range l.ids {
		if h.leasedBy[id] == lease {
			delete(h.leasedBy, id)
		}
	}
	h.mu.Unlock()
	h.ackMailConsumed(delivered)
	return len(delivered)
}

// claimTurnMail is the hand-off's last check before a delivered message's
// turn starts, made once the turn in flight has ended: false when a peek
// already delivered the message, so no turn is started for it; otherwise the
// turn takes it, and from then on no peek is handed it and no confirm of an
// earlier lease delivers it (a hook whose turn has ended can no longer have
// written it).
func (h *Home) claimTurnMail(id string) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.ensureMailMapsLocked()
	if h.consumed[id] {
		return false
	}
	h.turnTaken[id] = true
	return true
}

// holdPendingLocked records pm as waiting for delivery, in arrival order.
// Caller holds h.mu.
func (h *Home) holdPendingLocked(pm *agentcoordpb.PeerMessage) {
	id := pm.GetMessageId()
	h.ensureMailMapsLocked()
	if _, ok := h.pendingMail[id]; !ok {
		h.pendingOrder = append(h.pendingOrder, id)
	}
	h.pendingMail[id] = pm
	// The order list keeps ids already delivered until it is worth a pass.
	if len(h.pendingOrder) > 2*len(h.pendingMail)+16 {
		h.pendingOrder = slices.DeleteFunc(h.pendingOrder, func(id string) bool {
			_, ok := h.pendingMail[id]
			return !ok
		})
	}
}

// ensureMailMapsLocked makes the mid-turn mail state usable on a Home built
// without NewHome. Caller holds h.mu.
func (h *Home) ensureMailMapsLocked() {
	if h.pendingMail == nil {
		h.pendingMail = make(map[string]*agentcoordpb.PeerMessage)
	}
	if h.turnTaken == nil {
		h.turnTaken = make(map[string]bool)
	}
	if h.mailLeases == nil {
		h.mailLeases = make(map[string]mailLease)
	}
	if h.leasedBy == nil {
		h.leasedBy = make(map[string]string)
	}
}

// expireMailLeasesLocked frees every lease older than mailLeaseTTL. Caller
// holds h.mu.
func (h *Home) expireMailLeasesLocked(now time.Time) {
	for token, l := range h.mailLeases {
		if now.Sub(l.at) < mailLeaseTTL {
			continue
		}
		delete(h.mailLeases, token)
		for _, id := range l.ids {
			if h.leasedBy[id] == token {
				delete(h.leasedBy, id)
			}
		}
	}
}

// newMailLease mints an unguessable lease token.
func newMailLease() string {
	var b [12]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}
