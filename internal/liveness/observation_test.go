package liveness_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/liveness"
)

// ===========================================================================
// A BROKEN OBSERVATION IS NOT AN OBSERVATION OF BREAKAGE.
//
// U056-F01/F05: TranscriptStat had no observability bit, so "the file is not
// there" (the loudest possible progress signal), "the file could not be
// opened" (a broken instrument) and "the caller never asked" all arrived at
// the ladder as the same Exists:false — and graceRung turned every one of
// them into StateStalled, "the engine has emitted zero events".
//
// U056-F02: the 20 000-line scan bound read the HEAD of the file, so every
// last-record measurement went permanently stale on a long transcript —
// a healthy long-running agent reads as stalled and a cleanly-ended one as
// dead.
//
// U056-F03: Target.Ended was passed in and read by nothing, so a child the
// coordinator had already terminated kept being assessed as a live one and
// was condemned ten minutes later.
// ===========================================================================

// unopenablePath returns a path that EXISTS as far as the caller's intent goes
// but cannot be opened — a regular file used as a directory (ENOTDIR). Chosen
// over chmod 000 deliberately: a test that runs as root would silently stop
// exercising the failure it was written for.
func unopenablePath(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	notADir := filepath.Join(dir, "blocker")
	require.NoError(t, os.WriteFile(notADir, []byte("x"), 0o644))
	return filepath.Join(notADir, "transcript.jsonl")
}

// F05 — a read that reached a conclusion is Observed; one that broke is not.
func TestReadTranscript_ObservedSeparatesAbsenceFromBreakage(t *testing.T) {
	base := time.Date(2026, 7, 24, 10, 0, 0, 0, time.UTC)
	good := writeJSONL(t, []map[string]any{userLine(0, base, "hello")})

	st, err := liveness.ReadTranscript(good)
	require.NoError(t, err)
	assert.True(t, st.Observed, "a file read to a conclusion was observed")
	assert.True(t, st.Exists)

	st, err = liveness.ReadTranscript(filepath.Join(t.TempDir(), "nope.jsonl"))
	require.NoError(t, err)
	assert.True(t, st.Observed, "a conclusively ABSENT file is an observation, not a failure")
	assert.False(t, st.Exists)

	st, err = liveness.ReadTranscript(unopenablePath(t))
	require.Error(t, err, "a file that cannot be opened is a broken observation")
	assert.False(t, st.Observed, "a broken observation must never present as one")
	assert.False(t, st.Exists)
}

// F01 — a transcript that cannot be opened must not be reported as an engine
// that emitted zero events.
func TestMonitor_UnreadableTranscriptIsNotAStall(t *testing.T) {
	testHome(t)
	now := time.Now()
	m := liveness.New(liveness.Options{Now: func() time.Time { return now }})

	rep := m.Assess(context.Background(), liveness.Target{
		Harp: "cannot-look", Runtime: "host",
		StartedAt:      now.Add(-2 * time.Hour), // far past every grace
		TranscriptPath: unopenablePath(t),
	})
	assert.False(t, rep.Firing(), "a failed read must not fire: %s", rep.Reason)
	assert.Equal(t, liveness.StateUnknown, rep.State, "reason=%q", rep.Reason)
	assert.NotEmpty(t, rep.Evidence.TranscriptError, "the reason must carry WHY the monitor cannot speak")
	assert.Contains(t, rep.Reason, "transcript", "reason=%q", rep.Reason)
}

// F01 (the coordinator's half) — coord/liveness.go sets TranscriptPath to ""
// when paths resolution fails, which is also a broken observation and must
// not be laundered into a stall.
func TestMonitor_UnaskedTranscriptIsNotAStall(t *testing.T) {
	testHome(t)
	now := time.Now()
	m := liveness.New(liveness.Options{Now: func() time.Time { return now }})

	rep := m.Assess(context.Background(), liveness.Target{
		Harp: "never-asked", Runtime: "host",
		StartedAt: now.Add(-2 * time.Hour),
		// TranscriptPath deliberately empty: the caller could not resolve one.
	})
	assert.False(t, rep.Firing(), "no transcript evidence was gathered at all: %s", rep.Reason)
	assert.Equal(t, liveness.StateUnknown, rep.State, "reason=%q", rep.Reason)
}

// F01 — the content rung is an ABSENCE rule too ("zero assistant turns"), so
// it must be gated on the same bit. A scan that aborts part-way (a directory
// in the transcript's place) yields a partial stat that reads as an engine
// which never produced a turn.
func TestMonitor_AbortedScanIsNotZeroAssistantTurns(t *testing.T) {
	testHome(t)
	dir := t.TempDir()
	txPath := filepath.Join(dir, "transcript.jsonl")
	require.NoError(t, os.Mkdir(txPath, 0o755)) // opens fine, reads EISDIR

	now := time.Now()
	m := liveness.New(liveness.Options{Now: func() time.Time { return now }})
	rep := m.Assess(context.Background(), liveness.Target{
		Harp: "torn-read", Runtime: "host",
		StartedAt:      now.Add(-2 * time.Hour),
		LastActivity:   now.Add(-2 * time.Hour), // silent on every clock
		TranscriptPath: txPath,
	})
	assert.False(t, rep.Firing(), "a broken scan must not condemn: %s", rep.Reason)
}

// ---------------------------------------------------------------------------
// F02 — the scan bound must not make the last record invisible.
// ---------------------------------------------------------------------------

// longTranscript writes n+2 records: one opening user turn, n assistant
// filler turns, and a closing `complete` — i.e. a long, healthy, FINISHED
// session whose proof of health is entirely in its tail.
func longTranscript(t *testing.T, n int, first, last time.Time) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "transcript.jsonl")
	f, err := os.Create(path)
	require.NoError(t, err)
	defer func() { require.NoError(t, f.Close()) }()
	enc := json.NewEncoder(f)
	require.NoError(t, enc.Encode(userLine(0, first, "do the thing")))
	for i := 0; i < n; i++ {
		require.NoError(t, enc.Encode(map[string]any{
			"v": 1, "harp": "h", "engine": "claude", "seq": i + 1,
			"ts": first.Format(time.RFC3339Nano), "kind": "entry",
			"entry": map[string]any{"type": "assistant", "content": "step"},
		}))
	}
	require.NoError(t, enc.Encode(map[string]any{
		"v": 1, "harp": "h", "engine": "claude", "seq": n + 1,
		"ts": last.Format(time.RFC3339Nano), "kind": "complete",
	}))
	return path
}

func TestReadTranscript_LongTranscriptStillSeesItsTail(t *testing.T) {
	first := time.Date(2026, 7, 24, 10, 0, 0, 0, time.UTC)
	last := first.Add(90 * time.Minute)
	// Comfortably past maxTranscriptScan (20 000).
	path := longTranscript(t, 25000, first, last)

	st, err := liveness.ReadTranscript(path)
	require.NoError(t, err)
	assert.True(t, st.Observed)
	assert.True(t, st.Truncated, "the head bound was hit; the stat must say so")
	assert.True(t, st.LastTS.Equal(last), "LastTS came from the head, not the tail: %s", st.LastTS)
	assert.True(t, st.TurnClosed, "the closing `complete` is past the head bound and must still be seen")
	assert.Equal(t, 25001, st.MaxSeq, "the highest seq lives in the tail")
}

// F02 at the ladder: a long, quiet, cleanly-finished session must be neither
// stalled (stale LastTS) nor dead (unseen `complete`).
func TestMonitor_LongFinishedTranscriptIsNotDeadOrStalled(t *testing.T) {
	testHome(t)
	now := time.Now()
	path := longTranscript(t, 25000, now.Add(-2*time.Hour), now.Add(-time.Minute))
	dead := liveness.ProbeFunc{Fn: func(context.Context, liveness.Target) liveness.ProcState {
		return liveness.ProcState{Observed: true, Alive: false, Detail: "runner link lost"}
	}}
	m := liveness.New(liveness.Options{Now: func() time.Time { return now }, Probes: []liveness.Probe{dead}})

	rep := m.Assess(context.Background(), liveness.Target{
		Harp: "long-and-done", Runtime: "host",
		StartedAt: now.Add(-2 * time.Hour), TranscriptPath: path,
	})
	assert.False(t, rep.Firing(), "a finished long session is not a death: %s", rep.Reason)
}

// ---------------------------------------------------------------------------
// F03 — a run the coordinator has already ended cannot be stalled.
// ---------------------------------------------------------------------------

// unobservable stands in for the production state after a terminal: the runner
// registry entry is deleted, so no probe can see anything at all.
var unobservable = liveness.ProbeFunc{Fn: func(context.Context, liveness.Target) liveness.ProcState {
	return liveness.ProcState{Detail: "no runner connected"}
}}

func TestMonitor_EndedRunIsNeverStalled(t *testing.T) {
	testHome(t)
	const harp = "finished-an-hour-ago"
	healthySession(t, harp)
	path := transcriptPath(t, harp)
	backdateTranscript(t, path, time.Hour) // silent for an hour, because it is done

	now := time.Now()
	m := liveness.New(liveness.Options{
		Now:    func() time.Time { return now },
		Probes: []liveness.Probe{unobservable},
	})
	rep := m.Assess(context.Background(), liveness.Target{
		Harp: harp, Runtime: "host", RosterState: "ended", Ended: true,
		StartedAt: now.Add(-3 * time.Hour), LastActivity: now.Add(-time.Hour),
		TranscriptPath: path,
	})
	assert.False(t, rep.Firing(),
		"the coordinator already ended this run — the watchdog must not cry wolf on success: %s", rep.Reason)
}
