package tui

import (
	"context"
	"encoding/json"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/adapters/termui"
	"github.com/ctxloom/ctxloom/internal/core/agent"
	"github.com/ctxloom/ctxloom/internal/core/coord"
	"github.com/ctxloom/ctxloom/internal/core/engine"
)

// fakeQueue is a coord.ApprovalSource the test drives: it parks what the
// test adds, records every Answer and Revoke, and resolves like the real
// queue — an answered request leaves Pending, and a second answer to it is
// refused.
type fakeQueue struct {
	mu        sync.Mutex
	pending   map[coord.ApprovalID]coord.PendingApproval
	answers   []answerCall
	answerErr map[coord.ApprovalID]error
	grants    map[string][]coord.Grant
	revoked   []string
}

type answerCall struct {
	id coord.ApprovalID
	d  coord.ApprovalDecision
}

func newFakeQueue(ps ...coord.PendingApproval) *fakeQueue {
	q := &fakeQueue{pending: map[coord.ApprovalID]coord.PendingApproval{}, answerErr: map[coord.ApprovalID]error{}, grants: map[string][]coord.Grant{}}
	for _, p := range ps {
		q.pending[p.ID] = p
	}
	return q
}

func (q *fakeQueue) Pending() []coord.PendingApproval {
	q.mu.Lock()
	defer q.mu.Unlock()
	out := make([]coord.PendingApproval, 0, len(q.pending))
	for _, p := range q.pending {
		out = append(out, p)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Deadline.Before(out[j].Deadline) })
	return out
}

// Subscribe hands back a closed stream: the tests deliver every queue event
// themselves (apprHarness.event), so the model's pump ends at once instead
// of blocking the harness that runs its commands.
func (q *fakeQueue) Subscribe(context.Context) <-chan coord.QueueEvent {
	ch := make(chan coord.QueueEvent)
	close(ch)
	return ch
}

func (q *fakeQueue) Answer(id coord.ApprovalID, d coord.ApprovalDecision) error {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.answers = append(q.answers, answerCall{id, d})
	if err := q.answerErr[id]; err != nil {
		return err
	}
	if _, ok := q.pending[id]; !ok {
		return coord.ErrApprovalResolved
	}
	delete(q.pending, id)
	return nil
}

func (q *fakeQueue) Grants(harp string) []coord.Grant {
	q.mu.Lock()
	defer q.mu.Unlock()
	return append([]coord.Grant(nil), q.grants[harp]...)
}

func (q *fakeQueue) Revoke(harp, id string) error {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.revoked = append(q.revoked, harp+"/"+id)
	return nil
}

func (q *fakeQueue) add(p coord.PendingApproval) {
	q.mu.Lock()
	q.pending[p.ID] = p
	q.mu.Unlock()
}

// resolve takes a request out of the queue as the queue would without the
// human (a timeout, a withdrawal).
func (q *fakeQueue) resolve(id coord.ApprovalID) {
	q.mu.Lock()
	delete(q.pending, id)
	q.mu.Unlock()
}

func (q *fakeQueue) calls() []answerCall {
	q.mu.Lock()
	defer q.mu.Unlock()
	return append([]answerCall(nil), q.answers...)
}

// testClock is the view's clock.
type testClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *testClock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *testClock) advance(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

var apprBase = time.Date(2026, 9, 30, 12, 0, 0, 0, time.UTC)

func toolReq(id, harp, tool, input string, left time.Duration) coord.PendingApproval {
	return coord.PendingApproval{
		ID: coord.ApprovalID(id), Kind: coord.ApprovalTool,
		From: coord.Identity{Harp: harp}, Agent: "coder", Lineage: []string{"root", harp},
		Ask:         engine.PermissionAsk{Kind: engine.AskTool, Tool: tool, Input: json.RawMessage(input)},
		Transitions: []string{"default", "acceptEdits"}, Since: apprBase, Deadline: apprBase.Add(left),
	}
}

func bashReq(id, harp, cmd string, left time.Duration) coord.PendingApproval {
	in, _ := json.Marshal(map[string]string{"command": cmd})
	return toolReq(id, harp, "Bash", string(in), left)
}

// apprHarness drives a summoned modal's Model through Update, the way the
// tui tests drive the roster view: hermetic, no terminal.
type apprHarness struct {
	t   *testing.T
	q   *fakeQueue
	clk *testClock
	m   Model
	// quit is set once the model asked to close.
	quit bool
}

func newApprHarness(t *testing.T, q *fakeQueue) *apprHarness {
	t.Helper()
	clk := &testClock{t: apprBase}
	geo := termui.OverlayGeometry{Cols: 110, Rows: 40, PanelRows: 12}
	m := NewModel(context.Background(), Sources{Approvals: q, Now: clk.now}, geo, 0x1d)
	m, _ = m.openApprovals(true)
	// The tests deliver ticks themselves; the model's own tick then fires at
	// once and is dropped, instead of holding the harness for a second.
	m.appr.tickEvery = 0
	return &apprHarness{t: t, q: q, clk: clk, m: m}
}

// send delivers msg and runs whatever command comes back: a quit is noted,
// a decision's result is fed back in, and the standing pump's and tick's
// messages are dropped — the tests deliver those themselves.
func (h *apprHarness) send(msg tea.Msg) {
	h.t.Helper()
	next, cmd := h.m.Update(msg)
	h.m = next.(Model)
	h.run(cmd)
}

func (h *apprHarness) run(cmd tea.Cmd) {
	if cmd == nil {
		return
	}
	switch msg := cmd().(type) {
	case tea.QuitMsg:
		h.quit = true
	case tea.BatchMsg:
		for _, c := range msg {
			h.run(c)
		}
	case answerResultMsg, revokeResultMsg:
		h.send(msg)
	}
}

func (h *apprHarness) keys(ks ...string) {
	h.t.Helper()
	for _, k := range ks {
		h.send(apprKey(k))
	}
}

// event tells the model the queue changed, as its pump would.
func (h *apprHarness) event(kind coord.QueueEventKind, id string, d agent.Decider) {
	h.send(queueEventMsg{ev: coord.QueueEvent{Kind: kind, ID: coord.ApprovalID(id), Decider: d}, ok: true})
}

// settle moves the clock past the re-arm window.
func (h *apprHarness) settle() { h.clk.advance(rearmFor) }

func (h *apprHarness) screen() string { return ansi.Strip(h.m.render()) }

func apprKey(k string) tea.KeyPressMsg {
	switch k {
	case "up":
		return tea.KeyPressMsg{Code: tea.KeyUp}
	case "down":
		return tea.KeyPressMsg{Code: tea.KeyDown}
	case "left":
		return tea.KeyPressMsg{Code: tea.KeyLeft}
	case "right":
		return tea.KeyPressMsg{Code: tea.KeyRight}
	case "pgdown":
		return tea.KeyPressMsg{Code: tea.KeyPgDown}
	}
	return keyMsg(k)
}

// TestApprovals_NothingButNavigateAndEnterDecides is T11: the initial focus
// is Later; no printable key, digit or paste ever produces an Answer — from
// any focus — and Enter on Later hides without deciding.
func TestApprovals_NothingButNavigateAndEnterDecides(t *testing.T) {
	h := newApprHarness(t, newFakeQueue(bashReq("a", "wiry-otter", "rm -rf build/", 10*time.Minute)))
	assert.Contains(t, h.screen(), "[ Later ]", "focus starts on Later")

	for focus := 0; focus < 6; focus++ {
		for r := rune(0x20); r < 0x7f; r++ {
			if strings.ContainsRune("hjkl", r) {
				continue // navigation: never decides, but would move the focus under test
			}
			h.send(keyMsg(string(r)))
		}
		for _, r := range "é日⚑" {
			h.send(keyMsg(string(r)))
		}
		h.send(tea.PasteMsg{Content: "y\r\n1\rs\n"})
		require.Empty(t, h.q.calls(), "a printable key, digit or paste decided something (focus %d)", focus)
		require.False(t, h.quit, "a printable key hid the modal (focus %d)", focus)
		h.keys("right")
	}

	h = newApprHarness(t, newFakeQueue(bashReq("a", "wiry-otter", "ls", 10*time.Minute)))
	h.keys("enter")
	assert.True(t, h.quit, "Enter on Later hides")
	assert.Empty(t, h.q.calls(), "and decides nothing")
}

// TestApprovals_EnterOnAnActionAnswersTheLatchedRequest: navigate + Enter is
// the one way to decide, and it answers the request it was aimed at, once.
func TestApprovals_EnterOnAnActionAnswersTheLatchedRequest(t *testing.T) {
	h := newApprHarness(t, newFakeQueue(bashReq("a", "wiry-otter", "ls", 5*time.Minute), bashReq("b", "calm-heron", "pwd", 9*time.Minute)))
	h.keys("right")
	assert.Contains(t, h.screen(), "[ Allow once ]")
	h.keys("enter")
	require.Len(t, h.q.calls(), 1)
	assert.Equal(t, answerCall{id: "a", d: coord.ApprovalDecision{Allow: true}}, h.q.calls()[0])
	assert.Contains(t, h.screen(), "allowed Bash once for wiry-otter")
}

// TestApprovals_SelectionChangeResetsFocusAndRearms is T12's first case.
func TestApprovals_SelectionChangeResetsFocusAndRearms(t *testing.T) {
	h := newApprHarness(t, newFakeQueue(bashReq("a", "wiry-otter", "ls", 5*time.Minute), bashReq("b", "calm-heron", "rm -rf /", 9*time.Minute)))
	h.send(armedMsg(0)) // termui's own arming is over
	h.keys("right", "down")
	assert.Contains(t, h.screen(), "[ Later ]", "moving the selection put focus back on Later")
	h.keys("right", "enter")
	assert.Empty(t, h.q.calls(), "keys inside the re-arm window are discarded")
	assert.Contains(t, h.screen(), "re-arming")
	h.settle()
	h.keys("enter")
	assert.True(t, h.quit, "after the window, Enter is on Later: it hides")
	assert.Empty(t, h.q.calls())
}

// TestApprovals_ArrivalResetsFocusWithoutMovingTheSelection is T12's second
// case: an Enter aimed at A, typed as B arrives, never lands on anything.
func TestApprovals_ArrivalResetsFocusWithoutMovingTheSelection(t *testing.T) {
	h := newApprHarness(t, newFakeQueue(bashReq("a", "wiry-otter", "ls", 9*time.Minute)))
	h.keys("right")
	h.q.add(bashReq("b", "calm-heron", "rm -rf /", 1*time.Minute)) // sorts FIRST
	h.event(coord.QueueAdded, "b", 0)
	assert.Contains(t, h.screen(), "[ Later ]")
	assert.Equal(t, coord.ApprovalID("a"), h.m.appr.sel, "an arrival never moves the selection")
	h.keys("enter")
	assert.Empty(t, h.q.calls())
	h.settle()
	h.keys("right", "enter")
	require.Len(t, h.q.calls(), 1)
	assert.Equal(t, coord.ApprovalID("a"), h.q.calls()[0].id, "the decision went to the selected request, not the arrival")
}

// TestApprovals_ResolvedUnderTheCursorBecomesATombstone is T12's third case.
func TestApprovals_ResolvedUnderTheCursorBecomesATombstone(t *testing.T) {
	h := newApprHarness(t, newFakeQueue(bashReq("a", "wiry-otter", "ls", 1*time.Minute), bashReq("b", "calm-heron", "rm -rf /", 9*time.Minute)))
	h.keys("right")
	h.q.resolve("a")
	h.event(coord.QueueResolved, "a", agent.DeciderTimeout)
	assert.Equal(t, coord.ApprovalID("a"), h.m.appr.sel, "the selection stays on the resolved request")
	assert.Contains(t, h.screen(), "timed out → denied")
	h.settle()
	h.keys("right", "enter")
	assert.Empty(t, h.q.calls(), "a tombstone offers nothing but Later; nothing slid onto b")
	assert.True(t, h.quit)
}

// TestApprovals_TombstoneExpiresAndTheSelectionMovesOn: after tombstoneFor
// the row leaves; the selection moves, and that is a reset.
func TestApprovals_TombstoneExpiresAndTheSelectionMovesOn(t *testing.T) {
	h := newApprHarness(t, newFakeQueue(bashReq("a", "wiry-otter", "ls", 1*time.Minute), bashReq("b", "calm-heron", "pwd", 9*time.Minute)))
	h.q.resolve("a")
	h.event(coord.QueueResolved, "a", agent.DeciderCancelled)
	assert.Contains(t, h.screen(), "withdrawn (its turn ended) → denied")
	h.clk.advance(tombstoneFor - time.Millisecond)
	h.send(approvalsTickMsg{})
	assert.Contains(t, h.screen(), "withdrawn", "still shown inside its window")
	h.clk.advance(time.Millisecond)
	h.send(approvalsTickMsg{})
	assert.NotContains(t, h.screen(), "withdrawn")
	assert.Equal(t, coord.ApprovalID("b"), h.m.appr.sel)
	assert.True(t, h.clk.now().Before(h.m.appr.inertUntil), "the selection moving re-armed")
}

// TestApprovals_AnAnswerToAResolvedRequestSaysNothingWasApplied: the latch's
// last line — the queue refuses an id that already resolved, and the modal
// says so.
func TestApprovals_AnAnswerToAResolvedRequestSaysNothingWasApplied(t *testing.T) {
	q := newFakeQueue(bashReq("a", "wiry-otter", "ls", 5*time.Minute))
	q.answerErr["a"] = coord.ErrApprovalResolved
	h := newApprHarness(t, q)
	h.keys("right", "enter")
	assert.Contains(t, h.screen(), "it had already resolved — nothing was applied")
}

// TestApprovals_ScopePickerOffersOnlyTheEnginesTransitions is T13's first
// case: only the engine's session rules, and a suggested mode only when the
// engine offers that transition from the asker's posture. The decision
// carries the rule and nothing else — never a destination, never a mode it
// did not show.
func TestApprovals_ScopePickerOffersOnlyTheEnginesTransitions(t *testing.T) {
	p := bashReq("a", "wiry-otter", "git status", 5*time.Minute)
	p.Ask.Suggestions = []string{"Bash(git status:*)"}
	p.Ask.SuggestsSetMode = engine.Provide("bypass")
	p.Transitions = []string{"default", "acceptEdits"}
	assert.Equal(t, []scopeOption{{rule: "Bash(git status:*)"}}, scopeOptions(p), "a mode the engine offers no transition to is not offered")

	p.Ask.SuggestsSetMode = engine.Provide("acceptEdits")
	p.Transitions = []string{"default"}
	assert.Len(t, scopeOptions(p), 1, "a mode outside the transitions is not offered")
	p.Transitions = []string{"default", "acceptEdits"}
	assert.Len(t, scopeOptions(p), 2, "a transition the engine offers is")

	h := newApprHarness(t, newFakeQueue(p))
	h.keys("right", "right", "enter")
	assert.Contains(t, h.screen(), "grants: Bash(git status:*)")
	assert.Contains(t, h.screen(), "never saved to a settings file")
	h.keys("tab", "enter")
	require.Len(t, h.q.calls(), 1)
	assert.Equal(t, coord.ApprovalDecision{Allow: true, SessionRules: []string{"Bash(git status:*)"}}, h.q.calls()[0].d)
}

func planReq(id, harp string, transitions ...string) coord.PendingApproval {
	return coord.PendingApproval{
		ID: coord.ApprovalID(id), Kind: coord.ApprovalPlan, From: coord.Identity{Harp: harp}, Agent: "planner",
		Ask:         engine.PermissionAsk{Kind: engine.AskPlan, Tool: "ExitPlanMode", Plan: &engine.PlanProposal{Markdown: "# Plan\n- step one\n- step two", Path: "/tmp/plan.md"}},
		Transitions: transitions, Since: apprBase, Deadline: apprBase.Add(10 * time.Minute),
	}
}

// TestApprovals_PlanRejectRequiresFeedback is T13's second case.
func TestApprovals_PlanRejectRequiresFeedback(t *testing.T) {
	h := newApprHarness(t, newFakeQueue(planReq("p", "calm-heron", "default", "acceptEdits")))
	assert.Contains(t, h.screen(), "step one", "the plan is shown")
	h.keys("right", "right", "enter")
	h.keys("tab", "enter")
	assert.Empty(t, h.q.calls(), "Reject with no feedback is refused")
	assert.Contains(t, h.screen(), "feedback is required")
	h.send(tea.PasteMsg{Content: "   "})
	h.keys("enter")
	assert.Empty(t, h.q.calls(), "blank feedback is no feedback")
	for _, r := range "split step two" {
		h.send(keyMsg(string(r)))
	}
	h.keys("enter")
	require.Len(t, h.q.calls(), 1)
	assert.Equal(t, coord.ApprovalDecision{Message: "split step two"}, h.q.calls()[0].d)
}

// TestApprovals_PlanApproveOffersTheEnginesTransitions: the posture picker
// lists exactly the engine's transitions, starts on the first, and a move
// puts focus back on Back; none leaves nothing to approve into.
func TestApprovals_PlanApproveOffersTheEnginesTransitions(t *testing.T) {
	none := newApprHarness(t, newFakeQueue(planReq("p", "calm-heron")))
	none.keys("right", "enter")
	assert.Contains(t, none.screen(), "offers no posture to continue in")

	h := newApprHarness(t, newFakeQueue(planReq("p", "calm-heron", "default", "acceptEdits")))
	h.keys("right", "enter", "tab", "down")
	assert.Contains(t, h.screen(), "[ Back ]", "choosing another posture put focus back on Back")
	h.keys("tab", "enter")
	require.Len(t, h.q.calls(), 1)
	assert.Equal(t, coord.ApprovalDecision{Allow: true, SetMode: engine.Provide("acceptEdits")}, h.q.calls()[0].d)
}

func questionReq(id, harp string) coord.PendingApproval {
	return coord.PendingApproval{
		ID: coord.ApprovalID(id), Kind: coord.ApprovalQuestion, From: coord.Identity{Harp: harp},
		Ask: engine.PermissionAsk{Kind: engine.AskQuestion, Tool: "AskUserQuestion", Questions: []engine.Question{
			{Header: "DB", Text: "Which \x1b[31mdatabase\x1b[0m?", Options: []engine.QuestionOption{{Label: "postgres"}, {Label: "sqlite"}}},
			{Text: "Which extras?", MultiSelect: true, Options: []engine.QuestionOption{{Label: "auth"}, {Label: "cache"}}},
		}},
		Since: apprBase, Deadline: apprBase.Add(10 * time.Minute),
	}
}

// TestApprovals_QuestionSubmitIsRefusedUntilComplete is T13's third case,
// and pins that the answer carries the engine's text verbatim — what was
// sanitized is only what was shown.
func TestApprovals_QuestionSubmitIsRefusedUntilComplete(t *testing.T) {
	h := newApprHarness(t, newFakeQueue(questionReq("q", "wiry-otter")))
	h.keys("right", "enter")
	assert.Contains(t, h.screen(), "Which ⟨ESC⟩[31mdatabase⟨ESC⟩[0m?")
	h.keys("down", "space") // q0: sqlite
	h.keys("tab", "enter")
	assert.Empty(t, h.q.calls(), "one question unanswered: Submit is refused")
	assert.Contains(t, h.screen(), "answer every question first")
	h.keys("down", "down", "down", "space") // q1: cache
	h.keys("down")
	for _, r := range "logs" {
		h.send(keyMsg(string(r)))
	}
	h.keys("tab", "enter")
	require.Len(t, h.q.calls(), 1)
	assert.Equal(t, coord.ApprovalDecision{Allow: true, Answers: []engine.QuestionAnswer{
		{Question: "Which \x1b[31mdatabase\x1b[0m?", Labels: []string{"sqlite"}},
		{Question: "Which extras?", Labels: []string{"cache"}, Other: "logs"},
	}}, h.q.calls()[0].d)
}

// TestApprovals_DenyTakesANoteAndAPasteNeverDecides: the note field takes
// keys and pastes — a CR in a paste is text — and only Enter on Deny denies.
func TestApprovals_DenyTakesANoteAndAPasteNeverDecides(t *testing.T) {
	h := newApprHarness(t, newFakeQueue(bashReq("a", "wiry-otter", "rm -rf /", 5*time.Minute)))
	h.keys("right", "right", "right", "enter", "tab")
	h.keys("n", "o")
	h.send(tea.PasteMsg{Content: " — use\r\nthe\rsandbox"})
	assert.Empty(t, h.q.calls(), "typing and pasting with focus on Deny decide nothing")
	h.keys("enter")
	require.Len(t, h.q.calls(), 1)
	assert.Equal(t, coord.ApprovalDecision{Message: "no — use\r\nthe\rsandbox"}, h.q.calls()[0].d, "the whole paste is text, its CRs included")
}

// TestApprovals_DenyAllCoversOnlyWhatWasPendingWhenItOpened.
func TestApprovals_DenyAllCoversOnlyWhatWasPendingWhenItOpened(t *testing.T) {
	q := newFakeQueue(bashReq("a", "wiry-otter", "ls", 5*time.Minute), bashReq("b", "wiry-otter", "pwd", 6*time.Minute), bashReq("c", "calm-heron", "id", 7*time.Minute))
	h := newApprHarness(t, q)
	h.keys("right", "right", "right", "right", "enter")
	assert.Contains(t, h.screen(), "Deny all 2 pending request(s) from wiry-otter?")
	q.add(bashReq("d", "wiry-otter", "whoami", 8*time.Minute))
	h.event(coord.QueueAdded, "d", 0)
	h.settle()
	h.keys("tab", "enter")
	var ids []coord.ApprovalID
	for _, c := range h.q.calls() {
		ids = append(ids, c.id)
		assert.Equal(t, coord.ApprovalDecision{Message: denyAllMessage}, c.d)
	}
	assert.ElementsMatch(t, []coord.ApprovalID{"a", "b"}, ids, "the arrival after the confirmation opened is not denied")
}

// TestApprovals_GrantsListAndRevokeWithConfirm.
func TestApprovals_GrantsListAndRevokeWithConfirm(t *testing.T) {
	q := newFakeQueue(bashReq("a", "wiry-otter", "ls", 5*time.Minute))
	q.grants["wiry-otter"] = []coord.Grant{{ID: "g1", Harp: "wiry-otter", Rule: "Bash(git status:*)", At: apprBase}}
	h := newApprHarness(t, q)
	h.keys("left", "enter")
	assert.Contains(t, h.screen(), "Bash(git status:*)")
	h.keys("tab", "enter")
	assert.Contains(t, h.screen(), "Revoke Bash(git status:*) from wiry-otter?")
	assert.Empty(t, q.revoked, "the first Enter only asks")
	h.keys("tab", "enter")
	assert.Equal(t, []string{"wiry-otter/g1"}, q.revoked)
	assert.Contains(t, h.screen(), "revoked Bash(git status:*) from wiry-otter")
	assert.Empty(t, h.q.calls(), "revoking a grant answers no request")
}

// TestApprovals_TheModalClosesWhenTheLastRequestIsGone.
func TestApprovals_TheModalClosesWhenTheLastRequestIsGone(t *testing.T) {
	h := newApprHarness(t, newFakeQueue(bashReq("a", "wiry-otter", "ls", 5*time.Minute)))
	h.keys("right", "enter")
	assert.True(t, h.quit, "decided here: the modal closes at once")

	h = newApprHarness(t, newFakeQueue(bashReq("a", "wiry-otter", "ls", 5*time.Minute)))
	h.q.resolve("a")
	h.event(coord.QueueResolved, "a", agent.DeciderTimeout)
	assert.False(t, h.quit, "timed out: the tombstone says so first")
	h.clk.advance(tombstoneFor)
	h.send(approvalsTickMsg{})
	assert.True(t, h.quit)
}

// TestApprovals_EscCtrlCAndThePrefixHideWithoutDeciding — even inside a
// re-arm window, since hiding decides nothing.
func TestApprovals_EscCtrlCAndThePrefixHideWithoutDeciding(t *testing.T) {
	for _, k := range []string{"esc", "ctrl+]"} {
		h := newApprHarness(t, newFakeQueue(bashReq("a", "wiry-otter", "ls", 5*time.Minute)))
		h.keys("right", k)
		assert.True(t, h.quit, k)
		assert.Empty(t, h.q.calls(), k)
	}
	h := newApprHarness(t, newFakeQueue(bashReq("a", "wiry-otter", "ls", 5*time.Minute), bashReq("b", "calm-heron", "ls", 6*time.Minute)))
	h.keys("down")
	h.send(tea.KeyPressMsg{Code: 'c', Mod: tea.ModCtrl})
	assert.True(t, h.quit, "ctrl-c hides inside the re-arm window")
}

// TestApprovals_ArmingIsShownUntilArmed: the summoned modal says it is inert
// until termui arms it, then how many keys the window swallowed.
func TestApprovals_ArmingIsShownUntilArmed(t *testing.T) {
	h := newApprHarness(t, newFakeQueue(bashReq("a", "wiry-otter", "ls", 5*time.Minute)))
	assert.Contains(t, h.screen(), "arming — keys are ignored for a moment")
	h.send(armedMsg(3))
	assert.NotContains(t, h.screen(), "arming —")
	assert.Contains(t, h.screen(), "3 key(s) ignored while arming")
}

// TestApprovals_NoticeBannerThenAOpensTheView: a request that arrives while
// the human is in the roster view is a banner naming the asking harp; a
// opens the approvals view. The notice is sanitized like everything else.
func TestApprovals_NoticeBannerThenAOpensTheView(t *testing.T) {
	q := newFakeQueue(bashReq("a", "wiry-otter", "ls", 5*time.Minute))
	m := NewModel(context.Background(), Sources{Approvals: q}, termui.OverlayGeometry{Cols: 100, Rows: 30, PanelRows: 10}, 0x1d)
	next, _ := m.Update(noticeMsg("approval from wiry-otter\x1b[2J"))
	m = next.(Model)
	view := ansi.Strip(m.render())
	assert.Contains(t, view, "⚑ approval from wiry-otter⟨ESC⟩[2J — a to review")
	next, _ = m.Update(keyMsg("a"))
	m = next.(Model)
	assert.True(t, m.approvals)
	assert.True(t, m.full, "a takes the full screen over an engine on the main screen")
	assert.Contains(t, ansi.Strip(m.render()), "ctxloom approval ─ keys here never reach the engine")
	assert.NotContains(t, ansi.Strip(m.render()), "a to review", "the banner is spent once the view is open")
}

func TestApprovals_UnavailableWithoutACoordinator(t *testing.T) {
	m := NewModel(context.Background(), Sources{}, termui.OverlayGeometry{Cols: 100, Rows: 30, PanelRows: 10}, 0x1d)
	next, _ := m.Update(keyMsg("a"))
	m = next.(Model)
	assert.False(t, m.approvals)
	assert.Contains(t, ansi.Strip(m.render()), approvalsUnavailable)
}

// TestApprovals_RenderFitsEverySizeAndNeverCutsAnAction: exactly h lines of
// at most w columns, the bottom rows (actions, hints) kept longest, and the
// action row wrapped rather than cut.
func TestApprovals_RenderFitsEverySizeAndNeverCutsAnAction(t *testing.T) {
	long := strings.Repeat("echo a-very-long-argument ", 40)
	h := newApprHarness(t, newFakeQueue(bashReq("a", "a-rather-long-harp-name", long, 5*time.Minute), bashReq("b", "calm-heron", "ls", 6*time.Minute)))
	for _, size := range [][2]int{{110, 40}, {80, 24}, {40, 12}, {40, 6}, {20, 3}} {
		w, hgt := size[0], size[1]
		lines := h.m.appr.render(w, hgt, apprChrome{})
		require.Len(t, lines, hgt, "%dx%d", w, hgt)
		for _, l := range lines {
			assert.LessOrEqual(t, lipgloss.Width(l), w, "%dx%d: %q", w, hgt, ansi.Strip(l))
		}
	}
	narrow := ansi.Strip(strings.Join(h.m.appr.render(40, 30, apprChrome{}), "\n"))
	for _, label := range []string{"Later", "Allow once", "Allow for session…", "Deny…", "Grants…"} {
		assert.Contains(t, narrow, label, "the action %q was cut at 40 columns", label)
	}
	assert.Contains(t, ansi.Strip(strings.Join(h.m.appr.render(40, 6, apprChrome{}), "\n")), "Later", "a short screen keeps the actions")
}

// TestApprovals_CountdownTurnsAtTwoMinutes.
func TestApprovals_CountdownTurnsAtTwoMinutes(t *testing.T) {
	h := newApprHarness(t, newFakeQueue(bashReq("a", "wiry-otter", "ls", warnLeft+time.Second)))
	assert.Contains(t, h.m.render(), "02:01")
	assert.NotContains(t, h.m.render(), styleWarn.Render("02:01"))
	h.clk.advance(time.Second)
	h.send(approvalsTickMsg{})
	assert.Contains(t, h.m.render(), styleWarn.Render("02:00"))
}

// TestApprovals_ToolDetailByKind: an edit as a diff, an MCP call by its
// server and tool, anything else as its JSON.
func TestApprovals_ToolDetailByKind(t *testing.T) {
	edit := toolReq("e", "wiry-otter", "Edit", `{"file_path":"/w/main.go","old_string":"a := 1\n","new_string":"a := 2\n"}`, 5*time.Minute)
	lines := ansi.Strip(strings.Join(toolDetail(edit.Ask), "\n"))
	assert.Contains(t, lines, "wants to edit /w/main.go:")
	assert.Contains(t, lines, "-a := 1")
	assert.Contains(t, lines, "+a := 2")

	mcp := toolReq("m", "wiry-otter", "mcp__github__create_issue", `{"title":"x"}`, 5*time.Minute)
	assert.Contains(t, strings.Join(toolDetail(mcp.Ask), "\n"), "wants to call MCP github/create_issue:")
	assert.Contains(t, strings.Join(toolDetail(mcp.Ask), "\n"), `"title": "x"`)

	write := toolReq("w", "wiry-otter", "Write", `{"file_path":"/w/x","content":"one\ntwo"}`, 5*time.Minute)
	assert.Contains(t, strings.Join(toolDetail(write.Ask), "\n"), "wants to write /w/x (2 lines):")
}
