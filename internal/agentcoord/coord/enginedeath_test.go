package coord

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	agentcoordpb "github.com/ctxloom/ctxloom/internal/agentcoord"
	"github.com/ctxloom/ctxloom/internal/shared/agent"
)

// engineDeathTail is the distinctive diagnostic a dying engine adapter writes
// to stderr — the shape internal/acp now captures and wraps into the Chat
// error, which enginehost.adapt turns into a FAILED RunCompleted's
// Result.Text. It stands in for real evidence
// ("SyntaxError: Unexpected token 'with'", a JSON-RPC -32603 "Invalid API
// key") that was recoverable only via `docker logs` on a since-removed
// container.
const engineDeathTail = "acp: connection closed (engine stderr tail: SyntaxError: Unexpected token 'with')"

// TestTerminateRun_DeadEngineReasonReachesParentMailbox is the coordinator
// half of the engine-can-say-why-it-died work. A migrated child that dies
// BELOW the protocol (its adapter never answers a JSON-RPC call) emits a
// FAILED RunCompleted whose Result.Text carries the stderr tail, then reports
// RunExited. Before this change the parent learned only
// "agent ... exited (runner-exit)" — no reason anywhere, the 49-minute dead
// end. Asserting merely that the parent got SOME terminal notice is the exact
// failure this test exists to prevent: the assertion is on the REASON TEXT.
func TestTerminateRun_DeadEngineReasonReachesParentMailbox(t *testing.T) {
	resetStrictness(t)
	gate := make(chan struct{})
	sp := startRunSpawner(func() *scriptedChat { return &scriptedChat{turnGate: gate} })
	c := newTestCoordinator(t, sp, nil)

	out, err := c.AgentRun(context.Background(), ownerIdentity(), "worker", "do the thing", "", "")
	require.NoError(t, err)

	// Reach a live, journaled RunChannel so the synthetic FAILED terminal is
	// indistinguishable from a genuine one on the same (run_id, channel).
	require.Eventually(t, func() bool {
		var seq uint64
		c.mu.Lock()
		if ch := c.chans[out.Harp]; ch != nil {
			seq = ch.ackSeq
		}
		c.mu.Unlock()
		return seq > 0
	}, conformanceWait, 5*time.Millisecond, "the RunChannel must be live before the terminal is driven")

	var credHash string
	c.runs.View(func() {
		if r := c.runsF.run(out.RunID); r != nil {
			credHash = r.CredHash
		}
	})
	require.NotEmpty(t, credHash)

	// The engine dies below the protocol: a FAILED RunCompleted whose reason
	// IS the stderr tail (exactly what enginehost.adapt emits when
	// backend.Chat returns the acp-wrapped error), with NO final-channel
	// output — so bridgeTurnResult has nothing to deliver and the terminal
	// notice is the child's only voice.
	c.mu.Lock()
	ch := c.chans[out.Harp]
	seq := ch.ackSeq + 1
	c.mu.Unlock()
	require.NotNil(t, ch)
	c.handleAgentEvent(ch, &agentcoordpb.AgentEvent{
		RunId: out.RunID,
		Seq:   seq,
		Payload: &agentcoordpb.AgentEvent_RunCompleted{RunCompleted: &agentcoordpb.RunCompleted{
			Result: &agentcoordpb.Result{
				Status: agentcoordpb.Result_RUN_STATUS_FAILED,
				Text:   engineDeathTail,
			},
		}},
	})

	// The runner then reports the process-level exit (CauseRunnerExit), which
	// terminates the run and mails the parent.
	c.handleRunExited(credHash, &agentcoordpb.RunExited{RunId: out.RunID, TerminalEventSeen: true})

	msgs, err := c.AgentRecv(context.Background(), ownerIdentity(), 2*time.Second)
	require.NoError(t, err)
	require.NotEmpty(t, msgs, "the parent must learn the child died")

	var body string
	for _, m := range msgs {
		if m.From == out.Harp {
			body = m.Body
		}
	}
	require.NotEmpty(t, body, "a terminal notice from the dead child must be in the parent's mailbox")
	assert.True(t, strings.Contains(body, "SyntaxError: Unexpected token 'with'"),
		"the dead engine's own reason (the stderr tail) must reach the parent — a bare 'exited (runner-exit)' is the silent dead end this whole change exists to fix; got: %q", body)
}

// TestRunnerLoss_StderrTailReachesParentMailbox pins the fallback surface: a
// runner that dies WITHOUT emitting a FAILED RunCompleted — a docker-stop /
// OOM-kill, which reaches the coordinator as RUNNER LOSS (RunChannel
// disconnect) — still surfaces WHY, from the runner's captured stderr tail
// (the container's streamed dying words), not just "exited (runner-loss)".
func TestRunnerLoss_StderrTailReachesParentMailbox(t *testing.T) {
	resetStrictness(t)
	const containerTail = "FATAL: node: bad option: --nonsense (container entrypoint died)"
	gate := make(chan struct{})
	sp := startRunSpawner(func() *scriptedChat { return &scriptedChat{turnGate: gate} })
	sp.engineStderrTail = func() string { return containerTail }
	c := newTestCoordinator(t, sp, nil)

	out, err := c.AgentRun(context.Background(), ownerIdentity(), "worker", "do the thing", "", "")
	require.NoError(t, err)

	// Wait until the child is fully attached (its RunChannel live) so the
	// stderr-tail handle is stored on the runtime and runner loss does not
	// race the launch itself.
	require.Eventually(t, func() bool {
		var seq uint64
		c.mu.Lock()
		if ch := c.chans[out.Harp]; ch != nil {
			seq = ch.ackSeq
		}
		c.mu.Unlock()
		return seq > 0
	}, conformanceWait, 5*time.Millisecond, "the RunChannel must be live before runner loss is driven")

	var credHash string
	c.runs.View(func() {
		if r := c.runsF.run(out.RunID); r != nil {
			credHash = r.CredHash
		}
	})
	require.NotEmpty(t, credHash)

	// Runner loss: no RunCompleted, no RunExited — the credential's runner is
	// declared lost, and the coordinator synthesizes the terminal.
	c.runnerLost(credHash, "missed heartbeats past the loss bound")

	msgs, err := c.AgentRecv(context.Background(), ownerIdentity(), 2*time.Second)
	require.NoError(t, err)
	var body string
	for _, m := range msgs {
		if m.From == out.Harp {
			body = m.Body
		}
	}
	require.NotEmpty(t, body, "the parent must learn a lost child died")
	assert.True(t, strings.Contains(body, containerTail),
		"a runner that died without a terminal event must still carry its container's stderr tail to the parent; got: %q", body)
}

// --- The STANDUP window: a runner that dies before it ever dials home -------
//
// The two tests above both need a runner that came UP: one dies mid-run, the
// other is lost after its RunChannel went live. Neither covers the window
// BEFORE the runner ever dials home, which is where a spawn is at its most
// blind — readiness there is a PUSH (awaitRunner parks on a channel the
// runner's Hello closes), so nothing about that wait can distinguish a runner
// that died a millisecond after Start from one that is merely slow.
//
// That gap had teeth: issueStartRun waited out the WHOLE runnerAwaitTimeout
// (five minutes in production) before failChild queued the parent anything at
// all. For that entire window the parent's mailbox stayed EMPTY — no
// agent_send, no bridgeTurnResult turn copy, and not even a terminal notice —
// which reads exactly like a child that launched and hung. It is also longer
// than any realistic observation window: the live delegation floor
// (j002300_cross_engine_delegation.feature) watches for 240s, so a dead runner
// could exhaust that budget having said nothing, and be indistinguishable
// from a product defect in the engine path.

// runnerDeathTail is the distinctive dying message the runner PROCESS writes
// before exiting during standup — the shape isolation.RunnerHandle.Wait's
// error already embeds (both implementations wrap the exit with their bounded
// stderr tail). It stands in for the real shapes: an unknown backend name, a
// config the runner refused, a fail-loud startup finding.
const runnerDeathTail = `host runner exited: exit status 1 (stderr tail: Error: unknown backend: opencode)`

// deadRunnerSpawner spawns a runner that NEVER dials home and whose process
// exits immediately afterwards. It deliberately does NOT build the fake's
// usual in-process Home/EngineHost pair (fakeSpawner.StartEngine), because
// that pair dials home instantly and so can never model this window at all.
type deadRunnerSpawner struct {
	*fakeSpawner
	// exitErr is what the runner's Wait reports; nil models the quietest
	// failure of all — a runner that exits 0 without ever dialing home.
	exitErr error
	// waited records that the coordinator actually consumed the death signal
	// rather than merely timing out with it unread.
	waited chan struct{}
}

func newDeadRunnerSpawner(exitErr error) *deadRunnerSpawner {
	return &deadRunnerSpawner{
		fakeSpawner: newFakeSpawner(map[string]fakeAgent{
			"worker": {perm: "bypass", runtime: agent.RuntimeContainerRootless, viaStartRun: true},
		}, nil),
		exitErr: exitErr,
		waited:  make(chan struct{}, 1),
	}
}

func (s *deadRunnerSpawner) StartEngine(_ context.Context, _ *SpawnPlan, env, _ map[string]string) (*EngineSpawn, error) {
	return &EngineSpawn{
		WorkDir: "/work",
		Env:     env,
		Model:   "test-model",
		Kill:    func() {},
		Wait: func() error {
			select {
			case s.waited <- struct{}{}:
			default:
			}
			return s.exitErr
		},
	}, nil
}

// assertDeadRunnerIsReportedPromptly is the shared body of the two cases
// below: spawn a child whose runner dies at standup under a runnerAwaitTimeout
// far longer than the test's own patience, and require that the parent learns
// WELL INSIDE that budget. The generous budget is the whole point — a pass can
// only come from the death being OBSERVED, never from the deadline expiring.
func assertDeadRunnerIsReportedPromptly(t *testing.T, exitErr error, wantReason string) {
	t.Helper()
	resetStrictness(t)
	sp := newDeadRunnerSpawner(exitErr)
	c, err := New(Options{
		ProjectDir: t.TempDir(),
		StateDir:   t.TempDir(),
		Spawner:    sp,
		// Minutes, as in production. If this test passes only because this
		// elapsed, it would take minutes to do it — the 2s AgentRecv below
		// would have long since returned empty.
		RunnerAwaitTimeout: 5 * time.Minute,
	})
	require.NoError(t, err)
	require.NoError(t, c.Serve())
	t.Cleanup(c.Close)

	out, err := c.AgentRun(context.Background(), ownerIdentity(), "worker", "do the thing", "", "")
	require.NoError(t, err, "agent_run is async: the launch failure surfaces on the mailbox, not here")

	msgs, err := c.AgentRecv(context.Background(), ownerIdentity(), 2*time.Second)
	require.NoError(t, err)
	var body string
	for _, m := range msgs {
		if m.From == out.Harp {
			body = m.Body
		}
	}
	require.NotEmpty(t, body,
		"the parent's mailbox was still EMPTY 2s after a runner died at standup, with a 5-minute dial-home budget "+
			"left to run: this is the silent window a delegated child's coordinator cannot tell apart from a hung engine")
	assert.Contains(t, body, wantReason,
		"the parent must be told the RUNNER died and why — a bare dial-home deadline names neither; got: %q", body)

	select {
	case <-sp.waited:
	default:
		t.Fatal("the coordinator never consumed the runner's exit signal — a report that arrived without reading it is not this mechanism working")
	}
}

// TestIssueStartRun_DeadRunnerReachesParentBeforeTheDialHomeBudget is the
// standup-window sibling of the two tests above, in its load-bearing form:
// PROMPTLY, and with the runner's own dying words. Asserting merely that the
// parent eventually got some notice would be satisfied by the old five-minute
// timeout, which is the exact behaviour this pins against.
func TestIssueStartRun_DeadRunnerReachesParentBeforeTheDialHomeBudget(t *testing.T) {
	assertDeadRunnerIsReportedPromptly(t, errors.New(runnerDeathTail), runnerDeathTail)
}

// TestIssueStartRun_CleanlyExitedRunnerIsStillADeath covers the quietest
// failure: a runner that exits 0 without ever dialing home (`ctxloom` with no
// subcommand prints help and exits 0 — the shape pb.StartHostRunner refuses a
// bare self-exec to avoid). It has failed just as completely as one that
// crashed, and counting only non-zero exits would leave precisely the
// quietest failure on the old silent path.
func TestIssueStartRun_CleanlyExitedRunnerIsStillADeath(t *testing.T) {
	assertDeadRunnerIsReportedPromptly(t, nil, "exited cleanly")
}
