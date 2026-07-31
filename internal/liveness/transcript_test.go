package liveness_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/liveness"
)

// writeJSONL writes explicit envelopes so a test can control `ts` exactly —
// the real Recorder stamps time.Now(), which is right for production and
// useless for asserting on a CADENCE.
func writeJSONL(t *testing.T, lines []map[string]any) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "transcript.jsonl")
	f, err := os.Create(path)
	require.NoError(t, err)
	defer func() { require.NoError(t, f.Close()) }()
	enc := json.NewEncoder(f)
	for _, l := range lines {
		require.NoError(t, enc.Encode(l))
	}
	return path
}

func userLine(seq int, ts time.Time, content string) map[string]any {
	return map[string]any{
		"v": 1, "harp": "h", "engine": "claude", "seq": seq,
		"ts": ts.Format(time.RFC3339Nano), "kind": "entry",
		"entry": map[string]any{"type": "user", "content": content},
	}
}

func TestReadTranscript_MissingFileIsASignalNotAnError(t *testing.T) {
	st, err := liveness.ReadTranscript(filepath.Join(t.TempDir(), "nope.jsonl"))
	require.NoError(t, err, "absence is the loudest signal — it must never be swallowed as an error")
	assert.False(t, st.Exists)
	assert.Zero(t, st.Records)
}

func TestReadTranscript_EmptyPathIsAnError(t *testing.T) {
	_, err := liveness.ReadTranscript("")
	assert.Error(t, err, "an empty path is a caller bug, not an absent transcript")
}

// The incident's fingerprint, at its real numbers: the same composed-context
// block re-appended every ~4-5 seconds.
func TestReadTranscript_DetectsRedeliveryCadence(t *testing.T) {
	base := time.Date(2026, 7, 24, 10, 0, 0, 0, time.UTC)
	gaps := []time.Duration{0, 4500 * time.Millisecond, 9000 * time.Millisecond, 13400 * time.Millisecond, 17900 * time.Millisecond}
	var lines []map[string]any
	for _, g := range gaps {
		lines = append(lines, userLine(0, base.Add(g), composedContext))
	}
	st, err := liveness.ReadTranscript(writeJSONL(t, lines))
	require.NoError(t, err)

	require.NotNil(t, st.Redelivery, "identical content on a metronome is a loop")
	assert.Equal(t, "user", st.Redelivery.EntryType)
	assert.Equal(t, 5, st.Redelivery.Repeats)
	assert.InDelta(t, float64(4500*time.Millisecond), float64(st.Redelivery.Cadence), float64(200*time.Millisecond))
	assert.True(t, st.SeqPinned, "every line at seq 0 means every line came from a different Recorder")
	assert.Equal(t, []string{"user"}, st.EntryTypes)
	assert.Zero(t, st.AssistantEntries)
	assert.False(t, st.TurnClosed)
}

// Repetition alone is not a loop. A user (or a retry ladder) re-sending the
// same text at irregular intervals must not be flagged.
func TestReadTranscript_IrregularRepeatsAreNotACadence(t *testing.T) {
	base := time.Date(2026, 7, 24, 10, 0, 0, 0, time.UTC)
	lines := []map[string]any{
		userLine(0, base, "ping"),
		userLine(1, base.Add(3*time.Second), "ping"),
		userLine(2, base.Add(70*time.Second), "ping"),
		userLine(3, base.Add(9*time.Minute), "ping"),
	}
	st, err := liveness.ReadTranscript(writeJSONL(t, lines))
	require.NoError(t, err)
	assert.Nil(t, st.Redelivery, "irregular repeats are a human, not a machine loop")
	assert.False(t, st.SeqPinned, "seq advanced, so these came from one recorder")
}

// Two identical records are below the loop threshold on purpose.
func TestReadTranscript_TwoRepeatsIsNotYetALoop(t *testing.T) {
	base := time.Date(2026, 7, 24, 10, 0, 0, 0, time.UTC)
	st, err := liveness.ReadTranscript(writeJSONL(t, []map[string]any{
		userLine(0, base, "again"),
		userLine(1, base.Add(4*time.Second), "again"),
	}))
	require.NoError(t, err)
	assert.Nil(t, st.Redelivery)
}

func TestReadTranscript_TurnClosedAndPendingPermission(t *testing.T) {
	base := time.Date(2026, 7, 24, 10, 0, 0, 0, time.UTC)
	mk := func(seq int, off time.Duration, kind string, entry map[string]any) map[string]any {
		m := map[string]any{"v": 1, "harp": "h", "engine": "claude", "seq": seq,
			"ts": base.Add(off).Format(time.RFC3339Nano), "kind": kind}
		if entry != nil {
			m["entry"] = entry
		}
		return m
	}
	closed, err := liveness.ReadTranscript(writeJSONL(t, []map[string]any{
		mk(0, 0, "entry", map[string]any{"type": "user", "content": "go"}),
		mk(1, time.Second, "entry", map[string]any{"type": "assistant", "content": "done"}),
		mk(2, 2*time.Second, "complete", nil),
	}))
	require.NoError(t, err)
	assert.True(t, closed.TurnClosed)
	assert.False(t, closed.PendingPermission)
	assert.Equal(t, 1, closed.Completes)

	parked, err := liveness.ReadTranscript(writeJSONL(t, []map[string]any{
		mk(0, 0, "entry", map[string]any{"type": "user", "content": "go"}),
		mk(1, time.Second, "permission", nil),
	}))
	require.NoError(t, err)
	assert.True(t, parked.PendingPermission)
	assert.False(t, parked.TurnClosed)
}

// A newer schema version, or fields this reader does not know, must not turn
// a readable transcript into an unreadable one.
func TestReadTranscript_TolerantOfUnknownFieldsAndTrailingGarbage(t *testing.T) {
	base := time.Date(2026, 7, 24, 10, 0, 0, 0, time.UTC)
	path := writeJSONL(t, []map[string]any{
		{"v": 99, "harp": "h", "engine": "future", "seq": 0, "ts": base.Format(time.RFC3339Nano),
			"kind": "entry", "entry": map[string]any{"type": "assistant", "content": "hi", "brand_new": 1},
			"unknown_top_level": []int{1, 2}},
	})
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o644)
	require.NoError(t, err)
	_, err = f.WriteString("{ truncated par\n")
	require.NoError(t, err)
	require.NoError(t, f.Close())

	st, err := liveness.ReadTranscript(path)
	require.NoError(t, err)
	assert.Equal(t, 1, st.Records)
	assert.Equal(t, 1, st.Malformed, "a torn tail is counted, not fatal")
	assert.Equal(t, 1, st.AssistantEntries)
}

func TestNewestMTime_AbsentRootIsNotAnError(t *testing.T) {
	_, ok, err := liveness.NewestMTime(filepath.Join(t.TempDir(), "gone"))
	require.NoError(t, err)
	assert.False(t, ok)
}

// An unreadable worktree is a FAILED observation, not an observation of
// stillness. The walk callback used to swallow every error and return nil, so
// `filepath.WalkDir` consumed it and `chmod 000` came back as
// (zero, false, nil) — indistinguishable from an empty directory, which drops
// the filesystem clock out of `Quiet` and biases the verdict toward stalled
// (U056-F08). This is the same absent-vs-error collapse as U056-F01/F05.
func TestNewestMTime_UnreadableRootIsAnError(t *testing.T) {
	requireNonRoot(t)
	root := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(root, "a.go"), []byte("a"), 0o644))
	require.NoError(t, os.Chmod(root, 0o000))
	t.Cleanup(func() { _ = os.Chmod(root, 0o755) })

	_, ok, err := liveness.NewestMTime(root)
	require.Error(t, err, "an unreadable worktree must not be reported as an empty one")
	assert.False(t, ok, "nothing was observed, so ok must not claim otherwise")
}

// A subtree that cannot be read still lets the walk finish — the best
// available answer is returned — but the failure is REPORTED alongside it
// rather than dropped, so a caller can tell a complete observation from a
// partial one.
func TestNewestMTime_UnreadableSubtreeReportsBothAnswerAndError(t *testing.T) {
	requireNonRoot(t)
	root := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(root, "a.go"), []byte("a"), 0o644))
	locked := filepath.Join(root, "locked")
	require.NoError(t, os.MkdirAll(locked, 0o755))
	require.NoError(t, os.Chmod(locked, 0o000))
	t.Cleanup(func() { _ = os.Chmod(locked, 0o755) })

	newest, ok, err := liveness.NewestMTime(root)
	require.Error(t, err, "an unreadable subtree must be reported")
	assert.True(t, ok, "the readable part of the tree was still observed")
	assert.False(t, newest.IsZero())
}

// requireNonRoot skips tests whose mechanism is a permission denial: root
// ignores directory modes, so the provocation silently does not happen and the
// test would pass without exercising anything.
func requireNonRoot(t *testing.T) {
	t.Helper()
	if os.Geteuid() == 0 {
		t.Skip("running as root: directory permissions cannot deny this process, so the unreadable-walk path cannot be provoked")
	}
}

func TestNewestMTime_FindsNewestFileAndSkipsGit(t *testing.T) {
	root := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(root, ".git"), 0o755))
	old := time.Now().Add(-time.Hour)
	require.NoError(t, os.WriteFile(filepath.Join(root, "a.go"), []byte("a"), 0o644))
	require.NoError(t, os.Chtimes(filepath.Join(root, "a.go"), old, old))
	require.NoError(t, os.WriteFile(filepath.Join(root, "b.go"), []byte("b"), 0o644))
	// A .git file newer than everything must not be mistaken for agent work.
	require.NoError(t, os.WriteFile(filepath.Join(root, ".git", "index"), []byte("x"), 0o644))
	future := time.Now().Add(time.Hour)
	require.NoError(t, os.Chtimes(filepath.Join(root, ".git", "index"), future, future))

	newest, ok, err := liveness.NewestMTime(root)
	require.NoError(t, err)
	require.True(t, ok)
	assert.True(t, newest.Before(future.Add(-time.Minute)), "the .git tree must be skipped, got %s", newest)
	assert.True(t, newest.After(old.Add(time.Minute)), "b.go should be the newest, got %s", newest)
}

// detectRedelivery's doc promises a "stable, test-assertable answer", but its
// tie-break only ordered by entry TYPE — so two groups of the same type that
// repeated the same number of times differed only in CONTENT, and which one
// won was decided by Go's randomised map iteration. A monitor whose reason
// text changes between two reads of an unchanged file is one nobody can argue
// with from the record (U056-F16).
func TestReadTranscript_RedeliveryTieBreakIsDeterministic(t *testing.T) {
	base := time.Date(2026, 7, 24, 10, 0, 0, 0, time.UTC)
	var lines []map[string]any
	for i := 0; i < 4; i++ {
		at := base.Add(time.Duration(i) * 5 * time.Second)
		lines = append(lines, userLine(0, at, "aaa-first-loop-body"), userLine(0, at, "zzz-second-loop-body"))
	}
	path := writeJSONL(t, lines)

	seen := map[string]int{}
	for i := 0; i < 50; i++ {
		st, err := liveness.ReadTranscript(path)
		require.NoError(t, err)
		require.NotNil(t, st.Redelivery)
		require.Equal(t, 4, st.Redelivery.Repeats)
		seen[st.Redelivery.Sample]++
	}
	assert.Len(t, seen, 1, "the same file must yield the same redelivery verdict every time; got %v", seen)
	assert.Contains(t, seen, "aaa-first-loop-body", "ties resolve on content, lowest first")
}

// Zero is Thresholds' UNSET sentinel, and RedeliveryJitterRatio is the one
// field where that costs a caller something real: 0 is a meaningful tolerance
// (exact cadence only) and cannot be asked for, because normalize() reads it
// as "unset" and substitutes 0.25. The surprise is now written down on the
// field; this pins the behaviour the prose asserts, so the two cannot drift
// (U056-F14). Exact matching remains expressible via a ratio too small for any
// integer-nanosecond deviation to clear.
func TestReadTranscriptWith_ZeroJitterRatioMeansTheDefaultNotExactCadence(t *testing.T) {
	base := time.Date(2026, 7, 24, 10, 0, 0, 0, time.UTC)
	// Gaps of 4.0s / 5.0s / 4.0s: median 4s, max deviation 1s = 25% — inside
	// the default tolerance, outside an exact one.
	var lines []map[string]any
	for _, g := range []time.Duration{0, 4 * time.Second, 9 * time.Second, 13 * time.Second} {
		lines = append(lines, userLine(0, base.Add(g), composedContext))
	}
	path := writeJSONL(t, lines)

	zero, err := liveness.ReadTranscriptWith(path, liveness.Thresholds{RedeliveryJitterRatio: 0})
	require.NoError(t, err)
	require.NotNil(t, zero.Redelivery, "a zero ratio is the unset sentinel, so the default 0.25 applies")

	exact, err := liveness.ReadTranscriptWith(path, liveness.Thresholds{RedeliveryJitterRatio: 1e-20})
	require.NoError(t, err)
	assert.Nil(t, exact.Redelivery, "an effectively-exact tolerance is how a caller asks for exact-cadence-only")
}
