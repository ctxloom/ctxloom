package contextmetrics

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/testsupport"
)

const testHarp = "swift-amber-falcon"

// readLines returns the raw lines of a harp's metrics file. Tests assert on
// the FILE, not on a return value: the whole point of this package is bytes
// landing on disk, and a writer that reports success while writing nothing is
// the exact failure mode being guarded against.
func readLines(t *testing.T, harp string) []string {
	t.Helper()
	p, err := Path(harp)
	require.NoError(t, err)
	b, err := os.ReadFile(p)
	if os.IsNotExist(err) {
		return nil
	}
	require.NoError(t, err)
	trimmed := strings.TrimRight(string(b), "\n")
	if trimmed == "" {
		return nil
	}
	return strings.Split(trimmed, "\n")
}

// TestShouldAppend_SamplingRule pins the sampling policy directly: a
// statusline refreshes many times per assistant message, so a sample is kept
// only when it carries news — a percentage that moved by at least a point, or
// enough elapsed time that a flat stretch is evidence of its own.
//
// Asserted on the pure predicate rather than only through file counts so that
// deleting the guard fails HERE, naming the rule, instead of surfacing as a
// mysterious line-count mismatch three layers away.
func TestShouldAppend_SamplingRule(t *testing.T) {
	base := time.Date(2026, 8, 13, 12, 0, 0, 0, time.UTC)
	prev := Sample{TS: base, ContextPct: 40}

	cases := []struct {
		name string
		prev *Sample
		next Sample
		want bool
	}{
		{"no series yet always records", nil, Sample{TS: base, ContextPct: 40}, true},
		{"same percent, one second later", &prev, Sample{TS: base.Add(time.Second), ContextPct: 40}, false},
		{"sub-point drift is not news", &prev, Sample{TS: base.Add(time.Second), ContextPct: 40.6}, false},
		{"a full point up is news", &prev, Sample{TS: base.Add(time.Second), ContextPct: 41}, true},
		{"a full point DOWN is news too", &prev, Sample{TS: base.Add(time.Second), ContextPct: 39}, true},
		{"59s flat is not yet news", &prev, Sample{TS: base.Add(59 * time.Second), ContextPct: 40}, false},
		{"60s flat is news", &prev, Sample{TS: base.Add(60 * time.Second), ContextPct: 40}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, ShouldAppend(tc.prev, tc.next))
		})
	}
}

// TestRecord_SamplingRuleBoundsFileGrowth is the same rule observed where it
// matters — in the number of lines actually written. Ten refreshes reporting
// an unchanged percentage within the interval must leave ONE line, not ten.
func TestRecord_SamplingRuleBoundsFileGrowth(t *testing.T) {
	testsupport.Isolate(t)
	base := time.Date(2026, 8, 13, 12, 0, 0, 0, time.UTC)

	for i := range 10 {
		wrote, err := Record(testHarp, Sample{TS: base.Add(time.Duration(i) * time.Second), Harp: testHarp, ContextPct: 40, Window: 1000000})
		require.NoError(t, err)
		assert.Equal(t, i == 0, wrote, "only the first refresh carries news")
	}
	assert.Len(t, readLines(t, testHarp), 1, "an unchanged percentage must not grow the file")

	// A point of movement is news, and lands.
	wrote, err := Record(testHarp, Sample{TS: base.Add(11 * time.Second), Harp: testHarp, ContextPct: 41, Window: 1000000})
	require.NoError(t, err)
	assert.True(t, wrote)
	assert.Len(t, readLines(t, testHarp), 2)
}

// TestAppend_IsAppendOnly pins that a second write never rewrites the first —
// the file is a series, and concurrent statusline processes append to it.
func TestAppend_IsAppendOnly(t *testing.T) {
	testsupport.Isolate(t)
	base := time.Date(2026, 8, 13, 12, 0, 0, 0, time.UTC)

	require.NoError(t, Append(testHarp, Sample{TS: base, Harp: testHarp, ContextPct: 10}))
	require.NoError(t, Append(testHarp, Sample{TS: base.Add(time.Minute), Harp: testHarp, ContextPct: 20}))

	lines := readLines(t, testHarp)
	require.Len(t, lines, 2)

	var first Sample
	require.NoError(t, json.Unmarshal([]byte(lines[0]), &first))
	assert.Equal(t, 10.0, first.ContextPct, "the earlier sample must survive the later one")

	all, err := Read(testHarp)
	require.NoError(t, err)
	require.Len(t, all, 2)
	assert.Equal(t, 10.0, all[0].ContextPct, "oldest first")
	assert.Equal(t, 20.0, all[1].ContextPct)
}

// TestAppend_WritesAPayloadNotAnEmptyFile guards ctxloom's characteristic
// defect: a writer that returns nil having written zero bytes. The assertion
// is on the decoded CONTENT of the line, not on the call's error.
func TestAppend_WritesAPayloadNotAnEmptyFile(t *testing.T) {
	testsupport.Isolate(t)
	ts := time.Date(2026, 8, 13, 12, 0, 0, 0, time.UTC)

	require.NoError(t, Append(testHarp, Sample{
		TS: ts, Harp: testHarp, SessionID: "8ce20123", ContextPct: 37, TokensUsed: 372000, Window: 1000000, Model: "Fable 5",
	}))

	lines := readLines(t, testHarp)
	require.Len(t, lines, 1)
	var got Sample
	require.NoError(t, json.Unmarshal([]byte(lines[0]), &got))
	assert.True(t, ts.Equal(got.TS))
	assert.Equal(t, testHarp, got.Harp)
	assert.Equal(t, "8ce20123", got.SessionID)
	assert.Equal(t, 37.0, got.ContextPct)
	assert.Equal(t, 372000, got.TokensUsed)
	assert.Equal(t, 1000000, got.Window)
	assert.Equal(t, "Fable 5", got.Model)
}

// TestRead_AbsentSeriesIsEmptyNotAnError pins the shape the serving tool
// depends on to stay honest: a session that never recorded anything reads back
// as NO samples, which the caller can report as "unknown" — as distinct from
// both an error and a series of zeroes.
func TestRead_AbsentSeriesIsEmptyNotAnError(t *testing.T) {
	testsupport.Isolate(t)

	all, err := Read(testHarp)
	require.NoError(t, err)
	assert.Empty(t, all)

	last, err := Last(testHarp)
	require.NoError(t, err)
	assert.Nil(t, last, "no samples means no latest — not a zero-valued one")
}

// TestRead_SkipsUnparseableLines: concurrent appenders and abrupt process
// death can leave one torn line, and a reader consulted precisely when things
// are going badly must not lose every good sample around it.
func TestRead_SkipsUnparseableLines(t *testing.T) {
	testsupport.Isolate(t)
	base := time.Date(2026, 8, 13, 12, 0, 0, 0, time.UTC)

	require.NoError(t, Append(testHarp, Sample{TS: base, Harp: testHarp, ContextPct: 10}))
	p, err := Path(testHarp)
	require.NoError(t, err)
	f, err := os.OpenFile(p, os.O_APPEND|os.O_WRONLY, 0o644)
	require.NoError(t, err)
	_, err = f.WriteString("{\"ts\":\"2026-08-13T12:0\n")
	require.NoError(t, err)
	require.NoError(t, f.Close())
	require.NoError(t, Append(testHarp, Sample{TS: base.Add(time.Minute), Harp: testHarp, ContextPct: 20}))

	all, err := Read(testHarp)
	require.NoError(t, err)
	require.Len(t, all, 2, "the torn line is skipped; the good ones survive")
	assert.Equal(t, 10.0, all[0].ContextPct)
	assert.Equal(t, 20.0, all[1].ContextPct)
}

// TestTail_ReturnsTheMostRecentOldestFirst pins the trend's ordering and
// bound — the serving tool reads its "latest" off the END of this slice, so
// an inverted order would report the oldest sample as current.
func TestTail_ReturnsTheMostRecentOldestFirst(t *testing.T) {
	testsupport.Isolate(t)
	base := time.Date(2026, 8, 13, 12, 0, 0, 0, time.UTC)
	for i := range 5 {
		require.NoError(t, Append(testHarp, Sample{TS: base.Add(time.Duration(i) * time.Minute), Harp: testHarp, ContextPct: float64(10 * i)}))
	}

	got, err := Tail(testHarp, 3)
	require.NoError(t, err)
	require.Len(t, got, 3)
	assert.Equal(t, 20.0, got[0].ContextPct)
	assert.Equal(t, 40.0, got[2].ContextPct, "the last element is the most recent sample")

	none, err := Tail(testHarp, 0)
	require.NoError(t, err)
	assert.Empty(t, none)
}

// TestPath_RequiresAHarp: a sample attributed to no session has nowhere to go,
// and inventing a location for it would scatter unreadable files.
func TestPath_RequiresAHarp(t *testing.T) {
	testsupport.Isolate(t)
	_, err := Path("")
	require.Error(t, err)
}
