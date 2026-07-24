// Package-local implementation of the SHARED liveness definition, plus its
// hermetic unit coverage. Deliberately NOT build-tagged: the docker-gated
// container-progress test (container_progress_docker_integration_test.go) is
// the live consumer, but the signal logic itself must run under plain
// `just test` so the definition cannot rot behind a tag the way container
// coverage did.
//
// The definition is supplied, not invented here — a production liveness
// monitor is being built against the same wording, so any change to these
// signals is a change to a CONTRACT, not a local tweak. An agent is making
// PROGRESS when its canonical transcript shows all of:
//
//   - seq strictly advances past 0, contiguously from 0 (record.go's writer
//     contract). seq pinned at 0 is the signature failure of the container
//     defect this file exists to catch;
//   - at least one kind=="entry" record with entry.type=="assistant";
//   - entry-type VARIETY: more than one distinct entry.type across kind=="entry"
//     records (the failing agents were 100% "user");
//   - no identical content re-appended on a fixed cadence — group by
//     (entry.type, entry.content) and compare ts deltas (ts is RECEIPT time, so
//     a re-delivery cadence is directly measurable).
//
// Two more rules are part of the definition, not decorations on it:
//
//   - a MISSING transcript file is a FAILURE SIGNAL, not an error. The recorder
//     opens the file lazily on the first successful Record, so a zero-event chat
//     leaves no file at all — exactly the shape of "the agent never got its
//     prompt";
//   - an agent PARKED AWAITING APPROVAL is NOT stalled and must never be
//     treated as dead.
package coord

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/transcript"
)

// progressVerdict is one evaluation of the liveness definition against a
// harp's canonical transcript.
type progressVerdict struct {
	// Path is the transcript file the verdict was computed from.
	Path string
	// Present reports whether the file existed at all. False is a FAILURE
	// signal (see the package comment), never an error.
	Present bool
	// Records is the number of parsed JSONL lines.
	Records int
	// MaxSeq is the highest seq observed (-1 for no records).
	MaxSeq int
	// EntryTypes counts kind=="entry" records by entry.type.
	EntryTypes map[string]int
	// Parked reports that the transcript ENDS on an unanswered permission
	// request — awaiting approval, which the definition says is not stalled.
	Parked bool
	// Failures names every progress signal that did NOT hold, in the
	// definition's order. Empty means the agent is making progress.
	Failures []string
}

// progressing reports whether every signal held.
func (v progressVerdict) progressing() bool { return len(v.Failures) == 0 }

// stalled is the operational question: is this agent dead? A parked-awaiting-
// approval agent has failing progress signals (it produced no assistant turn
// yet) and is nonetheless ALIVE, so the two questions are deliberately
// distinct methods rather than one overloaded boolean.
func (v progressVerdict) stalled() bool { return !v.progressing() && !v.Parked }

// String renders a verdict for a failing assertion's message.
func (v progressVerdict) String() string {
	var b strings.Builder
	fmt.Fprintf(&b, "transcript %s: present=%t records=%d max_seq=%d parked=%t\n",
		v.Path, v.Present, v.Records, v.MaxSeq, v.Parked)
	types := make([]string, 0, len(v.EntryTypes))
	for t := range v.EntryTypes {
		types = append(types, t)
	}
	sort.Strings(types)
	for _, t := range types {
		fmt.Fprintf(&b, "  entry.type %-12s x%d\n", t, v.EntryTypes[t])
	}
	for _, f := range v.Failures {
		fmt.Fprintf(&b, "  FAIL: %s\n", f)
	}
	return b.String()
}

// cadenceMinSamples is how many identical (type, content) records must appear
// before their ts deltas are even considered a cadence. Two occurrences are a
// retry; three evenly spaced ones are a loop.
const cadenceMinSamples = 3

// cadenceRegularityFactor bounds how much the deltas between identical
// re-appends may vary before they count as a FIXED cadence. The observed
// defect re-delivered a byte-identical block every ~4-5s — a max/min ratio of
// ~1.25, comfortably inside this bound, while genuinely conversational
// repetition is bursty and blows straight past it.
const cadenceRegularityFactor = 2.0

// evaluateTranscriptProgress reads path and applies the shared liveness
// definition. A missing file yields Present=false plus the corresponding
// failure; an UNREADABLE or MALFORMED file yields an error, because that is a
// broken measurement rather than a verdict about the agent.
func evaluateTranscriptProgress(path string) (progressVerdict, error) {
	v := progressVerdict{Path: path, MaxSeq: -1, EntryTypes: map[string]int{}}

	f, err := os.Open(path)
	if errors.Is(err, fs.ErrNotExist) {
		v.Failures = append(v.Failures,
			"no transcript file: the recorder opens it lazily on the first event, so "+
				"absence means the agent produced ZERO events (it never got, or never "+
				"acted on, its prompt)")
		return v, nil
	}
	if err != nil {
		return v, fmt.Errorf("read transcript %s: %w", path, err)
	}
	defer func() { _ = f.Close() }()
	v.Present = true

	var recs []transcript.Record
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 8*1024*1024) // context leads are large
	for line := 1; sc.Scan(); line++ {
		raw := strings.TrimSpace(sc.Text())
		if raw == "" {
			continue
		}
		var r transcript.Record
		if err := json.Unmarshal([]byte(raw), &r); err != nil {
			return v, fmt.Errorf("transcript %s line %d: %w", path, line, err)
		}
		recs = append(recs, r)
	}
	if err := sc.Err(); err != nil {
		return v, fmt.Errorf("scan transcript %s: %w", path, err)
	}
	v.Records = len(recs)

	v.Failures = append(v.Failures, progressSeqFailures(recs, &v)...)
	v.Failures = append(v.Failures, progressEntryFailures(recs, &v)...)
	v.Failures = append(v.Failures, progressCadenceFailures(recs)...)
	v.Parked = progressParkedOnApproval(recs)
	return v, nil
}

// progressSeqFailures checks signal 1: seq starts at 0, is contiguous, and
// advances PAST 0. It also latches MaxSeq onto the verdict.
func progressSeqFailures(recs []transcript.Record, v *progressVerdict) []string {
	if len(recs) == 0 {
		return []string{"transcript is empty: no records at all"}
	}
	var out []string
	for _, r := range recs {
		if r.Seq > v.MaxSeq {
			v.MaxSeq = r.Seq
		}
	}
	// Contiguity is the writer's own documented contract (record.go: starts at
	// 0, no gaps), so a violation means either truncation or — the case that
	// matters here — a transcript appended to by SUCCESSIVE recorders, each
	// restarting its own seq at 0. That is precisely what a relaunch loop that
	// never gets a turn out of the engine leaves behind.
	for i, r := range recs {
		if r.Seq != i {
			out = append(out, fmt.Sprintf(
				"seq is not contiguous from 0: record %d carries seq %d (a restarted seq "+
					"means a second recorder appended to the same file — the relaunch-loop "+
					"signature)", i, r.Seq))
			break
		}
	}
	if v.MaxSeq <= 0 {
		out = append(out, fmt.Sprintf(
			"seq never advanced past 0 (max_seq=%d): the agent recorded at most its own "+
				"prompt and produced nothing", v.MaxSeq))
	}
	return out
}

// progressEntryFailures checks signals 2 and 3 (an assistant entry exists;
// more than one distinct entry.type appears) and latches the type census.
func progressEntryFailures(recs []transcript.Record, v *progressVerdict) []string {
	for _, r := range recs {
		if r.Kind != transcript.KindEntry || r.Entry == nil {
			continue
		}
		v.EntryTypes[r.Entry.Type]++
	}
	var out []string
	if v.EntryTypes[string(agentEntryTypeAssistant)] == 0 {
		out = append(out, "no entry.type==\"assistant\" record: the agent never produced a turn")
	}
	if len(v.EntryTypes) <= 1 {
		only := "none"
		for t := range v.EntryTypes {
			only = fmt.Sprintf("%q x%d", t, v.EntryTypes[t])
		}
		out = append(out, fmt.Sprintf(
			"no entry-type variety: every entry record is the same type (%s); the failing "+
				"container agents were 100%% \"user\"", only))
	}
	return out
}

// agentEntryTypeAssistant is the one entry-type string this file compares
// against by name, kept as a constant so a rename of the vocabulary is a
// compile-visible change here rather than a silently-never-matching literal.
const agentEntryTypeAssistant = "assistant"

// progressCadenceFailures checks signal 4: identical (entry.type,
// entry.content) content re-appended on a FIXED cadence — the re-delivery
// loop, measured on ts (receipt time).
func progressCadenceFailures(recs []transcript.Record) []string {
	type key struct{ typ, content string }
	groups := map[key][]time.Time{}
	order := []key{}
	for _, r := range recs {
		if r.Kind != transcript.KindEntry || r.Entry == nil || r.Entry.Content == "" {
			continue
		}
		k := key{r.Entry.Type, r.Entry.Content}
		if _, seen := groups[k]; !seen {
			order = append(order, k)
		}
		groups[k] = append(groups[k], r.TS)
	}
	var out []string
	for _, k := range order {
		ts := groups[k]
		if len(ts) < cadenceMinSamples {
			continue
		}
		sort.Slice(ts, func(i, j int) bool { return ts[i].Before(ts[j]) })
		minD, maxD := time.Duration(1<<62), time.Duration(0)
		for i := 1; i < len(ts); i++ {
			d := ts[i].Sub(ts[i-1])
			if d <= 0 {
				// Sub-tick identical stamps are a burst, not a cadence.
				minD = 0
				break
			}
			if d < minD {
				minD = d
			}
			if d > maxD {
				maxD = d
			}
		}
		if minD <= 0 {
			continue
		}
		if float64(maxD) <= cadenceRegularityFactor*float64(minD) {
			out = append(out, fmt.Sprintf(
				"identical %q content (%d chars) re-appended %d times on a fixed cadence "+
					"(deltas %s..%s): the agent is being re-delivered the same block, not "+
					"advancing", k.typ, len(k.content), len(ts), minD.Round(time.Millisecond),
				maxD.Round(time.Millisecond)))
		}
	}
	return out
}

// progressParkedOnApproval reports whether the transcript ENDS on a permission
// request — the definition's explicit carve-out: an agent awaiting approval is
// alive, and must never be reaped as dead.
func progressParkedOnApproval(recs []transcript.Record) bool {
	for i := len(recs) - 1; i >= 0; i-- {
		switch recs[i].Kind {
		case transcript.KindPermission:
			return true
		case transcript.KindEntry, transcript.KindComplete:
			return false
		}
	}
	return false
}

// ---------------------------------------------------------------------------
// Hermetic coverage of the signal logic. No docker, no engine — this runs
// under plain `just test`, which is the point (see the package comment).
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
	path := t.TempDir() + "/transcript.jsonl"
	var b strings.Builder
	for _, r := range recs {
		b.WriteString(progressLine(t, r))
	}
	require.NoError(t, os.WriteFile(path, []byte(b.String()), 0o600))
	return path
}

func TestEvaluateTranscriptProgress_MissingFileIsAFailureNotAnError(t *testing.T) {
	v, err := evaluateTranscriptProgress(t.TempDir() + "/never-written.jsonl")
	require.NoError(t, err, "a missing transcript is a VERDICT, never an error")
	assert.False(t, v.Present)
	assert.False(t, v.progressing())
	assert.True(t, v.stalled())
	require.Len(t, v.Failures, 1)
	assert.Contains(t, v.Failures[0], "no transcript file")
}

func TestEvaluateTranscriptProgress_HealthyTurnPasses(t *testing.T) {
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
	v, err := evaluateTranscriptProgress(path)
	require.NoError(t, err)
	assert.True(t, v.progressing(), "a healthy turn must show progress; got:\n%s", v)
	assert.False(t, v.stalled())
	assert.Equal(t, 5, v.MaxSeq)
	assert.Len(t, v.EntryTypes, 5)
}

func TestEvaluateTranscriptProgress_UserOnlySeqPinnedAtZero(t *testing.T) {
	// The exact shape a container child left behind when it never got going:
	// one user record, seq 0, nothing else.
	base := time.Date(2026, 7, 24, 12, 0, 0, 0, time.UTC)
	path := writeProgressFixture(t, progressEntry(0, base, "user", "composed context"))

	v, err := evaluateTranscriptProgress(path)
	require.NoError(t, err)
	assert.True(t, v.Present)
	assert.True(t, v.stalled())
	joined := strings.Join(v.Failures, "\n")
	assert.Contains(t, joined, "seq never advanced past 0")
	assert.Contains(t, joined, "no entry.type==\"assistant\"")
	assert.Contains(t, joined, "no entry-type variety")
}

func TestEvaluateTranscriptProgress_RedeliveryCadenceIsCaught(t *testing.T) {
	// The 21809-char block re-delivered every ~4-5s, in miniature: identical
	// content, evenly spaced, seq restarting at 0 per relaunch.
	base := time.Date(2026, 7, 24, 12, 0, 0, 0, time.UTC)
	const block = "COMPOSED CONTEXT BLOCK"
	path := writeProgressFixture(t,
		progressEntry(0, base, "user", block),
		progressEntry(0, base.Add(4*time.Second), "user", block),
		progressEntry(0, base.Add(9*time.Second), "user", block),
		progressEntry(0, base.Add(13*time.Second), "user", block),
	)
	v, err := evaluateTranscriptProgress(path)
	require.NoError(t, err)
	assert.True(t, v.stalled())
	joined := strings.Join(v.Failures, "\n")
	assert.Contains(t, joined, "fixed cadence")
	assert.Contains(t, joined, "seq is not contiguous from 0")
}

func TestEvaluateTranscriptProgress_BurstyRepetitionIsNotACadence(t *testing.T) {
	// A conversation that legitimately repeats a short string must NOT trip the
	// cadence signal — a false positive here would reap live agents.
	base := time.Date(2026, 7, 24, 12, 0, 0, 0, time.UTC)
	path := writeProgressFixture(t,
		progressEntry(0, base, "user", "ok"),
		progressEntry(1, base.Add(200*time.Millisecond), "assistant", "ok"),
		progressEntry(2, base.Add(400*time.Millisecond), "user", "ok"),
		progressEntry(3, base.Add(30*time.Second), "assistant", "ok"),
		progressEntry(4, base.Add(31*time.Second), "user", "ok"),
		progressEntry(5, base.Add(200*time.Second), "assistant", "ok"),
	)
	v, err := evaluateTranscriptProgress(path)
	require.NoError(t, err)
	assert.True(t, v.progressing(), "bursty repetition is not a fixed cadence; got:\n%s", v)
}

func TestEvaluateTranscriptProgress_ParkedOnApprovalIsNotStalled(t *testing.T) {
	base := time.Date(2026, 7, 24, 12, 0, 0, 0, time.UTC)
	path := writeProgressFixture(t,
		progressEntry(0, base, "user", "please edit the file"),
		transcript.Record{V: transcript.SchemaVersion, Harp: "fixture-harp", Engine: "mock",
			Seq: 1, TS: base.Add(time.Second), Kind: transcript.KindPermission,
			Permission: &transcript.PermissionPayload{ID: "perm-1", ToolName: "edit"}},
	)
	v, err := evaluateTranscriptProgress(path)
	require.NoError(t, err)
	assert.True(t, v.Parked)
	assert.False(t, v.progressing(), "it genuinely has produced no assistant turn yet")
	assert.False(t, v.stalled(), "an agent awaiting approval is ALIVE and must never be reaped")
}

func TestEvaluateTranscriptProgress_MalformedLineIsAnErrorNotAVerdict(t *testing.T) {
	path := t.TempDir() + "/transcript.jsonl"
	require.NoError(t, os.WriteFile(path, []byte("{not json}\n"), 0o600))
	_, err := evaluateTranscriptProgress(path)
	require.Error(t, err, "a corrupt transcript is a broken MEASUREMENT, not a dead agent")
}
