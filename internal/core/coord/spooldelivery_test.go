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

	agentcoordpb "github.com/ctxloom/ctxloom/internal/adapters/coordgrpc/pb"
	"github.com/ctxloom/ctxloom/internal/core/spool"
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
	teeHome(t)
	c, err := New(Options{
		ProjectDir: t.TempDir(),
		StateDir:   t.TempDir(),
		Spawner:    sp,

		OwnerHarp:          ownerIdentity().Harp,
		SpoolSweepInterval: sweep,
	})
	require.NoError(t, err, "new cutover coordinator")
	require.NoError(t, runnerHooks.Serve(c), "serve cutover coordinator")
	t.Cleanup(c.Close)
	return c
}

// cutoverSpawner is startRunSpawner with the given spool sweep interval. Only
// a runner-backed child is cut over, so every test here rides the StartRun
// path.
func cutoverSpawner(sweep time.Duration) *fakeSpawner {
	sp := startRunSpawner(nil)
	sp.spoolSweepInterval = sweep
	return sp
}

// awaitCutoverChild spawns "worker", waits for its runner and engine to be
// up, and returns the run plus its Home. It asserts the CUTOVER reached the
// runner: a coordinator cut over alone would write files nothing reads, and
// every test below would then be measuring the sweep of an empty directory.
func awaitCutoverChild(t *testing.T, c *Coordinator, sp *fakeSpawner, prompt string) (*RunOutcome, TestHome) {
	t.Helper()
	out, err := c.AgentRun(context.Background(), ownerIdentity(), "worker", prompt, "", "")
	require.NoError(t, err)
	ctx, cancel := context.WithTimeout(context.Background(), conformanceWait)
	defer cancel()
	require.NoError(t, c.awaitChildUp(ctx, out.Harp), "the migrated child never came up")
	require.Eventually(t, func() bool { return sp.engineHome(0) != nil }, conformanceWait, 10*time.Millisecond,
		"the runner half never appeared")
	home := sp.engineHome(0)
	// "Up" is StartRun round-tripped on the RUNNER channel; the RUN channel
	// dials on its own goroutine, unordered against it, so neither wait above
	// implies c.chans[out.Harp]. Home.Attached is the runner reading the
	// HelloAck, which AttachRun registers the channel before sending — so it
	// implies both halves (see dialHome).
	if sp.attachWaiting != nil {
		sp.attachWaiting <- struct{}{}
	}
	require.Eventually(t, home.Attached, conformanceWait, 10*time.Millisecond,
		"the child's run channel never attached")
	return out, home
}

// awaitCutoverChildIdle is awaitCutoverChild plus a wait for the child's FIRST
// TURN BOUNDARY to have passed.
//
// A test that acts on the child's in/ spool needs that boundary behind it: the
// boundary runs a sweep, so a fixture written while it is still pending would
// be delivered and consumed by it, and the test would be measuring which of
// the two happened to go first. After it, the next sweep is a production
// interval away and the test owns the directory.
func awaitCutoverChildIdle(t *testing.T, c *Coordinator, sp *fakeSpawner, prompt string) (*RunOutcome, TestHome) {
	t.Helper()
	out, home := awaitCutoverChild(t, c, sp, prompt)
	require.Eventually(t, func() bool {
		for _, e := range c.Roster(ownerIdentity()) {
			if e.Harp == out.Harp {
				return e.State == StateIdle
			}
		}
		return false
	}, conformanceWait, 10*time.Millisecond, "the child never reached its first turn boundary")
	return out, home
}

// spoolEntryWithBody finds the swept entry whose body is want, so a test can
// name the message it means instead of counting a directory whose contents a
// later feature may legitimately grow.
func spoolEntryWithBody(t *testing.T, harp string, dir spool.Dir, want string) (spool.Entry, bool) {
	t.Helper()
	require.NotEmpty(t, want, "an EMPTY body would match a message that carries nothing")
	for _, e := range spoolEntries(t, harp, dir) {
		if e.Message.Body == want {
			return e, true
		}
	}
	return spool.Entry{}, false
}

// awaitSpoolEntryWithBody waits for dir to hold a message whose body is want.
func awaitSpoolEntryWithBody(t *testing.T, harp string, dir spool.Dir, want, why string) spool.Entry {
	t.Helper()
	var got spool.Entry
	require.Eventually(t, func() bool {
		e, ok := spoolEntryWithBody(t, harp, dir, want)
		got = e
		return ok
	}, conformanceWait, 10*time.Millisecond, "%s: %s never held a message whose body is %q", why, dir, want)
	return got
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
		texts = sc.RecordedTexts()
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
	for _, got := range sc.RecordedTexts() {
		if strings.Contains(got, want) {
			n++
		}
	}
	return n
}

// assertNoMailboxJournal is the "no mailbox twin" assertion in its final
// form: there is no mailbox at all, so a coordinator that delivered anything
// must not have grown a mailbox journal in its state dir.
func assertNoMailboxJournal(t *testing.T, c *Coordinator) {
	t.Helper()
	_, err := os.Stat(filepath.Join(c.stateDir, "mailbox.jsonl"))
	assert.True(t, os.IsNotExist(err), "the file IS the delivery; a mailbox journal would be a second carrier")
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
	msgID, _, err := c.peerSend(ownerIdentity(), out.Harp, KindQuestion, "second task", structured, "corr-1")
	require.NoError(t, err)
	require.NotEmpty(t, msgID)

	// It reached the engine as a turn, framed with its provenance exactly as
	// a mailbox delivery is (FrameCoordinatorDelivery) — the cutover changes
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
	assertNoMailboxJournal(t, c)

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

// TestSpoolDelivery_ChildSendRidesOutAndReachesTheParent is the child->parent
// happy path: agent_send is a LOCAL file write with no coordinator round trip,
// the coordinator routes it out of the child's out/ into the parent's spool,
// and the file lands in out/consumed/.
func TestSpoolDelivery_ChildSendRidesOutAndReachesTheParent(t *testing.T) {
	resetStrictness(t)
	teeHome(t)
	sp := cutoverSpawner(0)
	c := newCutoverCoordinator(t, sp, 0)
	out, home := awaitCutoverChild(t, c, sp, "first task")

	structured, err := structpb.NewStruct(map[string]any{"confidence": "high"})
	require.NoError(t, err)
	resp, err := home.Request(context.Background(), &agentcoordpb.AgentRequest{
		Kind: &agentcoordpb.AgentRequest_PeerSend{PeerSend: &agentcoordpb.PeerSendRequest{
			ToRole:     ParentAddress,
			Text:       "a finding",
			Structured: structured,
			InReplyTo:  "corr-2",
			Kind:       agentcoordpb.MessageKind_MESSAGE_KIND_RESULT,
		}},
	})
	require.NoError(t, err)
	require.EqualValues(t, 0, resp.GetStatus().GetCode(), "the local write must succeed: %s", resp.GetStatus().GetMessage())
	msgID := resp.GetPeerSend().GetMessageId()
	require.NotEmpty(t, msgID, "the file's own name is the message id under the cutover")

	// The parent reads the swept file from its own spool.
	got := recvBody(t, c, "a finding", conformanceWait)
	require.NotEmpty(t, got, "the child's send must reach the parent from the child's out/ spool")
	assert.Equal(t, out.Harp, got[0].From, "sender identity is the spool the file was found in")
	assert.Equal(t, KindResult, got[0].Kind)
	assert.Equal(t, "corr-2", got[0].InReplyTo)
	require.NotEmpty(t, got[0].Structured, "the structured payload must survive the file")
	var payload map[string]any
	require.NoError(t, json.Unmarshal(got[0].Structured, &payload))
	assert.Equal(t, "high", payload["confidence"])

	// The audit journal's spool_mail_out entry is the durable record of this
	// routing that outlives the spool file itself, and the echo smoke
	// (scripts/agentcoord-echo-smoke.sh) proves its round trip from it: it must
	// name the coordinator-resolved sender, not only the recipient.
	var routed []auditEntry
	for _, e := range readAuditKind(t, c, "spool_mail_out") {
		if e.Detail["message_id"] == got[0].ID {
			routed = append(routed, e)
		}
	}
	require.Len(t, routed, 1, "exactly one spool_mail_out records the write into the parent's in/")
	assert.Equal(t, got[0].To, routed[0].Actor, "the audit actor is the recipient")
	assert.Equal(t, out.Harp, routed[0].Detail["from"], "the audit names the sender the coordinator resolved from the spool directory")
	assert.Equal(t, KindResult, routed[0].Detail["kind"])

	// Consumed by rename, not deleted. The message is SELECTED rather than
	// counted: this run's turn boundary also writes its automatic report into
	// out/ (spoolturnresult.go), so the directory legitimately holds more than
	// this one send.
	awaitSpoolCount(t, out.Harp, spool.DirOut, 0, "after routing")
	consumed := awaitSpoolEntryWithBody(t, out.Harp, spool.DirOutConsumed, "a finding", "after routing")
	assert.Equal(t, msgID, strings.TrimSuffix(consumed.Ref.Name, spool.MessageFileExt),
		"the consumed file is the one agent_send named")
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
	home, err := runnerHooks.NewHome(ctx, TestHomeConfig{
		Reporter: termSink(),
		// An address nothing serves: NewHome never fails hard, so the
		// channel loops just keep reconnecting and no doorbell is possible.
		URL:     "http://127.0.0.1:1/mcp",
		Token:   "unused",
		RunID:   "run-cold",
		Harness: "mock",
		Harp:    harp,

		// The PRODUCTION cadence, deliberately: at 30s nothing but the
		// STARTUP pass can deliver inside this test's budget, so a delivery
		// here is proof of the cold-start path and not of a fast timer.
	})
	require.NoError(t, err)
	t.Cleanup(func() { home.Crash() })
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

// TestSpoolDelivery_AwaitMailAckedWaitsForTheConsumeRename pins the wait the
// engine host performs before reporting its exit: it returns only once the
// delivered file has actually been renamed consumed — not when the engine
// accepted the turn, which is where the sink returns and where the race with
// the exit report begins.
func TestSpoolDelivery_AwaitMailAckedWaitsForTheConsumeRename(t *testing.T) {
	resetStrictness(t)
	teeHome(t)
	const harp = "await-ack-harp"

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	home, err := runnerHooks.NewHome(ctx, TestHomeConfig{
		Reporter: termSink(),
		URL:      "http://127.0.0.1:1/mcp", Token: "unused", RunID: "run-await", Harness: "mock", Harp: harp,
	})
	require.NoError(t, err)
	t.Cleanup(func() { home.Crash() })

	// The sink models the engine ACCEPTING the turn and the pump's tail
	// (transcript write) taking its time before the ack can happen.
	accepted := make(chan string, 1)
	release := make(chan struct{})
	home.SetTurnSink(func(pm *agentcoordpb.PeerMessage) bool {
		accepted <- pm.GetMessageId()
		<-release
		return true
	})

	w, err := spool.NewWriter(spool.NewHomeMapper(), harp, spool.DirIn, spoolWriterIDCoordinator)
	require.NoError(t, err)
	_, err = w.Write(&spool.Message{Kind: KindMessage, FromHarp: "coordinator-harp", To: harp, Body: "answer me"})
	require.NoError(t, err)
	home.SweepSpoolIn()

	var id string
	select {
	case id = <-accepted:
	case <-time.After(conformanceWait):
		t.Fatal("the sweep never delivered the file")
	}

	waited := make(chan error, 1)
	go func() {
		actx, acancel := context.WithTimeout(context.Background(), conformanceWait)
		defer acancel()
		waited <- home.AwaitMailAcked(actx, []string{id})
	}()
	select {
	case err := <-waited:
		t.Fatalf("AwaitMailAcked returned (%v) while the turn was accepted but not yet consumed — the file is still in in/", err)
	case <-time.After(150 * time.Millisecond):
	}
	require.Len(t, spoolEntries(t, harp, spool.DirIn), 1, "still unconsumed while the pump is in its tail")

	close(release)
	select {
	case err := <-waited:
		require.NoError(t, err)
	case <-time.After(conformanceWait):
		t.Fatal("AwaitMailAcked never returned after the ack")
	}
	assert.Empty(t, spoolEntries(t, harp, spool.DirIn), "by the time the wait returns the file has been renamed consumed")
	assert.Len(t, spoolEntries(t, harp, spool.DirInConsumed), 1)

	require.NoError(t, home.AwaitMailAcked(context.Background(), []string{"never-delivered"}),
		"an id this runner never delivered has no ack in flight: nothing to wait for")
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
		ProjectDir: t.TempDir(), StateDir: stateDir, Spawner: sp, OwnerHarp: ownerIdentity().Harp,
	})
	require.NoError(t, err)
	require.NoError(t, runnerHooks.Serve(first))
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
		ProjectDir: t.TempDir(), StateDir: stateDir, Spawner: newFakeSpawner(nil, nil), OwnerHarp: ownerIdentity().Harp,
	})
	require.NoError(t, err)
	require.NoError(t, runnerHooks.Serve(second))
	t.Cleanup(second.Close)

	got := recvBody(t, second, "written while the coordinator was down", conformanceWait)
	require.NotEmpty(t, got, "a coordinator coming up cold must drain what it finds in a child's out/ spool")
	assert.Equal(t, out.Harp, got[0].From)
	require.Eventually(t, func() bool { return len(spoolEntries(t, out.Harp, spool.DirOut)) == 0 }, conformanceWait, 10*time.Millisecond)
	awaitSpoolEntryWithBody(t, out.Harp, spool.DirOutConsumed, "written while the coordinator was down",
		"the routed file must be consumed by rename")
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

	_, _, err := c.peerSend(ownerIdentity(), out.Harp, KindMessage, "exactly once please", nil, "")
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
	fresh, err := runnerHooks.NewHome(ctx, TestHomeConfig{
		Reporter: termSink(),
		URL:      "http://127.0.0.1:1/mcp", Token: "unused", RunID: "run-fresh",
		Harness: "mock", Harp: out.Harp,
		SpoolSweepInterval: 50 * time.Millisecond,
	})
	require.NoError(t, err)
	t.Cleanup(func() { fresh.Crash() })
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
	// awaitCutoverChildIdle, not awaitCutoverChild: this test writes straight
	// into out.Harp's own in/ and out/ spools and then races its own manual
	// consume against them. Both spools are live — the runner's reactor sweeps
	// in/ on a turn boundary, and the coordinator's sweeps out/ the same way —
	// so without waiting for that boundary to pass first, the live pipeline
	// can reach the fixture file before the test's own "other path wins"
	// consume does, and this call fails with the very race the test exists to
	// pin, instead of the arranged one.
	out, _ := awaitCutoverChildIdle(t, c, sp, "first task")

	mapper := spool.NewHomeMapper()

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
	c.spoolReactor.Mark(out.Harp)

	got := recvBody(t, c, "whose message is this", conformanceWait)
	require.NotEmpty(t, got,
		"the message must be routed as the child's — a router that believed from_harp would resolve an unknown sender and drop it")
	assert.Equal(t, out.Harp, got[0].From,
		"the delivered sender is the spool the file was found in, not the name inside it")
	assert.Zero(t, c.SpoolDeliveryStats().Failed)

	require.Eventually(t, func() bool { return len(spoolEntries(t, out.Harp, spool.DirOut)) == 0 }, conformanceWait, 10*time.Millisecond)
	_ = ref
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
	msgID, _, err := c.peerSend(ownerIdentity(), out.Harp, KindMessage, "carrying an array", raw, "")
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
	// awaitCutoverChildIdle, not awaitCutoverChild: the runner's in/ reactor
	// sweeps on the first turn boundary, and without waiting for that boundary
	// to pass first, that sweep can deliver-and-consume these fixture files
	// before pendingCount reads the directory — this asserts the READER, not
	// a race, so the race has to be retired first.
	out, _ := awaitCutoverChildIdle(t, c, sp, "first task")

	w, err := spool.NewWriter(spool.NewHomeMapper(), out.Harp, spool.DirIn, spoolWriterIDCoordinator)
	require.NoError(t, err)
	for i := 0; i < 2; i++ {
		_, err = w.Write(&spool.Message{Kind: KindMessage, FromHarp: "coordinator-harp", To: out.Harp, Body: "queued"})
		require.NoError(t, err)
	}

	assert.GreaterOrEqual(t, c.pendingCount(out.Harp), 1,
		"pendingCount must read the spool: the count comes from the DIRECTORY")

	// A non-existent spool is zero rather than an error.
	assert.Zero(t, c.pendingCount("no-such-harp"))
}

// TestSpoolDelivery_ConsumedAckCreditedWithoutReadableBody pins that
// sweepChildConsumed reads in/consumed/ NAMES ONLY (deceptive-copartner): it
// must credit an acknowledgement even when the file's body is unparseable
// garbage, because the name existing in consumed/ IS the whole signal a
// rename-based ack carries.
//
// This is also the regression pin against the fix regressing to the
// read-and-parse contract: if sweepChildConsumed still swept via
// spool.Sweep, this fixture would come back as a Problem (spool.Sweep
// reports an unparseable body that way, see TestSweepNames_
// CreditsAnEntryWithAnUnreadableBody in the spool package) and never reach
// spoolSeen at all, so the stat below would never move.
func TestSpoolDelivery_ConsumedAckCreditedWithoutReadableBody(t *testing.T) {
	resetStrictness(t)
	teeHome(t)
	sp := cutoverSpawner(0)
	c := newCutoverCoordinator(t, sp, 0)
	out, _ := awaitCutoverChildIdle(t, c, sp, "first task")

	dir, err := spool.DirPath(spool.NewHomeMapper(), out.Harp, spool.DirInConsumed)
	require.NoError(t, err)
	require.NoError(t, os.MkdirAll(dir, 0o700))
	require.NoError(t, os.WriteFile(
		filepath.Join(dir, "00000000000000000099.00000001.coord.md"),
		[]byte("not frontmatter, not yaml, just garbage\n"), 0o600))

	before := c.SpoolDeliveryStats().Consumed
	c.sweepChildConsumed(out.Harp)
	require.Eventually(t, func() bool { return c.SpoolDeliveryStats().Consumed >= before+1 }, conformanceWait, 10*time.Millisecond,
		"an in/consumed/ entry with an unreadable body must still be credited: the name is the whole signal")
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

// TestSpoolDelivery_UnmappableKindReachesATerminalState pins the fix for the
// silent-skip defect: an in/ entry that PARSES as a message (unlike the
// unparsable-file case above) but carries a kind this build's mailbox
// vocabulary does not know — an unknown or future kind string — must not sit
// in in/ forever, re-warned about and re-skipped on every sweep while later
// entries in the same directory keep delivering around it.
//
// The runner here is deliberately cold (dials an address nothing serves, as
// TestSpoolDelivery_ColdRunnerDrainsItsSpoolBeforeAnyChannel does): the only
// thing that can act on the fixture is the reactor's own startup sweep, so a
// pass here is proof of sweepSpoolIn's own terminal handling and not of some
// other delivery path picking up the slack.
func TestSpoolDelivery_UnmappableKindReachesATerminalState(t *testing.T) {
	resetStrictness(t)
	teeHome(t)
	const harp = "unmappable-kind-harp"

	w, err := spool.NewWriter(spool.NewHomeMapper(), harp, spool.DirIn, spoolWriterIDCoordinator)
	require.NoError(t, err)
	ref, err := w.Write(&spool.Message{
		Kind: "a-kind-this-build-does-not-know", FromHarp: "coordinator-harp", To: harp,
		Body: "unmappable",
	})
	require.NoError(t, err)
	require.NotEmpty(t, ref.Name)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	home, err := runnerHooks.NewHome(ctx, TestHomeConfig{
		Reporter: termSink(),
		URL:      "http://127.0.0.1:1/mcp",
		Token:    "unused",
		RunID:    "run-unmappable-kind",
		Harness:  "mock",
		Harp:     harp,
	})
	require.NoError(t, err)
	t.Cleanup(func() { home.Crash() })
	require.False(t, home.Attached(), "this runner must never reach a coordinator")

	delivered := make(chan string, 8)
	home.SetTurnSink(func(pm *agentcoordpb.PeerMessage) bool {
		delivered <- pm.GetText()
		return true
	})

	// LOUD: the failure is counted, not swallowed.
	require.Eventually(t, func() bool { return home.SpoolDeliveryStats().Failed >= 1 }, conformanceWait, 10*time.Millisecond,
		"an unmappable kind must be counted as a failure")

	// TERMINAL: the file leaves in/ ...
	require.Eventually(t, func() bool { return len(spoolEntries(t, harp, spool.DirIn)) == 0 }, conformanceWait, 10*time.Millisecond,
		"the unmappable entry must not sit in in/ forever")
	// ... but it must NOT be in in/consumed/: it was never delivered, and
	// that directory's whole meaning is "the reader accepted this".
	assert.Empty(t, spoolEntries(t, harp, spool.DirInConsumed),
		"an entry that was never delivered must never be marked consumed — that would lie about delivery")
	// It must be findable at the distinct, present-but-unreadable location.
	root, err := spool.Root(spool.NewHomeMapper(), harp)
	require.NoError(t, err)
	failedPath := filepath.Join(root, "in", "failed", ref.Name)
	assert.FileExists(t, failedPath,
		"an unmappable entry must be PRESENT ON DISK at a terminal location distinguishable from both "+
			"\"never arrived\" and \"delivered\"")

	// NEVER DELIVERED: no turn is ever produced from it.
	select {
	case text := <-delivered:
		t.Fatalf("an unmappable message must never be delivered as a turn; got %q", text)
	case <-time.After(200 * time.Millisecond):
	}

	// NEVER RETRIED: a later sweep must not re-discover or re-count it —
	// the whole point of a terminal state.
	failedBefore := home.SpoolDeliveryStats().Failed
	home.SweepSpoolIn()
	time.Sleep(200 * time.Millisecond)
	assert.Equal(t, failedBefore, home.SpoolDeliveryStats().Failed,
		"a terminal entry must not be re-counted as a fresh failure on a later sweep")
}
