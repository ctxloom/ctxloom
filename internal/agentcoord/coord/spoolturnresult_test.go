package coord

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	agentcoordpb "github.com/ctxloom/ctxloom/internal/agentcoord"
	"github.com/ctxloom/ctxloom/internal/agentcoord/spool"
)

// Tests for the RESULT PLANE's cutover (spoolturnresult.go): a cut-over
// child's automatic turn report is written by its own runner into its own
// out/, and the coordinator's bridge does not fire for that run.
//
// The two halves of exactly-once are asserted separately because they fail
// differently: "the file arrives" is a delivery, "the bridge is silent" is an
// absence, and an absence is only meaningful against a run that would
// otherwise have produced one — which is why the flag-off twin runs the same
// script and IS compared against.

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

// TestSpoolTurnResult_RidesTheFileWithTheBridgesOwnPayload is the payload
// equivalence assertion, and it is made against the OTHER CARRIER rather than
// against a literal: the same agent, the same prompt and the same scripted
// engine are run once with the cutover on and once with it off, and what the
// parent reads must be the same message.
//
// A literal expectation would only pin what this build happens to compose. The
// bridge is the thing being replaced, so the bridge is the specification.
func TestSpoolTurnResult_RidesTheFileWithTheBridgesOwnPayload(t *testing.T) {
	resetStrictness(t)

	// --- the bridge (flag off) ---
	teeHome(t)
	bridgeSp := cutoverSpawner(0)
	bridgeC := newTestCoordinator(t, bridgeSp, nil)
	require.False(t, bridgeC.SpoolDeliveryEnabled())
	_, err := bridgeC.AgentRun(context.Background(), ownerIdentity(), "worker", "summarise the lockfile", "", "")
	require.NoError(t, err)
	viaBridge := bridgedResultFor(t, bridgeC, conformanceWait)
	require.NotEmpty(t, viaBridge.Body, "the bridge's own message must not be empty, or this comparison proves nothing")

	// --- the file (flag on) ---
	teeHome(t)
	sp := cutoverSpawner(0)
	c := newCutoverCoordinator(t, sp, 0)
	out, _ := awaitCutoverChild(t, c, sp, "summarise the lockfile")
	viaFile := bridgedResultFor(t, c, conformanceWait)

	assert.Equal(t, viaBridge.Kind, viaFile.Kind, "the kind the parent reads must not change with the carrier")
	assert.Equal(t, viaBridge.Body, viaFile.Body, "the report's text must be byte-identical: one substrate changed, not what the child said")
	assert.Equal(t, viaBridge.From, viaFile.From, "the sender is the child either way")
	assert.Equal(t, out.Harp, viaFile.From, "and it is resolved from the spool the file was found in")

	// The one ADDITION, named rather than smuggled: the file-borne report is
	// marked as automatic, because its correlation would otherwise let it pass
	// for something the child chose to send (see peerSend's collision note).
	assert.Empty(t, viaBridge.Structured, "the bridge's message carried no structured payload")
	require.NotEmpty(t, viaFile.Structured)
	assert.True(t, isAutoReport(viaFile.Structured),
		"the file-borne report must declare that the runner composed it, not the agent")

	// And it really was a file.
	awaitSpoolEntryWithBody(t, out.Harp, spool.DirOutConsumed, viaFile.Body,
		"the report must have travelled as a file in the child's own out/ spool")
}

// queuedResultsFrom reads the mailbox JOURNAL off disk and returns every
// result-kind message queued from harp, oldest first.
//
// The journal rather than the fold, because the fold forgets: a queued message
// that was consumed leaves `pending` empty and `seen` holding only its id, so
// neither can answer "how many reports were there". The durable fact can.
func queuedResultsFrom(t *testing.T, c *Coordinator, harp string) []mailQueued {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(c.stateDir, "mailbox.jsonl"))
	if os.IsNotExist(err) {
		return nil
	}
	require.NoError(t, err)
	var out []mailQueued
	for _, line := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
		if line == "" {
			continue
		}
		var f Fact
		require.NoError(t, json.Unmarshal([]byte(line), &f))
		if f.Kind != factMailQueued {
			continue
		}
		var q mailQueued
		require.NoError(t, json.Unmarshal(f.Data, &q))
		if q.From == harp && q.Kind == KindResult {
			out = append(out, q)
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

	// EXACTLY ONE REPORT, read off the DURABLE JOURNAL rather than the live
	// fold: a message that was queued and then consumed leaves the fold's
	// pending list empty, so a test that read that would call a double
	// delivery a success.
	//
	// Both carriers end as owner-mailbox mail — the owner is not itself a
	// spool run, so the routing hop converts the file into mail for it — and
	// what separates them is the marker. So the assertion is "one report, and
	// it is the runner's": a bridge that also fired would add an unmarked
	// second one.
	reports := queuedResultsFrom(t, c, out.Harp)
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
			ToRole: ParentAddress, Text: "in my own words",
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

// TestSpoolTurnResult_FlagOffStillBridgesAndTouchesNoDisk is the flag-off
// half: with the cutover unset the coordinator's bridge still delivers the
// report, and nothing writes a spool directory.
func TestSpoolTurnResult_FlagOffStillBridgesAndTouchesNoDisk(t *testing.T) {
	resetStrictness(t)
	home := teeHome(t)
	sp := cutoverSpawner(0)
	c := newTestCoordinator(t, sp, nil)

	_, err := c.AgentRun(context.Background(), ownerIdentity(), "worker", "do the thing", "", "")
	require.NoError(t, err)

	got := bridgedResultFor(t, c, conformanceWait)
	assert.Contains(t, got.Body, "do the thing", "the bridge must still deliver the turn's own output")
	assert.Empty(t, got.Structured, "the bridge's message is unchanged: no marker, because nothing correlates it")
	assert.Empty(t, got.InReplyTo)
	assert.Empty(t, spoolDirsUnder(t, home), "a bridged run must create no spool directory anywhere under HOME")
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
	first, err := New(Options{ProjectDir: t.TempDir(), StateDir: stateDir, Spawner: sp, SpoolDelivery: true})
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

	second, err := New(Options{ProjectDir: t.TempDir(), StateDir: stateDir, Spawner: newFakeSpawner(nil, nil), SpoolDelivery: true})
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
