package coord

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/core/spool"
)

// The runner's owner-loss clock runs only while the runner WAITS on its
// owner, and a runner whose owner is away starts no new turn: these pin both
// against a real coordinator that crashes and comes back.

// orphanWindow is the owner-loss window these runners run under: short, so
// the tests can outlive it several times over.
const orphanWindow = time.Second

func closed(ch <-chan struct{}) bool {
	select {
	case <-ch:
		return true
	default:
		return false
	}
}

// TestOwnerLoss_AnOrphanedTurnRunsToItsEnd_ThenTheRunnerWaits: the coordinator
// dies mid-turn and the turn outlives the window several times over. It is
// not cut off — the clock is paused while the turn makes progress — and the
// wait, and so the clock, starts only when the turn ends.
func TestOwnerLoss_AnOrphanedTurnRunsToItsEnd_ThenTheRunnerWaits(t *testing.T) {
	resetStrictness(t)
	gate := make(chan struct{})
	sp := startRunSpawner(func() *scriptedChat { return &scriptedChat{Gate: gate} })
	sp.ownerLossWindow = orphanWindow
	t.Cleanup(func() {
		for i := 0; i < sp.spawnCount(); i++ {
			sp.killEngine(i)
		}
	})
	c := newTestCoordinatorOver(t, t.TempDir(), sp)
	_, err := c.AgentRun(context.Background(), ownerIdentity(), "worker", "a long task", "", "")
	require.NoError(t, err)
	require.Eventually(t, func() bool { return sp.chatCount() == 1 && len(sp.chat(0).RecordedTexts()) == 1 }, conformanceWait, 10*time.Millisecond,
		"the briefing turn is running")
	home := sp.engineHome(0)

	crashCoordinator(c)
	time.Sleep(3 * orphanWindow)
	require.False(t, closed(home.OwnerLost()), "a turn making progress without its owner must not be cut off by the owner-loss window")

	ended := time.Now()
	close(gate)
	select {
	case <-home.OwnerLost():
		require.GreaterOrEqual(t, time.Since(ended), orphanWindow, "the wait starts when the turn ends")
	case <-time.After(conformanceWait + orphanWindow):
		t.Fatal("the runner, idle with no owner, never gave up")
	}
}

// TestOwnerLoss_AReadoptionMidTurnKeepsTheTurn: the runner keeps redialling
// mid-turn, so a coordinator that comes back while the turn is still running
// re-adopts it at once, and the turn's result reaches the new coordinator.
func TestOwnerLoss_AReadoptionMidTurnKeepsTheTurn(t *testing.T) {
	resetStrictness(t)
	stateDir := t.TempDir()
	gate := make(chan struct{})
	sp := startRunSpawner(func() *scriptedChat { return &scriptedChat{Gate: gate} })
	sp.ownerLossWindow = orphanWindow
	t.Cleanup(func() {
		for i := 0; i < sp.spawnCount(); i++ {
			sp.killEngine(i)
		}
	})
	first := newTestCoordinatorOver(t, stateDir, sp)
	out, err := first.AgentRun(context.Background(), ownerIdentity(), "worker", "a long task", "", "")
	require.NoError(t, err)
	require.Eventually(t, func() bool { return sp.chatCount() == 1 && len(sp.chat(0).RecordedTexts()) == 1 }, conformanceWait, 10*time.Millisecond)

	crashCoordinator(first)
	time.Sleep(2 * orphanWindow) // longer than the window: only the paused clock keeps the runner
	second := newTestCoordinatorOver(t, stateDir, sp)
	require.Eventually(t, func() bool { return second.runnerConnected(out.RunID) }, conformanceWait, 10*time.Millisecond,
		"the runner redials mid-turn and is re-adopted while the turn still runs")

	close(gate)
	res := recvWhere(t, second, func(m Message) bool { return m.Kind == "result" && strings.Contains(m.Body, "a long task") }, conformanceWait)
	require.NotEmpty(t, res, "the orphaned turn's result reaches the coordinator that re-adopted it")
	require.False(t, closed(sp.engineHome(0).OwnerLost()))
}

// TestOwnerLoss_NoNewTurnWhileTheOwnerIsAway: "let it finish, then wait".
// Mail lands in the idle runner's in/ spool while its coordinator is down —
// the one way a turn can be offered with no coordinator — and the sweep that
// reads it hands it on, but no turn starts. The returning coordinator
// re-adopts the runner, and the waiting mail becomes the next turn.
func TestOwnerLoss_NoNewTurnWhileTheOwnerIsAway(t *testing.T) {
	resetStrictness(t)
	stateDir := t.TempDir()
	sp := startRunSpawner(func() *scriptedChat { return &scriptedChat{} })
	t.Cleanup(func() {
		for i := 0; i < sp.spawnCount(); i++ {
			sp.killEngine(i)
		}
	})
	first := newTestCoordinatorOver(t, stateDir, sp)
	out, err := first.AgentRun(context.Background(), ownerIdentity(), "worker", "task one", "", "")
	require.NoError(t, err)
	require.Eventually(t, func() bool { return rosterState(first, out.Harp) == StateIdle }, conformanceWait, 10*time.Millisecond)
	home := sp.engineHome(0)
	chat := sp.chat(0)
	turns := len(chat.RecordedTexts())

	crashCoordinator(first)
	wr, err := spool.NewWriter(spool.NewHomeMapper(), out.Harp, spool.DirIn, "coord")
	require.NoError(t, err)
	_, err = wr.Write(&spool.Message{Kind: KindMessage, FromHarp: ownerIdentity().Harp, To: out.Harp, Body: "mail while away\n"})
	require.NoError(t, err)
	before := home.SpoolDeliveryStats().Delivered
	home.SweepSpoolIn()
	require.Eventually(t, func() bool { return home.SpoolDeliveryStats().Delivered > before }, conformanceWait, 10*time.Millisecond,
		"the sweep read the mail and offered it")
	time.Sleep(500 * time.Millisecond) // the offer is held at the owner gate, not in flight
	require.Len(t, chat.RecordedTexts(), turns, "no new turn starts while the owner is away")

	second := newTestCoordinatorOver(t, stateDir, sp)
	require.Eventually(t, func() bool { return second.runnerConnected(out.RunID) }, conformanceWait, 10*time.Millisecond)
	require.Eventually(t, func() bool {
		texts := chat.RecordedTexts()
		return len(texts) == turns+1 && strings.Contains(texts[turns], "mail while away")
	}, conformanceWait, 10*time.Millisecond, "the waiting mail becomes the next turn once the owner is back")
}
