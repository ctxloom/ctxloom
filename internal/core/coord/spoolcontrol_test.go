package coord

import (
	"context"
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
// write into the child's out/ spool. It carries a sender kind like any mail:
// an answer is ordinary mail to the asker.
func answerAsk(t *testing.T, home TestHome, askID, text string, structured *structpb.Struct) {
	t.Helper()
	resp, err := home.Request(context.Background(), &agentcoordpb.AgentRequest{
		Kind: &agentcoordpb.AgentRequest_PeerSend{PeerSend: &agentcoordpb.PeerSendRequest{
			ToRole: ParentAddress, Text: text, InReplyTo: askID, Structured: structured,
			Kind: agentcoordpb.MessageKind_MESSAGE_KIND_RESULT,
		}},
	})
	require.NoError(t, err)
	require.EqualValues(t, 0, resp.GetStatus().GetCode(), "the reply must be accepted: %s", resp.GetStatus().GetMessage())
}

// askOverTheWire asks harp's child a question from the owner over the
// ControlRun wire and returns the ask id, failing if the ask holds the
// asker's turn instead of answering at once.
func askOverTheWire(t *testing.T, c *Coordinator, harp, text string) string {
	t.Helper()
	select {
	case resp := <-controlRunAsync(t, ownerHome(t, c), &agentcoordpb.ControlQuestion{Harp: harp, Text: text}):
		require.EqualValues(t, 0, resp.GetStatus().GetCode(), resp.GetStatus().GetMessage())
		id := resp.GetControlRun().GetQuestion().GetAskId()
		require.NotEmpty(t, id)
		return id
	case <-time.After(conformanceWait):
		t.Fatal("the ask held the asker's turn")
		return ""
	}
}

// TestSpoolAsk_ChildThatEndsWithoutAnsweringSendsACorrelatedNotice: an asker
// whose child ends without answering hears so, correlated to the ask — not
// silence, and not only the child's uncorrelated exit notice, which cannot
// say which of several outstanding asks went unanswered.
//
// The child's turn DID end with the runner's automatic report quoting the
// ask. That report is not an answer (the cooperative-reply ruling: the answer
// is what the child chose to send), so the ask is still unanswered when the
// child dies, and the notice says so.
func TestSpoolAsk_ChildThatEndsWithoutAnsweringSendsACorrelatedNotice(t *testing.T) {
	resetStrictness(t)
	teeHome(t)
	sp := cutoverSpawner(0)
	c := newCutoverCoordinator(t, sp, 0)
	out, _ := awaitCutoverChildIdle(t, c, sp, "first task")

	askID := askOverTheWire(t, c, out.Harp, "which migration path?")
	require.NotEmpty(t, recvWhere(t, c, func(m Message) bool {
		return m.InReplyTo == askID && IsAutoReport(m.Structured)
	}, conformanceWait), "the turn the ask started must have ended and been reported")
	c.noticeUnansweredAsks(out.Harp)
	require.True(t, askOpen(c, askID), "a LIVE child may still answer, and is owed no notice")

	c.terminateRun(out.RunID, CauseRunnerExit, "engine crashed")

	notice := recvWhere(t, c, func(m Message) bool { return m.Kind == KindExited && m.InReplyTo == askID }, conformanceWait)
	require.Len(t, notice, 1, "the asker must get one terminal notice correlated to the unanswered ask")
	assert.Equal(t, out.Harp, notice[0].From, "the notice is about the child asked")
	assert.Contains(t, notice[0].Body, askID)
	assert.Contains(t, notice[0].Body, CauseRunnerExit, "the notice says why the child ended")
}

// askOpen reports whether askID is still recorded as unanswered.
func askOpen(c *Coordinator, askID string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	_, ok := c.openAsks[askID]
	return ok
}

// TestSpoolAsk_RecordedOpenBeforeItIsPublished pins the ordering: the ask is
// recorded open at the instant before its file can exist, so an answer that
// lands the moment the file is observable finds the record and closes it.
// Recorded afterwards, that answer would close nothing, and the child's end
// would report an answered ask as unanswered.
func TestSpoolAsk_RecordedOpenBeforeItIsPublished(t *testing.T) {
	resetStrictness(t)
	teeHome(t)
	sp := cutoverSpawner(0)
	c := newCutoverCoordinator(t, sp, 0)
	out, home := awaitCutoverChildIdle(t, c, sp, "first task")

	var openAtPublish bool
	c.onAskPublished = func(id string) {
		openAtPublish = askOpen(c, id)
		answerAsk(t, home, id, "answered before the question was even read", nil)
	}
	res, err := c.Control(context.Background(), humanInitiator(),
		ControlRequest{Verb: ControlVerbQuestion, Harp: out.Harp, Body: "are you there?"})
	require.NoError(t, err)
	require.True(t, openAtPublish, "the ask must be recorded open before it is published")
	require.Eventually(t, func() bool { return !askOpen(c, res.AskID) }, conformanceWait, 10*time.Millisecond,
		"an answer racing the publish must still close the ask")
}

// TestSpoolAsk_OnlyTheTargetsDeliberateReplyClosesIt pins the two halves of
// "answered": an id is not authority, so a reply quoting it from any harp but
// the target leaves the ask open; and the runner's automatic turn report,
// which quotes the id of the ask that started the turn, is not the child
// choosing to answer. Uncorrelated output closes nothing either.
func TestSpoolAsk_OnlyTheTargetsDeliberateReplyClosesIt(t *testing.T) {
	resetStrictness(t)
	teeHome(t)
	sp := cutoverSpawner(0)
	c := newCutoverCoordinator(t, sp, 0)
	out, home := awaitCutoverChildIdle(t, c, sp, "first task")

	askID := askOverTheWire(t, c, out.Harp, "which migration path?")
	require.NotEmpty(t, recvWhere(t, c, func(m Message) bool {
		return m.InReplyTo == askID && IsAutoReport(m.Structured)
	}, conformanceWait), "the turn the ask started must have been reported automatically")
	assert.True(t, askOpen(c, askID), "the automatic turn report is not the answer")

	c.settleAsk("not-the-target", askID, nil)
	assert.True(t, askOpen(c, askID), "a reply from a harp that was not asked must not close the ask")

	resp, err := home.Request(context.Background(), &agentcoordpb.AgentRequest{
		Kind: &agentcoordpb.AgentRequest_PeerSend{PeerSend: &agentcoordpb.PeerSendRequest{
			ToRole: ParentAddress, Text: "unrelated turn output", Kind: agentcoordpb.MessageKind_MESSAGE_KIND_RESULT,
		}},
	})
	require.NoError(t, err)
	require.EqualValues(t, 0, resp.GetStatus().GetCode())
	require.NotEmpty(t, recvBody(t, c, "unrelated turn output", conformanceWait))
	assert.True(t, askOpen(c, askID), "uncorrelated output must not close the ask")

	answerAsk(t, home, askID, "the reversible one", nil)
	require.Eventually(t, func() bool { return !askOpen(c, askID) }, conformanceWait, 10*time.Millisecond,
		"the target's deliberate reply closes the ask")
}

// TestSpoolAsk_UnreadAskOfAnEndedChildIsNotNoticed: an ask still unread in an
// ended child's in/ may yet be answered — the ended-child rule resumes the
// child for it — so it is not reported unanswered until it has been taken.
func TestSpoolAsk_UnreadAskOfAnEndedChildIsNotNoticed(t *testing.T) {
	resetStrictness(t)
	teeHome(t)
	sp := cutoverSpawner(0)
	c := newCutoverCoordinator(t, sp, 0)
	out, _ := awaitCutoverChildIdle(t, c, sp, "first task")
	// Stopped: the one cause the ended-child rule does not resume, so the run
	// stays ended while the test holds an unread ask in its spool.
	c.terminateRun(out.RunID, CauseStopped, "test terminal")

	const askID = "m-unread-ask"
	ref := writeInSpool(t, out.Harp, KindQuestion, askID, "still unread")
	c.mu.Lock()
	c.openAsks = map[string]openAsk{askID: {target: out.Harp, kind: KindQuestion}}
	c.mu.Unlock()

	c.noticeUnansweredAsks(out.Harp)
	assert.True(t, askOpen(c, askID), "an unread ask may still be answered and must not be reported unanswered")

	_, err := spool.Withdraw(spool.NewHomeMapper(), ref)
	require.NoError(t, err)
	c.noticeUnansweredAsks(out.Harp)
	assert.False(t, askOpen(c, askID))
	notice := recvWhere(t, c, func(m Message) bool { return m.Kind == KindExited && m.InReplyTo == askID }, conformanceWait)
	require.Len(t, notice, 1, "once taken and unanswered, the ask is reported once, correlated to it")
	assert.Contains(t, notice[0].Body, CauseStopped)
}

// TestSpoolAsk_AnAskThatFailsToPublishIsNotLeftOpen: an ask whose file was
// never written was never asked, so nothing may later report it unanswered.
func TestSpoolAsk_AnAskThatFailsToPublishIsNotLeftOpen(t *testing.T) {
	resetStrictness(t)
	teeHome(t)
	sp := cutoverSpawner(0)
	c := newCutoverCoordinator(t, sp, 0)
	out, _ := awaitCutoverChildIdle(t, c, sp, "first task")

	_, err := c.controlAsk(humanInitiator(), out.Harp, KindQuestion, strings.Repeat("x", ArtifactUploadSizeCap+1))
	require.ErrorIs(t, err, ErrBodyTooLarge)
	c.mu.Lock()
	defer c.mu.Unlock()
	assert.Empty(t, c.openAsks, "a failed publish must not leave an open ask behind")
}

// TestSpoolAsk_EmptyTextIsRefused: empty input fails rather than asking
// nothing.
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

	_, err = c.controlAsk(humanInitiator(), out.Harp, KindQuestion, "")
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
