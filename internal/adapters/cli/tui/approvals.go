package tui

import (
	"errors"
	"fmt"
	"slices"
	"sort"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/ctxloom/ctxloom/internal/core/agent"
	"github.com/ctxloom/ctxloom/internal/core/coord"
)

// The approvals model is the modal's content: the list of parked requests,
// the selected one's detail, and the action row, with the sub-views an action
// opens. It is shared by the summoned modal and the prefix-opened view.
//
// Its rules are the modal's fourth focus lock, "nothing decides by accident":
//   - focus starts on the neutral action (Later; Back in a sub-view), and
//     deciding means moving onto an action and pressing Enter — no letter or
//     digit decides, and a paste only ever types into an open text field;
//   - a selection change, an arrival, or the selected request resolving under
//     the cursor resets focus to the neutral action and makes the model inert
//     for rearmFor, so a key typed for one request cannot land on another;
//   - a resolved request stays as a tombstone row for tombstoneFor instead of
//     letting the selection slide onto a different request;
//   - every action is latched to the request id it was aimed at, and the
//     queue refuses an id that has already resolved.

const (
	// tombstoneFor is how long a request that resolved without the human
	// (timed out, withdrawn) stays in the list, saying so.
	tombstoneFor = 5 * time.Second
	// rearmFor is the inert window after a focus reset: half termui's default
	// arming window. The summoned modal's first arming is termui's (keys are
	// discarded before they reach the overlay); a reset happens inside the
	// overlay, where only the model can hold keys back.
	rearmFor = 375 * time.Millisecond
	// approvalsTickEvery re-renders the countdowns and expires tombstones.
	approvalsTickEvery = time.Second
	// warnLeft is when a countdown changes colour.
	warnLeft = 2 * time.Minute
)

// apprRow is one list row: a parked request, or the tombstone of one.
type apprRow struct {
	p coord.PendingApproval
	// tomb is set once the request resolved without a decision made here: it
	// says how, until tombUntil.
	tomb      string
	tombUntil time.Time
}

func (r apprRow) live() bool { return r.tomb == "" }

// approvalsModel is the approvals view's state. A value, like Model: Update
// returns the next one.
type approvalsModel struct {
	src       coord.ApprovalSource
	now       func() time.Time
	prefixKey string
	events    <-chan coord.QueueEvent
	// tickEvery is the countdown cadence (approvalsTickEvery).
	tickEvery time.Duration

	rows  []apprRow
	sel   coord.ApprovalID
	focus int // index into actions(); 0 is the neutral one
	// latch is the request the focused action was aimed at.
	latch coord.ApprovalID
	sub   *subState
	// inertUntil: keys before it are discarded (counted in inertDiscarded).
	inertUntil     time.Time
	inertDiscarded int
	scroll         int

	// deciders is how the queue said each request resolved, for its tombstone.
	deciders map[coord.ApprovalID]agent.Decider
	// answered holds the requests decided here: they leave without a tombstone.
	answered map[coord.ApprovalID]bool
	// hadRows: the list has held a request, so emptying it closes the view.
	hadRows bool
	// synced: the first read of the queue is done. What that read finds is
	// what the view opened on, not an arrival.
	synced bool

	note, errMsg string
}

func newApprovalsModel(src coord.ApprovalSource, now func() time.Time, prefixKey string) approvalsModel {
	return approvalsModel{
		src:       src,
		now:       now,
		prefixKey: prefixKey,
		tickEvery: approvalsTickEvery,
		deciders:  map[coord.ApprovalID]agent.Decider{},
		answered:  map[coord.ApprovalID]bool{},
	}
}

// Messages.
type queueEventMsg struct {
	ev coord.QueueEvent
	ok bool
}
type approvalsTickMsg struct{}
type answerResultMsg struct {
	ids  []coord.ApprovalID
	what string
	errs []error
}
type revokeResultMsg struct {
	harp, rule string
	err        error
}

// waitQueueCmd receives one queue event; re-armed per event, like the feed.
func waitQueueCmd(ch <-chan coord.QueueEvent) tea.Cmd {
	return func() tea.Msg {
		ev, ok := <-ch
		return queueEventMsg{ev: ev, ok: ok}
	}
}

func (a approvalsModel) tick() tea.Cmd {
	return tea.Tick(a.tickEvery, func(time.Time) tea.Msg { return approvalsTickMsg{} })
}

// initCmds are the view's standing commands: the queue pump and the tick.
func (a approvalsModel) initCmds() tea.Cmd {
	return tea.Batch(waitQueueCmd(a.events), a.tick())
}

// done reports that the list held requests and now holds nothing, not even
// a tombstone: the view has nothing left to show.
func (a approvalsModel) done() bool { return a.hadRows && len(a.rows) == 0 }

// resync re-reads the queue and applies the focus rules to what changed.
func (a *approvalsModel) resync() {
	pending := a.src.Pending()
	now := a.now()
	wasLive := a.selectedLive()
	live := make(map[coord.ApprovalID]coord.PendingApproval, len(pending))
	for _, p := range pending {
		live[p.ID] = p
	}
	known := make(map[coord.ApprovalID]bool, len(a.rows))
	var rows []apprRow
	for _, r := range a.rows {
		known[r.p.ID] = true
		if next, keep := a.carry(r, live, now); keep {
			rows = append(rows, next)
		}
	}
	arrived := false
	for _, p := range pending {
		if !known[p.ID] {
			rows = append(rows, apprRow{p: p})
			arrived = a.synced
		}
	}
	a.synced = true
	sort.SliceStable(rows, func(i, j int) bool { return rows[i].p.Deadline.Before(rows[j].p.Deadline) })
	a.rows = rows
	a.hadRows = a.hadRows || len(rows) > 0
	a.afterSync(arrived, wasLive)
}

// carry is a known row's next state: refreshed while pending, a tombstone
// once resolved without a decision made here, gone when that expires.
func (a *approvalsModel) carry(r apprRow, live map[coord.ApprovalID]coord.PendingApproval, now time.Time) (apprRow, bool) {
	if p, ok := live[r.p.ID]; ok {
		r.p = p
		return r, true
	}
	if a.answered[r.p.ID] {
		return r, false
	}
	if r.live() {
		r.tomb = tombText(a.deciders[r.p.ID], a.deciderKnown(r.p.ID))
		r.tombUntil = now.Add(tombstoneFor)
	}
	return r, now.Before(r.tombUntil)
}

func (a *approvalsModel) deciderKnown(id coord.ApprovalID) bool {
	_, ok := a.deciders[id]
	return ok
}

// tombText says how a request resolved without the human deciding it here.
func tombText(d agent.Decider, known bool) string {
	switch {
	case !known:
		return "resolved"
	case d == agent.DeciderTimeout:
		return "timed out → denied"
	case d == agent.DeciderCancelled:
		return "withdrawn (its turn ended) → denied"
	case d == agent.DeciderGrant:
		return "allowed by a session grant"
	case d == agent.DeciderHuman:
		return "decided"
	}
	return "resolved (" + d.String() + ")"
}

// afterSync applies the focus rules: an arrival, a resolution under the
// cursor, or a selection that had to move all reset focus and re-arm; a
// sub-view aimed at a request that is gone closes.
func (a *approvalsModel) afterSync(arrived, wasLive bool) {
	row, found := a.find(a.sel)
	reset := arrived
	switch {
	case a.sel == "":
		a.selectFirst()
	case !found:
		a.selectFirst()
		reset = true
	case wasLive && !row.live():
		reset = true
	}
	if a.sub != nil && !a.isLive(a.sub.target) && a.sub.kind.aimed() {
		a.sub = nil
		reset = true
	}
	if reset {
		a.resetFocus()
	}
}

func (a *approvalsModel) selectFirst() {
	a.sel = ""
	for _, r := range a.rows {
		if r.live() {
			a.sel = r.p.ID
			return
		}
	}
	if len(a.rows) > 0 {
		a.sel = a.rows[0].p.ID
	}
}

// resetFocus puts focus back on the neutral action and re-arms.
func (a *approvalsModel) resetFocus() {
	a.focus = 0
	a.latch = ""
	a.scroll = 0
	if a.sub != nil {
		a.sub.focus = 0
	}
	a.inertUntil = a.now().Add(rearmFor)
}

func (a approvalsModel) find(id coord.ApprovalID) (apprRow, bool) {
	i := slices.IndexFunc(a.rows, func(r apprRow) bool { return r.p.ID == id })
	if i < 0 {
		return apprRow{}, false
	}
	return a.rows[i], true
}

func (a approvalsModel) isLive(id coord.ApprovalID) bool {
	r, ok := a.find(id)
	return ok && r.live()
}

func (a approvalsModel) selectedLive() bool { return a.isLive(a.sel) }

func (a approvalsModel) selectedIndex() int {
	return slices.IndexFunc(a.rows, func(r apprRow) bool { return r.p.ID == a.sel })
}

func (a approvalsModel) liveCount() int {
	n := 0
	for _, r := range a.rows {
		if r.live() {
			n++
		}
	}
	return n
}

// applyQueueEvent records how a request resolved and re-syncs. The event is
// a hint to re-read, never the state itself: a subscriber that fell behind
// lost events, and Pending is the truth.
func (a approvalsModel) applyQueueEvent(msg queueEventMsg) (approvalsModel, tea.Cmd) {
	if !msg.ok {
		return a, nil
	}
	if msg.ev.Kind == coord.QueueResolved {
		a.deciders[msg.ev.ID] = msg.ev.Decider
	}
	a.resync()
	return a, waitQueueCmd(a.events)
}

func (a approvalsModel) applyTick() (approvalsModel, tea.Cmd) {
	a.resync()
	return a, a.tick()
}

// applyAnswerResult lands a decision's outcome. A request that resolved
// before the answer reached it is said so: nothing was applied to it.
func (a approvalsModel) applyAnswerResult(msg answerResultMsg) approvalsModel {
	var failed []string
	for i, err := range msg.errs {
		if err == nil {
			a.answered[msg.ids[i]] = true
			continue
		}
		if errors.Is(err, coord.ErrApprovalResolved) || errors.Is(err, coord.ErrNoSuchApproval) {
			failed = append(failed, "it had already resolved — nothing was applied")
			continue
		}
		failed = append(failed, err.Error())
	}
	if len(failed) > 0 {
		a.note, a.errMsg = "", msg.what+": "+failed[0]
	} else {
		a.note, a.errMsg = msg.what, ""
	}
	a.resync()
	return a
}

func (a approvalsModel) applyRevokeResult(msg revokeResultMsg) approvalsModel {
	if msg.err != nil {
		a.note, a.errMsg = "", fmt.Sprintf("revoke %s from %s: %v", sanitizeForDisplay(msg.rule), msg.harp, msg.err)
		return a
	}
	a.note, a.errMsg = fmt.Sprintf("revoked %s from %s (applies from its next turn)", sanitizeForDisplay(msg.rule), msg.harp), ""
	if a.sub != nil {
		a.sub = openGrants(a.src, msg.harp)
	}
	return a
}

// answerCmd answers each id off the update loop, latched to the ids given.
func answerCmd(src coord.ApprovalSource, what string, ids []coord.ApprovalID, d coord.ApprovalDecision) tea.Cmd {
	return func() tea.Msg {
		errs := make([]error, len(ids))
		for i, id := range ids {
			errs[i] = src.Answer(id, d)
		}
		return answerResultMsg{ids: ids, what: what, errs: errs}
	}
}

// key handles one key press; hide reports that the human dismissed the view
// (Later, Esc, ctrl-c, the prefix) — which never decides anything.
func (a approvalsModel) key(msg tea.KeyPressMsg) (next approvalsModel, cmd tea.Cmd, hide bool) {
	k := msg.String()
	if k == "ctrl+c" || k == a.prefixKey {
		return a, nil, true
	}
	if a.now().Before(a.inertUntil) {
		a.inertDiscarded++
		return a, nil, false
	}
	if a.sub != nil {
		a, cmd = a.subKey(msg)
		return a, cmd, false
	}
	return a.mainKey(k)
}

// mainNav are the main view's keys that move without deciding. Printable
// ones are navigation only: no letter or digit decides anything.
var mainNav = map[string]func(*approvalsModel){
	"up":        func(a *approvalsModel) { a.move(-1) },
	"k":         func(a *approvalsModel) { a.move(-1) },
	"down":      func(a *approvalsModel) { a.move(1) },
	"j":         func(a *approvalsModel) { a.move(1) },
	"left":      func(a *approvalsModel) { a.shiftFocus(-1) },
	"h":         func(a *approvalsModel) { a.shiftFocus(-1) },
	"shift+tab": func(a *approvalsModel) { a.shiftFocus(-1) },
	"right":     func(a *approvalsModel) { a.shiftFocus(1) },
	"l":         func(a *approvalsModel) { a.shiftFocus(1) },
	"tab":       func(a *approvalsModel) { a.shiftFocus(1) },
	"pgup":      func(a *approvalsModel) { a.scroll = max(a.scroll-detailPage, 0) },
	"pgdown":    func(a *approvalsModel) { a.scroll += detailPage },
}

// detailPage is how far PgUp/PgDn scroll the detail pane.
const detailPage = 10

func (a approvalsModel) mainKey(k string) (approvalsModel, tea.Cmd, bool) {
	if f, ok := mainNav[k]; ok {
		f(&a)
		return a, nil, false
	}
	switch k {
	case "esc":
		return a, nil, true
	case "enter":
		return a.activate()
	}
	return a, nil, false
}

// move changes the selection; a change resets focus and re-arms.
func (a *approvalsModel) move(d int) {
	i := a.selectedIndex()
	j := min(max(i+d, 0), len(a.rows)-1)
	if j < 0 || j == i {
		return
	}
	a.sel = a.rows[j].p.ID
	a.resetFocus()
}

// shiftFocus moves along the action row. Leaving the neutral action latches
// the selected request; returning to it releases the latch.
func (a *approvalsModel) shiftFocus(d int) {
	n := len(a.actions())
	a.focus = (a.focus + d + n) % n
	if a.focus == 0 {
		a.latch = ""
	} else if a.latch == "" {
		a.latch = a.sel
	}
}

// activate runs the focused action on the latched request. The neutral
// action hides; anything else re-checks the latch and the request's
// liveness first, so an Enter aimed at one request never lands on another.
func (a approvalsModel) activate() (approvalsModel, tea.Cmd, bool) {
	if a.focus == 0 {
		return a, nil, true
	}
	row, ok := a.find(a.latch)
	if !ok || !row.live() || a.latch != a.sel {
		a.note, a.errMsg = "", "that request is no longer pending — nothing was applied"
		a.resetFocus()
		return a, nil, false
	}
	act := a.actions()[a.focus]
	a, cmd := act.run(a, row.p)
	return a, cmd, false
}

// paste types a paste into an open text field. It never decides: a CR or
// newline inside it is text.
func (a approvalsModel) paste(content string) approvalsModel {
	if a.sub != nil && a.now().Before(a.inertUntil) {
		a.inertDiscarded++
		return a
	}
	if a.sub != nil {
		a.sub.typeText(content)
	}
	return a
}
