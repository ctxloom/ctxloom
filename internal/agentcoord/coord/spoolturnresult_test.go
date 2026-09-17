package coord

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	agentcoordpb "github.com/ctxloom/ctxloom/internal/agentcoord"
	"github.com/ctxloom/ctxloom/internal/agentcoord/spool"
)

// Tests for the RESULT PLANE (spoolturnresult.go): a child's automatic turn
// report is written by its own runner into its own out/, exactly once.

// bridgedResultFor runs one child under the given coordinator and returns the
// result message its parent received, waiting for it.
//
// Selecting the RESULT kind matters: the owner's mailbox also carries other
// traffic, and a test that took the first message would be asserting about
// whatever arrived first.
func bridgedResultFor(t *testing.T, c *Coordinator, wait time.Duration) Message {
	t.Helper()
	msgs := recvKind(t, c, KindResult, wait)
	require.NotEmpty(t, msgs, "the parent never received this child's turn result")
	return msgs[0]
}

// ownerResultsFrom reads the owner's SPOOL off disk — in/ and in/consumed/
// together — and returns every result-kind message routed to it from harp.
//
// Both directories, because a file is in exactly one of them: delivered but
// unacked, or acked. Neither alone can answer "how many reports were there",
// and the owner's in/ is the durable record of what reached it, the way the
// mailbox journal was before the owner became a spool recipient.
func ownerResultsFrom(t *testing.T, harp string) []Message {
	t.Helper()
	var out []Message
	for _, dir := range []spool.Dir{spool.DirIn, spool.DirInConsumed} {
		for _, e := range spoolEntries(t, ownerIdentity().Harp, dir) {
			if e.Message.FromHarp != harp {
				continue
			}
			m, err := mailFromSpool(e, e.Message.FromHarp)
			require.NoError(t, err)
			if m.Kind == KindResult {
				out = append(out, m)
			}
		}
	}
	return out
}

// TestSpoolTurnResult_CorrelatesToTheMessageThatStartedTheTurn pins the one
// thing the file carries that the mailbox bridge could not: which delivery
// this turn was about.
//
// A parent that sent three children the same question can tell which answer
// answers which ask — without a convention, and without the child having to
// cooperate.
func TestSpoolTurnResult_CorrelatesToTheMessageThatStartedTheTurn(t *testing.T) {
	resetStrictness(t)
	teeHome(t)
	sp := cutoverSpawner(0)
	c := newCutoverCoordinator(t, sp, 0)
	out, _ := awaitCutoverChildIdle(t, c, sp, "first task")
	// Drain the briefing turn's own report so the assertion below is about the
	// turn this test starts.
	require.NotEmpty(t, bridgedResultFor(t, c, conformanceWait))

	msgID, _, _, err := c.peerSend(ownerIdentity(), out.Harp, KindMessage, "check the lockfile", nil, "")
	require.NoError(t, err)
	require.NotEmpty(t, msgID)

	got := recvWhere(t, c, func(m Message) bool {
		return m.Kind == KindResult && m.InReplyTo == msgID
	}, conformanceWait)
	require.NotEmpty(t, got, "the turn's report must quote the message that started it")
	assert.Contains(t, got[0].Body, "check the lockfile",
		"and it must be the report of THAT turn, not a correlation stapled to an unrelated one")

	// A turn nothing delivered started — the briefing — carries no
	// correlation, rather than a fabricated one.
	entries := spoolEntries(t, out.Harp, spool.DirOutConsumed)
	require.NotEmpty(t, entries)
	var sawUncorrelated bool
	for _, e := range entries {
		if e.Message.InReplyTo == "" {
			sawUncorrelated = true
		}
	}
	assert.True(t, sawUncorrelated, "the briefing turn's report must have an EMPTY in_reply_to, not an invented one")
}

// TestSpoolTurnResult_ExactlyOnceFileXorBridge is the double-delivery pin, in
// both of the shapes it could take: the coordinator ALSO bridging, and the
// file being routed twice.
func TestSpoolTurnResult_ExactlyOnceFileXorBridge(t *testing.T) {
	resetStrictness(t)
	teeHome(t)
	sp := cutoverSpawner(0)
	c := newCutoverCoordinator(t, sp, 0)
	out, home := awaitCutoverChildIdle(t, c, sp, "one turn only")

	first := bridgedResultFor(t, c, conformanceWait)
	require.NotEmpty(t, first.Body)

	// EXACTLY ONE REPORT, read off the owner's spool on DISK rather than
	// through a receive: a message that was delivered and then acked is gone
	// from a receive's view, so a test that read that would call a double
	// delivery a success.
	//
	// Both carriers end as a file in the owner's in/ — the routing hop turns
	// the runner's out/ file into mail for the owner, and a bridge would queue
	// mail for the owner too — and what separates them is the marker. So the
	// assertion is "one report, and it is the runner's": a bridge that also
	// fired would add an unmarked second one.
	reports := ownerResultsFrom(t, out.Harp)
	require.Len(t, reports, 1,
		"the coordinator must not ALSO bridge a cut-over child's turn: the parent would read the same turn twice")
	assert.True(t, isAutoReport(reports[0].Structured),
		"the surviving report must be the one the RUNNER wrote; an unmarked one is the bridge having fired")

	// Hammer every in-process trigger. The consume-rename is the arbiter.
	for i := 0; i < 20; i++ {
		c.spoolReactor.mark(out.Harp)
		home.SweepSpoolIn()
	}
	require.Never(t, func() bool {
		got := recvWhere(t, c, func(m Message) bool { return m.Body == first.Body }, 10*time.Millisecond)
		return len(got) > 0
	}, 500*time.Millisecond, 25*time.Millisecond,
		"repeated sweeps of a routed report must not deliver it again")
}

// TestSpoolTurnResult_AskStaysParkedUntilTheDeliberateReply is THE COLLISION,
// pinned as its own case.
//
// A question is delivered; the child's turn produces an automatic report that
// quotes the question's id. Both rulings must hold at once: the ask is
// answered only by what the child CHOSE to send, and the report still reaches
// the parent with its correlation intact.
func TestSpoolTurnResult_AskStaysParkedUntilTheDeliberateReply(t *testing.T) {
	resetStrictness(t)
	teeHome(t)
	sp := cutoverSpawner(0)
	c := newCutoverCoordinator(t, sp, 0)
	out, home := awaitCutoverChildIdle(t, c, sp, "first task")

	askIDs := make(chan string, 1)
	c.onAskPublished = func(id string) { askIDs <- id }
	answers := make(chan AskAnswer, 1)
	errs := make(chan error, 1)
	ctx, cancel := context.WithTimeout(context.Background(), conformanceWait)
	defer cancel()
	go func() {
		ans, err := c.ControlQuestion(ctx, humanInitiator(), out.Harp, "why sqlx?")
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

	// The child's turn runs and its runner reports it automatically —
	// correlated to the ask, because that is what started the turn.
	report := recvWhere(t, c, func(m Message) bool {
		return m.Kind == KindResult && m.InReplyTo == askID
	}, conformanceWait)
	require.NotEmpty(t, report, "the automatic report must still reach the parent, correlation and all")
	assert.Contains(t, report[0].Body, "why sqlx?")
	assert.True(t, isAutoReport(report[0].Structured), "and it must be marked as the runner's composition")

	// AND THE ASK IS STILL PARKED. This is the whole collision: that report
	// quoted the ask's id, and correlation is what resolves an ask.
	select {
	case ans := <-answers:
		t.Fatalf("an automatic turn report answered the ask: %q", ans.Text)
	case err := <-errs:
		t.Fatalf("the ask failed instead of staying parked: %v", err)
	default:
	}

	// Only the child's own, deliberate reply answers it.
	answerAsk(t, home, askID, "compile-time checked queries", nil)
	select {
	case ans := <-answers:
		assert.Equal(t, "compile-time checked queries", ans.Text)
	case err := <-errs:
		t.Fatalf("the deliberate reply did not resolve the ask: %v", err)
	case <-time.After(conformanceWait):
		t.Fatal("the deliberate reply never resolved the ask")
	}
}

// TestSpoolAsk_DeliberateResultKindReplyStillAnswers is why the collision is
// resolved by AUTHORSHIP and not by KIND.
//
// A child answering an ask with its findings naturally sends KindResult — the
// same kind an automatic report carries. A resolver that told the two apart by
// kind would refuse this reply, and the asker would sit out its whole budget
// and report that the child never answered a question the child answered. That
// is this project's characteristic defect, and it is what this test makes
// impossible to reintroduce quietly.
func TestSpoolAsk_DeliberateResultKindReplyStillAnswers(t *testing.T) {
	resetStrictness(t)
	teeHome(t)
	sp := cutoverSpawner(0)
	c := newCutoverCoordinator(t, sp, 0)
	out, home := awaitCutoverChildIdle(t, c, sp, "first task")

	c.onAskPublished = func(askID string) {
		structured := mustStruct(t, map[string]any{"kind": KindResult})
		resp, err := home.Request(context.Background(), &agentcoordpb.AgentRequest{
			Kind: &agentcoordpb.AgentRequest_PeerSend{PeerSend: &agentcoordpb.PeerSendRequest{
				ToRole: ParentAddress, Text: "sqlx, for the compile-time checks", InReplyTo: askID, Structured: structured,
			}},
		})
		require.NoError(t, err)
		require.EqualValues(t, 0, resp.GetStatus().GetCode(), "%s", resp.GetStatus().GetMessage())
	}
	ctx, cancel := context.WithTimeout(context.Background(), conformanceWait)
	defer cancel()
	ans, err := c.ControlQuestion(ctx, humanInitiator(), out.Harp, "which driver?")
	require.NoError(t, err, "a deliberate reply must answer the ask whatever kind the child chose")
	assert.Equal(t, "sqlx, for the compile-time checks", ans.Text)
	assert.False(t, isAutoReport(ans.Structured), "and it is the CHILD's message, not a composed report")
}

// TestSpoolTurnResult_SelfReportSuppressesIt pins the no-double-delivery rule
// on its runner-side home: a child that reported in its own words during the
// turn must not also have the runner report the same turn.
func TestSpoolTurnResult_SelfReportSuppressesIt(t *testing.T) {
	resetStrictness(t)
	teeHome(t)
	sp := cutoverSpawner(0)
	c := newCutoverCoordinator(t, sp, 0)
	_, home := awaitCutoverChildIdle(t, c, sp, "first task")
	require.NotEmpty(t, bridgedResultFor(t, c, conformanceWait), "the briefing turn reports normally")

	// The child sends its own report, then a turn boundary passes.
	resp, err := home.Request(context.Background(), &agentcoordpb.AgentRequest{
		Kind: &agentcoordpb.AgentRequest_PeerSend{PeerSend: &agentcoordpb.PeerSendRequest{
			ToRole: ParentAddress, Text: "in my own words", Kind: agentcoordpb.MessageKind_MESSAGE_KIND_RESULT,
		}},
	})
	require.NoError(t, err)
	require.EqualValues(t, 0, resp.GetStatus().GetCode())
	require.NoError(t, home.ReportTurnResult("whatever the model happened to say", ""))

	require.NotEmpty(t, recvBody(t, c, "in my own words", conformanceWait), "the child's own report must arrive")
	assert.Empty(t, recvBody(t, c, "whatever the model happened to say", 300*time.Millisecond),
		"a child that already reported must not have the same turn reported for it as well")
}

// TestSpoolTurnResult_EmptyTurnIsReportedAsAnError pins the empty-turn arm on
// the file plane. The bridge's own diagnostic went to COORDINATOR stderr — a
// channel the parent, an agent whose sole input is its mail, cannot read — and
// the mailbox notice it grew is what the file has to preserve.
//
// An empty body is not written as an empty result: that is this project's
// signature silent no-op, not a report.
func TestSpoolTurnResult_EmptyTurnIsReportedAsAnError(t *testing.T) {
	resetStrictness(t)
	teeHome(t)
	sp := cutoverSpawner(0)
	c := newCutoverCoordinator(t, sp, 0)
	out, home := awaitCutoverChildIdle(t, c, sp, "first task")
	require.NotEmpty(t, bridgedResultFor(t, c, conformanceWait))

	require.NoError(t, home.ReportTurnResult("   \n  ", ""))

	got := recvKind(t, c, KindError, conformanceWait)
	require.NotEmpty(t, got, "an empty turn must reach the PARENT, not just the runner's stderr")
	assert.Contains(t, got[0].Body, "no output")
	assert.Contains(t, got[0].Body, out.Harp)
	assert.Equal(t, out.Harp, got[0].From)
}

// TestSpoolTurnResult_RestartWindowDeliversByOneCarrier covers the seam S5a
// documented: between a coordinator's adopt() and the child's respawn a
// cut-over harp is not yet tracked, so the cutover predicate reads false.
//
// A report written into out/ during that window must still arrive, and must
// arrive ONCE. It does because the two carriers are decided by who WROTE the
// report, not by who reads it: the runner wrote a file, so no bridge exists to
// duplicate it, and the fresh coordinator's startup sweep routes that file
// exactly as it routes any other.
func TestSpoolTurnResult_RestartWindowDeliversByOneCarrier(t *testing.T) {
	resetStrictness(t)
	teeHome(t)
	stateDir := t.TempDir()

	sp := cutoverSpawner(0)
	teeHome(t)
	first, err := New(Options{ProjectDir: t.TempDir(), StateDir: stateDir, Spawner: sp, OwnerHarp: ownerIdentity().Harp})
	require.NoError(t, err)
	require.NoError(t, first.Serve())
	out, _ := awaitCutoverChild(t, first, sp, "first task")
	first.Close()

	// The runner reports a turn while nothing is listening — the window.
	w, err := spool.NewWriter(spool.NewHomeMapper(), out.Harp, spool.DirOut, out.Harp)
	require.NoError(t, err)
	structured, err := json.Marshal(map[string]any{autoReportKey: true})
	require.NoError(t, err)
	var head map[string]any
	require.NoError(t, json.Unmarshal(structured, &head))
	_, err = w.Write(&spool.Message{
		Kind: KindResult, FromHarp: out.Harp, To: ParentAddress,
		Body: "reported across the restart", Structured: head,
	})
	require.NoError(t, err)

	teeHome(t)
	second, err := New(Options{ProjectDir: t.TempDir(), StateDir: stateDir, Spawner: newFakeSpawner(nil, nil), OwnerHarp: ownerIdentity().Harp})
	require.NoError(t, err)
	require.NoError(t, second.Serve())
	t.Cleanup(second.Close)

	got := recvBody(t, second, "reported across the restart", conformanceWait)
	require.Len(t, got, 1, "a report written in the restart window must arrive exactly once")
	assert.Equal(t, out.Harp, got[0].From)
	assert.True(t, isAutoReport(got[0].Structured), "and it must still be recognisable as the runner's composition")
	assert.Empty(t, recvBody(t, second, "reported across the restart", 300*time.Millisecond),
		"and not a second time on the next sweep")
}
