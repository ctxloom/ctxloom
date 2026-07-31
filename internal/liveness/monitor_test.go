package liveness_test

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/liveness"
	"github.com/ctxloom/ctxloom/internal/paths"
	"github.com/ctxloom/ctxloom/internal/shared/agent"
	"github.com/ctxloom/ctxloom/internal/transcript"
)

// composedContext stands in for the block the incident's launcher re-delivered
// every ~4-5 seconds. Its exact text does not matter; that it is IDENTICAL on
// every delivery is the whole signal.
const composedContext = "# Project context\n\nYou are a delegated agent. Here is the composed context...\n"

// ---------------------------------------------------------------------------
// The stuck agent, reproduced.
//
// The 2026-07-24 incident was not a crashed process and not a slow one. The
// launcher kept (re)starting the child engine, delivered the composed context
// as its first turn, and never got a turn back — so the canonical transcript
// grew forever with the same `user` block, seq pinned at 0 (a fresh
// transcript.Recorder per relaunch restarts the numbering against the same
// O_APPEND file), 100% `user` entries, zero assistant turns.
//
// This helper is that loop, driving the REAL internal/transcript recorder
// against the REAL canonical path, so the monitor is tested against the actual
// artifact rather than against a hand-rolled imitation of one.
// ---------------------------------------------------------------------------

// stuckSpawner never produces a turn: it relaunches, re-delivers, repeat.
type stuckSpawner struct {
	harp       string
	engine     string
	deliveries int
	cadence    time.Duration
}

func (s stuckSpawner) loop(t *testing.T) {
	t.Helper()
	for i := 0; i < s.deliveries; i++ {
		// A relaunch: a brand-new Recorder, hence seq restarting at 0.
		rec, err := transcript.NewRecorder(s.harp, s.engine)
		require.NoError(t, err)
		transcript.RecordUserText(rec, composedContext)
		require.NoError(t, rec.Close())
		if s.cadence > 0 && i < s.deliveries-1 {
			time.Sleep(s.cadence)
		}
	}
}

// healthySession is what progress actually looks like: ONE recorder, seq
// advancing, several distinct entry types, assistant output, a closed turn.
func healthySession(t *testing.T, harp string) {
	t.Helper()
	rec, err := transcript.NewRecorder(harp, "claude")
	require.NoError(t, err)
	defer func() { require.NoError(t, rec.Close()) }()
	transcript.RecordUserText(rec, composedContext)
	require.NoError(t, rec.Record(agent.ChatEvent{Entry: &agent.SessionEntry{
		Type: agent.EntryTypeAssistant, Content: "Reading the failing test first.",
	}}))
	require.NoError(t, rec.Record(agent.ChatEvent{Entry: &agent.SessionEntry{
		Type: agent.EntryTypeToolUse, ToolName: "Read", Content: "internal/liveness/monitor.go",
	}}))
	require.NoError(t, rec.Record(agent.ChatEvent{Entry: &agent.SessionEntry{
		Type: agent.EntryTypeToolResult, Content: "package liveness",
	}}))
	require.NoError(t, rec.Record(agent.ChatEvent{Entry: &agent.SessionEntry{
		Type: agent.EntryTypeAssistant, Content: "The ladder is missing the redelivery rule.",
	}}))
	require.NoError(t, rec.Record(agent.ChatEvent{Complete: &agent.TurnMeta{StopReason: "end_turn"}}))
}

// approvalParkedSession ends on a forwarded permission request with nothing
// after it — a child holding on a human, which an approval rung may
// legitimately do for many minutes.
func approvalParkedSession(t *testing.T, harp string) {
	t.Helper()
	rec, err := transcript.NewRecorder(harp, "claude")
	require.NoError(t, err)
	defer func() { require.NoError(t, rec.Close()) }()
	transcript.RecordUserText(rec, composedContext)
	require.NoError(t, rec.Record(agent.ChatEvent{Entry: &agent.SessionEntry{
		Type: agent.EntryTypeAssistant, Content: "I need to run the migration.",
	}}))
	require.NoError(t, rec.Record(agent.ChatEvent{Permission: &agent.PermissionRequest{
		ID: "perm-1", ToolName: "Bash",
	}}))
}

// testHome points paths.HarpCanonicalTranscriptPath at a temp dir so the real
// Recorder writes somewhere disposable.
func testHome(t *testing.T) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
}

// backdateTranscript shifts every record's receipt time back by `by`. The real
// Recorder stamps time.Now(), which is exactly right in production and useless
// for exercising a quiet-window rule, so the file is rewritten in place rather
// than the reader being stubbed out — the tests keep reading a real transcript
// through the real reader.
func backdateTranscript(t *testing.T, path string, by time.Duration) {
	t.Helper()
	raw, err := os.ReadFile(path)
	require.NoError(t, err)
	var out []byte
	for _, line := range splitLines(raw) {
		var rec map[string]any
		require.NoError(t, json.Unmarshal(line, &rec))
		ts, err := time.Parse(time.RFC3339Nano, rec["ts"].(string))
		require.NoError(t, err)
		rec["ts"] = ts.Add(-by).Format(time.RFC3339Nano)
		enc, err := json.Marshal(rec)
		require.NoError(t, err)
		out = append(append(out, enc...), '\n')
	}
	require.NoError(t, os.WriteFile(path, out, 0o644))
}

func splitLines(raw []byte) [][]byte {
	var out [][]byte
	start := 0
	for i, b := range raw {
		if b != '\n' {
			continue
		}
		if i > start {
			out = append(out, raw[start:i])
		}
		start = i + 1
	}
	return out
}

func transcriptPath(t *testing.T, harp string) string {
	t.Helper()
	p, err := paths.HarpCanonicalTranscriptPath(harp)
	require.NoError(t, err)
	return p
}

// aliveNoCPU is a probe standing in for a runtime the coordinator can see is
// up but cannot look inside (the container case, answered by a heartbeat).
var aliveNoCPU = liveness.ProbeFunc{Fn: func(context.Context, liveness.Target) liveness.ProcState {
	return liveness.ProcState{Observed: true, Alive: true, Detail: "runner heartbeat 2s ago"}
}}

// ===========================================================================
// THE THREE-DIRECTION PROOF
//
// A monitor that always reports healthy is worse than no monitor: it launders
// the failure and hands out false confidence. So the load-bearing assertion is
// not that the monitor RUNS, or that it returns a value — it is that it FIRES
// on a genuinely stuck agent, and that it stays silent on the two things that
// look superficially similar.
// ===========================================================================

// Direction 1 (the one that matters): the monitor FIRES on a genuinely stuck
// agent — the incident, reproduced through the real recorder.
func TestMonitor_FiresOnStuckAgent(t *testing.T) {
	testHome(t)
	const harp = "stuck-agent-harp"
	stuckSpawner{harp: harp, engine: "claude", deliveries: 6}.loop(t)

	now := time.Now()
	m := liveness.New(liveness.Options{
		Now:    func() time.Time { return now },
		Probes: []liveness.Probe{aliveNoCPU},
	})
	rep := m.Assess(context.Background(), liveness.Target{
		Harp:           harp,
		Agent:          "worker",
		Runtime:        "container",
		RosterState:    "executing", // the lie: the roster said this was fine
		StartedAt:      now.Add(-50 * time.Minute),
		LastActivity:   now.Add(-1 * time.Second), // the transcript IS growing
		TranscriptPath: transcriptPath(t, harp),
	})

	t.Logf("verdict: %s — %s", rep.State, rep.Reason)
	require.Equal(t, liveness.StateStalled, rep.State,
		"the monitor must FIRE on the reproduced incident: %d identical user deliveries, seq pinned at 0, zero assistant turns.\nreason=%q\nevidence=%s",
		6, rep.Reason, mustJSON(t, rep.Evidence))
	assert.True(t, rep.Firing(), "a stalled verdict must be one an operator is told about")
	assert.NotEmpty(t, rep.Reason, "a verdict a human cannot audit is the false confidence this package removes")

	// And the underlying measurements must match the shared progress
	// definition clause for clause, so a future change to the ladder cannot
	// quietly stop looking at any of them.
	ev := rep.Evidence.Transcript
	assert.True(t, ev.Exists, "the incident's transcript existed and was growing")
	assert.Equal(t, 6, ev.Records)
	assert.Equal(t, 0, ev.MaxSeq, "seq pinned at 0 is the signature failure")
	assert.True(t, ev.SeqPinned)
	assert.Equal(t, 0, ev.AssistantEntries, "zero assistant turns")
	assert.Equal(t, []string{"user"}, ev.EntryTypes, "100% user entries — no variety")
	assert.False(t, ev.TurnClosed, "no complete for the in-flight turn")
}

// Direction 1b: the SECOND loop rule must be independently load-bearing. The
// incident happened to trip both (seq pinned AND a re-delivery cadence), so a
// re-delivery loop whose seq DOES advance — a single long-lived recorder
// re-receiving the same block — is constructed here explicitly. Without this,
// the redelivery detector could be dead code and every test would still pass.
func TestMonitor_FiresOnRedeliveryCadenceEvenWhenSeqAdvances(t *testing.T) {
	base := time.Date(2026, 7, 24, 10, 0, 0, 0, time.UTC)
	path := filepath.Join(t.TempDir(), "transcript.jsonl")
	f, err := os.Create(path)
	require.NoError(t, err)
	enc := json.NewEncoder(f)
	// Seq advances normally; the CONTENT is the same block every 4.5s.
	for i, off := range []time.Duration{0, 4500, 9000, 13500, 18000} {
		require.NoError(t, enc.Encode(map[string]any{
			"v": 1, "harp": "h", "engine": "claude", "seq": i,
			"ts":    base.Add(off * time.Millisecond).Format(time.RFC3339Nano),
			"kind":  "entry",
			"entry": map[string]any{"type": "user", "content": composedContext},
		}))
	}
	require.NoError(t, f.Close())

	now := base.Add(20 * time.Second)
	m := liveness.New(liveness.Options{
		Now:    func() time.Time { return now },
		Probes: []liveness.Probe{aliveNoCPU},
	})
	rep := m.Assess(context.Background(), liveness.Target{
		Harp: "redelivered", Runtime: "container", RosterState: "executing",
		StartedAt: base.Add(-time.Hour), LastActivity: now, TranscriptPath: path,
	})
	t.Logf("verdict: %s — %s", rep.State, rep.Reason)
	require.False(t, rep.Evidence.Transcript.SeqPinned, "precondition: seq advanced, so the OTHER rule must be what fires")
	assert.Equal(t, liveness.StateStalled, rep.State)
	assert.True(t, rep.Firing())
	require.NotNil(t, rep.Evidence.Transcript.Redelivery)
	assert.Equal(t, 5, rep.Evidence.Transcript.Redelivery.Repeats)
}

// Direction 2: it does NOT fire on a healthy agent.
func TestMonitor_DoesNotFireOnHealthyAgent(t *testing.T) {
	testHome(t)
	const harp = "healthy-agent-harp"
	healthySession(t, harp)

	now := time.Now()
	m := liveness.New(liveness.Options{
		Now:    func() time.Time { return now },
		Probes: []liveness.Probe{aliveNoCPU},
	})
	rep := m.Assess(context.Background(), liveness.Target{
		Harp:           harp,
		Agent:          "worker",
		Runtime:        "container",
		RosterState:    "executing",
		StartedAt:      now.Add(-20 * time.Minute),
		LastActivity:   now.Add(-2 * time.Second),
		TranscriptPath: transcriptPath(t, harp),
	})

	t.Logf("verdict: %s — %s", rep.State, rep.Reason)
	assert.Equal(t, liveness.StateHealthy, rep.State, "reason=%q evidence=%s", rep.Reason, mustJSON(t, rep.Evidence))
	assert.False(t, rep.Firing(), "a working agent must never be reported as stalled or dead")
	ev := rep.Evidence.Transcript
	assert.Greater(t, ev.MaxSeq, 0, "seq must have advanced past 0")
	assert.GreaterOrEqual(t, ev.AssistantEntries, 1)
	assert.Greater(t, len(ev.EntryTypes), 1, "entry-type variety")
	assert.Nil(t, ev.Redelivery)
}

// Direction 3: it does NOT fire on an agent parked awaiting approval — not
// even when that park has lasted far longer than the quiet grace. An approval
// rung can legitimately hold a child, and reaping one turns a working system
// into one that kills its own children.
func TestMonitor_DoesNotFireOnApprovalPark(t *testing.T) {
	testHome(t)
	const harp = "approval-parked-harp"
	approvalParkedSession(t, harp)

	now := time.Now()
	m := liveness.New(liveness.Options{
		Now:    func() time.Time { return now },
		Probes: []liveness.Probe{aliveNoCPU},
	})

	// (a) The coordinator tells us it is parked.
	rep := m.Assess(context.Background(), liveness.Target{
		Harp:             harp,
		Agent:            "worker",
		Runtime:          "container",
		RosterState:      "parked",
		AwaitingApproval: true,
		StartedAt:        now.Add(-90 * time.Minute),
		LastActivity:     now.Add(-45 * time.Minute), // quiet FAR past QuietGrace
		TranscriptPath:   transcriptPath(t, harp),
	})
	t.Logf("verdict (coordinator says parked): %s — %s", rep.State, rep.Reason)
	assert.Equal(t, liveness.StateAwaitingApproval, rep.State, "reason=%q", rep.Reason)
	assert.False(t, rep.Firing(), "a child waiting on a human must NEVER be reaped")

	// (b) And even when the coordinator does NOT say so, the transcript's own
	// trailing permission record must hold the verdict back — the two halves
	// are independent on purpose, because whichever one is wired up wrongly
	// must not be able to get a parked child killed.
	rep = m.Assess(context.Background(), liveness.Target{
		Harp:           harp,
		Agent:          "worker",
		Runtime:        "container",
		RosterState:    "executing",
		StartedAt:      now.Add(-90 * time.Minute),
		LastActivity:   now.Add(-45 * time.Minute),
		TranscriptPath: transcriptPath(t, harp),
	})
	t.Logf("verdict (transcript alone): %s — %s", rep.State, rep.Reason)
	assert.Equal(t, liveness.StateAwaitingApproval, rep.State, "reason=%q", rep.Reason)
	assert.False(t, rep.Firing())
}

// ===========================================================================
// The rest of the ladder.
// ===========================================================================

// A missing transcript is a FAILURE SIGNAL, not an error: the recorder opens
// lazily on first successful Record, so no file means zero events were ever
// produced.
func TestMonitor_MissingTranscriptIsAStallNotAnError(t *testing.T) {
	testHome(t)
	now := time.Now()
	m := liveness.New(liveness.Options{
		Now:    func() time.Time { return now },
		Probes: []liveness.Probe{aliveNoCPU},
	})
	missing := filepath.Join(t.TempDir(), "persist", "transcript.jsonl")

	// Inside the launch grace: not a verdict yet.
	rep := m.Assess(context.Background(), liveness.Target{
		Harp: "young", Runtime: "container",
		StartedAt: now.Add(-30 * time.Second), TranscriptPath: missing,
	})
	assert.Equal(t, liveness.StateStarting, rep.State, "a slow launch is not a stall")
	assert.False(t, rep.Firing())

	// Past it: an engine that has emitted literally nothing is stalled.
	rep = m.Assess(context.Background(), liveness.Target{
		Harp: "old", Runtime: "container",
		StartedAt: now.Add(-30 * time.Minute), TranscriptPath: missing,
	})
	assert.Equal(t, liveness.StateStalled, rep.State, "reason=%q", rep.Reason)
	assert.False(t, rep.Evidence.Transcript.Exists)
}

// DIED is the process being gone with no `complete` for the in-flight turn —
// and must not be confused with a clean end.
func TestMonitor_DiedVersusCleanEnd(t *testing.T) {
	testHome(t)
	dead := liveness.ProbeFunc{Fn: func(context.Context, liveness.Target) liveness.ProcState {
		return liveness.ProcState{Observed: true, Alive: false, Detail: "runner link lost"}
	}}
	now := time.Now()
	m := liveness.New(liveness.Options{Now: func() time.Time { return now }, Probes: []liveness.Probe{dead}})

	const openHarp = "died-mid-turn"
	rec, err := transcript.NewRecorder(openHarp, "codex")
	require.NoError(t, err)
	transcript.RecordUserText(rec, "do the thing")
	require.NoError(t, rec.Record(agent.ChatEvent{Entry: &agent.SessionEntry{
		Type: agent.EntryTypeAssistant, Content: "starting",
	}}))
	require.NoError(t, rec.Close())

	rep := m.Assess(context.Background(), liveness.Target{
		Harp: openHarp, Runtime: "host", Ended: true,
		StartedAt: now.Add(-10 * time.Minute), TranscriptPath: transcriptPath(t, openHarp),
	})
	assert.Equal(t, liveness.StateDied, rep.State, "reason=%q", rep.Reason)
	assert.True(t, rep.Firing())

	const closedHarp = "ended-cleanly"
	healthySession(t, closedHarp)
	rep = m.Assess(context.Background(), liveness.Target{
		Harp: closedHarp, Runtime: "host", Ended: true,
		StartedAt: now.Add(-10 * time.Minute), TranscriptPath: transcriptPath(t, closedHarp),
	})
	assert.False(t, rep.Firing(), "a run that finished its turn and ended is not a death: %s", rep.Reason)
}

// LEGITIMATELY SLOW: quiet on the TRANSCRIPT past the grace, but still
// writing files. A ten-minute build says nothing and is not a hang.
//
// This used to be proved through a CPU-burn probe. That probe could never run
// in production — no liveness.Target ever carried a pid, because
// Spawner.StartEngine returns a Kill closure and not one (U056-F04) — so the
// distinction it drew was a distinction the shipped monitor could not make.
// The worktree mtime clock draws the same one from evidence the coordinator
// actually has.
func TestMonitor_WorktreeWritesRescueAQuietTranscript(t *testing.T) {
	testHome(t)
	const harp = "thinking-hard"
	healthySession(t, harp)
	path := transcriptPath(t, harp)
	backdateTranscript(t, path, 30*time.Minute) // the transcript is silent...

	work := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(work, "built.o"), []byte("x"), 0o644)) // ...the build is not

	now := time.Now()
	m := liveness.New(liveness.Options{
		Now:    func() time.Time { return now },
		Probes: []liveness.Probe{aliveNoCPU},
	})
	target := liveness.Target{
		Harp: harp, Runtime: "host", RosterState: "executing",
		StartedAt: now.Add(-2 * time.Hour), LastActivity: now.Add(-30 * time.Minute),
		TranscriptPath: path, WorkDir: work,
	}
	rep := m.Assess(context.Background(), target)
	assert.False(t, rep.Firing(), "an agent writing files is not stalled: %s", rep.Reason)

	// Take the worktree clock away and the same target IS a stall — the
	// rescue must come from the evidence, not from the rung being toothless.
	target.WorkDir = ""
	rep = m.Assess(context.Background(), target)
	assert.Equal(t, liveness.StateStalled, rep.State, "reason=%q", rep.Reason)
	assert.NotContains(t, rep.Reason, "CPU", "no verdict may cite evidence this monitor cannot gather")
}

// A runtime whose process the coordinator can only see through a heartbeat
// must still be assessable — otherwise the container case, which is where the
// incident happened, is exactly the case the monitor cannot speak about.
func TestMonitor_HeartbeatOnlyRuntimeStillReachesAVerdict(t *testing.T) {
	testHome(t)
	const harp = "quiet-container"
	healthySession(t, harp)
	backdateTranscript(t, transcriptPath(t, harp), 30*time.Minute)
	now := time.Now()
	m := liveness.New(liveness.Options{
		Now:    func() time.Time { return now },
		Probes: []liveness.Probe{aliveNoCPU},
	})
	rep := m.Assess(context.Background(), liveness.Target{
		Harp: harp, Runtime: "container", RosterState: "executing",
		StartedAt: now.Add(-2 * time.Hour), LastActivity: now.Add(-30 * time.Minute),
		TranscriptPath: transcriptPath(t, harp),
	})
	assert.Equal(t, liveness.StateStalled, rep.State,
		"30 minutes of silence on every clock is a stall: %s", rep.Reason)
}

// Filesystem activity is evidence of life even when the transcript is quiet.
func TestMonitor_WorktreeActivityCountsAsProgress(t *testing.T) {
	testHome(t)
	const harp = "writing-files"
	healthySession(t, harp)
	backdateTranscript(t, transcriptPath(t, harp), 30*time.Minute) // the transcript is quiet...
	work := t.TempDir()                                            // ...but the worktree is not
	require.NoError(t, os.WriteFile(filepath.Join(work, "out.go"), []byte("package x"), 0o644))

	now := time.Now()
	m := liveness.New(liveness.Options{
		Now:    func() time.Time { return now },
		Probes: []liveness.Probe{aliveNoCPU},
	})
	rep := m.Assess(context.Background(), liveness.Target{
		Harp: harp, Runtime: "container", RosterState: "executing",
		StartedAt: now.Add(-2 * time.Hour), LastActivity: now.Add(-30 * time.Minute),
		TranscriptPath: transcriptPath(t, harp), WorkDir: work,
	})
	assert.False(t, rep.Firing(), "an agent still writing to its worktree is working: %s", rep.Reason)
	assert.True(t, rep.Evidence.FSObserved)
}

// A worktree the monitor could not WALK is a broken clock, not a silent one.
// "Quiet on every clock" is only sayable when every clock was actually read;
// an unreadable worktree could be full of writes. Condemning anyway is the
// same verdict-manufactured-from-a-failure as condemning on an unreadable
// transcript (U056-F01/F05/F08).
func TestMonitor_UnreadableWorktreeIsUnknownNotStalled(t *testing.T) {
	requireNonRoot(t)
	testHome(t)
	const harp = "locked-worktree"
	healthySession(t, harp)
	backdateTranscript(t, transcriptPath(t, harp), 30*time.Minute)
	work := t.TempDir()
	// The worktree is BUSY — it just cannot be looked at.
	require.NoError(t, os.WriteFile(filepath.Join(work, "out.go"), []byte("package x"), 0o644))
	require.NoError(t, os.Chmod(work, 0o000))
	t.Cleanup(func() { _ = os.Chmod(work, 0o755) })

	now := time.Now()
	m := liveness.New(liveness.Options{
		Now:    func() time.Time { return now },
		Probes: []liveness.Probe{aliveNoCPU},
	})
	rep := m.Assess(context.Background(), liveness.Target{
		Harp: harp, Runtime: "container", RosterState: "executing",
		StartedAt: now.Add(-2 * time.Hour), LastActivity: now.Add(-30 * time.Minute),
		TranscriptPath: transcriptPath(t, harp), WorkDir: work,
	})
	assert.Equal(t, liveness.StateUnknown, rep.State,
		"a worktree that could not be walked cannot support a silence verdict: %s", rep.Reason)
	assert.False(t, rep.Firing())
	assert.NotEmpty(t, rep.Evidence.FSError, "the walk failure must be carried on the evidence")
	assert.Contains(t, rep.Reason, "worktree", "the reason must name the instrument that failed: %s", rep.Reason)
}

// The stall reason asserts what the monitor OBSERVED. Three filesystem states
// are distinct and must read differently: nobody asked (no WorkDir), the walk
// found nothing countable, and the walk saw files that are simply old.
func TestMonitor_StallReasonDistinguishesUnaskedFromUnwrittenWorktree(t *testing.T) {
	testHome(t)
	now := time.Now()
	m := liveness.New(liveness.Options{
		Now:    func() time.Time { return now },
		Probes: []liveness.Probe{aliveNoCPU},
	})
	assess := func(harp, work string) liveness.Report {
		healthySession(t, harp)
		backdateTranscript(t, transcriptPath(t, harp), 30*time.Minute)
		return m.Assess(context.Background(), liveness.Target{
			Harp: harp, Runtime: "container", RosterState: "executing",
			StartedAt: now.Add(-2 * time.Hour), LastActivity: now.Add(-30 * time.Minute),
			TranscriptPath: transcriptPath(t, harp), WorkDir: work,
		})
	}

	unasked := assess("no-worktree", "")
	require.Equal(t, liveness.StateStalled, unasked.State, unasked.Reason)
	assert.Contains(t, unasked.Reason, "no worktree was looked at",
		"with no WorkDir the monitor never looked, so it must not report an absence of writes: %s", unasked.Reason)

	old := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(old, "a.go"), []byte("a"), 0o644))
	stamp := now.Add(-30 * time.Minute)
	require.NoError(t, os.Chtimes(filepath.Join(old, "a.go"), stamp, stamp))
	stale := assess("stale-worktree", old)
	require.Equal(t, liveness.StateStalled, stale.State, stale.Reason)
	assert.True(t, stale.Evidence.FSObserved)
	assert.Contains(t, stale.Reason, "no worktree write", stale.Reason)

	empty := assess("empty-worktree", t.TempDir())
	require.Equal(t, liveness.StateStalled, empty.State, empty.Reason)
	assert.False(t, empty.Evidence.FSObserved)
	assert.Contains(t, empty.Reason, "no countable file in the worktree",
		"an empty worktree yields no clock at all, which is not the same as a worktree with old files: %s", empty.Reason)
}

// AssessAll preserves order and assesses every target.
func TestMonitor_AssessAllCoversEveryTarget(t *testing.T) {
	testHome(t)
	healthySession(t, "a-healthy")
	stuckSpawner{harp: "b-stuck", engine: "claude", deliveries: 4}.loop(t)
	now := time.Now()
	m := liveness.New(liveness.Options{Now: func() time.Time { return now }, Probes: []liveness.Probe{aliveNoCPU}})
	reps := m.AssessAll(context.Background(), []liveness.Target{
		{Harp: "a-healthy", StartedAt: now.Add(-time.Hour), LastActivity: now, TranscriptPath: transcriptPath(t, "a-healthy")},
		{Harp: "b-stuck", StartedAt: now.Add(-time.Hour), LastActivity: now, TranscriptPath: transcriptPath(t, "b-stuck")},
	})
	require.Len(t, reps, 2)
	assert.Equal(t, "a-healthy", reps[0].Harp)
	assert.False(t, reps[0].Firing())
	assert.Equal(t, "b-stuck", reps[1].Harp)
	assert.True(t, reps[1].Firing())
}

func mustJSON(t *testing.T, v any) string {
	t.Helper()
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return fmt.Sprintf("<unmarshalable: %v>", err)
	}
	return string(b)
}

// Report is a JSON-serialisable evidence trail whose whole point is that a
// verdict can be argued from the record. A record whose keys are inconsistent
// is one a consumer has to special-case, so every field marshals snake_case —
// including At, which carried no tag at all and marshalled as "At" while every
// sibling was lower-case (U056-F17).
func TestReport_MarshalsEveryFieldSnakeCase(t *testing.T) {
	raw, err := json.Marshal(liveness.Report{
		Harp: "h", State: liveness.StateHealthy, Reason: "r", At: time.Unix(1, 0).UTC(),
	})
	require.NoError(t, err)
	var obj map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(raw, &obj))
	keys := make([]string, 0, len(obj))
	for k := range obj {
		keys = append(keys, k)
		assert.Equal(t, strings.ToLower(k), k, "Report key %q is not snake_case", k)
	}
	sort.Strings(keys)
	assert.Contains(t, keys, "at", "Report.At must marshal as \"at\"")
}

// Target.StartedAt's contract is that ZERO disables every age-gated rule —
// "the monitor will not invent an age it does not know". The quiet ladder read
// it the other way round: `!StartedAt.IsZero() && age < grace` makes an unknown
// age NOT-young, which ENABLES the age-gated content clauses on exactly the
// targets whose age nobody could supply (U056-F06).
func TestMonitor_UnknownStartTimeDoesNotEnableTheAgeGatedRules(t *testing.T) {
	now := time.Date(2026, 7, 24, 12, 0, 0, 0, time.UTC)
	quiet := now.Add(-20 * time.Minute)
	path := writeJSONL(t, []map[string]any{
		userLine(1, quiet, "a"), userLine(2, quiet, "b"), userLine(3, quiet, "c"),
	})
	m := liveness.New(liveness.Options{Now: func() time.Time { return now }, Probes: []liveness.Probe{aliveNoCPU}})

	rep := m.Assess(context.Background(), liveness.Target{Harp: "no-start", TranscriptPath: path})

	assert.Equal(t, liveness.StateUnknown, rep.State,
		"with no start time the launch grace cannot be applied, so no absence verdict is available: %s", rep.Reason)
	assert.False(t, rep.Firing(), "an unknown age must never be condemned")
}

// StartGrace is documented as "how long after StartedAt the monitor refuses to
// reach any ABSENCE-based verdict". The quiet ladder's terminal rung condemned
// regardless of age — the `young` guard only skipped the content clauses and
// then fell through to StateStalled anyway, so the launch grace protected
// nothing (U056-F06; at census this fell through to the since-deleted cpuRung,
// which had the same hole).
func TestMonitor_LaunchGraceSuppressesTheQuietStall(t *testing.T) {
	now := time.Date(2026, 7, 24, 12, 0, 0, 0, time.UTC)
	quiet := now.Add(-20 * time.Minute)
	path := writeJSONL(t, []map[string]any{
		userLine(1, quiet, "a"), userLine(2, quiet, "b"), userLine(3, quiet, "c"),
	})
	m := liveness.New(liveness.Options{
		Now:        func() time.Time { return now },
		Thresholds: liveness.Thresholds{StartGrace: 30 * time.Minute},
		Probes:     []liveness.Probe{aliveNoCPU},
	})

	rep := m.Assess(context.Background(), liveness.Target{
		Harp: "still-launching", StartedAt: quiet, TranscriptPath: path,
	})

	assert.Equal(t, liveness.StateStarting, rep.State,
		"inside the launch grace absence proves nothing, on the quiet clocks as much as on the missing transcript: %s", rep.Reason)
	assert.False(t, rep.Firing())
}
