// The coordinator-side ADAPTER onto the shared liveness definition, plus its
// hermetic coverage. Deliberately NOT build-tagged: the docker-gated
// container-progress test (container_progress_docker_integration_test.go) is
// the live consumer, but the adapter itself must run under plain `just test` so
// it cannot rot behind a tag the way container coverage did.
//
// THERE IS NO SIGNAL LOGIC IN THIS FILE, AND THERE MUST NEVER BE ANY AGAIN.
//
// This file previously carried a SECOND implementation of the progress
// definition — its own seq/assistant/variety/cadence rules, its own repeat
// threshold, its own jitter model. It was written from the same brief as
// internal/liveness and drifted from it anyway, in two ways that mattered:
//
//   - it accepted a cadence when max delta <= 2 x min delta, where liveness
//     accepts when consecutive gaps stay within +/-25% of their MEDIAN. Those
//     disagree on borderline input;
//   - it failed unconditionally on "no entry.type == assistant record", where
//     liveness gates that clause on the agent also having gone QUIET. The
//     unconditional form is wrong: a turn can legitimately be tool-only (an
//     assistant invokes a tool with no preamble text, yielding
//     {user, tool_use, tool_result} and zero assistant entries) on a perfectly
//     healthy agent, so the old rule was a flake waiting for a real engine.
//
// Two implementations of one definition is how that drift happens, so the
// definition now lives in exactly one place — internal/liveness — and this file
// only PROJECTS its verdicts into the shape the container tests assert on. If
// you are about to add a threshold, a ratio, or an `if` over transcript
// contents here, it belongs in internal/liveness instead.
package coord

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/liveness"
	"github.com/ctxloom/ctxloom/internal/transcript"
)

// progressVerdict is one liveness.Report, projected. Every method below is a
// READ of something liveness already decided or measured; none of them decides
// anything, which is what keeps this an adapter rather than a second monitor.
type progressVerdict struct {
	// Path is the transcript file the verdict was computed from.
	Path string
	// Report is the verdict, verbatim, from internal/liveness.
	Report liveness.Report
}

// progressing is the positive answer: liveness has POSITIVE evidence of
// progress. Deliberately not "did not fire" — StateStarting and StateSlow are
// "we cannot say yet", and a test that accepted those as progress would be the
// silent no-op this whole cluster is about.
func (v progressVerdict) progressing() bool { return v.Report.State == liveness.StateHealthy }

// stalled is the operational question: is this agent one an operator must be
// told about? It is liveness's own Firing(), so "stalled" here can never drift
// from "stalled" there.
func (v progressVerdict) stalled() bool { return v.Report.Firing() }

// parked is the definition's carve-out: an agent awaiting approval is ALIVE.
func (v progressVerdict) parked() bool { return v.Report.State == liveness.StateAwaitingApproval }

func (v progressVerdict) present() bool         { return v.Report.Evidence.Transcript.Exists }
func (v progressVerdict) records() int          { return v.Report.Evidence.Transcript.Records }
func (v progressVerdict) maxSeq() int           { return v.Report.Evidence.Transcript.MaxSeq }
func (v progressVerdict) entryTypes() []string  { return v.Report.Evidence.Transcript.EntryTypes }
func (v progressVerdict) assistantEntries() int { return v.Report.Evidence.Transcript.AssistantEntries }
func (v progressVerdict) reason() string        { return v.Report.Reason }

// String renders a verdict for a failing assertion's message. liveness
// guarantees Reason is never empty, so a failure here is always auditable.
func (v progressVerdict) String() string {
	tx := v.Report.Evidence.Transcript
	var b strings.Builder
	fmt.Fprintf(&b, "transcript %s: state=%s present=%t records=%d max_seq=%d seq_pinned=%t\n",
		v.Path, v.Report.State, tx.Exists, tx.Records, tx.MaxSeq, tx.SeqPinned)
	fmt.Fprintf(&b, "  entry types: %s (assistant x%d, entries x%d)\n",
		joinOrNone(tx.EntryTypes), tx.AssistantEntries, tx.EntryRecords)
	if r := tx.Redelivery; r != nil {
		fmt.Fprintf(&b, "  redelivery: %q x%d on a %s cadence (max deviation %s)\n",
			r.EntryType, r.Repeats, r.Cadence, r.MaxDeviation)
	}
	fmt.Fprintf(&b, "  quiet: %s (observed=%t) age: %s\n",
		v.Report.Evidence.Quiet, v.Report.Evidence.QuietObserved, v.Report.Evidence.Age)
	fmt.Fprintf(&b, "  reason: %s\n", v.Report.Reason)
	return b.String()
}

func joinOrNone(s []string) string {
	if len(s) == 0 {
		return "none"
	}
	return strings.Join(s, ",")
}

// progressMonitor builds the liveness monitor these tests judge with. thr's
// zero fields normalize to the PRODUCTION defaults inside liveness, so a caller
// overriding one grace never silently loosens the rest.
func progressMonitor(thr liveness.Thresholds, now func() time.Time) *liveness.Monitor {
	return liveness.New(liveness.Options{Now: now, Thresholds: thr})
}

// assessTranscriptProgress is the ONE way anything in this package reaches a
// progress verdict: hand liveness the transcript path and what we know about
// the run, and report what it says.
//
// Note what it does NOT do: return an error. liveness.Monitor.Assess never
// errors — a failed observation lands as ABSENT evidence, never as a silently
// healthy agent — and a missing transcript is a VERDICT (the recorder opens its
// file lazily on the first event, so absence means zero events).
func assessTranscriptProgress(mon *liveness.Monitor, harp, path string, startedAt time.Time) progressVerdict {
	return progressVerdict{Path: path, Report: mon.Assess(context.Background(), liveness.Target{
		Harp:           harp,
		TranscriptPath: path,
		StartedAt:      startedAt,
	})}
}

// ---------------------------------------------------------------------------
// Hermetic coverage of the ADAPTER, on a frozen clock. No docker, no engine —
// this runs under plain `just test`, which is the point.
//
// The rules themselves are covered in internal/liveness (transcript_test.go for
// the measurements, monitor_test.go for the ladder). What is proven here is
// that the projection above answers the questions the container tests ask, and
// that it answers them the way liveness does.
// ---------------------------------------------------------------------------

// progressLine renders one transcript record as a JSONL line for the fixtures
// below, so a fixture reads as the SHAPE it is testing.
func progressLine(t *testing.T, r transcript.Record) string {
	t.Helper()
	b, err := json.Marshal(r)
	require.NoError(t, err)
	return string(b) + "\n"
}

func progressEntry(seq int, ts time.Time, typ, content string) transcript.Record {
	return transcript.Record{
		V: transcript.SchemaVersion, Harp: "fixture-harp", Engine: "mock",
		Seq: seq, TS: ts, Kind: transcript.KindEntry,
		Entry: &transcript.EntryPayload{Type: typ, Content: content},
	}
}

func writeProgressFixture(t *testing.T, recs ...transcript.Record) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "transcript.jsonl")
	var b strings.Builder
	for _, r := range recs {
		b.WriteString(progressLine(t, r))
	}
	require.NoError(t, os.WriteFile(path, []byte(b.String()), 0o600))
	return path
}

// assessFixture judges path under the PRODUCTION thresholds with the clock
// frozen at now. Freezing the clock rather than compressing the graces is what
// lets a hermetic test exercise a 5-minute or 10-minute rule as the real
// deployment would apply it.
func assessFixture(path string, now, startedAt time.Time) progressVerdict {
	return assessTranscriptProgress(
		progressMonitor(liveness.Thresholds{}, func() time.Time { return now }),
		"fixture-harp", path, startedAt)
}

func TestTranscriptProgress_HealthyTurnIsProgressing(t *testing.T) {
	base := time.Date(2026, 7, 24, 12, 0, 0, 0, time.UTC)
	path := writeProgressFixture(t,
		progressEntry(0, base, "user", "do the thing"),
		progressEntry(1, base.Add(300*time.Millisecond), "thinking", "considering"),
		progressEntry(2, base.Add(600*time.Millisecond), "tool_use", "mock_tool"),
		progressEntry(3, base.Add(900*time.Millisecond), "tool_result", "ok"),
		progressEntry(4, base.Add(1200*time.Millisecond), "assistant", "did the thing"),
		transcript.Record{V: transcript.SchemaVersion, Harp: "fixture-harp", Engine: "mock",
			Seq: 5, TS: base.Add(1300 * time.Millisecond), Kind: transcript.KindComplete,
			Complete: &transcript.CompletePayload{StopReason: "end_turn"}},
	)

	v := assessFixture(path, base.Add(2*time.Second), base.Add(-time.Minute))
	assert.True(t, v.progressing(), "a healthy turn must show progress; got:\n%s", v)
	assert.False(t, v.stalled())
	assert.Equal(t, 5, v.maxSeq(), "seq must advance past 0")
	assert.Positive(t, v.assistantEntries())
	for _, want := range []string{"user", "thinking", "tool_use", "tool_result", "assistant"} {
		assert.Contains(t, v.entryTypes(), want, "the full entry vocabulary must survive; got:\n%s", v)
	}
}

// The GATED assistant clause, in both directions — the divergence this file's
// old private copy got wrong.
//
// A tool-only turn (an assistant invoking a tool with no preamble text) records
// {user, tool_use, tool_result} and ZERO assistant entries on a perfectly
// healthy agent. The old unconditional rule called that dead. liveness calls it
// dead only once the agent has ALSO gone silent past the quiet grace, which is
// the correct reading: "has not spoken yet" and "has stopped" are different
// facts.
func TestTranscriptProgress_ToolOnlyTurnIsHealthyUntilItGoesQuiet(t *testing.T) {
	base := time.Date(2026, 7, 24, 12, 0, 0, 0, time.UTC)
	path := writeProgressFixture(t,
		progressEntry(0, base, "user", "run the suite"),
		progressEntry(1, base.Add(time.Second), "tool_use", "Bash: just test-acceptance"),
		progressEntry(2, base.Add(2*time.Second), "tool_result", "running..."),
	)
	// Well past the LAUNCH grace, so nothing here is rescued by youth alone.
	started := base.Add(-10 * time.Minute)

	live := assessFixture(path, base.Add(30*time.Second), started)
	require.Zero(t, live.assistantEntries(), "precondition: this turn has produced no assistant entry")
	assert.True(t, live.progressing(), "a recent tool-only turn is HEALTHY, not stalled; got:\n%s", live)
	assert.False(t, live.stalled())

	quiet := assessFixture(path, base.Add(30*time.Minute), started)
	assert.True(t, quiet.stalled(), "the same transcript, silent for half an hour, IS a stall; got:\n%s", quiet)
	assert.Contains(t, quiet.reason(), "zero assistant turns")
}

// POSITIVE evidence of a loop carries NO grace period — which is what turns the
// incident's hour of nothing into seconds. Asserted at age zero, inside every
// grace the ladder has.
func TestTranscriptProgress_RelaunchLoopFiresWithNoGrace(t *testing.T) {
	base := time.Date(2026, 7, 24, 12, 0, 0, 0, time.UTC)
	// The 21809-char block re-delivered every ~4-5s, in miniature: identical
	// content, evenly spaced, seq restarting at 0 per relaunch.
	const block = "COMPOSED CONTEXT BLOCK"
	path := writeProgressFixture(t,
		progressEntry(0, base, "user", block),
		progressEntry(0, base.Add(4*time.Second), "user", block),
		progressEntry(0, base.Add(9*time.Second), "user", block),
		progressEntry(0, base.Add(13*time.Second), "user", block),
	)
	now := base.Add(14 * time.Second)

	v := assessFixture(path, now, now) // age 0: inside the launch grace
	assert.True(t, v.stalled(), "a re-delivery loop is observed, not inferred, so no grace applies; got:\n%s", v)
	assert.False(t, v.progressing())
	assert.Equal(t, 4, v.records())
	assert.Contains(t, v.reason(), "re-delivered")

	// Contrast, so the rule is not read as "seq 0 is bad": ONE record at seq 0
	// is not the relaunch signature — a healthy conversation's first record
	// carries seq 0 too, and the old private copy condemned it for that.
	single := writeProgressFixture(t, progressEntry(0, base, "user", block))
	sv := assessFixture(single, base.Add(5*time.Second), base.Add(-10*time.Minute))
	assert.False(t, sv.stalled(), "one record at seq 0 is a conversation starting; got:\n%s", sv)
}

// A missing transcript is a VERDICT, never an error — and, inside the launch
// grace, not even a firing one. This pins the adapter's no-error contract: the
// dark-container test below depends on absence reaching it as a verdict.
func TestTranscriptProgress_MissingTranscriptIsAVerdictNotAnError(t *testing.T) {
	now := time.Date(2026, 7, 24, 12, 0, 0, 0, time.UTC)
	missing := filepath.Join(t.TempDir(), "persist", "transcript.jsonl")

	young := assessFixture(missing, now, now.Add(-30*time.Second))
	assert.False(t, young.present())
	assert.False(t, young.stalled(), "a slow launch is not a stall; got:\n%s", young)
	assert.False(t, young.progressing(), "nor is it evidence of progress")

	old := assessFixture(missing, now, now.Add(-30*time.Minute))
	assert.False(t, old.present())
	assert.True(t, old.stalled(), "past the launch grace, zero events is a stall; got:\n%s", old)
	assert.Contains(t, old.reason(), "no canonical transcript exists")
}

// The definition's explicit carve-out: an agent awaiting approval is ALIVE and
// must never be reaped. Asserted with every absence rule below the approval
// rung primed to condemn it — no assistant turn, and silent for 45 minutes.
func TestTranscriptProgress_ParkedOnApprovalIsNotStalled(t *testing.T) {
	base := time.Date(2026, 7, 24, 12, 0, 0, 0, time.UTC)
	path := writeProgressFixture(t,
		progressEntry(0, base, "user", "please edit the file"),
		transcript.Record{V: transcript.SchemaVersion, Harp: "fixture-harp", Engine: "mock",
			Seq: 1, TS: base.Add(time.Second), Kind: transcript.KindPermission,
			Permission: &transcript.PermissionPayload{ID: "perm-1", ToolName: "edit"}},
	)

	v := assessFixture(path, base.Add(45*time.Minute), base.Add(-time.Hour))
	assert.True(t, v.parked())
	assert.False(t, v.stalled(), "an agent awaiting approval is ALIVE and must never be reaped; got:\n%s", v)
	assert.False(t, v.progressing(), "it genuinely has produced no assistant turn yet")
}
