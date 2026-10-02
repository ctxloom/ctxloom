package coord

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	agentcoordpb "github.com/ctxloom/ctxloom/internal/adapters/coordgrpc/pb"
	"github.com/ctxloom/ctxloom/internal/core/agent"
	"github.com/ctxloom/ctxloom/internal/core/spool"
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

// ownerResultsFrom reads the owner's SPOOL off disk — in/ and in/claimed/
// together — and returns every result-kind message routed to it from harp
// that the owner has NOT yet delivered (a delivered file is deleted). For a
// count that includes delivered ones, see ownerResultsRouted.
// ownerResultsRouted counts every result-kind message the coordinator wrote
// into the owner's in/ FROM harp, delivered or not: the audit journal's
// spool_mail_out entries, which outlive the files they record.
func ownerResultsRouted(t *testing.T, c *Coordinator, harp string) int {
	t.Helper()
	n := 0
	for _, e := range readAuditKind(t, c, "spool_mail_out") {
		if e.Actor == ownerIdentity().Harp && e.Detail["from"] == harp && e.Detail["kind"] == KindResult {
			n++
		}
	}
	return n
}

func ownerResultsFrom(t *testing.T, harp string) []Message {
	t.Helper()
	var out []Message
	for _, dir := range []spool.Dir{spool.DirIn, spool.ClaimedDirName} {
		for _, e := range spoolEntries(t, ownerIdentity().Harp, dir) {
			if e.Message.FromHarp != harp {
				continue
			}
			m, err := MailFromSpool(e, e.Message.FromHarp)
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

	msgID, _, err := c.peerSend(ownerIdentity(), out.Harp, KindMessage, "check the lockfile", nil, "")
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

	// EXACTLY ONE REPORT, counted from the audit journal rather than through
	// a receive: a message that was delivered and then acked is gone from a
	// receive's view — and, deleted, from the disk — so a test that read
	// either would call a double delivery a success.
	//
	// Both carriers end as a file in the owner's in/ — the routing hop turns
	// the runner's out/ file into mail for the owner, and a bridge would queue
	// mail for the owner too — and what separates them is the marker. So the
	// assertion is "one report, and it is the runner's": a bridge that also
	// fired would add an unmarked second one.
	require.Equal(t, 1, ownerResultsRouted(t, c, out.Harp),
		"the coordinator must not ALSO bridge a cut-over child's turn: the parent would read the same turn twice")
	assert.True(t, IsAutoReport(first.Structured),
		"the surviving report must be the one the RUNNER wrote; an unmarked one is the bridge having fired")

	// Hammer every in-process trigger. The delivered record is the arbiter.
	for i := 0; i < 20; i++ {
		c.spoolReactor.Mark(out.Harp)
		home.SweepSpoolIn()
	}
	// A synchronous window, not require.Never: Never runs its condition on a
	// goroutine it does not join when its timer fires, and this condition is
	// a receive — it claims and acks in the owner's in/ spool. Left running,
	// it recreates in/claimed/ or in/delivered/ under a HOME the test's
	// cleanup is already removing.
	again := recvWhere(t, c, func(m Message) bool { return m.Body == first.Body }, 500*time.Millisecond)
	require.Empty(t, again, "repeated sweeps of a routed report must not deliver it again")
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
	require.NoError(t, home.ReportTurnResult("whatever the model happened to say", "", nil))

	require.NotEmpty(t, recvBody(t, c, "in my own words", conformanceWait), "the child's own report must arrive")
	assert.Empty(t, recvBody(t, c, "whatever the model happened to say", 300*time.Millisecond),
		"a child that already reported must not have the same turn reported for it as well")
}

// TestSpoolTurnResult_BlockedTurnSaysBlocked pins what the parent reads
// when a child's engine refused a tool call: "blocked on X", never a plain
// result that reads as done — even when the turn said nothing else, which
// is NOT the empty-turn error (the turn has something to report: what
// stopped it). The structured payload lists each refusal, and the report is
// still the runner's automatic one.
func TestSpoolTurnResult_BlockedTurnSaysBlocked(t *testing.T) {
	resetStrictness(t)
	teeHome(t)
	sp := cutoverSpawner(0)
	c := newCutoverCoordinator(t, sp, 0)
	out, home := awaitCutoverChildIdle(t, c, sp, "first task")
	require.NotEmpty(t, bridgedResultFor(t, c, conformanceWait))

	denial := agent.PermissionDenial{ToolName: "Bash", ToolCallID: "t1", Reason: "needs approval"}
	require.NoError(t, home.ReportTurnResult("I could not run the migration.", "", []agent.PermissionDenial{denial}))
	got := recvWhere(t, c, func(m Message) bool { return strings.HasPrefix(m.Body, "BLOCKED") }, conformanceWait)
	require.Len(t, got, 1, "the parent must hear the turn was blocked")
	assert.Equal(t, KindResult, got[0].Kind)
	assert.Equal(t, out.Harp, got[0].From)
	assert.Contains(t, got[0].Body, "BLOCKED on Bash: needs approval (decided by policy)")
	assert.Contains(t, got[0].Body, "I could not run the migration.", "what the turn did say still arrives")
	assert.True(t, IsAutoReport(got[0].Structured))
	var structured struct {
		Blocked []BlockedCall `json:"blocked"`
	}
	require.NoError(t, json.Unmarshal(got[0].Structured, &structured))
	assert.Equal(t, []BlockedCall{{Tool: "Bash", Reason: "needs approval", Decider: "policy"}}, structured.Blocked)

	require.NoError(t, home.ReportTurnResult("  ", "", []agent.PermissionDenial{{ToolName: "Write"}}))
	got = recvWhere(t, c, func(m Message) bool { return strings.HasPrefix(m.Body, "BLOCKED on Write") }, conformanceWait)
	require.Len(t, got, 1, "a blocked turn that said nothing else is still a blocked report")
	assert.Equal(t, KindResult, got[0].Kind, "not the empty-turn error: the turn has a report — what stopped it")
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

	require.NoError(t, home.ReportTurnResult("   \n  ", "", nil))

	got := recvKind(t, c, KindError, conformanceWait)
	require.NotEmpty(t, got, "an empty turn must reach the PARENT, not just the runner's stderr")
	assert.Contains(t, got[0].Body, "no output")
	assert.Contains(t, got[0].Body, out.Harp)
	assert.Equal(t, out.Harp, got[0].From)
}

// TestSpoolTurnResult_RestartWindowDeliversByOneCarrier covers the restart
// window: between a coordinator's adopt() and the child's respawn a
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
	require.NoError(t, runnerHooks.Serve(first))
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
	require.NoError(t, runnerHooks.Serve(second))
	t.Cleanup(second.Close)

	got := recvBody(t, second, "reported across the restart", conformanceWait)
	require.Len(t, got, 1, "a report written in the restart window must arrive exactly once")
	assert.Equal(t, out.Harp, got[0].From)
	assert.True(t, IsAutoReport(got[0].Structured), "and it must still be recognisable as the runner's composition")
	assert.Empty(t, recvBody(t, second, "reported across the restart", 300*time.Millisecond),
		"and not a second time on the next sweep")
}
