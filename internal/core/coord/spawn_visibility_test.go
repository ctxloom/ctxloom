package coord

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"go.uber.org/zap/zaptest/observer"

	"github.com/ctxloom/ctxloom/pkg/clifmt/clidiag"
)

// This file closes the "agent_run blocks before registering, with no trace"
// defect (task affected-yearly). The harm measured was not slowness by itself:
// it was slowness that NOTHING could observe. A spawn spent minutes inside
// AgentRun before run.enqueued existed, so the caller's request deadline fired
// against an empty roster, an empty journal and an empty log, and the operator
// reasonably concluded no child had been created. Retrying produced a second
// child executing the identical brief in the identical checkout.
//
// EVERY TEST HERE ASSERTS ORDER OR CONTENT, NEVER ELAPSED TIME. A wall-clock
// threshold ("the spawn returned in under N") would restate the symptom on a
// load-sensitive box and say nothing about the property that actually
// prevents the double-spawn — which is that the slow work happens AFTER the
// run is visible, and that a span which is still slow announces itself. The
// slow step is injected (fakeSpawner's resolveGate / versionProbeGate) so
// each ordering is forced rather than raced.

// TestAgentRun_RegistersTheRunBeforeProbingTheEngineVersion is the ordering
// statement. The engine-version probe execs the vendor's own CLI — the
// strongest candidate for the measured multi-minute stall, and unbounded on
// this path until now — and it used to run inside AssignSession, i.e. between
// minting the child's harp and journaling run.enqueued. Now it runs after,
// from a tracked goroutine, so a probe that hangs cannot hide the run.
//
// The proof is taken from OUTSIDE agent_run while the probe is parked: the
// roster, which is exactly the surface the operator checked and found empty.
func TestAgentRun_RegistersTheRunBeforeProbingTheEngineVersion(t *testing.T) {
	resetStrictness(t)
	sp := newFakeSpawner(t, map[string]fakeAgent{"worker": {perm: "bypass"}}, nil)
	sp.versionProbeEntered = make(chan string, 1)
	sp.versionProbeGate = make(chan struct{})
	c := newTestCoordinator(t, sp, nil)

	type result struct {
		out *RunOutcome
		err error
	}
	done := make(chan result, 1)
	go func() {
		out, err := c.AgentRun(context.Background(), ownerIdentity(), "worker", "do the thing", "")
		done <- result{out, err}
	}()

	var probedHarp string
	select {
	case probedHarp = <-sp.versionProbeEntered:
	case <-time.After(conformanceWait):
		t.Fatal("the engine-version probe never ran at all — it must still happen, just not before registration")
	}

	// THE ASSERTION. The probe is parked right now. If it were still on the
	// pre-registration path, nothing would name this child anywhere.
	roster := c.Roster(ownerIdentity())
	var found *RosterEntry
	for i := range roster {
		if roster[i].Harp == probedHarp {
			found = &roster[i]
			break
		}
	}
	require.NotNil(t, found,
		"while the engine-version probe is still running, the run must ALREADY be in the roster — "+
			"a caller that cannot see the child is a caller that spawns a second one")
	assert.Equal(t, "worker", found.Agent, "and it names the agent, so the caller can tell it is THEIR spawn")
	assert.NotEmpty(t, found.State, "and carries a state, not a placeholder")

	close(sp.versionProbeGate)
	select {
	case got := <-done:
		require.NoError(t, got.err)
		assert.Equal(t, probedHarp, got.out.Harp, "the probed harp is this run's own")
	case <-time.After(conformanceWait):
		t.Fatal("AgentRun never returned")
	}

	assert.Equal(t, []string{probedHarp}, sp.probedVersions(),
		"the version is still recorded exactly once — moving it off the critical path must not drop it")
}

// TestAgentRun_RecordsTheVersionOfTheBinaryTheLaunchNames: the version a
// session records is asked of the binary its launch spawns — the resolved
// Launch's engine and label binary (a label's binary_path) — not the
// engine's default install, so a session on a pinned binary is read back
// with the reader for the version that wrote it.
func TestAgentRun_RecordsTheVersionOfTheBinaryTheLaunchNames(t *testing.T) {
	resetStrictness(t)
	sp := newFakeSpawner(t, map[string]fakeAgent{
		"pinned": {perm: "bypass", backend: "mock", binary: "/opt/pinned/bin/engine"},
		"plain":  {perm: "bypass", backend: "mock"},
	}, nil)
	sp.versionProbeEntered = make(chan string, 2)
	c := newTestCoordinator(t, sp, nil)

	for _, agent := range []string{"pinned", "plain"} {
		_, err := c.AgentRun(context.Background(), ownerIdentity(), agent, "do the thing", "")
		require.NoError(t, err)
		select {
		case <-sp.versionProbeEntered:
		case <-time.After(conformanceWait):
			t.Fatalf("%s: the engine-version probe never ran", agent)
		}
	}
	assert.Equal(t, []string{"mock /opt/pinned/bin/engine", "mock "}, sp.probedBinaries(),
		"each session's version is asked of the binary its launch spawns; none named is the engine's own")
}

// TestAgentRun_JournalsAcceptanceBeforeTheUntraceableSpan pins the durable
// record. Three separate steps between the verb and run.enqueued can block
// for an unbounded time, and until one of them completes there is no run id
// to journal anything against. An audit fact written BEFORE the span is what
// turns "nothing happened" into "accepted at T, not yet registered" — the one
// piece of evidence the incident's three journals did not contain.
//
// Agent resolution is used as the slow step here (not the version probe) so
// the assertion covers the whole span, not just its tail.
func TestAgentRun_JournalsAcceptanceBeforeTheUntraceableSpan(t *testing.T) {
	resetStrictness(t)
	sp := newFakeSpawner(t, map[string]fakeAgent{"worker": {perm: "bypass"}}, nil)
	sp.resolveEntered = make(chan string, 1)
	sp.resolveGate = make(chan struct{})
	c := newTestCoordinator(t, sp, nil)

	done := make(chan error, 1)
	go func() {
		_, err := c.AgentRun(context.Background(), ownerIdentity(), "worker", "do the thing", "")
		done <- err
	}()

	select {
	case <-sp.resolveEntered:
	case <-time.After(conformanceWait):
		t.Fatal("Resolve never ran")
	}

	accepted := readAuditKind(t, c, "agent_run.accepted")
	require.Len(t, accepted, 1,
		"the acceptance must be durable BEFORE the span that can block for minutes, not after it")
	assert.Equal(t, "worker", accepted[0].Detail["agent"], "the record names what was asked for")
	assert.Equal(t, ownerIdentity().Harp, accepted[0].Actor, "…and who asked")
	assert.Empty(t, readAuditKind(t, c, "agent_run"),
		"and it is a DISTINCT fact from the registered-run one, which cannot exist yet")

	close(sp.resolveGate)
	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(conformanceWait):
		t.Fatal("AgentRun never returned")
	}
}

// TestAgentRun_ASpawnStillPreparingSaysSoOutLoud pins the live half. A
// durable audit fact answers a post-mortem; it does not reach the operator
// who is sitting in front of a deadline error deciding whether to retry. A
// spawn whose pre-registration span outlives its notice budget warns, names
// the caller and the agent, and states the thing the operator cannot
// otherwise know: it is starting, not lost.
//
// The budget is shrunk to make the notice certain; nothing here asserts how
// long anything took.
func TestAgentRun_ASpawnStillPreparingSaysSoOutLoud(t *testing.T) {
	assertParkedSpawnSaysSo(t, func() {})
}

// TestAgentRun_ASpawnNoticeIsFoundBehindAForeignWarning forces the
// interleaving the package run produced: the clidiag sink is process-wide,
// so a warning from any other writer — a goroutine an earlier test's
// coordinator left past its close budget, an unrelated coordinator
// diagnostic — can land on it before the spawn's notice. The foreign line is
// written FIRST, deterministically; the notice must still be found.
func TestAgentRun_ASpawnNoticeIsFoundBehindAForeignWarning(t *testing.T) {
	assertParkedSpawnSaysSo(t, func() {
		clidiag.Warn("coord-test", "an unrelated warning from another writer")
	})
}

// assertParkedSpawnSaysSo parks a spawn inside Resolve past a 1ms notice
// budget and asserts the notice it prints. foreign runs after the sink is
// installed and before the spawn starts, so a test can put other lines on
// the sink ahead of the notice.
func assertParkedSpawnSaysSo(t *testing.T, foreign func()) {
	t.Helper()
	resetStrictness(t)
	sp := newFakeSpawner(t, map[string]fakeAgent{"worker": {perm: "bypass"}}, nil)
	sp.resolveEntered = make(chan string, 1)
	sp.resolveGate = make(chan struct{})
	c := newTestCoordinator(t, sp, nil)
	c.spawnNoticeAfter = time.Millisecond

	// The sink is process-wide, so it carries every writer's lines, not only
	// this spawn's: the notice is SELECTED by its own text (msgSpawnPending
	// over this caller, agent and budget), never taken as the first line.
	sink := &signallingSink{seen: make(chan string, 64)}
	restore := clidiag.SetSink(sink)
	defer restore()
	foreign()

	done := make(chan error, 1)
	go func() {
		_, err := c.AgentRun(context.Background(), ownerIdentity(), "worker", "do the thing", "")
		done <- err
	}()

	want := fmt.Sprintf(msgSpawnPending, ownerIdentity().Harp, "worker", c.spawnNoticeAfter)
	var notice string
	deadline := time.After(conformanceWait)
	for notice == "" {
		select {
		case line := <-sink.seen:
			if strings.Contains(line, want) {
				notice = line
			}
		case <-deadline:
			t.Fatal("a spawn parked past its notice budget never reported itself — the operator sees nothing at all")
		}
	}
	assert.Contains(t, notice, "agent_run", "the notice names the verb that is stuck")
	assert.Contains(t, notice, ownerIdentity().Harp, "…the caller")
	assert.Contains(t, notice, "worker", "…the agent being spawned")
	assert.Contains(t, notice, "not registered yet",
		"…and the fact that explains why the roster is empty")
	assert.Contains(t, strings.ToLower(notice), "do not spawn a second one",
		"…which exists to stop exactly one action")

	require.Eventually(t, func() bool { return len(readAuditKind(t, c, "agent_run.pending")) == 1 },
		conformanceWait, 5*time.Millisecond,
		"the notice is journaled too, so it survives a runner whose stderr nobody is reading")

	close(sp.resolveGate)
	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(conformanceWait):
		t.Fatal("AgentRun never returned")
	}
}

// TestAgentRun_APromptSpawnNeverReportsItselfStuck is the other half of the
// notice's contract: it must not fire for the ordinary case. A warning that
// prints on every spawn is a warning nobody reads, which is how the real one
// would be missed.
//
// The sink is process-wide, so its first line may be anyone's: the notice is
// looked for among EVERY line the sink received, by its own text. The spawn
// is driven to completion before the sink is drained, and the span's end
// stands the notice down for good, so the drain sees every notice this spawn
// could ever print.
func TestAgentRun_APromptSpawnNeverReportsItselfStuck(t *testing.T) {
	resetStrictness(t)
	sp := newFakeSpawner(t, map[string]fakeAgent{"worker": {perm: "bypass"}}, nil)
	c := newTestCoordinator(t, sp, nil)

	sink := &signallingSink{seen: make(chan string, 64)}
	restore := clidiag.SetSink(sink)
	defer restore()

	_, err := c.AgentRun(context.Background(), ownerIdentity(), "worker", "do the thing", "")
	require.NoError(t, err)

	assertNoSpawnNotice(t, sink, fmt.Sprintf(msgSpawnPending, ownerIdentity().Harp, "worker", c.spawnNoticeAfter),
		"an ordinary spawn is well inside the notice budget and must stay silent")
	assert.Empty(t, readAuditKind(t, c, "agent_run.pending"),
		"and journals no pending fact")
}

// TestAgentRun_ASpawnNoticeRacingTheSpanEndStaysSilent forces the timer race:
// the notice's callback has already STARTED when the span ends (its stop
// reports it could not recall it, as time.Timer.Stop does for a callback
// already running), and its body runs only after the run is registered. A
// notice then would claim "not registered yet" about a run that is in the
// roster, which is the false statement the notice exists to prevent the
// operator from acting on. Whichever of the callback and the span's end
// takes the guard first wins; here the end does, so the callback says and
// journals nothing.
func TestAgentRun_ASpawnNoticeRacingTheSpanEndStaysSilent(t *testing.T) {
	resetStrictness(t)
	sp := newFakeSpawner(t, map[string]fakeAgent{"worker": {perm: "bypass"}}, nil)
	sp.resolveEntered = make(chan string, 1)
	sp.resolveGate = make(chan struct{})
	armed := make(chan func(), 1)
	c := newTestCoordinatorWith(t, sp, func(o *Options) {
		o.AfterFunc = func(_ time.Duration, f func()) func() bool {
			select {
			case armed <- f:
			default:
			}
			return func() bool { return false }
		}
	})

	sink := &signallingSink{seen: make(chan string, 64)}
	restore := clidiag.SetSink(sink)
	defer restore()

	done := make(chan error, 1)
	go func() {
		_, err := c.AgentRun(context.Background(), ownerIdentity(), "worker", "do the thing", "")
		done <- err
	}()
	select {
	case <-sp.resolveEntered:
	case <-time.After(conformanceWait):
		t.Fatal("Resolve never ran")
	}
	var fire func()
	select {
	case fire = <-armed:
	default:
		t.Fatal("the span is open and its notice timer was never armed")
	}

	close(sp.resolveGate)
	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(conformanceWait):
		t.Fatal("AgentRun never returned")
	}

	fire()

	assertNoSpawnNotice(t, sink, fmt.Sprintf(msgSpawnPending, ownerIdentity().Harp, "worker", c.spawnNoticeAfter),
		"a notice whose callback runs after the span ended describes a run that is already registered")
	assert.Empty(t, readAuditKind(t, c, "agent_run.pending"),
		"and the journal must not record a pending spawn after its registration")
}

// assertNoSpawnNotice drains every line the sink has received and fails if
// any carries want. It does not wait: the caller has already brought about
// every write it means to rule out.
func assertNoSpawnNotice(t *testing.T, sink *signallingSink, want, why string) {
	t.Helper()
	for {
		select {
		case line := <-sink.seen:
			assert.NotContains(t, line, want, why)
		default:
			return
		}
	}
}

// signallingSink is a clidiag sink that hands each written line to a test on
// a channel, so a test can BLOCK until a diagnostic arrives instead of
// polling a bytes.Buffer (which is also not safe to read while the writer
// goroutine may still be writing it).
type signallingSink struct {
	mu   sync.Mutex
	seen chan string
}

func (s *signallingSink) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	select {
	case s.seen <- string(p):
	default:
	}
	return len(p), nil
}

// TestAgentRun_ASpawnStillPreparingLeavesAStructuredRecord is the third leg
// of the pending-spawn notice. The clidiag warning reaches an operator who is
// watching; the audit fact survives in the coordinator's own journal; but the
// project's post-mortem channel is the structured log, and a package that
// never writes there leaves a stall invisible to anyone reading ctxloom.log
// after the fact. The record is asserted by level, message and fields, never
// by scraping text.
func TestAgentRun_ASpawnStillPreparingLeavesAStructuredRecord(t *testing.T) {
	resetStrictness(t)
	sp := newFakeSpawner(t, map[string]fakeAgent{"worker": {perm: "bypass"}}, nil)
	sp.resolveEntered = make(chan string, 1)
	sp.resolveGate = make(chan struct{})
	c := newTestCoordinator(t, sp, nil)
	c.spawnNoticeAfter = time.Millisecond

	core, logs := observer.New(zapcore.WarnLevel)
	restoreLogger := zap.ReplaceGlobals(zap.New(core))
	defer restoreLogger()
	// The clidiag line still prints; route it somewhere a parallel test
	// cannot see it and this test does not read it.
	restore := clidiag.SetSink(&signallingSink{seen: make(chan string, 4)})
	defer restore()

	done := make(chan error, 1)
	go func() {
		_, err := c.AgentRun(context.Background(), ownerIdentity(), "worker", "do the thing", "")
		done <- err
	}()

	require.Eventually(t, func() bool { return logs.FilterMessage(logSpawnPending).Len() == 1 },
		conformanceWait, 5*time.Millisecond,
		"a spawn parked past its notice budget wrote no structured record; nothing on disk outlives the stderr line. Records seen: %v", logs.All())
	entry := logs.FilterMessage(logSpawnPending).All()[0]
	assert.Equal(t, zapcore.WarnLevel, entry.Level, "a spawn past budget is a warning")
	fields := entry.ContextMap()
	assert.Equal(t, ownerIdentity().Harp, fields["caller"], "the record names the caller")
	assert.Equal(t, "worker", fields["agent"], "…and the agent being spawned")
	assert.Equal(t, c.spawnNoticeAfter, fields["after"], "…and the budget it outran")

	close(sp.resolveGate)
	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(conformanceWait):
		t.Fatal("AgentRun never returned")
	}
}
