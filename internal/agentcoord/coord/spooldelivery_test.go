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
	"google.golang.org/protobuf/types/known/structpb"

	agentcoordpb "github.com/ctxloom/ctxloom/internal/agentcoord"
	"github.com/ctxloom/ctxloom/internal/agentcoord/spool"
)

// Tests for the MAIL-PLANE CUTOVER (spooldelivery.go): under
// delegation.spool_delivery the file IS the delivery, in both directions.
//
// Every test redirects HOME (teeHome) before anything can resolve a spool
// path, for the reason that helper's own doc gives: paths.HarpPersistDir
// resolves against $HOME, so a test that forgot would deliver mail into the
// developer's real session store and pass.

// newCutoverCoordinator is newTestCoordinator with the CUTOVER on, and an
// explicit sweep cadence.
//
// sweep is a parameter rather than a package default because the two things
// tests need from it are opposite: a test proving the SWEEP recovers a
// never-rung doorbell has to let it run, and every other test wants the
// production cadence precisely so that it can never be what made the test
// pass. A test that got its delivery from a fast timer instead of the
// doorbell would be reporting the doorbell works when it does not.
func newCutoverCoordinator(t *testing.T, sp Spawner, sweep time.Duration) *Coordinator {
	t.Helper()
	c, err := New(Options{
		ProjectDir:         t.TempDir(),
		StateDir:           t.TempDir(),
		Spawner:            sp,
		SpoolDelivery:      true,
		SpoolSweepInterval: sweep,
	})
	require.NoError(t, err, "new cutover coordinator")
	require.NoError(t, c.Serve(), "serve cutover coordinator")
	t.Cleanup(c.Close)
	return c
}

// cutoverSpawner is startRunSpawner plus the runner capabilities a migrated
// child advertises. Only a runner-backed child is cut over, so every test
// here rides the StartRun path.
func cutoverSpawner(sweep time.Duration) *fakeSpawner {
	sp := startRunSpawner(nil)
	sp.engineCaps = RunnerCapabilities(true)
	sp.spoolSweepInterval = sweep
	return sp
}

// awaitCutoverChild spawns "worker", waits for its runner and engine to be
// up, and returns the run plus its Home. It asserts the CUTOVER reached the
// runner: a coordinator cut over alone would write files nothing reads, and
// every test below would then be measuring the sweep of an empty directory.
func awaitCutoverChild(t *testing.T, c *Coordinator, sp *fakeSpawner, prompt string) (*RunOutcome, *Home) {
	t.Helper()
	out, err := c.AgentRun(context.Background(), ownerIdentity(), "worker", prompt, "", "")
	require.NoError(t, err)
	ctx, cancel := context.WithTimeout(context.Background(), conformanceWait)
	defer cancel()
	require.NoError(t, c.awaitChildUp(ctx, out.Harp), "the migrated child never came up")
	require.Eventually(t, func() bool { return sp.engineHome(0) != nil }, conformanceWait, 10*time.Millisecond,
		"the runner half never appeared")
	home := sp.engineHome(0)
	require.True(t, home.SpoolDeliveryEnabled(),
		"the runner must have learned the cutover from the coordinator's per-spawn env stamp; "+
			"a run cut over on one side only delivers nothing at all")
	return out, home
}

// awaitChatText waits until the i-th scripted engine has been driven with a
// turn containing want, and returns every recorded turn.
func awaitChatText(t *testing.T, sp *fakeSpawner, i int, want string) []string {
	t.Helper()
	require.NotEmpty(t, want, "waiting for an EMPTY text would be satisfied by any turn at all")
	var texts []string
	require.Eventually(t, func() bool {
		sc := sp.chat(i)
		if sc == nil {
			return false
		}
		texts = sc.recordedTexts()
		for _, got := range texts {
			if strings.Contains(got, want) {
				return true
			}
		}
		return false
	}, conformanceWait, 10*time.Millisecond, "the child engine was never driven with %q (saw %q)", want, texts)
	return texts
}

// countChatText counts how many of the i-th engine's turns contain want.
func countChatText(sp *fakeSpawner, i int, want string) int {
	sc := sp.chat(i)
	if sc == nil {
		return 0
	}
	n := 0
	for _, got := range sc.recordedTexts() {
		if strings.Contains(got, want) {
			n++
		}
	}
	return n
}

// mailboxEverQueued reports every message id the mailbox fold has EVER seen —
// pending and consumed alike.
//
// It is the "no mailbox twin" assertion, and it has to read `seen` rather than
// `pending`: a message that was queued AND consumed leaves `pending` empty, so
// a test that checked pending would call a full mailbox delivery a successful
// cutover.
func mailboxEverQueued(c *Coordinator) []string {
	var ids []string
	c.mail.View(func() {
		for id := range c.mailF.seen {
			ids = append(ids, id)
		}
	})
	return ids
}

// awaitSpoolCount waits for a spool directory to hold exactly n entries.
func awaitSpoolCount(t *testing.T, harp string, dir spool.Dir, n int, why string) []spool.Entry {
	t.Helper()
	var got []spool.Entry
	require.Eventually(t, func() bool {
		got = spoolEntries(t, harp, dir)
		return len(got) == n
	}, conformanceWait, 10*time.Millisecond, "%s: %s should hold %d file(s), holds %d", why, dir, n, len(got))
	return got
}

// TestSpoolDelivery_DisabledDeliversByMailboxAndTouchesNoDisk pins the
// flag-off promise, which is the only thing that makes shipping a half-rolled
// cutover defensible: with delegation.spool_delivery unset, a full two-way
// exchange behaves exactly as it always did and leaves the filesystem as it
// found it.
//
// It asserts the ABSENCE of the spool directory rather than an empty one. A
// cutover that created its directories eagerly and delivered by mailbox would
// pass a "no files" check while already having changed what an operator sees
// on disk — and consumed/ appearing is precisely the trace a half-entered
// cutover would leave.
func TestSpoolDelivery_DisabledDeliversByMailboxAndTouchesNoDisk(t *testing.T) {
	resetStrictness(t)
	home := teeHome(t)
	sp := cutoverSpawner(0)
	c := newTestCoordinator(t, sp, nil)
	require.False(t, c.SpoolDeliveryEnabled(), "the cutover must be off unless a project asks for it")

	out, err := c.AgentRun(context.Background(), ownerIdentity(), "worker", "first task", "", "")
	require.NoError(t, err)
	upCtx, upCancel := context.WithTimeout(context.Background(), conformanceWait)
	defer upCancel()
	require.NoError(t, c.awaitChildUp(upCtx, out.Harp))
	require.Eventually(t, func() bool { return sp.engineHome(0) != nil }, conformanceWait, 10*time.Millisecond)
	runnerHome := sp.engineHome(0)
	require.False(t, runnerHome.SpoolDeliveryEnabled(), "the runner must not cut over on its own")

	// Down: an owner send still becomes a turn on the child.
	msgID, _, _, err := c.peerSend(ownerIdentity(), out.Harp, KindMessage, "second task", nil, "")
	require.NoError(t, err)
	require.NotEmpty(t, msgID)
	awaitChatText(t, sp, 0, "second task")

	// Up: the child's own send still reaches the parent's mailbox.
	child := Identity{Harp: out.Harp, RunID: out.RunID, Depth: 1}
	_, _, _, err = c.peerSend(child, ParentAddress, KindResult, "a finding", nil, "")
	require.NoError(t, err)
	require.NotEmpty(t, recvBody(t, c, "a finding", conformanceWait),
		"the disabled cutover must not disturb ordinary delivery")

	assert.Contains(t, mailboxEverQueued(c), msgID, "with the flag off the mailbox is still the carrier")
	assert.Empty(t, spoolDirsUnder(t, home),
		"the disabled cutover must create no spool directory — and therefore no consumed/ — anywhere under HOME")
	assert.Equal(t, SpoolDeliveryStats{}, c.SpoolDeliveryStats())
	assert.Equal(t, SpoolDeliveryStats{}, runnerHome.SpoolDeliveryStats())
}

// TestSpoolDelivery_CoordinatorMailRidesTheFileAndIsConsumed is the
// coordinator->child happy path, end to end: the send becomes ONE file in the
// child's in/ and no mailbox entry at all, the runner delivers it into the run
// as a framed turn with its payload and correlation intact, and the file ends
// up in in/consumed/ — the rename that IS the delivery acknowledgement.
func TestSpoolDelivery_CoordinatorMailRidesTheFileAndIsConsumed(t *testing.T) {
	resetStrictness(t)
	teeHome(t)
	sp := cutoverSpawner(0)
	c := newCutoverCoordinator(t, sp, 0)
	out, _ := awaitCutoverChild(t, c, sp, "first task")

	structured := json.RawMessage(`{"ticket":"T-9","severity":"high"}`)
	msgID, _, _, err := c.peerSend(ownerIdentity(), out.Harp, KindQuestion, "second task", structured, "corr-1")
	require.NoError(t, err)
	require.NotEmpty(t, msgID)

	// It reached the engine as a turn, framed with its provenance exactly as
	// a mailbox delivery is (frameCoordinatorDelivery) — the cutover changes
	// the carrier, never what the model sees.
	turns := awaitChatText(t, sp, 0, "second task")
	var delivered string
	for _, turn := range turns {
		if strings.Contains(turn, "second task") {
			delivered = turn
		}
	}
	require.NotEmpty(t, delivered)
	assert.Contains(t, delivered, "kind="+KindQuestion, "the kind survives onto the frame")
	assert.Contains(t, delivered, ownerIdentity().Harp, "the sender survives onto the frame")

	// NO MAILBOX TWIN: the fold never saw this message at all. (Other mail
	// legitimately still rides the mailbox in the same run — the child's own
	// bridged result goes to the OWNER, which is not a cut-over recipient —
	// so the assertion is about THIS id, not about the fold being empty.)
	assert.NotContains(t, mailboxEverQueued(c), msgID,
		"under the cutover the spool write IS the delivery; a mailbox fact would be a second copy nobody consumes")

	// The file was consumed by RENAME, not deleted: in/ empty, in/consumed/
	// holding exactly the message that was delivered.
	awaitSpoolCount(t, out.Harp, spool.DirIn, 0, "after delivery")
	consumed := awaitSpoolCount(t, out.Harp, spool.DirInConsumed, 1, "after delivery")
	got := consumed[0].Message
	assert.Equal(t, msgID, got.OriginID, "the consumed file is the message that was sent")
	assert.Equal(t, KindQuestion, got.Kind)
	assert.Equal(t, "second task", got.Body)
	assert.Equal(t, "corr-1", got.InReplyTo)
	assert.Equal(t, ownerIdentity().Harp, got.FromHarp)
	back, err := mailStructured(got.Structured)
	require.NoError(t, err)
	assert.JSONEq(t, string(structured), string(back), "the structured payload rode the file")

	// And the coordinator observed the consume-rename as the delivery ack.
	require.Eventually(t, func() bool { return c.SpoolDeliveryStats().Consumed >= 1 }, conformanceWait, 10*time.Millisecond,
		"the consume-rename doorbell is how the coordinator learns the child took its mail")
}

// TestSpoolDelivery_ChildSendRidesOutAndReachesAgentRecv is the child->parent
// happy path: agent_send is a LOCAL file write with no coordinator round trip,
// the coordinator routes it out of the child's out/, the parent's agent_recv
// long-poll is satisfied with it under the same wait semantics as before, and
// the file lands in out/consumed/.
func TestSpoolDelivery_ChildSendRidesOutAndReachesAgentRecv(t *testing.T) {
	resetStrictness(t)
	teeHome(t)
	sp := cutoverSpawner(0)
	c := newCutoverCoordinator(t, sp, 0)
	out, home := awaitCutoverChild(t, c, sp, "first task")

	structured, err := structpb.NewStruct(map[string]any{"kind": KindResult, "confidence": "high"})
	require.NoError(t, err)
	resp, err := home.Request(context.Background(), &agentcoordpb.AgentRequest{
		Kind: &agentcoordpb.AgentRequest_PeerSend{PeerSend: &agentcoordpb.PeerSendRequest{
			ToRole:     ParentAddress,
			Text:       "a finding",
			Structured: structured,
			InReplyTo:  "corr-2",
		}},
	})
	require.NoError(t, err)
	require.EqualValues(t, 0, resp.GetStatus().GetCode(), "the local write must succeed: %s", resp.GetStatus().GetMessage())
	msgID := resp.GetPeerSend().GetMessageId()
	require.NotEmpty(t, msgID, "the file's own name is the message id under the cutover")

	// The parent's agent_recv long-poll is satisfied from the swept file.
	got := recvBody(t, c, "a finding", conformanceWait)
	require.NotEmpty(t, got, "the parent's agent_recv must be satisfied from the child's out/ spool")
	assert.Equal(t, out.Harp, got[0].From, "sender identity is the spool the file was found in")
	assert.Equal(t, KindResult, got[0].Kind)
	assert.Equal(t, "corr-2", got[0].InReplyTo)
	require.NotEmpty(t, got[0].Structured, "the structured payload must survive the file")
	var payload map[string]any
	require.NoError(t, json.Unmarshal(got[0].Structured, &payload))
	assert.Equal(t, "high", payload["confidence"])

	// Consumed by rename, not deleted.
	awaitSpoolCount(t, out.Harp, spool.DirOut, 0, "after routing")
	consumed := awaitSpoolCount(t, out.Harp, spool.DirOutConsumed, 1, "after routing")
	assert.Equal(t, "a finding", consumed[0].Message.Body)
}

// TestSpoolDelivery_SweepDeliversWhatNoDoorbellEverAnnounced pins the floor:
// the doorbell only bounds latency, and a message that was never announced at
// all still arrives.
//
// The file is written STRAIGHT INTO the child's in/ spool, so no doorbell is
// rung by anyone — a stronger simulation than dropping one, because there is
// nothing to drop. Only the reconciliation sweep can deliver it, which is why
// this is the one test that shortens the cadence.
func TestSpoolDelivery_SweepDeliversWhatNoDoorbellEverAnnounced(t *testing.T) {
	resetStrictness(t)
	teeHome(t)
	const cadence = 150 * time.Millisecond
	sp := cutoverSpawner(cadence)
	c := newCutoverCoordinator(t, sp, cadence)
	out, home := awaitCutoverChild(t, c, sp, "first task")
	require.Zero(t, home.SpoolDoorbellStats().Rejected)

	ringsBefore := home.SpoolDoorbellStats()
	w, err := spool.NewWriter(spool.NewHomeMapper(), out.Harp, spool.DirIn, spoolWriterIDCoordinator)
	require.NoError(t, err)
	ref, err := w.Write(&spool.Message{
		Kind: KindMessage, FromHarp: ownerIdentity().Harp, To: out.Harp,
		OriginID: "m-unannounced", Body: "nobody rang the bell",
	})
	require.NoError(t, err)
	require.NotEmpty(t, ref.Name)

	awaitChatText(t, sp, 0, "nobody rang the bell")
	awaitSpoolCount(t, out.Harp, spool.DirIn, 0, "after the sweep delivered it")
	awaitSpoolCount(t, out.Harp, spool.DirInConsumed, 1, "after the sweep delivered it")
	assert.Equal(t, ringsBefore.Rejected, home.SpoolDoorbellStats().Rejected,
		"an unannounced delivery is an ordinary sweep, not a doorbell fault")
	assert.Zero(t, home.SpoolDeliveryStats().Failed)
}

// TestSpoolDelivery_ColdRunnerDrainsItsSpoolBeforeAnyChannel pins the STARTUP
// SCAN as a first-class delivery path, in its hardest form: mail written while
// the receiving side was down, delivered by a runner that comes up with NO
// COORDINATOR AT ALL to ring it.
//
// The Home here dials an address nothing is listening on, so its run channel
// never attaches and no doorbell can ever arrive. Everything that reaches the
// turn sink got there because the reactor swept the directory on the way up.
func TestSpoolDelivery_ColdRunnerDrainsItsSpoolBeforeAnyChannel(t *testing.T) {
	resetStrictness(t)
	teeHome(t)
	const harp = "cold-start-harp"

	// Mail written while the receiving side does not exist yet.
	w, err := spool.NewWriter(spool.NewHomeMapper(), harp, spool.DirIn, spoolWriterIDCoordinator)
	require.NoError(t, err)
	for _, body := range []string{"written while down one", "written while down two"} {
		_, err = w.Write(&spool.Message{Kind: KindMessage, FromHarp: "coordinator-harp", To: harp, Body: body})
		require.NoError(t, err)
	}
	require.Len(t, spoolEntries(t, harp, spool.DirIn), 2, "the fixture itself must be on disk before the runner exists")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	home, err := NewHome(ctx, HomeConfig{
		// An address nothing serves: NewHome never fails hard, so the
		// channel loops just keep reconnecting and no doorbell is possible.
		URL:                "http://127.0.0.1:1/mcp",
		Token:              "unused",
		RunID:              "run-cold",
		Harness:            "mock",
		Harp:               harp,
		SpoolDelivery:      true,
		SpoolSweepInterval: 150 * time.Millisecond,
	})
	require.NoError(t, err)
	t.Cleanup(func() { home.crash() })
	require.False(t, home.Attached(), "this runner must never reach a coordinator")

	delivered := make(chan string, 8)
	home.SetTurnSink(func(pm *agentcoordpb.PeerMessage) bool {
		delivered <- pm.GetText()
		return true
	})

	seen := map[string]bool{}
	for len(seen) < 2 {
		select {
		case text := <-delivered:
			seen[text] = true
		case <-time.After(conformanceWait):
			t.Fatalf("the cold runner never drained its spool; saw %v", seen)
		}
	}
	assert.True(t, seen["written while down one"] && seen["written while down two"])
	assert.False(t, home.Attached(), "delivery must not have depended on a coordinator")

	require.Eventually(t, func() bool { return len(spoolEntries(t, harp, spool.DirIn)) == 0 }, conformanceWait, 10*time.Millisecond,
		"a delivered file must be consumed even with no coordinator to announce it to")
	assert.Len(t, spoolEntries(t, harp, spool.DirInConsumed), 2)
}

// TestSpoolDelivery_ColdCoordinatorRoutesWhatItFindsInOut is the startup scan's
// coordinator half: a message a child wrote while the coordinator was DOWN is
// routed by the next coordinator to come up on the same state, before any
// channel of that child's exists.
func TestSpoolDelivery_ColdCoordinatorRoutesWhatItFindsInOut(t *testing.T) {
	resetStrictness(t)
	teeHome(t)
	stateDir := t.TempDir()

	sp := cutoverSpawner(0)
	first, err := New(Options{
		ProjectDir: t.TempDir(), StateDir: stateDir, Spawner: sp, SpoolDelivery: true,
	})
	require.NoError(t, err)
	require.NoError(t, first.Serve())
	out, _ := awaitCutoverChild(t, first, sp, "first task")
	first.Close()

	// The child writes its report into out/ with nobody listening.
	w, err := spool.NewWriter(spool.NewHomeMapper(), out.Harp, spool.DirOut, out.Harp)
	require.NoError(t, err)
	_, err = w.Write(&spool.Message{
		Kind: KindResult, FromHarp: out.Harp, To: ParentAddress, Body: "written while the coordinator was down",
	})
	require.NoError(t, err)

	// A fresh coordinator on the same state: adopt() replays the run
	// records, then the startup sweep finds the file.
	second, err := New(Options{
		ProjectDir: t.TempDir(), StateDir: stateDir, Spawner: newFakeSpawner(nil, nil), SpoolDelivery: true,
	})
	require.NoError(t, err)
	require.NoError(t, second.Serve())
	t.Cleanup(second.Close)

	got := recvBody(t, second, "written while the coordinator was down", conformanceWait)
	require.NotEmpty(t, got, "a coordinator coming up cold must drain what it finds in a child's out/ spool")
	assert.Equal(t, out.Harp, got[0].From)
	require.Eventually(t, func() bool { return len(spoolEntries(t, out.Harp, spool.DirOut)) == 0 }, conformanceWait, 10*time.Millisecond)
	assert.Len(t, spoolEntries(t, out.Harp, spool.DirOutConsumed), 1)
}

// TestSpoolDelivery_ConsumedMailIsNeverDeliveredTwice pins the arbitration.
//
// Two things could deliver one file twice: two triggers inside one process
// (the doorbell racing a sweep), and a second process reading a directory the
// first already read. The first is ruled out by the reactor's serialisation
// plus the delivery seam's own dedupe, and is asserted here by hammering the
// sweep; the second is ruled out ONLY by the consume-rename, and is asserted
// by standing a fresh runner up on the same spool — which is exactly what a
// relaunch or a reconnecting replacement runner is.
func TestSpoolDelivery_ConsumedMailIsNeverDeliveredTwice(t *testing.T) {
	resetStrictness(t)
	teeHome(t)
	sp := cutoverSpawner(0)
	c := newCutoverCoordinator(t, sp, 0)
	out, home := awaitCutoverChild(t, c, sp, "first task")

	_, _, _, err := c.peerSend(ownerIdentity(), out.Harp, KindMessage, "exactly once please", nil, "")
	require.NoError(t, err)
	awaitChatText(t, sp, 0, "exactly once please")
	awaitSpoolCount(t, out.Harp, spool.DirInConsumed, 1, "after the first delivery")

	// Hammer every in-process trigger there is. Each is a full sweep of the
	// same directory; none of them may produce a second turn.
	for i := 0; i < 20; i++ {
		home.SweepSpoolIn()
	}
	require.Never(t, func() bool { return countChatText(sp, 0, "exactly once please") > 1 },
		500*time.Millisecond, 10*time.Millisecond,
		"repeated sweeps of a consumed directory must not re-deliver")

	// A FRESH runner on the same spool — the relaunch case, where the first
	// runner's in-memory dedupe is gone and only the rename remains.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	fresh, err := NewHome(ctx, HomeConfig{
		URL: "http://127.0.0.1:1/mcp", Token: "unused", RunID: "run-fresh",
		Harness: "mock", Harp: out.Harp, SpoolDelivery: true,
		SpoolSweepInterval: 50 * time.Millisecond,
	})
	require.NoError(t, err)
	t.Cleanup(func() { fresh.crash() })
	redelivered := make(chan string, 4)
	fresh.SetTurnSink(func(pm *agentcoordpb.PeerMessage) bool {
		redelivered <- pm.GetText()
		return true
	})
	select {
	case text := <-redelivered:
		t.Fatalf("a replacement runner re-delivered already-consumed mail: %q", text)
	case <-time.After(500 * time.Millisecond):
	}
	assert.Equal(t, 1, countChatText(sp, 0, "exactly once please"),
		"the consume-rename is the arbiter: delivered exactly once, whatever reads the directory")
}

// TestSpoolDelivery_ConsumeThatLostItsRaceIsNotAFailure pins the ENOENT
// contract on BOTH sides. A doorbell or a sweep that reaches a file the other
// path already consumed is the design working, not a fault: it must be silent,
// and it must not be counted as a failed delivery.
//
// Surfacing it as an error is the tempting mistake, and it would turn every
// won race into an alarm an operator cannot act on — while a caller that read
// it as success would drop messages instead.
func TestSpoolDelivery_ConsumeThatLostItsRaceIsNotAFailure(t *testing.T) {
	resetStrictness(t)
	teeHome(t)
	sp := cutoverSpawner(0)
	c := newCutoverCoordinator(t, sp, 0)
	out, home := awaitCutoverChild(t, c, sp, "first task")

	mapper := spool.NewHomeMapper()

	// Runner side: a file whose consume the other path already won.
	inW, err := spool.NewWriter(mapper, out.Harp, spool.DirIn, spoolWriterIDCoordinator)
	require.NoError(t, err)
	inRef, err := inW.Write(&spool.Message{Kind: KindMessage, FromHarp: "coordinator-harp", To: out.Harp, Body: "raced"})
	require.NoError(t, err)
	_, err = spool.Consume(mapper, inRef) // the other path wins
	require.NoError(t, err)

	failedBefore := home.SpoolDeliveryStats().Failed
	home.rememberSpoolRef("m-raced", inRef)
	home.ackMailConsumed([]string{"m-raced"})
	assert.Equal(t, failedBefore, home.SpoolDeliveryStats().Failed,
		"a consume that lost its race is the other path having won, never a failed delivery")

	// Coordinator side: the same, for an out/ file.
	outW, err := spool.NewWriter(mapper, out.Harp, spool.DirOut, out.Harp)
	require.NoError(t, err)
	outRef, err := outW.Write(&spool.Message{Kind: KindResult, FromHarp: out.Harp, To: ParentAddress, Body: "raced back"})
	require.NoError(t, err)
	_, err = spool.Consume(mapper, outRef)
	require.NoError(t, err)

	coordFailedBefore := c.SpoolDeliveryStats().Failed
	c.consumeSpool(out.Harp, outRef)
	assert.Equal(t, coordFailedBefore, c.SpoolDeliveryStats().Failed,
		"the coordinator's half must read a lost race the same way")
}

// TestSpoolDelivery_SenderIdentityIsTheDirectoryNotTheFile pins the
// INTERIOR-CLAIM discipline, the file plane's equivalent of deriving a
// caller's identity from its credential rather than from what it wrote.
//
// A child writes an out/ file whose from_harp names a session that does not
// exist. Routing that trusted the interior claim would resolve THAT name's
// lineage — here, nothing — and the message would vanish with a plausible
// error; against a real sibling's name it would deliver a message in the
// sibling's name to the sibling's parent. The directory is the identity.
func TestSpoolDelivery_SenderIdentityIsTheDirectoryNotTheFile(t *testing.T) {
	resetStrictness(t)
	teeHome(t)
	sp := cutoverSpawner(0)
	c := newCutoverCoordinator(t, sp, 0)
	out, _ := awaitCutoverChild(t, c, sp, "first task")

	w, err := spool.NewWriter(spool.NewHomeMapper(), out.Harp, spool.DirOut, out.Harp)
	require.NoError(t, err)
	ref, err := w.Write(&spool.Message{
		Kind: KindResult,
		// The lie: a harp this coordinator has never heard of.
		FromHarp: "not-a-real-harp",
		To:       ParentAddress,
		Body:     "whose message is this",
	})
	require.NoError(t, err)
	c.spoolReactor.mark(out.Harp)

	got := recvBody(t, c, "whose message is this", conformanceWait)
	require.NotEmpty(t, got,
		"the message must be routed as the child's — a router that believed from_harp would resolve an unknown sender and drop it")
	assert.Equal(t, out.Harp, got[0].From,
		"the delivered sender is the spool the file was found in, not the name inside it")
	assert.Zero(t, c.SpoolDeliveryStats().Failed)

	require.Eventually(t, func() bool { return len(spoolEntries(t, out.Harp, spool.DirOut)) == 0 }, conformanceWait, 10*time.Millisecond)
	_ = ref
}

// TestSpoolDelivery_ForeignHarpDoorbellIsRefused is the same discipline on the
// runner's inbound side: a runner has exactly one spool, and a doorbell naming
// any other harp is refused rather than followed.
func TestSpoolDelivery_ForeignHarpDoorbellIsRefused(t *testing.T) {
	resetStrictness(t)
	teeHome(t)
	sp := cutoverSpawner(0)
	c := newCutoverCoordinator(t, sp, 0)
	out, home := awaitCutoverChild(t, c, sp, "first task")

	before := home.SpoolDoorbellStats().Rejected
	home.handleSpoolChanged(&agentcoordpb.SpoolChanged{
		Harp: "somebody-elses-harp",
		Dir:  agentcoordpb.SpoolDir_SPOOL_DIR_IN,
		Name: "00000000000000000001.00000001.coord.md",
	})
	assert.Equal(t, before+1, home.SpoolDoorbellStats().Rejected,
		"a runner pointed at another session's spool must refuse, loudly and countably")
	assert.NotEqual(t, "somebody-elses-harp", out.Harp)
}

// TestSpoolDelivery_NonObjectStructuredSurvivesTheDelivery pins the wrapper on
// the DELIVERY path, not just at the projection: a payload that is not a JSON
// object used to be refused, which under the cutover would be the message
// itself being lost. It now round trips byte for byte, all the way to what the
// receiving side reads back.
func TestSpoolDelivery_NonObjectStructuredSurvivesTheDelivery(t *testing.T) {
	resetStrictness(t)
	teeHome(t)
	sp := cutoverSpawner(0)
	c := newCutoverCoordinator(t, sp, 0)
	out, _ := awaitCutoverChild(t, c, sp, "first task")

	// A bare array; a number no YAML round trip preserves; a string YAML
	// would hand back as a number.
	raw := json.RawMessage(`[1,"two",12345678901234567890123,"0640"]`)
	msgID, _, _, err := c.peerSend(ownerIdentity(), out.Harp, KindMessage, "carrying an array", raw, "")
	require.NoError(t, err)

	awaitChatText(t, sp, 0, "carrying an array")
	consumed := awaitSpoolCount(t, out.Harp, spool.DirInConsumed, 1, "after delivery")
	require.Equal(t, msgID, consumed[0].Message.OriginID)

	back, err := mailStructured(consumed[0].Message.Structured)
	require.NoError(t, err)
	assert.Equal(t, string(raw), string(back),
		"a non-object payload must come back byte for byte; a YAML re-rendering would silently change it")
	assert.Zero(t, c.SpoolDeliveryStats().Failed)
}

// TestSpoolDelivery_PendingCountReadsTheSpool pins the answer every
// leftover-mail decision depends on. Under the cutover the mailbox fold holds
// nothing for a cut-over child, so a pendingCount that still read the fold
// would report a permanent zero — and "this child has unread mail, resume it"
// would silently become "nothing to do" for every child, forever.
func TestSpoolDelivery_PendingCountReadsTheSpool(t *testing.T) {
	resetStrictness(t)
	teeHome(t)
	sp := cutoverSpawner(0)
	c := newCutoverCoordinator(t, sp, 0)
	out, _ := awaitCutoverChild(t, c, sp, "first task")

	// Write straight into in/ so nothing can deliver (and therefore consume)
	// it before the count is taken: this asserts the READER, not a race.
	w, err := spool.NewWriter(spool.NewHomeMapper(), out.Harp, spool.DirIn, spoolWriterIDCoordinator)
	require.NoError(t, err)
	for i := 0; i < 2; i++ {
		_, err = w.Write(&spool.Message{Kind: KindMessage, FromHarp: "coordinator-harp", To: out.Harp, Body: "queued"})
		require.NoError(t, err)
	}

	assert.GreaterOrEqual(t, c.pendingCount(out.Harp), 1,
		"pendingCount must read the spool for a cut-over child; the fold holds nothing for it")
	var foldPending []Message
	c.mail.View(func() { foldPending = c.mailF.pendingFor(out.Harp) })
	assert.Empty(t, foldPending,
		"the count came from the DIRECTORY: the mailbox fold holds nothing for this child, which is why reading it would report a permanent zero")

	// A harp that is not cut over still answers from the mailbox, and a
	// non-existent spool is zero rather than an error.
	assert.Zero(t, c.pendingCount("no-such-harp"))
}

// TestSpoolDelivery_UnparsableFileIsReportedNeverSkipped pins the loud half of
// the reader contract. A file in in/ that is not a message must be COUNTED as
// a failure, because a reader that silently skips what it cannot understand is
// this project's characteristic defect: the sweep reports success, the message
// never arrives, and every cheap signal says the system is healthy.
func TestSpoolDelivery_UnparsableFileIsReportedNeverSkipped(t *testing.T) {
	resetStrictness(t)
	teeHome(t)
	sp := cutoverSpawner(150 * time.Millisecond)
	c := newCutoverCoordinator(t, sp, 0)
	out, home := awaitCutoverChild(t, c, sp, "first task")

	dir, err := spool.DirPath(spool.NewHomeMapper(), out.Harp, spool.DirIn)
	require.NoError(t, err)
	require.NoError(t, os.MkdirAll(dir, 0o700))
	require.NoError(t, os.WriteFile(
		filepath.Join(dir, "00000000000000000001.00000001.coord.md"),
		[]byte("no frontmatter here, just prose\n"), 0o600))

	home.SweepSpoolIn()
	require.Eventually(t, func() bool { return home.SpoolDeliveryStats().Failed >= 1 }, conformanceWait, 10*time.Millisecond,
		"an unreadable spool file must be counted, not skipped")
}
