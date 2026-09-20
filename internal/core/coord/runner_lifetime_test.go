package coord

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/core/agent"
	"github.com/ctxloom/ctxloom/internal/core/engine"
)

// newTestCoordinatorIdle is newTestCoordinator with the idle reaper's
// timeout set (Options.IdleTimeout) and an injected clock.
func newTestCoordinatorIdle(t *testing.T, sp Spawner, clock func() time.Time, idle time.Duration) *Coordinator {
	t.Helper()
	teeHome(t)
	c, err := New(Options{
		ProjectDir:  t.TempDir(),
		StateDir:    t.TempDir(),
		Spawner:     sp,
		Clock:       clock,
		IdleTimeout: idle,
		OwnerHarp:   ownerIdentity().Harp,
		Reporter:    termSink(),
	})
	require.NoError(t, err)
	require.NoError(t, c.Serve())
	t.Cleanup(c.Close)
	return c
}

// TestRunnerLifetime_OneShotBoundaryParksTheRunner_MailRidesTheSameRunner is
// slice 9's process-shape gate at the coordinator: a driving:oneshot child
// runs turn 1; at the boundary its ENGINE process ends but the RUNNER stays
// (the run is idle, not ended — no new incarnation, no new endpoint); a later
// agent_send lands as turn 2 on the SAME runner, driven as a fresh engine
// process resumed by the captured native key. One spawn for two turns.
func TestRunnerLifetime_OneShotBoundaryParksTheRunner_MailRidesTheSameRunner(t *testing.T) {
	resetStrictness(t)
	sp := oneShotSpawner(func() *scriptedChat { return &scriptedChat{resumable: true} })
	c := newTestCoordinator(t, sp, nil)

	out, err := c.AgentRun(context.Background(), ownerIdentity(), "worker", "task one", "", "")
	require.NoError(t, err)

	require.Eventually(t, func() bool { return rosterState(c, out.Harp) == StateIdle }, conformanceWait, 10*time.Millisecond,
		"the one-shot boundary parks the runner: the run is IDLE, not ended")
	assert.Equal(t, "", runCause(c, out.RunID), "no terminal at the boundary — the run is alive on a parked runner")
	assert.Equal(t, 1, sp.spawnCount(), "one runner for the session")
	res1 := recvWhere(t, c, func(m Message) bool { return m.Kind == "result" && strings.Contains(m.Body, "task one") }, conformanceWait)
	require.NotEmpty(t, res1, "turn 1's result must bridge to the parent")
	assertNoMailKind(t, c, KindExited, 200*time.Millisecond)

	// Turn 2 is mail: the spool delivers it to the SAME runner, which drives a
	// discrete engine process resumed by the native key it learned on turn 1.
	_, err = c.AgentSend(ownerIdentity(), out.Harp, KindMessage, "carry on", nil, "")
	require.NoError(t, err)
	res2 := recvWhere(t, c, func(m Message) bool { return m.Kind == "result" && strings.Contains(m.Body, "carry on") }, conformanceWait)
	require.NotEmpty(t, res2, "the second turn's result must be delivered")

	assert.Equal(t, 1, sp.spawnCount(), "turn 2 rode the same runner: no second spawn")
	assert.Equal(t, out.RunID, currentRunID(c, out.Harp), "the run incarnation is unchanged across the boundary")
	sc := sp.chat(0)
	require.NotNil(t, sc)
	sc.mu.Lock()
	requests := append([]agent.ChatRequest(nil), sc.requests...)
	sc.mu.Unlock()
	require.Len(t, requests, 2, "a discrete engine process per turn, inside one runner")
	assert.Equal(t, "", requests[0].ResumeSessionID)
	assert.Equal(t, "native-sess-42", requests[1].ResumeSessionID, "turn 2 resumes the engine by the key turn 1 reported")
	assertNoMailKind(t, c, KindExited, 200*time.Millisecond)
}

// TestRunnerLifetime_TurnFrameDrivesAParkedRunner: RunnerTransport.Turn is
// the one-shot turn injection — a frame to the LIVE runner, not a new run.
// It drives one engine turn on the parked runner and answers with the turn's
// result and the key the next turn resumes by.
func TestRunnerLifetime_TurnFrameDrivesAParkedRunner(t *testing.T) {
	resetStrictness(t)
	sp := oneShotSpawner(func() *scriptedChat { return &scriptedChat{resumable: true} })
	c := newTestCoordinator(t, sp, nil)

	out, err := c.AgentRun(context.Background(), ownerIdentity(), "worker", "task one", "", "")
	require.NoError(t, err)
	require.Eventually(t, func() bool { return rosterState(c, out.Harp) == StateIdle }, conformanceWait, 10*time.Millisecond)

	ctx, cancel := context.WithTimeout(context.Background(), conformanceWait)
	defer cancel()
	res, err := c.Turn(ctx, out.RunID, engine.Turn{Prompt: "framed turn", Resume: harnessSessionID(c, out.Harp)})
	require.NoError(t, err)
	assert.Equal(t, "native-sess-42", res.NativeKey)
	assert.Contains(t, res.Answer, "framed turn", "the frame's answer is the turn's final text")
	assert.Equal(t, 1, sp.spawnCount(), "a Turn frame never spawns")

	_, err = c.Turn(ctx, "run-that-does-not-exist", engine.Turn{Prompt: "x"})
	require.ErrorIs(t, err, ErrNoLiveRun)
}

// TestRunnerLifetime_IdleReaper_EndsAParkedRunnerAfterIdleTimeout: a runner
// with no turn for delegation.idle_timeout is ended by the reaper — the one
// door besides agent_stop, terminateRun and the runner's own exit. The harp
// stays resumable: the next mail starts a new incarnation through the resume
// arm. The clock is injected and the sweep is invoked, never awaited.
func TestRunnerLifetime_IdleReaper_EndsAParkedRunnerAfterIdleTimeout(t *testing.T) {
	resetStrictness(t)
	now := time.Date(2026, 9, 20, 12, 0, 0, 0, time.UTC)
	var clockMu sync.Mutex
	clock := func() time.Time { clockMu.Lock(); defer clockMu.Unlock(); return now }
	advance := func(d time.Duration) { clockMu.Lock(); now = now.Add(d); clockMu.Unlock() }

	sp := startRunSpawner(func() *scriptedChat { return &scriptedChat{} })
	c := newTestCoordinatorIdle(t, sp, clock, time.Minute)

	out, err := c.AgentRun(context.Background(), ownerIdentity(), "worker", "task one", "", "")
	require.NoError(t, err)
	require.Eventually(t, func() bool { return rosterState(c, out.Harp) == StateIdle }, conformanceWait, 10*time.Millisecond)

	advance(30 * time.Second)
	c.reapIdleRuns()
	assert.Equal(t, StateIdle, rosterState(c, out.Harp), "under the timeout nothing is reaped")

	advance(31 * time.Second)
	c.reapIdleRuns()
	require.Eventually(t, func() bool { return rosterState(c, out.Harp) == StateEnded }, conformanceWait, 10*time.Millisecond,
		"past the timeout the idle run is ended")
	assert.Equal(t, CauseIdleReaped, runCause(c, out.RunID))
	select {
	case <-sp.released[0]:
	case <-time.After(conformanceWait):
		t.Fatal("the reaper must release the runner (the one teardown door was used)")
	}
	assertNoMailKind(t, c, KindExited, 200*time.Millisecond)

	// The harp is resumable: mail starts a NEW incarnation (a second spawn).
	_, err = c.AgentSend(ownerIdentity(), out.Harp, KindMessage, "wake", nil, "")
	require.NoError(t, err)
	res := recvWhere(t, c, func(m Message) bool { return m.Kind == "result" && strings.Contains(m.Body, "wake") }, conformanceWait)
	require.NotEmpty(t, res)
	assert.Equal(t, 2, sp.spawnCount(), "a reaped session resumes through a new runner incarnation")
	assert.NotEqual(t, out.RunID, currentRunID(c, out.Harp), "a new incarnation is a new run id")
}

// TestRunnerLifetime_EndpointUnavailable_RebindsAndReissuesToTheSameRunner:
// the runner refuses to bind the recorded address (another process took the
// port between two incarnations); the coordinator answers that ONE refusal
// by re-resolving with RebindEndpoint and re-issuing StartRun to the SAME
// runner — a new endpoint, no new spawn, and the run comes up.
func TestRunnerLifetime_EndpointUnavailable_RebindsAndReissuesToTheSameRunner(t *testing.T) {
	resetStrictness(t)
	sp := startRunSpawner(func() *scriptedChat { return &scriptedChat{} })
	sp.refuseBinds = 1
	c := newTestCoordinator(t, sp, nil)

	out, err := c.AgentRun(context.Background(), ownerIdentity(), "worker", "task one", "", "")
	require.NoError(t, err)
	res := recvWhere(t, c, func(m Message) bool { return m.Kind == "result" && strings.Contains(m.Body, "task one") }, conformanceWait)
	require.NotEmpty(t, res, "the run comes up after the rebind")

	launches := sp.resolvedLaunches()
	require.Len(t, launches, 2, "the launch is resolved twice: once, then again with a rebind")
	assert.False(t, sp.rebinds()[0])
	assert.True(t, sp.rebinds()[1], "the second resolution asks for a rebind")
	assert.NotEqual(t, launches[0].MCP, launches[1].MCP, "the rebind minted a new endpoint")
	assert.Equal(t, 1, sp.spawnCount(), "the same runner: a rebind is a frame, not a spawn")
	assert.Equal(t, out.RunID, currentRunID(c, out.Harp))
}

// TestRunnerLifetime_EndpointUnavailableTwice_FailsTheChild: the rebind is
// answered ONCE. A runner that cannot bind a freshly minted address has a
// problem no second mint fixes, and the child fails loud.
func TestRunnerLifetime_EndpointUnavailableTwice_FailsTheChild(t *testing.T) {
	resetStrictness(t)
	sp := startRunSpawner(func() *scriptedChat { return &scriptedChat{} })
	sp.refuseBinds = 2
	c := newTestCoordinator(t, sp, nil)

	out, err := c.AgentRun(context.Background(), ownerIdentity(), "worker", "task one", "", "")
	require.NoError(t, err)
	require.Eventually(t, func() bool { return rosterState(c, out.Harp) == StateEnded }, conformanceWait, 10*time.Millisecond)
	assert.Len(t, sp.resolvedLaunches(), 2, "exactly one rebind is attempted")
	errs := recvKind(t, c, KindError, conformanceWait)
	require.NotEmpty(t, errs, "the parent learns the launch failed")
}
