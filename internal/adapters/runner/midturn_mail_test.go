package runner

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"

	agentcoordpb "github.com/ctxloom/ctxloom/internal/adapters/coordgrpc/pb"
	"github.com/ctxloom/ctxloom/internal/core/coord"
	"github.com/ctxloom/ctxloom/internal/core/launch"
	"github.com/ctxloom/ctxloom/internal/core/sessions"
	"github.com/ctxloom/ctxloom/internal/core/spool"
)

// MID-TURN MAIL. A child's runner hands it mail as a NEW turn, so a message
// that arrives while the child runs one long turn waits behind that turn —
// alpha issue 1, a parent's binding reply unread through a whole turn. The
// mail-peek hook asks the runner for that mail at each tool call instead, and
// the runner stays the one reader of the spool: it hands the hook what waits
// (PeekMail), the hook writes it into the engine's context, and only then
// confirms (ConfirmMail). These tests pin the runner half.

const midTurnHarp = "child-harp-1" // testStartRun's identity

// gatedRunOverHome starts run-1 over a REAL Home (so its spool reader, turn
// pump and acks are the production ones) whose first turn holds at the
// scripted chat's gate, and returns once that turn is inside the engine.
func gatedRunOverHome(t *testing.T) (*EngineHost, *Home, *scriptedChat) {
	t.Helper()
	resetStrictness(t)
	teeHome(t)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	home, err := NewHome(ctx, HomeConfig{
		Reporter: termSink(),
		URL:      "http://127.0.0.1:1/mcp", Token: "unused", RunID: "run-1", Harness: "mock",
		SpoolSweepInterval: time.Hour, // only the test's own sweeps deliver
	})
	require.NoError(t, err)
	t.Cleanup(func() { home.Crash() })
	// No coordinator is dialed here, so the owner never arrives on its own;
	// a turn hand-off waits for it (handOffTurn's owner gate).
	home.setOwnerPresent(true)

	sc := &scriptedChat{Gate: make(chan struct{})}
	eh := newTestEngineHost(context.Background(), sc, "claude-code", "run-1")
	t.Cleanup(eh.Close)
	eh.BindHome(home)
	resp := handleBounded(t, eh, &agentcoordpb.RunnerRequest{Kind: &agentcoordpb.RunnerRequest_StartRun{StartRun: testStartRun("run-1")}})
	require.Equal(t, int32(codes.OK), resp.GetStatus().GetCode(), resp.GetStatus().GetMessage())
	require.Eventually(t, func() bool { return len(sc.RecordedTexts()) == 1 }, 5*time.Second, 5*time.Millisecond,
		"the briefing turn never reached the engine")
	return eh, home, sc
}

// mailArrives writes body into the child's inbox and has the runner sweep it,
// returning once the runner holds it for delivery.
func mailArrives(t *testing.T, home *Home, body string) {
	t.Helper()
	writeSpoolMail(t, midTurnHarp, "parent-harp", coord.KindMessage, body)
	home.SweepSpoolIn()
	require.Eventually(t, func() bool { return home.holdsMail(body) }, 5*time.Second, 5*time.Millisecond,
		"the runner never took %q for delivery", body)
}

// mailSwept writes body into the child's inbox and has the runner sweep it,
// for mail that becomes a turn at once (no wait on the runner holding it).
func mailSwept(t *testing.T, home *Home, body string) {
	t.Helper()
	writeSpoolMail(t, midTurnHarp, "parent-harp", coord.KindMessage, body)
	home.SweepSpoolIn()
}

// holdsMail reports whether the runner holds a message carrying body for
// delivery, not yet delivered.
func (h *Home) holdsMail(body string) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, pm := range h.pendingMail {
		if strings.Contains(pm.GetText(), body) {
			return true
		}
	}
	return false
}

// turnTexts is every turn the engine was handed, joined, for "was X ever a
// turn" assertions.
func turnTexts(sc *scriptedChat) string { return strings.Join(sc.RecordedTexts(), "\n----\n") }

// openGate releases the turn held at the scripted chat's gate and re-arms a
// fresh gate for the turns after it, so the next turn is held too.
func openGate(sc *scriptedChat) {
	sc.Mu.Lock()
	g := sc.Gate
	sc.Gate = make(chan struct{})
	sc.Mu.Unlock()
	close(g)
}

// TestMidTurnMail_AConfirmedPeekIsNeverDeliveredAgainAsATurn is the
// double-delivery risk, pinned before anything else. The reply arrives while
// the child is mid-turn: the turn pump has already taken it and its hand-off
// is waiting on the turn in flight (turnBusy). The mail-peek hook takes it
// and confirms. When the turn ends, the waiting hand-off must see that the
// message was delivered and start NO turn for it — and nothing about it may
// read as a failed ack.
func TestMidTurnMail_AConfirmedPeekIsNeverDeliveredAgainAsATurn(t *testing.T) {
	_, home, sc := gatedRunOverHome(t)
	mailArrives(t, home, "BINDING REPLY")

	peek := home.PeekMail(7500)
	require.Contains(t, peek.Context, "BINDING REPLY", "mail waiting behind the turn in flight is handed to the peek")
	require.Len(t, peek.IDs, 1)
	assert.Equal(t, 1, home.ConfirmMail(peek.Lease, peek.IDs))
	delivered, err := spool.Delivered(home.fs, home.cfg.Mapper, midTurnHarp, peek.IDs[0])
	require.NoError(t, err)
	assert.True(t, delivered, "a confirmed peek IS the delivery: its identity is recorded")

	// The turn in flight ends; a later message proves the pump moved past
	// the confirmed one rather than starting a turn for it.
	openGate(sc)
	mailSwept(t, home, "LATER MAIL")
	openGate(sc)
	require.Eventually(t, func() bool { return strings.Contains(turnTexts(sc), "LATER MAIL") }, 5*time.Second, 5*time.Millisecond)
	assert.NotContains(t, turnTexts(sc), "BINDING REPLY", "the peeked reply must not ALSO be delivered as a turn")
	assert.Zero(t, home.SpoolDeliveryStats().Failed, "skipping a peeked message is not a failed ack")
	assert.Empty(t, home.PeekMail(7500).IDs, "nothing is left to peek")
}

// TestMidTurnMail_AnUnconfirmedPeekStaysQueuedForTheTurn: the hook fetched
// but never confirmed — it died, or its write failed. The message was never
// delivered, so it is delivered as the next turn, and the stale lease confirms
// nothing afterwards.
func TestMidTurnMail_AnUnconfirmedPeekStaysQueuedForTheTurn(t *testing.T) {
	_, home, sc := gatedRunOverHome(t)
	mailArrives(t, home, "UNCONFIRMED")

	peek := home.PeekMail(7500)
	require.Len(t, peek.IDs, 1)
	openGate(sc)
	require.Eventually(t, func() bool { return strings.Contains(turnTexts(sc), "UNCONFIRMED") }, 5*time.Second, 5*time.Millisecond,
		"an unconfirmed peek must leave the message to its turn")
	assert.Zero(t, home.ConfirmMail(peek.Lease, peek.IDs), "the turn took it: a late confirm confirms nothing")
}

// TestMidTurnMail_ConcurrentPeeksNeverHandOutTheSameMessage: an engine can
// run several tool calls at once, each firing the hook. A message one peek
// holds is not handed to another.
func TestMidTurnMail_ConcurrentPeeksNeverHandOutTheSameMessage(t *testing.T) {
	_, home, _ := gatedRunOverHome(t)
	mailArrives(t, home, "ONCE ONLY")

	first := home.PeekMail(7500)
	require.Len(t, first.IDs, 1)
	assert.Empty(t, home.PeekMail(7500).IDs, "a message one peek holds is not handed to a second")
}

// TestMidTurnMail_DeliversWhatFitsAndCarriesTheRest: one hook's context is
// capped, so a peek hands over what fits, says how much more waits, and the
// rest is handed to the next peek.
func TestMidTurnMail_DeliversWhatFitsAndCarriesTheRest(t *testing.T) {
	_, home, _ := gatedRunOverHome(t)
	mailArrives(t, home, "FIRST "+strings.Repeat("a", 400))
	mailArrives(t, home, "SECOND "+strings.Repeat("b", 400))

	first := home.PeekMail(800)
	require.Len(t, first.IDs, 1, "only one message fits the cap")
	assert.LessOrEqual(t, len(first.Context), 800, "the context never exceeds the cap")
	assert.Contains(t, first.Context, "FIRST")
	assert.Equal(t, 1, first.Carried)
	require.Equal(t, 1, home.ConfirmMail(first.Lease, first.IDs))

	second := home.PeekMail(800)
	require.Len(t, second.IDs, 1)
	assert.Contains(t, second.Context, "SECOND")
	assert.Zero(t, second.Carried)
}

// TestMidTurnMail_TheMessageThatStartedATurnIsNotPeeked: once a message's turn
// has started, the engine already has it; a tool call inside that turn must
// not hand it over again.
func TestMidTurnMail_TheMessageThatStartedATurnIsNotPeeked(t *testing.T) {
	_, home, sc := gatedRunOverHome(t)
	openGate(sc) // the briefing ends; the next turn is held at a fresh gate
	mailSwept(t, home, "STARTS A TURN")
	require.Eventually(t, func() bool { return strings.Contains(turnTexts(sc), "STARTS A TURN") }, 5*time.Second, 5*time.Millisecond)
	assert.Empty(t, home.PeekMail(7500).IDs)
}

// TestTurn_EngineEnvCarriesTheHookAddress: the interactive terminal composes
// its engine's env from the launch, not from Exec — it must still carry the
// hook address, or an interactive run's mail peek has nowhere to ask.
func TestTurn_EngineEnvCarriesTheHookAddress(t *testing.T) {
	l := ownerLaunch(midTurnHarp, "claude-code", "fast", "claude-sonnet-5", "/work", "bypassPermissions")
	hookEnv := sessions.EncodeHookReach(sessions.Endpoint{URL: "http://127.0.0.1:4100/hook", Credential: "bearer"})
	env := Turn{Launch: l, hookEnv: hookEnv}.EngineEnv()
	for k, v := range hookEnv {
		assert.Equal(t, v, env[k], k)
	}
	assert.Equal(t, l.EngineEnv()[sessions.EnvHarp], env[sessions.EnvHarp], "the launch's own env is still the base")
}

// TestMidTurnMailContext_FitsAPrefixAndSaysWhatWaits: frames are carried in
// order up to the cap — never reordered so a short one overtakes a long one —
// and the rest is named; when not even the first fits, the engine is still
// told mail waits; when not even that fits, nothing is written.
func TestMidTurnMailContext_FitsAPrefixAndSaysWhatWaits(t *testing.T) {
	long, short := strings.Repeat("L", 600), "S"
	text, n := MidTurnMailContext([]string{short, long}, 400)
	assert.Equal(t, 1, n)
	assert.Contains(t, text, "during this turn")
	assert.Contains(t, text, "1 more message(s) wait")
	assert.LessOrEqual(t, len(text), 400)

	text, n = MidTurnMailContext([]string{long, short}, 400)
	assert.Zero(t, n, "the short one does not overtake the long one ahead of it")
	assert.Contains(t, text, "2 more message(s) wait", "but the engine learns mail waits")

	text, n = MidTurnMailContext([]string{short, short}, 10_000)
	assert.Equal(t, 2, n)
	assert.NotContains(t, text, "more message(s)")

	text, n = MidTurnMailContext([]string{long}, 10)
	assert.Empty(t, text)
	assert.Zero(t, n)
}

// TestMidTurnMail_AConfirmAfterTheTurnTookTheMessageDeliversNothing: the
// lease was issued, then the hand-off claimed the message for its turn. A
// confirm arriving now (a hook outliving its turn) must not deliver it a
// second time.
func TestMidTurnMail_AConfirmAfterTheTurnTookTheMessageDeliversNothing(t *testing.T) {
	_, home, _ := gatedRunOverHome(t)
	mailArrives(t, home, "CLAIMED BY ITS TURN")
	peek := home.PeekMail(7500)
	require.Len(t, peek.IDs, 1)
	require.True(t, home.claimTurnMail(peek.IDs[0]))
	assert.Zero(t, home.ConfirmMail(peek.Lease, peek.IDs))
	delivered, err := spool.Delivered(home.fs, home.cfg.Mapper, midTurnHarp, peek.IDs[0])
	require.NoError(t, err)
	assert.False(t, delivered, "the turn, not the peek, delivers it")
}

// TestMidTurnMail_AnExpiredLeaseFreesItsMessages: a hook that fetched and
// died holds nothing for long — once its lease is past mailLeaseTTL the next
// peek is handed the message.
func TestMidTurnMail_AnExpiredLeaseFreesItsMessages(t *testing.T) {
	_, home, _ := gatedRunOverHome(t)
	mailArrives(t, home, "ORPHANED")
	first := home.PeekMail(7500)
	require.Len(t, first.IDs, 1)
	require.Empty(t, home.PeekMail(7500).IDs)

	home.mu.Lock()
	l := home.mailLeases[first.Lease]
	l.at = l.at.Add(-mailLeaseTTL)
	home.mailLeases[first.Lease] = l
	home.mu.Unlock()
	again := home.PeekMail(7500)
	assert.Equal(t, first.IDs, again.IDs, "the dead hook's lease no longer holds the message")
	assert.Zero(t, home.ConfirmMail(first.Lease, first.IDs), "and the expired lease confirms nothing")
}

// TestMidTurnMail_AConfirmDeliversOnlyWhatItsOwnLeaseHolds: a hook confirms
// the ids it was handed under its lease; an id another peek holds is not its
// to deliver, or two hooks would both have written it.
func TestMidTurnMail_AConfirmDeliversOnlyWhatItsOwnLeaseHolds(t *testing.T) {
	_, home, _ := gatedRunOverHome(t)
	mailArrives(t, home, "ONE "+strings.Repeat("a", 400))
	mailArrives(t, home, "TWO "+strings.Repeat("b", 400))
	a := home.PeekMail(800)
	b := home.PeekMail(800)
	require.Len(t, a.IDs, 1)
	require.Len(t, b.IDs, 1)
	assert.Equal(t, 1, home.ConfirmMail(a.Lease, append(slices.Clone(a.IDs), b.IDs...)), "only lease A's own id")
	assert.Equal(t, 1, home.ConfirmMail(b.Lease, b.IDs), "lease B still delivers its own")
}

// TestMidTurnMail_AMessageItsTurnHasClaimedIsNotPeeked: between the hand-off
// claiming a message and its turn starting, a peek must not hand it out.
func TestMidTurnMail_AMessageItsTurnHasClaimedIsNotPeeked(t *testing.T) {
	_, home, _ := gatedRunOverHome(t)
	mailArrives(t, home, "MID HAND-OFF")
	peek := home.PeekMail(7500)
	require.Len(t, peek.IDs, 1)
	// Release the peek's lease, so only the turn's claim can hold it back.
	home.mu.Lock()
	delete(home.mailLeases, peek.Lease)
	delete(home.leasedBy, peek.IDs[0])
	home.mu.Unlock()
	require.True(t, home.claimTurnMail(peek.IDs[0]))
	assert.Empty(t, home.PeekMail(7500).IDs)
}

// TestHookReachEnv_AnUnusableEndpointRefusesOnlyAnApprovedLaunch: an endpoint
// with no address the hook could reach refuses the launch when approvals
// route through that hook; without approvals the launch goes ahead with no
// hook address, losing only mid-turn mail.
func TestHookReachEnv_AnUnusableEndpointRefusesOnlyAnApprovedLaunch(t *testing.T) {
	var l launch.Launch
	l.MCP = sessions.Endpoint{URL: "no-host-here", Credential: "bearer"}

	env, err := hookReachEnv(l, &approvalSpec{})
	require.ErrorContains(t, err, "has no address for the approval hook")
	assert.Nil(t, env)

	env, err = hookReachEnv(l, nil)
	require.NoError(t, err)
	assert.Nil(t, env)
}

// waitLine is the line MidTurnMailContext writes for rest frames it could not
// carry.
func waitLine(rest int) string {
	return fmt.Sprintf("ctxloom: %d more message(s) wait for you and did not fit here; they arrive at your next tool call or turn boundary.", rest)
}

// TestMidTurnMailContext_ExactShapes pins the rendered text byte for byte:
// the lead only when a frame is carried, a blank line between the carried
// frames and the waiting line only when both are there, and a cap met
// exactly is a cap that fits.
func TestMidTurnMailContext_ExactShapes(t *testing.T) {
	long, short := strings.Repeat("L", 600), "S"

	text, n := MidTurnMailContext([]string{long}, 400)
	assert.Zero(t, n)
	assert.Equal(t, waitLine(1), text, "no frame carried: no lead, no separator, the waiting line alone")

	text, n = MidTurnMailContext([]string{short, long}, 400)
	assert.Equal(t, 1, n)
	assert.Equal(t, midTurnMailLead+"\n\n"+short+"\n\n"+waitLine(1), text)

	all := midTurnMailLead + "\n\n" + short + "\n\n" + short
	text, n = MidTurnMailContext([]string{short, short}, len(all))
	assert.Equal(t, 2, n, "a context exactly the cap's size fits")
	assert.Equal(t, all, text)
}

// TestMidTurnMail_AConfirmSkipsAnIdItsLeaseDoesNotHoldAndDeliversTheRest: an
// id the lease never held confirms nothing, and does not stop the ids after
// it from being delivered.
func TestMidTurnMail_AConfirmSkipsAnIdItsLeaseDoesNotHoldAndDeliversTheRest(t *testing.T) {
	_, home, _ := gatedRunOverHome(t)
	mailArrives(t, home, "HELD")
	peek := home.PeekMail(7500)
	require.Len(t, peek.IDs, 1)
	assert.Equal(t, 1, home.ConfirmMail(peek.Lease, append([]string{"never-leased"}, peek.IDs...)))
}

// TestMidTurnMail_AConfirmReleasesWhatItDidNotDeliver: a hook that wrote only
// part of what it was handed leaves the rest free for the very next peek, not
// held until the lease would have expired.
func TestMidTurnMail_AConfirmReleasesWhatItDidNotDeliver(t *testing.T) {
	_, home, _ := gatedRunOverHome(t)
	mailArrives(t, home, "WRITTEN")
	mailArrives(t, home, "NOT WRITTEN")
	peek := home.PeekMail(7500)
	require.Len(t, peek.IDs, 2)
	require.Equal(t, 1, home.ConfirmMail(peek.Lease, peek.IDs[:1]))

	again := home.PeekMail(7500)
	assert.Equal(t, peek.IDs[1:], again.IDs, "the unwritten message is handed out again at once")
	assert.Contains(t, again.Context, "NOT WRITTEN")
}

// TestHoldPending_TheOrderListIsCompactedOnlyPastItsBound: the arrival-order
// list keeps ids already delivered until it holds more than twice the live
// ones plus 16, and is then cut to the live ones.
func TestHoldPending_TheOrderListIsCompactedOnlyPastItsBound(t *testing.T) {
	msg := func(id string) *agentcoordpb.PeerMessage { return &agentcoordpb.PeerMessage{MessageId: id} }
	// withStale is a Home holding "live" behind `stale` ids already delivered
	// (still in the order list, gone from the pending set).
	withStale := func(stale int) *Home {
		h := &Home{}
		h.ensureMailMapsLocked()
		for i := range stale {
			h.pendingOrder = append(h.pendingOrder, fmt.Sprintf("gone-%d", i))
		}
		h.pendingOrder = append(h.pendingOrder, "live")
		h.pendingMail["live"] = msg("live")
		return h
	}

	at := withStale(18) // after the next hold: 20 ids, 2 live — exactly 2*2+16
	at.holdPendingLocked(msg("next"))
	assert.Len(t, at.pendingOrder, 20, "at the bound the list is left alone")

	past := withStale(19) // after the next hold: 21 ids, 2 live — one past it
	past.holdPendingLocked(msg("next"))
	assert.Equal(t, []string{"live", "next"}, past.pendingOrder, "past the bound only the live ids remain, in order")
}

// TestExpireMailLeases_ALeaseExpiresAtExactlyItsTTL: a lease is live for
// strictly less than mailLeaseTTL; at the TTL its messages are free.
func TestExpireMailLeases_ALeaseExpiresAtExactlyItsTTL(t *testing.T) {
	t0 := time.Now()
	leased := func() *Home {
		h := &Home{}
		h.ensureMailMapsLocked()
		h.mailLeases["tok"] = mailLease{ids: []string{"m"}, at: t0}
		h.leasedBy["m"] = "tok"
		return h
	}

	h := leased()
	h.expireMailLeasesLocked(t0.Add(mailLeaseTTL - time.Nanosecond))
	assert.Contains(t, h.mailLeases, "tok", "just short of the TTL the lease holds")
	assert.Equal(t, "tok", h.leasedBy["m"])

	h = leased()
	h.expireMailLeasesLocked(t0.Add(mailLeaseTTL))
	assert.NotContains(t, h.mailLeases, "tok", "at the TTL the lease is gone")
	assert.NotContains(t, h.leasedBy, "m", "and its message is free")
}

// TestExpireMailLeases_ALiveLeaseDoesNotShieldAnExpiredOne: one pass frees
// every expired lease, whatever live leases it meets first.
func TestExpireMailLeases_ALiveLeaseDoesNotShieldAnExpiredOne(t *testing.T) {
	now := time.Now()
	// Map order is random: enough trials that a pass stopping at the first
	// live lease it meets cannot get past them all by luck.
	for trial := range 50 {
		h := &Home{}
		h.ensureMailMapsLocked()
		for i := range 7 {
			h.mailLeases[fmt.Sprintf("live-%d", i)] = mailLease{at: now}
		}
		h.mailLeases["dead"] = mailLease{ids: []string{"m"}, at: now.Add(-mailLeaseTTL)}
		h.leasedBy["m"] = "dead"
		h.expireMailLeasesLocked(now)
		require.NotContains(t, h.mailLeases, "dead", "trial %d", trial)
		require.NotContains(t, h.leasedBy, "m", "trial %d", trial)
		require.Len(t, h.mailLeases, 7, "trial %d: the live leases stay", trial)
	}
}
