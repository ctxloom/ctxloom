package coord

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/types/known/structpb"

	agentcoordpb "github.com/ctxloom/ctxloom/internal/agentcoord"
	"github.com/ctxloom/ctxloom/internal/agentcoord/spool"
)

// Tests for the INTERACTION-PLANE CUTOVER (spoolcontrol.go): steer as a
// durable withdrawable file, question/summarize as cooperative correlated
// asks, pause/resume as runner requests, and the approval relay's hop riding
// files.
//
// Every test redirects HOME (teeHome) before anything resolves a spool path,
// for the reason that helper's doc gives.

// writeInSpool writes one file STRAIGHT into a harp's in/ spool, with no
// doorbell — the fixture shape the mail-plane tests use for the same reason:
// at the production sweep cadence nothing can deliver it before the test acts
// on it, so what is being asserted is the operation and not a race.
func writeInSpool(t *testing.T, harp, kind, originID, body string) spool.Ref {
	t.Helper()
	w, err := spool.NewWriter(spool.NewHomeMapper(), harp, spool.DirIn, spoolWriterIDCoordinator)
	require.NoError(t, err)
	ref, err := w.Write(&spool.Message{
		Kind: kind, FromHarp: UserSender, To: harp, OriginID: originID, Body: body,
	})
	require.NoError(t, err)
	return ref
}

// ---- steer --------------------------------------------------------------

// TestSpoolSteer_RidesTheFileAndIsConsumedAtTheTurn is the steer plane's happy
// path: under the cutover the instruction is ONE durable file with the
// reserved `steer` kind, it reaches the engine as an ordinary framed turn, and
// it is consumed by rename at that turn.
//
// It also pins what the cutover REPLACED: no control body is parked in the
// runner's recv buffer. The plane-2 route's whole shape is
// park-body-then-announce-a-reminder-the-agent-pulls, and a steer that took
// both routes would be delivered twice — once as a turn, once as a pulled
// payload — which is exactly the double delivery the one-object collapse
// exists to prevent.
func TestSpoolSteer_RidesTheFileAndIsConsumedAtTheTurn(t *testing.T) {
	resetStrictness(t)
	teeHome(t)
	sp := cutoverSpawner(0)
	c := newCutoverCoordinator(t, sp, 0)
	out, home := awaitCutoverChild(t, c, sp, "first task")

	outcome, err := c.ControlSteer(context.Background(), humanInitiator(), out.Harp, "stop and rebase first")
	require.NoError(t, err)
	require.NotEmpty(t, outcome.MessageID, "a durable steer must return the handle its withdrawal takes")
	assert.Equal(t, agentcoordpb.SteerResult_APPLIED_UNSPECIFIED, outcome.Applied,
		"the spool route must not report a plane-2 applied state it never observed")

	turns := awaitChatText(t, sp, 0, "stop and rebase first")
	var delivered string
	for _, turn := range turns {
		if strings.Contains(turn, "stop and rebase first") {
			delivered = turn
		}
	}
	require.NotEmpty(t, delivered)
	assert.Contains(t, delivered, "kind="+KindSteer,
		"the reserved kind must render into the provenance header: an instruction the agent cannot tell from ordinary chatter is not a steer")

	// One file, consumed by rename, carrying the instruction verbatim.
	awaitSpoolCount(t, out.Harp, spool.DirIn, 0, "after the steer was taken")
	consumed := awaitSpoolCount(t, out.Harp, spool.DirInConsumed, 1, "after the steer was taken")
	assert.Equal(t, KindSteer, consumed[0].Message.Kind)
	assert.Equal(t, "stop and rebase first", consumed[0].Message.Body)
	assert.Equal(t, outcome.MessageID, consumed[0].Message.OriginID)

	assert.Empty(t, home.PendingControlPayloads(),
		"the durable steer must not ALSO park a control body: two carriers for one instruction is the double delivery this replaced")
	assert.NotContains(t, mailboxEverQueued(c), outcome.MessageID,
		"the file IS the steer; a mailbox fact would be a second copy nobody consumes")
}

// TestSpoolSteer_WithdrawnBeforeReadNeverReachesTheEngine is the withdrawal
// property, in its strongest form: an instruction retracted before the target
// took it is not delivered to THIS runner, and not to a replacement runner
// reading the same spool afterwards either.
//
// The fixture is written straight into in/ so no doorbell exists to race the
// withdrawal — at the production cadence the sweep cannot have run — which
// makes this a test of the retraction rather than of who won a timer.
func TestSpoolSteer_WithdrawnBeforeReadNeverReachesTheEngine(t *testing.T) {
	resetStrictness(t)
	teeHome(t)
	sp := cutoverSpawner(0)
	c := newCutoverCoordinator(t, sp, 0)
	out, home := awaitCutoverChild(t, c, sp, "first task")

	writeInSpool(t, out.Harp, KindSteer, "m-withdraw-me", "delete the production database")
	require.Len(t, spoolEntries(t, out.Harp, spool.DirIn), 1, "the fixture must be on disk before the withdrawal")

	require.NoError(t, c.WithdrawSteer(humanInitiator(), out.Harp, "m-withdraw-me"))

	assert.Empty(t, spoolEntries(t, out.Harp, spool.DirIn), "a retracted instruction must leave the delivery directory")
	withdrawn := spoolEntries(t, out.Harp, spool.DirInWithdrawn)
	require.Len(t, withdrawn, 1, "withdrawal is a MOVE: the retracted instruction stays readable as the audit trail")
	assert.Equal(t, "delete the production database", withdrawn[0].Message.Body)
	assert.Empty(t, spoolEntries(t, out.Harp, spool.DirInConsumed),
		"a withdrawn instruction must never appear as consumed: consumed means the agent acted on it")

	// Force every reader there is. None may find it.
	home.SweepSpoolIn()
	require.Never(t, func() bool { return countChatText(sp, 0, "delete the production database") > 0 },
		500*time.Millisecond, 10*time.Millisecond,
		"a withdrawn instruction must never reach the engine")

	// And not a REPLACEMENT runner either — the relaunch case, where the first
	// runner's in-memory state is gone and only the directories remain.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	fresh, err := NewHome(ctx, HomeConfig{
		URL: "http://127.0.0.1:1/mcp", Token: "unused", RunID: "run-fresh-steer",
		Harness: "mock", Harp: out.Harp, SpoolDelivery: true,
		SpoolSweepInterval: 50 * time.Millisecond,
	})
	require.NoError(t, err)
	t.Cleanup(func() { fresh.crash() })
	seen := make(chan string, 4)
	fresh.SetTurnSink(func(pm *agentcoordpb.PeerMessage) bool { seen <- pm.GetText(); return true })
	select {
	case text := <-seen:
		t.Fatalf("a replacement runner delivered a withdrawn instruction: %q", text)
	case <-time.After(500 * time.Millisecond):
	}
}

// TestSpoolSteer_WithdrawAfterConsumeSaysSoHonestly pins the losing side of
// the race. Once the target has taken the instruction there is nothing to
// retract, and BOTH other answers are harmful: reporting success would leave a
// human believing they pulled back an instruction the agent is already acting
// on, and reporting a generic failure would read as "the withdrawal broke" and
// invite a retry that can never succeed.
func TestSpoolSteer_WithdrawAfterConsumeSaysSoHonestly(t *testing.T) {
	resetStrictness(t)
	teeHome(t)
	sp := cutoverSpawner(0)
	c := newCutoverCoordinator(t, sp, 0)
	out, _ := awaitCutoverChild(t, c, sp, "first task")

	outcome, err := c.ControlSteer(context.Background(), humanInitiator(), out.Harp, "rebase first")
	require.NoError(t, err)
	awaitChatText(t, sp, 0, "rebase first")
	awaitSpoolCount(t, out.Harp, spool.DirInConsumed, 1, "the target must have taken it before the withdrawal")

	err = c.WithdrawSteer(humanInitiator(), out.Harp, outcome.MessageID)
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrSteerAlreadyDelivered,
		"the caller must be able to tell 'too late' from 'it failed' — they call for different next moves")
	assert.NotErrorIs(t, err, ErrNoSuchSteer, "the instruction existed; only its window closed")

	// An id nobody ever queued is the OTHER answer, and must not be confused
	// with it: a typo must not read as a delivery.
	err = c.WithdrawSteer(humanInitiator(), out.Harp, "m-never-existed")
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrNoSuchSteer)
	assert.NotErrorIs(t, err, ErrSteerAlreadyDelivered)
}

// TestSpoolSteer_FlagOffKeepsThePlaneTwoRouteAndTouchesNoDisk is the flag-off
// half of the steer plane: with the cutover unset a steer still rides the
// plane-2 request (body parked, reminder injected, pulled by the agent), still
// reports an APPLIED state, offers no withdraw handle — and leaves no spool
// directory anywhere under HOME.
func TestSpoolSteer_FlagOffKeepsThePlaneTwoRouteAndTouchesNoDisk(t *testing.T) {
	resetStrictness(t)
	home := teeHome(t)
	sp := cutoverSpawner(0)
	c := newTestCoordinator(t, sp, nil)
	require.False(t, c.SpoolDeliveryEnabled())

	out, err := c.AgentRun(context.Background(), ownerIdentity(), "worker", "first task", "", "")
	require.NoError(t, err)
	upCtx, upCancel := context.WithTimeout(context.Background(), conformanceWait)
	defer upCancel()
	require.NoError(t, c.awaitChildUp(upCtx, out.Harp))
	require.Eventually(t, func() bool { return sp.engineHome(0) != nil }, conformanceWait, 10*time.Millisecond)
	runnerHome := sp.engineHome(0)

	outcome, err := c.ControlSteer(context.Background(), humanInitiator(), out.Harp, "check the lockfile")
	require.NoError(t, err)
	assert.Empty(t, outcome.MessageID, "there is no durable object to withdraw on the request route")
	assert.NotEqual(t, agentcoordpb.SteerResult_APPLIED_UNSPECIFIED, outcome.Applied,
		"the plane-2 route must still report how the steer landed")
	require.Eventually(t, func() bool { return len(runnerHome.PendingControlPayloads()) == 1 },
		conformanceWait, 10*time.Millisecond,
		"the request route parks the body for the agent to pull — that is the behaviour the flag preserves")

	assert.Empty(t, spoolDirsUnder(t, home),
		"a steer with the cutover off must create no spool directory anywhere under HOME")
	assert.ErrorIs(t, c.WithdrawSteer(humanInitiator(), out.Harp, "anything"), ErrNoSuchSteer,
		"withdrawal must refuse rather than pretend to retract a body that lives in a runner's memory")
}

// ---- correlated asks ----------------------------------------------------

// answerAsk replies to a delivered ask from the CHILD's own runner — an
// ordinary agent_send quoting the ask's id, which under the cutover is a local
// write into the child's out/ spool.
func answerAsk(t *testing.T, home *Home, askID, text string, structured *structpb.Struct) {
	t.Helper()
	resp, err := home.Request(context.Background(), &agentcoordpb.AgentRequest{
		Kind: &agentcoordpb.AgentRequest_PeerSend{PeerSend: &agentcoordpb.PeerSendRequest{
			ToRole: ParentAddress, Text: text, InReplyTo: askID, Structured: structured,
		}},
	})
	require.NoError(t, err)
	require.EqualValues(t, 0, resp.GetStatus().GetCode(), "the reply must be accepted: %s", resp.GetStatus().GetMessage())
}

// TestSpoolAsk_QuestionIsAnsweredByCorrelation is the ask plane's happy path:
// the question is delivered to the child as a turn it can read, and the answer
// is the child's OWN send quoting it. Correlation is by in_reply_to and by
// nothing else.
func TestSpoolAsk_QuestionIsAnsweredByCorrelation(t *testing.T) {
	resetStrictness(t)
	teeHome(t)
	sp := cutoverSpawner(0)
	c := newCutoverCoordinator(t, sp, 0)
	out, home := awaitCutoverChild(t, c, sp, "first task")

	askIDs := make(chan string, 1)
	c.onAskPublished = func(id string) { askIDs <- id }

	answers := make(chan AskAnswer, 1)
	errs := make(chan error, 1)
	go func() {
		ans, err := c.ControlQuestion(context.Background(), humanInitiator(), out.Harp, "why sqlx over diesel?")
		if err != nil {
			errs <- err
			return
		}
		answers <- ans
	}()

	var askID string
	select {
	case askID = <-askIDs:
	case err := <-errs:
		t.Fatalf("the ask failed before it was published: %v", err)
	case <-time.After(conformanceWait):
		t.Fatal("the ask was never published")
	}
	require.NotEmpty(t, askID)

	// The child is IDLE, so its runner PROMPTS it: the question arrives as a
	// turn within one delivery, not at some later boundary of its own.
	turns := awaitChatText(t, sp, 0, "why sqlx over diesel?")
	var delivered string
	for _, turn := range turns {
		if strings.Contains(turn, "why sqlx over diesel?") {
			delivered = turn
		}
	}
	require.NotEmpty(t, delivered)
	assert.Contains(t, delivered, "kind="+KindQuestion, "the child must be able to see that it is being asked")

	structured, err := structpb.NewStruct(map[string]any{"confidence": "high"})
	require.NoError(t, err)
	answerAsk(t, home, askID, "compile-time checked queries", structured)

	select {
	case ans := <-answers:
		assert.Equal(t, askID, ans.AskID)
		assert.Equal(t, out.Harp, ans.From, "the answerer is the spool the reply was found in")
		assert.Equal(t, "compile-time checked queries", ans.Text)
		require.NotEmpty(t, ans.Structured, "the reply's structured companion must survive the file")
		var payload map[string]any
		require.NoError(t, json.Unmarshal(ans.Structured, &payload))
		assert.Equal(t, "high", payload["confidence"])
	case err := <-errs:
		t.Fatalf("the ask went unanswered: %v", err)
	case <-time.After(conformanceWait):
		t.Fatal("the correlated reply never resolved the ask")
	}

	// The answer resolved the ask and did NOT also become mail: the asker is
	// the coordinator, and delivering it onward would give the target's parent
	// a message nobody sent it.
	assert.Empty(t, recvBody(t, c, "compile-time checked queries", 200*time.Millisecond),
		"an ask's answer is consumed by its waiter, never mailed onward as well")
}

// TestSpoolAsk_ReplyArrivingAtThePublishInstantStillResolves pins the
// REGISTER-BEFORE-PUBLISH ordering — the property whose absence is
// pulpy-whiff: the answer arrives, finds no waiter, degrades to ordinary mail,
// and the asker sits out its whole budget reporting a timeout that never
// happened.
//
// The reply is sent from INSIDE the publish hook, which is the earliest
// instant an answer can exist at all. Only a hook fired there can assert the
// ordering deterministically rather than by racing an Eventually.
func TestSpoolAsk_ReplyArrivingAtThePublishInstantStillResolves(t *testing.T) {
	resetStrictness(t)
	teeHome(t)
	sp := cutoverSpawner(0)
	c := newCutoverCoordinator(t, sp, 0)
	out, home := awaitCutoverChild(t, c, sp, "first task")

	c.onAskPublished = func(askID string) {
		answerAsk(t, home, askID, "answered in the same instant", nil)
	}
	ctx, cancel := context.WithTimeout(context.Background(), conformanceWait)
	defer cancel()
	ans, err := c.ControlQuestion(ctx, humanInitiator(), out.Harp, "are you there?")
	require.NoError(t, err, "a reply landing at the publish instant must resolve, not time out")
	assert.Equal(t, "answered in the same instant", ans.Text)
	assert.NotEmpty(t, ans.AskID)
}

// TestSpoolAsk_TurnOutputIsNotTheAnswer pins the COOPERATIVE-REPLY ruling: the
// answer is what the child CHOSE to send back, correlated by in_reply_to.
//
// A child's ordinary turn report — the shape the automatic turn-boundary
// bridge produces, kind `result` with no correlation — must NOT resolve an
// outstanding ask. Involuntary capture would answer a question with whatever
// the child happened to be saying at the time, and report it to a human as the
// agent's answer.
func TestSpoolAsk_TurnOutputIsNotTheAnswer(t *testing.T) {
	resetStrictness(t)
	teeHome(t)
	sp := cutoverSpawner(0)
	c := newCutoverCoordinator(t, sp, 0)
	out, home := awaitCutoverChild(t, c, sp, "first task")

	askIDs := make(chan string, 1)
	c.onAskPublished = func(id string) { askIDs <- id }
	answers := make(chan AskAnswer, 1)
	errs := make(chan error, 1)
	ctx, cancel := context.WithTimeout(context.Background(), conformanceWait)
	defer cancel()
	go func() {
		ans, err := c.ControlQuestion(ctx, humanInitiator(), out.Harp, "which migration path?")
		if err != nil {
			errs <- err
			return
		}
		answers <- ans
	}()
	var askID string
	select {
	case askID = <-askIDs:
	case <-time.After(conformanceWait):
		t.Fatal("the ask was never published")
	}

	// The child reports its turn the way the bridge does: kind result, no
	// correlation at all.
	structured, err := structpb.NewStruct(map[string]any{"kind": KindResult})
	require.NoError(t, err)
	resp, err := home.Request(context.Background(), &agentcoordpb.AgentRequest{
		Kind: &agentcoordpb.AgentRequest_PeerSend{PeerSend: &agentcoordpb.PeerSendRequest{
			ToRole: ParentAddress, Text: "unrelated turn output", Structured: structured,
		}},
	})
	require.NoError(t, err)
	require.EqualValues(t, 0, resp.GetStatus().GetCode())

	// It must land as ORDINARY MAIL to the parent...
	require.NotEmpty(t, recvBody(t, c, "unrelated turn output", conformanceWait),
		"an uncorrelated report is ordinary mail and must still be delivered")
	// ...and must NOT have answered the question.
	select {
	case ans := <-answers:
		t.Fatalf("uncorrelated turn output was captured as the answer: %q", ans.Text)
	case err := <-errs:
		t.Fatalf("the ask failed instead of staying outstanding: %v", err)
	default:
	}

	// The child's own, addressed answer is what resolves it.
	answerAsk(t, home, askID, "the reversible one", nil)
	select {
	case ans := <-answers:
		assert.Equal(t, "the reversible one", ans.Text, "only the correlated reply is the answer")
	case err := <-errs:
		t.Fatalf("the correlated reply did not resolve the ask: %v", err)
	case <-time.After(conformanceWait):
		t.Fatal("the correlated reply never resolved the ask")
	}
}

// TestSpoolAsk_SummarizeCarriesItsOwnKind pins that the two asks are
// distinguishable to the child: a summarize is not a question wearing the same
// label, or the agent cannot tell what it is being asked for.
func TestSpoolAsk_SummarizeCarriesItsOwnKind(t *testing.T) {
	resetStrictness(t)
	teeHome(t)
	sp := cutoverSpawner(0)
	c := newCutoverCoordinator(t, sp, 0)
	out, home := awaitCutoverChild(t, c, sp, "first task")

	c.onAskPublished = func(askID string) {
		answerAsk(t, home, askID, "we chose sqlx, then wrote the migrations", nil)
	}
	ctx, cancel := context.WithTimeout(context.Background(), conformanceWait)
	defer cancel()
	ans, err := c.ControlSummarize(ctx, humanInitiator(), out.Harp, "just the schema decisions")
	require.NoError(t, err)
	assert.Equal(t, "we chose sqlx, then wrote the migrations", ans.Text)

	consumed := spoolEntries(t, out.Harp, spool.DirInConsumed)
	require.NotEmpty(t, consumed, "the ask must have been delivered as a file")
	kinds := make([]string, 0, len(consumed))
	for _, e := range consumed {
		kinds = append(kinds, e.Message.Kind)
	}
	assert.Contains(t, kinds, KindSummarize, "a summarize ask must carry its own kind, not a question's (saw %v)", kinds)
}

// TestSpoolAsk_RefusedWhenTheTargetIsNotCutOver pins the flag-off answer.
// Question and summarize were NEVER built on plane 2 (HandleControl has only a
// steer arm), so with the cutover off there is no path — and saying so is the
// only honest answer. A verb that silently waited out its budget against a
// target that can never reply would report "it did not answer" for something
// it never asked.
func TestSpoolAsk_RefusedWhenTheTargetIsNotCutOver(t *testing.T) {
	resetStrictness(t)
	home := teeHome(t)
	sp := cutoverSpawner(0)
	c := newTestCoordinator(t, sp, nil)

	out, err := c.AgentRun(context.Background(), ownerIdentity(), "worker", "first task", "", "")
	require.NoError(t, err)
	upCtx, upCancel := context.WithTimeout(context.Background(), conformanceWait)
	defer upCancel()
	require.NoError(t, c.awaitChildUp(upCtx, out.Harp))

	_, err = c.ControlQuestion(context.Background(), humanInitiator(), out.Harp, "why?")
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrAskUnavailable)
	assert.NotErrorIs(t, err, ErrAskTimeout, "refusing to ask is not the same fact as asking and getting no answer")

	_, err = c.ControlSummarize(context.Background(), humanInitiator(), out.Harp, "everything")
	assert.ErrorIs(t, err, ErrAskUnavailable)
	assert.Empty(t, spoolDirsUnder(t, home), "a refused ask must leave no spool directory behind")

	// Empty input fails rather than asking nothing and waiting out a budget.
	_, err = c.ControlQuestion(context.Background(), humanInitiator(), out.Harp, "")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "text is required")
}

// ---- pause / resume ------------------------------------------------------

// TestSpoolControl_PauseHoldsTurnsAndLeavesMailUnconsumed pins the pause
// plane's two properties at once, and the second is the load-bearing one: a
// paused run takes no new turn, AND the mail behind that turn is NOT consumed.
//
// Consuming it would convert a pause into silent data loss on the very path
// pause exists to make safe — the file would sit in consumed/ with the agent
// never having seen it, and a relaunch would find nothing to deliver.
//
// It also pins that pause is NOT a delivery: nothing about it appears in the
// message carrier. The only file in the child's spool is the mail the test
// sent; the pause and the resume left none.
func TestSpoolControl_PauseHoldsTurnsAndLeavesMailUnconsumed(t *testing.T) {
	resetStrictness(t)
	teeHome(t)
	sp := cutoverSpawner(0)
	c := newCutoverCoordinator(t, sp, 0)
	out, _ := awaitCutoverChild(t, c, sp, "first task")

	ctx, cancel := context.WithTimeout(context.Background(), conformanceWait)
	defer cancel()
	require.NoError(t, c.ControlPause(ctx, humanInitiator(), out.Harp, "human is reviewing"))

	_, _, _, err := c.peerSend(ownerIdentity(), out.Harp, KindMessage, "work item while paused", nil, "")
	require.NoError(t, err)

	require.Never(t, func() bool { return countChatText(sp, 0, "work item while paused") > 0 },
		750*time.Millisecond, 10*time.Millisecond,
		"a paused run must take no new turn")
	assert.Empty(t, spoolEntries(t, out.Harp, spool.DirInConsumed),
		"mail held at the pause gate must stay UNCONSUMED: consuming it would lose it, since the agent never saw it")
	require.Len(t, spoolEntries(t, out.Harp, spool.DirIn), 1,
		"the held message must still be in the delivery directory, where a relaunched run would find it")

	// Pause is idempotent, and says which it did.
	require.NoError(t, c.ControlPause(ctx, humanInitiator(), out.Harp, "still reviewing"))

	require.NoError(t, c.ControlResume(ctx, humanInitiator(), out.Harp))
	awaitChatText(t, sp, 0, "work item while paused")
	awaitSpoolCount(t, out.Harp, spool.DirInConsumed, 1, "after the resume released the held turn")

	// THE CARRIER SHOWS NOTHING. Pause and resume are runner requests: they
	// left no message in the mailbox and no file in the spool.
	for _, e := range spoolEntries(t, out.Harp, spool.DirInConsumed) {
		assert.Equal(t, "work item while paused", e.Message.Body,
			"the only file the whole exchange produced must be the mail; pause is not a delivery")
	}
	for _, id := range mailboxEverQueued(c) {
		var m Message
		c.mail.View(func() {
			for _, pending := range c.mailF.pendingFor(out.Harp) {
				if pending.ID == id {
					m = pending
				}
			}
		})
		assert.NotEqual(t, "pause", m.Kind)
	}
}

// TestSpoolControl_PauseRefusedWhenNotOnTheRunnerPlane pins the flag-off
// answer: pause and resume were never built as executors on plane 2, so a run
// that predates the cutover is refused with the typed capability error rather
// than silently doing nothing.
func TestSpoolControl_PauseRefusedWhenNotOnTheRunnerPlane(t *testing.T) {
	resetStrictness(t)
	teeHome(t)
	sp := cutoverSpawner(0)
	c := newTestCoordinator(t, sp, nil)

	out, err := c.AgentRun(context.Background(), ownerIdentity(), "worker", "first task", "", "")
	require.NoError(t, err)
	upCtx, upCancel := context.WithTimeout(context.Background(), conformanceWait)
	defer upCancel()
	require.NoError(t, c.awaitChildUp(upCtx, out.Harp))

	err = c.ControlPause(context.Background(), humanInitiator(), out.Harp, "review")
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrCapabilityUnavailable)
	assert.ErrorIs(t, c.ControlResume(context.Background(), humanInitiator(), out.Harp), ErrCapabilityUnavailable)
}

// TestSpoolControl_PauseRefusesAnotherRunsId pins the A9 correlation on the
// new runner requests: a runner hosts exactly ONE run, and a request naming
// another must be refused rather than applied to whatever run is here.
func TestSpoolControl_PauseRefusesAnotherRunsId(t *testing.T) {
	resetStrictness(t)
	teeHome(t)
	sp := cutoverSpawner(0)
	c := newCutoverCoordinator(t, sp, 0)
	out, _ := awaitCutoverChild(t, c, sp, "first task")

	var credHash string
	c.runs.View(func() {
		if r := c.runsF.currentRun(out.Harp); r != nil {
			credHash = r.CredHash
		}
	})
	require.NotEmpty(t, credHash)

	ctx, cancel := context.WithTimeout(context.Background(), conformanceWait)
	defer cancel()
	resp, err := c.requestRunner(ctx, credHash, &agentcoordpb.RunnerRequest{
		Kind: &agentcoordpb.RunnerRequest_PauseRun{PauseRun: &agentcoordpb.PauseRun{RunId: "some-other-run"}},
	})
	require.NoError(t, err)
	assert.NotEqualValues(t, 0, resp.GetStatus().GetCode(),
		"a pause naming another run must be refused, not applied to the run that happens to be hosted here")
	assert.Contains(t, resp.GetStatus().GetMessage(), "A9 correlation")

	// And the refusal left the run RUNNING: mail still lands.
	_, _, _, err = c.peerSend(ownerIdentity(), out.Harp, KindMessage, "still running", nil, "")
	require.NoError(t, err)
	awaitChatText(t, sp, 0, "still running")
}
