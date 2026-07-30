package coord

import (
	"context"
	"regexp"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	agentcoordpb "github.com/ctxloom/ctxloom/internal/agentcoord"
	"github.com/ctxloom/ctxloom/internal/shared/agent"
)

// The turn-boundary re-announcer (F10).
//
// The defect these tests exist for is a SILENT LOSS: a steer is acknowledged,
// its body is parked, the engine takes the announcement turn without calling
// agent_recv, and the human's instruction sits in the buffer until the process
// dies. Every signal in the system reads healthy. A happy-path suite — steer,
// pull, assert the body — passes over it without noticing, which is why the
// cases below are written around the turn NOBODY pulls in.

// steppedChat is a StructuredChat the test drives one turn at a time: it
// reports each turn's text on `got` and does not produce the turn's entries (so
// does not reach a boundary) until the test calls finish.
//
// The shared scriptedChat completes every turn the moment it arrives, which
// makes boundaries race the assertions. The re-announcer fires ON boundaries,
// so the fixture has to own them.
type steppedChat struct {
	mu    sync.Mutex
	texts []string
	got   chan string
	end   chan struct{}
}

func newSteppedChat() *steppedChat {
	return &steppedChat{got: make(chan string, 64), end: make(chan struct{}, 64)}
}

func (s *steppedChat) Chat(ctx context.Context, req agent.ChatRequest, in <-chan agent.ChatMessage, out chan<- agent.ChatEvent) error {
	defer close(out)
	send := func(ev agent.ChatEvent) bool {
		select {
		case out <- ev:
			return true
		case <-ctx.Done():
			return false
		}
	}
	if !send(agent.ChatEvent{Session: &agent.ChatSessionInfo{Model: req.Model, SessionID: "native-sess-42"}}) {
		return ctx.Err()
	}
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case msg, ok := <-in:
			if !ok {
				return nil
			}
			if msg.Text == "" {
				continue
			}
			s.mu.Lock()
			s.texts = append(s.texts, msg.Text)
			s.mu.Unlock()
			select {
			case s.got <- msg.Text:
			case <-ctx.Done():
				return ctx.Err()
			}
			// The turn is now RUNNING and has produced nothing: the engine has
			// been handed the text and has not reached a boundary. finish ends
			// it.
			select {
			case <-s.end:
			case <-ctx.Done():
				return ctx.Err()
			}
			if !send(agent.ChatEvent{Entry: &agent.SessionEntry{Type: agent.EntryTypeAssistant, Content: "ack"}}) ||
				!send(agent.ChatEvent{Complete: &agent.TurnMeta{StopReason: "end_turn"}}) {
				return ctx.Err()
			}
		}
	}
}

// finish ends the turn currently in progress, producing the boundary the
// re-announcer triggers on.
func (s *steppedChat) finish() { s.end <- struct{}{} }

// nextTurn waits for the engine to be handed one more turn's text.
func (s *steppedChat) nextTurn(t *testing.T) string {
	t.Helper()
	select {
	case txt := <-s.got:
		return txt
	case <-time.After(5 * time.Second):
		t.Fatalf("no turn was handed to the engine within the budget; texts so far: %v", s.recorded())
		return ""
	}
}

// noMoreTurns asserts the engine is handed nothing further — the assertion that
// makes "re-announced ZERO times" and "the bound really terminates" mean
// something rather than merely being un-asserted.
func (s *steppedChat) noMoreTurns(t *testing.T, within time.Duration) {
	t.Helper()
	select {
	case txt := <-s.got:
		t.Fatalf("an unexpected turn was injected: %q (all texts: %v)", txt, s.recorded())
	case <-time.After(within):
	}
}

func (s *steppedChat) recorded() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.texts...)
}

var (
	reannounceSecsRe    = regexp.MustCompile(`waiting_seconds="(\d+)"`)
	reannounceUrgencyRe = regexp.MustCompile(`urgency="([A-Z_]+)"`)
)

// parseReannouncement pulls the two anti-habituation attributes out of an
// injected frame. It parses rather than compares against a rebuilt expected
// string because the age is real elapsed time: pinning it exactly would make
// the assertion a stopwatch, and this is the load-sensitive package.
func parseReannouncement(t *testing.T, frame string) (uint32, string) {
	t.Helper()
	secs := reannounceSecsRe.FindStringSubmatch(frame)
	urg := reannounceUrgencyRe.FindStringSubmatch(frame)
	require.Len(t, secs, 2, "not a re-announcement frame: %q", frame)
	require.Len(t, urg, 2, "not a re-announcement frame: %q", frame)
	n, err := strconv.ParseUint(secs[1], 10, 32)
	require.NoError(t, err)
	return uint32(n), urg[1]
}

// startSteppedRun boots an engine host over a test-driven chat and consumes the
// briefing turn, leaving the engine idle with an empty tag FIFO.
func startSteppedRun(t *testing.T) (*EngineHost, *fakeEngineHome, *steppedChat) {
	t.Helper()
	home := &fakeEngineHome{}
	sc := newSteppedChat()
	eh := NewEngineHost(context.Background(), sc, "claude-code", "run-1")
	t.Cleanup(eh.Close)
	eh.BindHome(home)
	require.Equal(t, int32(0), eh.Handle(&agentcoordpb.RunnerRequest{
		Kind: &agentcoordpb.RunnerRequest_StartRun{StartRun: testStartRun("run-1")},
	}).GetStatus().GetCode())

	require.Equal(t, "CTX\n\ndo the thing", sc.nextTurn(t))
	sc.finish()
	return eh, home, sc
}

// steerAndAwaitAnnouncement issues a steer and consumes its ONE announcement
// turn, leaving that turn running and unpulled.
func steerAndAwaitAnnouncement(t *testing.T, home *fakeEngineHome, sc *steppedChat, reqID string) {
	t.Helper()
	resp := home.control(context.Background(), steerRequest(reqID, "prefer sqlx", CapSteer))
	require.Equal(t, int32(0), resp.GetStatus().GetCode(), resp.GetStatus().GetMessage())
	require.Equal(t, (&agentcoordpb.SteerPendingReminder{}).XmlLike(), sc.nextTurn(t),
		"the control verb's own announcement must be the next turn")
}

// TestReannounce_TurnThatNeverPulledGetsAnotherAnnouncement is the defect
// itself. The engine is handed the announcement, takes the turn, and ends it
// without calling agent_recv. Before this trigger existed, that was the end of
// the story: the instruction stayed parked, no error, nothing observable, and
// the human's steer was gone.
func TestReannounce_TurnThatNeverPulledGetsAnotherAnnouncement(t *testing.T) {
	_, home, sc := startSteppedRun(t)
	steerAndAwaitAnnouncement(t, home, sc, "ctl-lost")

	// The turn ends. The body is still sitting in the buffer, unpulled.
	require.Len(t, home.PendingControlPayloads(), 1)
	sc.finish()

	secs, urgency := parseReannouncement(t, sc.nextTurn(t))
	assert.Equal(t, agentcoordpb.PendingPullReminder_URGENCY_REPEAT.String(), urgency,
		"the first re-announcement is the bottom rung")
	assert.GreaterOrEqual(t, secs, uint32(0))
}

// TestReannounce_ConsumedBodyIsNeverReannounced is the no-double-delivery half.
// An agent that DID pull must not be told again: a second announcement for an
// instruction already in hand is the double-delivery the one-object collapse
// exists to remove, and it would read to the model as a second instruction.
func TestReannounce_ConsumedBodyIsNeverReannounced(t *testing.T) {
	eh, home, sc := startSteppedRun(t)
	steerAndAwaitAnnouncement(t, home, sc, "ctl-pulled")

	// The well-behaved agent pulls the body during the announcement turn.
	home.pullControlPayload(steerBodyID("ctl-pulled"))
	sc.finish()

	// A sentinel proves the turn stream is LIVE — otherwise "no re-announcement"
	// would also be satisfied by an engine that stopped taking turns at all,
	// which is the vacuous pass this whole family of tests is prone to.
	require.NoError(t, eh.enqueueTurn(context.Background(), turnTag{}, "sentinel"))
	assert.Equal(t, "sentinel", sc.nextTurn(t), "a re-announcement jumped ahead of the sentinel")
	sc.finish()
	sc.noMoreTurns(t, 300*time.Millisecond)
	assert.Empty(t, home.customValues(CustomControlUnpulled), "nothing was lost, so nothing may be reported lost")
}

// TestReannounce_EscalatesWithTheOldestPendingAge pins anti-habituation. A
// notice repeated verbatim is a notice a model learns to skip, so successive
// frames must differ — on the continuous channel (how long the instruction has
// been waiting) and on the closed one (which rung of the escalation).
func TestReannounce_EscalatesWithTheOldestPendingAge(t *testing.T) {
	_, home, sc := startSteppedRun(t)
	steerAndAwaitAnnouncement(t, home, sc, "ctl-aging")

	// Age the parked body without waiting on a wall clock.
	home.backdateControlPayloads(90 * time.Second)
	sc.finish()
	first := sc.nextTurn(t)
	secs1, urg1 := parseReannouncement(t, first)
	assert.GreaterOrEqual(t, secs1, uint32(90), "the frame must report how long the instruction has waited")
	assert.Equal(t, agentcoordpb.PendingPullReminder_URGENCY_REPEAT.String(), urg1)

	home.backdateControlPayloads(60 * time.Second)
	sc.finish()
	second := sc.nextTurn(t)
	secs2, urg2 := parseReannouncement(t, second)
	assert.GreaterOrEqual(t, secs2, uint32(150), "the age must track the OLDEST pending item, not reset")
	assert.Equal(t, agentcoordpb.PendingPullReminder_URGENCY_URGENT.String(), urg2)

	sc.finish()
	third := sc.nextTurn(t)
	_, urg3 := parseReannouncement(t, third)
	assert.Equal(t, agentcoordpb.PendingPullReminder_URGENCY_FINAL.String(), urg3,
		"the last frame must SAY it is the last one")

	// The property that matters is not the individual values: it is that no two
	// consecutive notices read the same.
	assert.NotEqual(t, first, second)
	assert.NotEqual(t, second, third)
}

// TestReannounce_BoundTerminatesObservably pins the other half of the bound. An
// unbounded re-announcer would occupy every turn of the run forever, so it
// stops — but a QUIET stop is the same silent loss in a different coat, so the
// give-up has to be visible on the event plane.
func TestReannounce_BoundTerminatesObservably(t *testing.T) {
	_, home, sc := startSteppedRun(t)
	steerAndAwaitAnnouncement(t, home, sc, "ctl-abandoned")
	home.backdateControlPayloads(120 * time.Second)

	for i := 0; i < reannounceLimit; i++ {
		sc.finish()
		_, urgency := parseReannouncement(t, sc.nextTurn(t))
		if i == reannounceLimit-1 {
			assert.Equal(t, agentcoordpb.PendingPullReminder_URGENCY_FINAL.String(), urgency)
		}
	}

	// One more boundary past the limit: the re-announcer gives up.
	sc.finish()
	require.Eventually(t, func() bool { return len(home.customValues(CustomControlUnpulled)) == 1 },
		5*time.Second, 10*time.Millisecond,
		"giving up SILENTLY is the defect this trigger closes; the give-up must be reported")

	v := home.customValues(CustomControlUnpulled)[0]
	assert.Equal(t, steerBodyID("ctl-abandoned"), v["message_id"])
	assert.Equal(t, reannounceLimit+1, v["announcements"], "the original announcement counts too")
	assert.GreaterOrEqual(t, v["waiting_seconds"], 120)

	// And it really stops: the run keeps its remaining turns.
	sc.noMoreTurns(t, 300*time.Millisecond)

	// Giving up is per BODY, not per run: it is reported once, not once per
	// boundary for the rest of the run.
	sc.finish()
	sc.noMoreTurns(t, 300*time.Millisecond)
	assert.Len(t, home.customValues(CustomControlUnpulled), 1)
}

// TestPlanReannounce_HoldsOffWhileATurnIsAlreadyQueued pins the hold-off rule.
// A non-empty tag FIFO means a locally-originated turn is enqueued and the
// engine has not started it — typically the body's OWN announcement, queued
// while the engine was mid-turn. Re-announcing then would nag about something
// the agent has not been told, and stack turns nobody asked for.
func TestPlanReannounce_HoldsOffWhileATurnIsAlreadyQueued(t *testing.T) {
	eh := NewEngineHost(context.Background(), &scriptedChat{}, "claude-code", "run-1")
	t.Cleanup(eh.Close)
	eh.mu.Lock()
	eh.runCtx = context.Background()
	eh.pendingTags = []turnTag{{}}
	eh.mu.Unlock()

	pending := []PendingControlPayload{{MessageID: "steer-x", ParkedAt: time.Now().Add(-time.Minute)}}

	plan, _ := eh.planReannounce(pending)
	assert.False(t, plan.emit, "a turn the engine has not taken yet is already an unread notice")
	assert.False(t, plan.giveUp)

	// The queued turn is taken; now the boundary is a real one.
	eh.mu.Lock()
	eh.pendingTags = nil
	eh.mu.Unlock()
	plan, _ = eh.planReannounce(pending)
	assert.True(t, plan.emit)
	assert.Equal(t, "steer-x", plan.messageID)
}

// TestPlanReannounce_BudgetIsPerBody: a second instruction must not inherit the
// first one's exhaustion, and a body the limit retired must not block the one
// behind it.
func TestPlanReannounce_BudgetIsPerBody(t *testing.T) {
	eh := NewEngineHost(context.Background(), &scriptedChat{}, "claude-code", "run-1")
	t.Cleanup(eh.Close)
	eh.mu.Lock()
	eh.runCtx = context.Background()
	eh.mu.Unlock()

	older := PendingControlPayload{MessageID: "steer-a", ParkedAt: time.Now().Add(-time.Minute)}
	newer := PendingControlPayload{MessageID: "steer-b", ParkedAt: time.Now()}
	pending := []PendingControlPayload{older, newer}

	// Burn the older body's whole budget. Each plan sets the in-flight guard,
	// which a real enqueue clears when it lands.
	for i := 0; i < reannounceLimit; i++ {
		plan, _ := eh.planReannounce(pending)
		require.True(t, plan.emit)
		require.Equal(t, "steer-a", plan.messageID)
		eh.finishReannounce()
	}
	plan, _ := eh.planReannounce(pending)
	require.True(t, plan.giveUp)
	require.Equal(t, "steer-a", plan.messageID)

	// The retired body steps aside; the one behind it starts at the bottom rung
	// with a budget of its own.
	plan, _ = eh.planReannounce(pending)
	require.True(t, plan.emit)
	assert.Equal(t, "steer-b", plan.messageID)
	assert.Equal(t, agentcoordpb.PendingPullReminder_URGENCY_REPEAT, plan.urgency)
}

// TestWholeSeconds_ClampsABackwardsClock: the age is rendered into an UNSIGNED
// attribute, so a clock that stepped backwards would otherwise print an
// enormous number and make the notice absurd.
func TestWholeSeconds_ClampsABackwardsClock(t *testing.T) {
	assert.Equal(t, uint32(0), wholeSeconds(-5*time.Second))
	assert.Equal(t, uint32(0), wholeSeconds(999*time.Millisecond))
	assert.Equal(t, uint32(90), wholeSeconds(90*time.Second+400*time.Millisecond))
}
