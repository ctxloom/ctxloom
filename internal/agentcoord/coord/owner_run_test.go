package coord

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	agentcoordpb "github.com/ctxloom/ctxloom/internal/agentcoord"
	"github.com/ctxloom/ctxloom/internal/shared/agent"
)

// errStopBeforeDial is a test-only sentinel a starter returns to abort
// StartOwnedRun right after handing back the spawn env, without standing up
// a real runner — for tests that only need to inspect the stamped env.
var errStopBeforeDial = errors.New("test: stop before dial")

// ownerRunStarter builds an OwnedRunStarter that spawns an in-process runner
// half (Home + EngineHost around a scriptedChat) dialing the coordinator's live
// listeners with the per-run trio StartOwnedRun mints — the same in-process
// runner fakeSpawner.StartEngine uses, minus a real container. started flips
// true once the starter is invoked (proving StartOwnedRun launches the runner).
func ownerRunStarter(ctx context.Context, sc *scriptedChat, backend string) (OwnedRunStarter, *bool) {
	started := new(bool)
	starter := func(_ context.Context, spawnEnv map[string]string) (func(), string, error) {
		*started = true
		sctx, cancel := context.WithCancel(ctx)
		host := NewEngineHost(sctx, sc, backend, spawnEnv[EnvRunID])
		home, err := NewHome(sctx, HomeConfig{
			URL:     spawnEnv[EnvCoordURL],
			Token:   spawnEnv[EnvCoordCred],
			RunID:   spawnEnv[EnvRunID],
			Harness: backend,
			Version: "test",
			Engine:  host.Handle,
		})
		if err != nil {
			cancel()
			return nil, "", err
		}
		host.BindHome(home)
		// No real container behind this in-process fake — "" is the correct
		// (host-shaped) return, exercised by
		// TestListRunsSnapshot_ContainerNameEmptyForHostRun.
		return func() { cancel(); home.crash() }, "", nil
	}
	return starter, started
}

// collectFinalDeltas drains events for wait, concatenating every FINAL-channel
// MessageDelta text for runID (the payload the host renders over Transport 2).
func collectFinalDeltas(events <-chan *agentcoordpb.AgentEvent, runID string, wait time.Duration) string {
	deadline := time.After(wait)
	final := map[string]bool{}
	var out string
	for {
		select {
		case ev := <-events:
			if ev.GetRunId() != runID {
				continue
			}
			switch p := ev.GetPayload().(type) {
			case *agentcoordpb.AgentEvent_MessageStarted:
				if p.MessageStarted.GetChannel() == agentcoordpb.MessageChannel_MESSAGE_CHANNEL_FINAL {
					final[p.MessageStarted.GetMessageId()] = true
				}
			case *agentcoordpb.AgentEvent_MessageDelta:
				if final[p.MessageDelta.GetMessageId()] {
					out += p.MessageDelta.GetText()
				}
			}
		case <-deadline:
			return out
		}
	}
}

// TestStartOwnedRun_ParentLessOwnerRunYieldsPayload is Phase 2a-B's core unit
// proof: StartOwnedRun mints an OWNER-OWNED, PARENT-LESS run (ParentRunID ""),
// spawns the runner via the injected starter, issues StartRun over the existing
// RunnerChannel, and the engine's answer text reaches the host over the
// in-process WatchRuns event stream — no client.Chat, no proto change.
func TestStartOwnedRun_ParentLessOwnerRunYieldsPayload(t *testing.T) {
	sp := newFakeSpawner(nil, nil)
	c := newTestCoordinator(t, sp, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	const ownerHarp = "owner-harp"
	token, err := c.RegisterSessionOwner(ownerHarp)
	require.NoError(t, err)
	owner, ok := c.Identify(token)
	require.True(t, ok)
	require.Equal(t, 0, owner.Depth, "the session owner is depth 0")

	// Watch BEFORE the run so no early delta is missed.
	_, events, wcancel, _ := c.WatchRuns(nil)
	defer wcancel()

	sc := &scriptedChat{}
	starter, started := ownerRunStarter(ctx, sc, "claude-code")

	outcome, err := c.StartOwnedRun(ctx, owner, OwnerRunSpec{
		Harp:       ownerHarp,
		Backend:    "claude-code",
		Label:      "fast",
		Model:      "sonnet",
		WorkDir:    "/work",
		Permission: agent.PermissionBypass,
	}, starter, "hello owner run")
	require.NoError(t, err)
	require.True(t, *started, "StartOwnedRun must launch the runner via the starter")
	require.Equal(t, ownerHarp, outcome.Harp)
	require.NotEmpty(t, outcome.RunID)

	// Parent-less, owner-owned: ParentRunID is empty on the roster projection.
	var found *agentcoordpb.ListRunsResult_RunInfo
	for _, r := range c.ListRuns(true, "").GetRuns() {
		if r.GetRunId() == outcome.RunID {
			found = r
		}
	}
	require.NotNil(t, found, "the owner-owned run must appear in the roster")
	assert.Equal(t, "", found.GetParentRunId(), "an owner-owned run is parent-less")

	// PAYLOAD over Transport 2: the scriptedChat echoes the first-turn prompt.
	got := collectFinalDeltas(events, outcome.RunID, 10*time.Second)
	assert.Contains(t, got, "echo: hello owner run", "the engine's answer must reach the host over WatchRuns")
}

// TestStartOwnedRun_StampsOwnerAtDepthZero pins the regression this whole
// depth-based leaf design exists to prevent: the container top-level owned
// run's runner env carries EnvRunDepth "0" — the SAME depth the session
// owner itself is — never owner.Depth+1. A wrong stamp here would make the
// top-level session a LEAF at the built-in cap and silently strip
// agent_run/roster/agent_stop/agent_fetch_artifact from a `ctxloom run
// --structured`/`--print` session on runtime:container.
func TestStartOwnedRun_StampsOwnerAtDepthZero(t *testing.T) {
	sp := newFakeSpawner(nil, nil)
	c := newTestCoordinator(t, sp, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	const ownerHarp = "owner-harp"
	token, err := c.RegisterSessionOwner(ownerHarp)
	require.NoError(t, err)
	owner, ok := c.Identify(token)
	require.True(t, ok)

	var gotEnv map[string]string
	starter := func(_ context.Context, spawnEnv map[string]string) (func(), string, error) {
		gotEnv = spawnEnv
		// No real runner needed for this test — it inspects the stamped
		// env, not the run's live behavior. Fail loud rather than hanging
		// the coordinator's await-runner budget on a Home that never dials
		// home.
		return func() {}, "", errStopBeforeDial
	}

	_, err = c.StartOwnedRun(ctx, owner, OwnerRunSpec{
		Harp: ownerHarp, Backend: "claude-code", Label: "fast", Model: "sonnet",
		WorkDir: "/work", Permission: agent.PermissionBypass,
	}, starter, "hello")
	require.ErrorIs(t, err, errStopBeforeDial)
	require.NotNil(t, gotEnv, "the starter must have been invoked with the runner env")
	assert.Equal(t, "0", gotEnv[EnvRunDepth], "the owner-owned run must stamp depth 0, matching the session owner it IS")
}

// TestStartOwnedRun_OwnerHarpRoleNoCollision is the §5.B2 named collision
// audit: an owner-owned run REUSES the owner's harp as its run role, so the
// role-keyed coordinator maps (byHarp/chans/mailbox) must stay coherent and the
// self-report bridge must NOT fire (a bridge to the owner's own mailbox would
// re-deliver the run's output as its own next turn — an infinite self-loop).
func TestStartOwnedRun_OwnerHarpRoleNoCollision(t *testing.T) {
	sp := newFakeSpawner(nil, nil)
	c := newTestCoordinator(t, sp, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	const ownerHarp = "owner-harp"
	token, err := c.RegisterSessionOwner(ownerHarp)
	require.NoError(t, err)
	owner, ok := c.Identify(token)
	require.True(t, ok)

	sc := &scriptedChat{}
	starter, _ := ownerRunStarter(ctx, sc, "claude-code")
	outcome, err := c.StartOwnedRun(ctx, owner, OwnerRunSpec{
		Harp: ownerHarp, Backend: "claude-code", Label: "fast", Model: "sonnet",
		WorkDir: "/work", Permission: agent.PermissionBypass,
	}, starter, "turn one")
	require.NoError(t, err)

	// The owner's credential still resolves to depth 0 — the per-run credential
	// StartOwnedRun minted did NOT clobber the session-owner credential; both
	// coexist under the same harp.
	still, ok := c.Identify(token)
	require.True(t, ok)
	assert.Equal(t, 0, still.Depth, "the session-owner credential survives the owner-owned run")

	// The run registers at byHarp[ownerHarp] as an owner run, and its RunChannel
	// attaches at chans[ownerHarp] (the runner dialed home) — no map collision.
	require.Eventually(t, func() bool {
		c.mu.Lock()
		defer c.mu.Unlock()
		return c.chans[ownerHarp] != nil
	}, 5*time.Second, 20*time.Millisecond, "the owner-owned run's RunChannel must attach at chans[ownerHarp]")
	c.mu.Lock()
	rt := c.byHarp[ownerHarp]
	c.mu.Unlock()
	require.NotNil(t, rt, "byHarp[ownerHarp] must be the owner-owned run")
	assert.True(t, rt.ownerRun, "the run is flagged ownerRun")
	assert.Equal(t, outcome.RunID, rt.runID)

	// Drive one FOLLOW-UP turn via SendOwnedRunTurn (the mailbox→pushMail path).
	require.NoError(t, c.SendOwnedRunTurn(outcome.RunID, "turn two"))

	// The engine received EXACTLY the two turns we sent — the initial StartRun
	// input and the one follow-up. A self-bridge loop would balloon this count
	// (each completed turn re-queued as a new turn), so an exact 2 is the
	// no-collision, no-self-loop proof.
	require.Eventually(t, func() bool {
		return len(sc.recordedTexts()) == 2
	}, 10*time.Second, 50*time.Millisecond, "exactly the two sent turns reach the engine")
	// And it never grows past two (no self-loop) over a further window.
	time.Sleep(500 * time.Millisecond)
	assert.Len(t, sc.recordedTexts(), 2, "no extra turns — the owner-owned run does not self-report into its own mailbox")

	// The owner's own mailbox never accumulated a bridged "result": the bridge
	// is suppressed for an owner-owned run (nothing to report to — the host
	// watches the run directly). An empty mailbox times out with no message,
	// which is exactly the outcome we want to prove — never a "result".
	msgs, err := c.AgentRecv(context.Background(), owner, 200*time.Millisecond)
	if err == nil {
		for _, m := range msgs {
			assert.NotEqual(t, "result", m.Kind, "no self-bridged result may land in the owner's mailbox")
		}
	}
}
