package coord

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/core/engine"
	"github.com/ctxloom/ctxloom/internal/core/launch"
	"github.com/ctxloom/ctxloom/internal/core/present"
	"github.com/ctxloom/ctxloom/internal/core/sessions"
)

// engineDeathTail is the distinctive diagnostic a dying engine adapter writes
// to stderr — the shape the stderr-tail capture wraps into the Chat error,
// which EngineHost.adapt turns into a FAILED RunCompleted's Result.Text.
// It stands in for real evidence
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
	qe := newQuiescentEngine()
	sp := startRunSpawner(nil)
	sp.nextBackend = func() engine.Instance { return qe }
	c := newTestCoordinator(t, sp, nil)

	out, err := c.AgentRun(context.Background(), ownerIdentity(), "worker", "do the thing", "", "")
	require.NoError(t, err)

	// The test speaks for the runner on the runner's own (run, seq) space, so
	// it may only do so once the runner has nothing left to say: a seq the
	// live Home assigns after the test picked one is a collision, and
	// HandleEvent drops the second arrival as a duplicate. The engine is
	// inside its turn and relays nothing, so the Home's seq is FINAL here.
	select {
	case <-qe.entered:
	case <-time.After(conformanceWait):
		t.Fatal("the engine never entered its first turn")
	}
	home := sp.engineHome(0)
	require.NotNil(t, home)
	last := home.EmittedSeq()
	require.Positive(t, last, "RunStarted precedes the first turn")
	var ch *RunChannel
	require.Eventually(t, func() bool {
		c.mu.Lock()
		defer c.mu.Unlock()
		ch = c.chans[out.Harp]
		return ch != nil && ch.ackSeq >= last
	}, conformanceWait, 5*time.Millisecond, "the coordinator must have received every event the quiescent runner emitted")

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
	c.HandleEvent(ch, Event{
		RunID:   out.RunID,
		Seq:     last + 1,
		Payload: RunCompleted{Result: &Result{Status: RunStatusFailed, Text: engineDeathTail}},
	})
	select {
	case <-ch.completed:
	default:
		t.Fatal("the synthetic FAILED terminal was not journaled — a seq the runner had already used makes HandleEvent drop it as a duplicate")
	}

	// The runner then reports the process-level exit (CauseRunnerExit), which
	// terminates the run and mails the parent.
	c.RunnerExited(credHash, RunExited{RunID: out.RunID, TerminalEventSeen: true})

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

// quiescentEngine is an engine whose turn relays nothing and ends only when
// its run is cancelled; entered closes the moment the first turn begins. It
// makes "the runner has emitted everything it will emit" an observable
// point rather than a guess.
type quiescentEngine struct {
	entered chan struct{}
	once    sync.Once
}

func newQuiescentEngine() *quiescentEngine {
	return &quiescentEngine{entered: make(chan struct{})}
}

func (q *quiescentEngine) Exec([]present.Presentation) (engine.Exec, error) {
	return engine.Exec{Binary: "quiescent", Env: map[string]string{}}, nil
}
func (q *quiescentEngine) Drivers() []engine.StructuredDriver { return []engine.StructuredDriver{q} }
func (q *quiescentEngine) Resume(string) error                { return nil }
func (q *quiescentEngine) Turn(ctx context.Context, _ engine.Exec, _ engine.Turn, _ chan<- engine.Event) (engine.TurnResult, error) {
	q.once.Do(func() { close(q.entered) })
	<-ctx.Done()
	return engine.TurnResult{}, ctx.Err()
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
	sp := startRunSpawner(func() *scriptedChat { return &scriptedChat{Gate: gate} })
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
const runnerDeathTail = `host runner exited: exit status 1 (stderr tail: Error: unknown backend: nosuchengine)`

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
			"worker": {perm: "bypass", runtime: launch.RuntimeRootless},
		}, nil),
		exitErr: exitErr,
		waited:  make(chan struct{}, 1),
	}
}

func (s *deadRunnerSpawner) Start(_ context.Context, _ launch.Launch, _ sessions.Endpoint) (*EngineSpawn, error) {
	return &EngineSpawn{
		Kill: func() {},
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
	teeHome(t)
	c, err := New(Options{
		ProjectDir: t.TempDir(),
		StateDir:   t.TempDir(),
		Spawner:    sp,
		// Minutes, as in production. If this test passes only because this
		// elapsed, it would take minutes to do it — the 2s AgentRecv below
		// would have long since returned empty.
		RunnerAwaitTimeout: 5 * time.Minute,
		OwnerHarp:          ownerIdentity().Harp,
	})
	require.NoError(t, err)
	require.NoError(t, runnerHooks.Serve(c))
	t.Cleanup(c.Close)

	out, err := c.AgentRun(context.Background(), ownerIdentity(), "worker", "do the thing", "", "")
	require.NoError(t, err, "agent_run is async: the launch failure surfaces on the mailbox, not here")

	msgs, err := c.AgentRecv(context.Background(), ownerIdentity(), 2*time.Second)
	require.NoError(t, err)
	// Every message the child's death produced is read together (the death
	// notice and the exit notice are two files, swept as one batch), so the
	// reason is looked for in ALL of them rather than in whichever came last.
	var bodies []string
	for _, m := range msgs {
		if m.From == out.Harp {
			bodies = append(bodies, m.Body)
		}
	}
	require.NotEmpty(t, bodies,
		"the parent's inbox was still EMPTY 2s after a runner died at standup, with a 5-minute dial-home budget "+
			"left to run: this is the silent window a delegated child's coordinator cannot tell apart from a hung engine")
	assert.Contains(t, strings.Join(bodies, "\n"), wantReason,
		"the parent must be told the RUNNER died and why — a bare dial-home deadline names neither; got: %q", bodies)

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
