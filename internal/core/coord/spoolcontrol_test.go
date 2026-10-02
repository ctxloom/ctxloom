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

	agentcoordpb "github.com/ctxloom/ctxloom/internal/adapters/coordgrpc/pb"
	"github.com/ctxloom/ctxloom/internal/core/spool"
)

// Tests for the INTERACTION-PLANE CUTOVER (spoolcontrol.go): steer as a
// durable withdrawable file, question/summarize as cooperative correlated
// asks, and pause/resume as runner requests.
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
	out, _ := awaitCutoverChild(t, c, sp, "first task")

	outcome, err := c.ControlSteer(context.Background(), humanInitiator(), out.Harp, "stop and rebase first", false)
	require.NoError(t, err)
	require.NotEmpty(t, outcome.MessageID, "a durable steer must return the handle its withdrawal takes")

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

	// One file, delivered: the handle the steer returned is the identity in
	// the target's delivered record, and the file is gone from in/.
	awaitDelivered(t, out.Harp, outcome.MessageID, "after the steer was taken")

	assertNoMailboxJournal(t, c)
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
	// IDLE first: the withdrawal must race nothing. A fixture written while the
	// child's first turn boundary is still pending would be delivered by that
	// boundary's sweep, and this test would be measuring which went first.
	out, home := awaitCutoverChildIdle(t, c, sp, "first task")

	writeInSpool(t, out.Harp, KindSteer, "m-withdraw-me", "delete the production database")
	require.Len(t, spoolEntries(t, out.Harp, spool.DirIn), 1, "the fixture must be on disk before the withdrawal")

	require.NoError(t, c.WithdrawSteer(humanInitiator(), out.Harp, "m-withdraw-me"))

	assert.Empty(t, spoolEntries(t, out.Harp, spool.DirIn), "a retracted instruction must leave the delivery directory")
	withdrawn := spoolEntries(t, out.Harp, spool.DirInWithdrawn)
	require.Len(t, withdrawn, 1, "withdrawal is a MOVE: the retracted instruction stays readable as the audit trail")
	assert.Equal(t, "delete the production database", withdrawn[0].Message.Body)
	_, recorded := spoolDelivered(t, out.Harp)["m-withdraw-me"]
	assert.False(t, recorded,
		"a withdrawn instruction must never be recorded as delivered: delivered means the agent acted on it")

	// Force every reader there is. None may find it.
	home.SweepSpoolIn()
	require.Never(t, func() bool { return countChatText(sp, 0, "delete the production database") > 0 },
		500*time.Millisecond, 10*time.Millisecond,
		"a withdrawn instruction must never reach the engine")

	// And not a REPLACEMENT runner either — the relaunch case, where the first
	// runner's in-memory state is gone and only the directories remain.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	fresh, err := runnerHooks.NewHome(ctx, TestHomeConfig{
		Reporter: termSink(),
		URL:      "http://127.0.0.1:1/mcp", Token: "unused", RunID: "run-fresh-steer",
		Harness: "mock", Harp: out.Harp,
		SpoolSweepInterval: 50 * time.Millisecond,
	})
	require.NoError(t, err)
	t.Cleanup(func() { fresh.Crash() })
	seen := make(chan string, 4)
	fresh.SetTurnSink(func(pm *agentcoordpb.PeerMessage) bool { seen <- pm.GetText(); return true })
	select {
	case text := <-seen:
		t.Fatalf("a replacement runner delivered a withdrawn instruction: %q", text)
	case <-time.After(500 * time.Millisecond):
	}
}

// TestSpoolSteer_WithdrawAfterDeliverySaysSoHonestly pins the losing side of
// the race. Once the target has taken the instruction there is nothing to
// retract, and BOTH other answers are harmful: reporting success would leave a
// human believing they pulled back an instruction the agent is already acting
// on, and reporting a generic failure would read as "the withdrawal broke" and
// invite a retry that can never succeed.
func TestSpoolSteer_WithdrawAfterDeliverySaysSoHonestly(t *testing.T) {
	resetStrictness(t)
	teeHome(t)
	sp := cutoverSpawner(0)
	c := newCutoverCoordinator(t, sp, 0)
	out, _ := awaitCutoverChild(t, c, sp, "first task")

	outcome, err := c.ControlSteer(context.Background(), humanInitiator(), out.Harp, "rebase first", false)
	require.NoError(t, err)
	awaitChatText(t, sp, 0, "rebase first")
	// Delivered and DELETED: the only thing left to answer from is the
	// delivered record.
	awaitDelivered(t, out.Harp, outcome.MessageID, "the target must have taken it before the withdrawal")

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

	// An id no file or record entry can be named is the same answer, not a
	// lookup failure.
	assert.ErrorIs(t, c.WithdrawSteer(humanInitiator(), out.Harp, "../escape"), ErrNoSuchSteer)
}

// ---- correlated asks ----------------------------------------------------

// answerAsk replies to a delivered ask from the CHILD's own runner — an
// ordinary agent_send quoting the ask's id, which under the cutover is a local
// write into the child's out/ spool.
func answerAsk(t *testing.T, home TestHome, askID, text string, structured *structpb.Struct) {
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

	registered := false
	c.onAskPublished = func(askID string) {
		// The STRUCTURAL half, asserted at the one instant that separates this
		// ordering from every wrong one: the ask is about to be published, and
		// its waiter is ALREADY in the table. The reply below then exercises
		// the same fact end to end — one asserts the invariant, the other
		// asserts that the invariant is what makes the answer land.
		c.mu.Lock()
		_, registered = c.asks[askID]
		c.mu.Unlock()
		answerAsk(t, home, askID, "answered in the same instant", nil)
	}
	ctx, cancel := context.WithTimeout(context.Background(), conformanceWait)
	defer cancel()
	ans, err := c.ControlQuestion(ctx, humanInitiator(), out.Harp, "are you there?")
	require.NoError(t, err, "a reply landing at the publish instant must resolve, not time out")
	assert.Equal(t, "answered in the same instant", ans.Text)
	assert.NotEmpty(t, ans.AskID)
	assert.True(t, registered,
		"the waiter must be in the table BEFORE the ask is observable: a reply that finds no waiter degrades to ordinary mail and the asker times out on an answer it was given")
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
	resp, err := home.Request(context.Background(), &agentcoordpb.AgentRequest{
		Kind: &agentcoordpb.AgentRequest_PeerSend{PeerSend: &agentcoordpb.PeerSendRequest{
			ToRole: ParentAddress, Text: "unrelated turn output", Kind: agentcoordpb.MessageKind_MESSAGE_KIND_RESULT,
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

// TestSpoolAsk_OnlyTheTargetCanAnswer pins resolveAskReply's answerer check:
// the id alone is not authority. A reply quoting an outstanding ask's id from
// any harp but the one asked must NOT resolve it — otherwise the asker is
// handed someone else's words as the target's answer, a WRONG ANSWER rather
// than an error, with nothing downstream able to tell.
//
// The foreign reply is sent from inside the publish hook, before the target
// can have seen the ask at all, so what is asserted is the check and not who
// won a race.
func TestSpoolAsk_OnlyTheTargetCanAnswer(t *testing.T) {
	resetStrictness(t)
	teeHome(t)
	sp := cutoverSpawner(0)
	c := newCutoverCoordinator(t, sp, 0)
	out, home := awaitCutoverChild(t, c, sp, "first task")

	askIDs := make(chan string, 1)
	var foreignErr error
	c.onAskPublished = func(id string) {
		// A harp that is NOT the target quotes the id. The owner is one such
		// harp; which non-target it is does not matter to the check.
		_, foreignErr = c.AgentSend(ownerIdentity(), out.Harp, KindMessage, "forged answer", nil, id)
		askIDs <- id
	}
	answers := make(chan AskAnswer, 1)
	errs := make(chan error, 1)
	ctx, cancel := context.WithTimeout(context.Background(), conformanceWait)
	defer cancel()
	go func() {
		ans, err := c.ControlQuestion(ctx, humanInitiator(), out.Harp, "who may answer this?")
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
	require.NoError(t, foreignErr, "a non-target's correlated send degrades to ordinary mail; it is not refused")

	select {
	case ans := <-answers:
		t.Fatalf("a non-target's reply was taken as the target's answer: %q from %q", ans.Text, ans.From)
	case err := <-errs:
		t.Fatalf("the ask failed instead of staying outstanding: %v", err)
	default:
	}

	answerAsk(t, home, askID, "the target's own answer", nil)
	select {
	case ans := <-answers:
		assert.Equal(t, "the target's own answer", ans.Text)
		assert.Equal(t, out.Harp, ans.From)
	case err := <-errs:
		t.Fatalf("the target's reply did not resolve the ask: %v", err)
	case <-time.After(conformanceWait):
		t.Fatal("the target's reply never resolved the ask")
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

	askIDs := make(chan string, 1)
	c.onAskPublished = func(id string) { askIDs <- id }
	answers := make(chan AskAnswer, 1)
	errs := make(chan error, 1)
	ctx, cancel := context.WithTimeout(context.Background(), conformanceWait)
	defer cancel()
	go func() {
		ans, err := c.ControlSummarize(ctx, humanInitiator(), out.Harp, "just the schema decisions")
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
	// Answer only AFTER the child has actually been given the ask, so the file
	// this test then reads has genuinely been delivered and consumed rather
	// than short-circuited by a reply that beat its own question.
	asked := awaitChatText(t, sp, 0, "just the schema decisions")
	answerAsk(t, home, askID, "we chose sqlx, then wrote the migrations", nil)
	select {
	case ans := <-answers:
		assert.Equal(t, "we chose sqlx, then wrote the migrations", ans.Text)
	case err := <-errs:
		t.Fatalf("the summarize ask failed: %v", err)
	case <-time.After(conformanceWait):
		t.Fatal("the summarize ask went unanswered")
	}

	awaitDelivered(t, out.Harp, askID, "the ask must have been delivered as a file")
	var frame string
	for _, turn := range asked {
		if strings.Contains(turn, "just the schema decisions") {
			frame = turn
		}
	}
	assert.Contains(t, frame, "kind="+KindSummarize, "a summarize ask must carry its own kind, not a question's")
}

// TestSpoolAsk_EmptyTextIsRefused: empty input fails rather than asking
// nothing and waiting out a budget for an answer to a question nobody asked.
func TestSpoolAsk_EmptyTextIsRefused(t *testing.T) {
	resetStrictness(t)
	teeHome(t)
	sp := cutoverSpawner(0)
	c := newTestCoordinator(t, sp, nil)

	out, err := c.AgentRun(context.Background(), ownerIdentity(), "worker", "first task", "", "")
	require.NoError(t, err)
	upCtx, upCancel := context.WithTimeout(context.Background(), conformanceWait)
	defer upCancel()
	require.NoError(t, c.awaitChildUp(upCtx, out.Harp))

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
// pause exists to make safe — the file would be deleted and its identity
// recorded with the agent never having seen it, and a relaunch would find
// nothing to deliver.
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
	newly, err := c.ControlPause(ctx, humanInitiator(), out.Harp, "human is reviewing")
	require.NoError(t, err)
	assert.True(t, newly, "the first pause is the one that installed the gate")

	mailID, _, err := c.peerSend(ownerIdentity(), out.Harp, KindMessage, "work item while paused", nil, "")
	require.NoError(t, err)

	require.Never(t, func() bool { return countChatText(sp, 0, "work item while paused") > 0 },
		750*time.Millisecond, 10*time.Millisecond,
		"a paused run must take no new turn")
	assert.Empty(t, spoolDelivered(t, out.Harp),
		"mail held at the pause gate must stay UNDELIVERED: recording it would lose it, since the agent never saw it")
	require.Len(t, spoolEntries(t, out.Harp, spool.DirIn), 1,
		"the held message must still be in the delivery directory, where a relaunched run would find it")

	// Pause is idempotent, and says which it did.
	newly, err = c.ControlPause(ctx, humanInitiator(), out.Harp, "still reviewing")
	require.NoError(t, err)
	assert.False(t, newly, "a second pause finds the gate already installed and must say so")

	newly, err = c.ControlResume(ctx, humanInitiator(), out.Harp)
	require.NoError(t, err)
	assert.True(t, newly, "the resume is the one that released the gate")
	awaitChatText(t, sp, 0, "work item while paused")
	awaitDelivered(t, out.Harp, mailID, "after the resume released the held turn")

	// THE CARRIER SHOWS NOTHING. Pause and resume are runner requests: they
	// left no file in the spool and grew no other carrier.
	for id := range spoolDelivered(t, out.Harp) {
		assert.Equal(t, mailID, id,
			"the only delivery the whole exchange produced must be the mail; pause is not a delivery")
	}
	assertNoMailboxJournal(t, c)
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
	resp, err := c.requestRunner(ctx, credHash, RunnerRequest{Kind: PauseRun{RunID: "some-other-run"}})
	require.NoError(t, err)
	require.Error(t, resp.Err,
		"a pause naming another run must be refused, not applied to the run that happens to be hosted here")
	assert.Contains(t, resp.Err.Error(), "A9 correlation")

	// And the refusal left the run RUNNING: mail still lands.
	_, _, err = c.peerSend(ownerIdentity(), out.Harp, KindMessage, "still running", nil, "")
	require.NoError(t, err)
	awaitChatText(t, sp, 0, "still running")
}

// TestControlBudgets_AsksWaitThirtyMinutesMechanicalVerbsFailFast pins the Q7
// ruling: a question or summarize waits for a cooperative answer, which can
// take as long as the child's current turn, so its deadline-free budget is 30
// minutes; steer, pause and resume are mechanical effects on an attached
// target and keep the 60s default so a wedged runner fails fast. The wire
// budget must outlast the ask's, or a transport would replace the
// coordinator's ErrAskTimeout verdict with a bare deadline.
func TestControlBudgets_AsksWaitThirtyMinutesMechanicalVerbsFailFast(t *testing.T) {
	assert.Equal(t, 30*time.Minute, controlAskBudget, "question/summarize budget")
	assert.Equal(t, 60*time.Second, DefaultRequestTimeout, "pause/resume keep the default request budget")
	assert.Greater(t, AskWireBudget, controlAskBudget, "the wire must outlast the ask it carries")
}

// TestSpoolAsk_LateReplyIsDroppedNotMailedOnward pins what a timed-out ask
// promises its caller: ErrAskTimeout says an answer that still arrives "will
// be dropped". Review finding F5's failure class is an answer landing where
// nobody asked for it; a late reply that fell through to ordinary mail would
// do exactly that — the child's parent (not the asker, who for a human
// initiator is not a mailbox at all) receives a message quoting an ask it
// never made, which nothing can place.
func TestSpoolAsk_LateReplyIsDroppedNotMailedOnward(t *testing.T) {
	resetStrictness(t)
	teeHome(t)
	sp := cutoverSpawner(0)
	c := newCutoverCoordinator(t, sp, 0)
	out, home := awaitCutoverChild(t, c, sp, "first task")

	askIDs := make(chan string, 1)
	c.onAskPublished = func(id string) { askIDs <- id }
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	_, err := c.ControlQuestion(ctx, humanInitiator(), out.Harp, "are you still there?")
	require.ErrorIs(t, err, ErrAskTimeout)
	askID := <-askIDs

	// Sent as a real agent_send sends it: WITH a kind. A kindless reply is
	// refused by the sender-vocabulary check once no ask intercepts it, so it
	// would never reach the parent whatever the ask table did, and this test
	// would pass for a reason that has nothing to do with the ask.
	resp, err := home.Request(context.Background(), &agentcoordpb.AgentRequest{
		Kind: &agentcoordpb.AgentRequest_PeerSend{PeerSend: &agentcoordpb.PeerSendRequest{
			ToRole: ParentAddress, Text: "a late answer", InReplyTo: askID,
			Kind: agentcoordpb.MessageKind_MESSAGE_KIND_MESSAGE,
		}},
	})
	require.NoError(t, err)
	require.EqualValues(t, 0, resp.GetStatus().GetCode())
	assert.Empty(t, recvBody(t, c, "a late answer", time.Second),
		"a reply to a timed-out ask must be dropped, as ErrAskTimeout promised, never mailed onward to the child's parent")
}

// TestSpoolSteer_ToAPausedTargetIsNotReportedAsANewTurn pins the surviving
// shape of review finding F2's "REJECTED-but-applied" path. The steer is one
// durable file, so it cannot be refused and applied at once — but it CAN be
// described wrongly: a paused idle target holds the file at its gate until a
// resume, and telling the initiator "new-turn" (woke it into a turn) is a
// report of something that did not happen.
func TestSpoolSteer_ToAPausedTargetIsNotReportedAsANewTurn(t *testing.T) {
	resetStrictness(t)
	teeHome(t)
	sp := cutoverSpawner(0)
	c := newCutoverCoordinator(t, sp, 0)
	out, _ := awaitCutoverChild(t, c, sp, "first task")

	// IDLE is the state whose disposition is "new-turn"; a child still inside
	// its first turn is described as queued whatever the pause says.
	require.Eventually(t, func() bool {
		st, _ := c.observeRecipient(out.Harp)
		return st == StateIdle
	}, conformanceWait, 10*time.Millisecond, "the child must finish its first turn")

	ctx, cancel := context.WithTimeout(context.Background(), conformanceWait)
	defer cancel()
	_, err := c.ControlPause(ctx, humanInitiator(), out.Harp, "human is reviewing")
	require.NoError(t, err)

	outcome, err := c.ControlSteer(ctx, humanInitiator(), out.Harp, "steer while paused", false)
	require.NoError(t, err)
	assert.Equal(t, DeliveryQueued, outcome.Delivery,
		"a paused target is not woken: the steer waits at its gate, which is a queue, not a new turn")
	require.Never(t, func() bool { return countChatText(sp, 0, "steer while paused") > 0 },
		500*time.Millisecond, 10*time.Millisecond, "the paused run must not take the steer")

	_, err = c.ControlResume(ctx, humanInitiator(), out.Harp)
	require.NoError(t, err)
	awaitChatText(t, sp, 0, "steer while paused")
	state, _ := c.observeRecipient(out.Harp)
	assert.NotEqual(t, deliveryPaused, state, "a resume must clear the coordinator's record of the pause")
}
