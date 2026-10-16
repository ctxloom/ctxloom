package main

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// testPacing uses small, distinct values so the expected timeline is easy to
// compute by hand in assertions below (ReadablePacing's real values are only
// tuned for how a rendered page looks, not for arithmetic clarity).
var testPacing = Pacing{
	Narrate:      100 * time.Millisecond,
	TypeChar:     10 * time.Millisecond,
	BeforeOutput: 20 * time.Millisecond,
	DwellMin:     200 * time.Millisecond,
	DwellPerLine: 50 * time.Millisecond,
	DwellMax:     1 * time.Second,
}

// twoStepCast is a fixture with two steps, each one marker followed by one
// typed command and its output -- the shape PublishCast exists to re-pace.
const twoStepCast = `{"version":2,"width":80,"height":24,"timestamp":0,"title":"t"}
[0.001,"m","Given step one"]
[0.002,"o","$ ctxloom foo\r\n"]
[0.0021,"o","ok\r\n"]
[0.004,"m","When step two"]
[0.005,"o","$ ctxloom bar\r\n"]
[0.006,"o","line1\r\n"]
[0.0061,"o","line2\r\n"]
`

type parsedLine struct {
	t    float64
	code string
	data string
}

func parseLines(t *testing.T, raw []byte) (header map[string]any, lines []parsedLine) {
	t.Helper()
	rows := strings.Split(strings.TrimRight(string(raw), "\n"), "\n")
	require.NotEmpty(t, rows)
	require.NoError(t, json.Unmarshal([]byte(rows[0]), &header))
	for _, row := range rows[1:] {
		var arr [3]json.RawMessage
		require.NoError(t, json.Unmarshal([]byte(row), &arr))
		var pl parsedLine
		require.NoError(t, json.Unmarshal(arr[0], &pl.t))
		require.NoError(t, json.Unmarshal(arr[1], &pl.code))
		require.NoError(t, json.Unmarshal(arr[2], &pl.data))
		lines = append(lines, pl)
	}
	return header, lines
}

// findFirst returns the index of the first line satisfying want, or -1.
// Events are written in one strict sequential order (PublishCast never
// reorders), so an index comparison on the parsed lines is equivalent to a
// byte-offset comparison on the raw output -- and unlike a raw substring
// search, it survives the prompt line being split into one event per
// character.
func findFirst(lines []parsedLine, want func(parsedLine) bool) int {
	for i, l := range lines {
		if want(l) {
			return i
		}
	}
	return -1
}

func TestPublishCast_NarrationPrecedesItsCommand(t *testing.T) {
	out, err := PublishCast([]byte(twoStepCast), testPacing)
	require.NoError(t, err)
	_, lines := parseLines(t, out)

	iNarr1 := findFirst(lines, func(l parsedLine) bool { return strings.Contains(l.data, "Given step one") })
	iCmd1 := findFirst(lines, func(l parsedLine) bool { return l.code == "o" && l.data == "$" })
	iNarr2 := findFirst(lines, func(l parsedLine) bool { return strings.Contains(l.data, "When step two") })
	iCmd2 := findFirst(lines[iNarr2+1:], func(l parsedLine) bool { return l.code == "o" && l.data == "$" })

	require.NotEqual(t, -1, iNarr1)
	require.NotEqual(t, -1, iCmd1)
	require.NotEqual(t, -1, iNarr2)
	require.NotEqual(t, -1, iCmd2)
	iCmd2 += iNarr2 + 1 // rebase: found within the slice starting after iNarr2
	assert.Less(t, iNarr1, iCmd1, "step one's narration must land before its command")
	assert.Less(t, iCmd1, iNarr2, "step one's command must land before step two's narration")
	assert.Less(t, iNarr2, iCmd2, "step two's narration must land before its command")
}

func TestPublishCast_PromptIsTypedCharByChar(t *testing.T) {
	out, err := PublishCast([]byte(twoStepCast), testPacing)
	require.NoError(t, err)
	_, lines := parseLines(t, out)

	const prompt = "$ ctxloom foo\r\n"
	want := []rune(prompt)

	// Collect the run of single-rune "o" events starting at the first one
	// whose data is the prompt's first character.
	start := -1
	for i, l := range lines {
		if l.code == "o" && l.data == string(want[0]) {
			start = i
			break
		}
	}
	require.NotEqual(t, -1, start, "no typed-prompt events found")
	require.GreaterOrEqual(t, len(lines), start+len(want))

	for i, r := range want {
		got := lines[start+i]
		assert.Equal(t, "o", got.code)
		assert.Equal(t, string(r), got.data, "character %d of the typed prompt", i)
		if i > 0 {
			prev := lines[start+i-1]
			assert.InDelta(t, testPacing.TypeChar.Seconds(), got.t-prev.t, 1e-9, "character %d not spaced by TypeChar", i)
		}
	}
}

func TestPublishCast_OutputDwellPaidOncePerCommand(t *testing.T) {
	out, err := PublishCast([]byte(twoStepCast), testPacing)
	require.NoError(t, err)
	_, lines := parseLines(t, out)

	// "ok\r\n" is a single line of output, all from one command's run, so the
	// dwell before the NEXT marker's narration must be paid exactly once:
	// DwellMin + DwellPerLine*1, not once per output event even though this
	// fixture only emits one -- the assertion below is on the gap itself, the
	// property being tested (not per-event) is that intermediate output
	// events of a multi-event run (step two's "line1"/"line2") share one
	// timestamp rather than each independently advancing the clock.
	okIdx, narr2Idx := -1, -1
	for i, l := range lines {
		if l.code == "o" && l.data == "ok\r\n" {
			okIdx = i
		}
		if l.code == "m" && l.data == "When step two" {
			narr2Idx = i
		}
	}
	require.NotEqual(t, -1, okIdx)
	require.NotEqual(t, -1, narr2Idx)
	gap := lines[narr2Idx].t - lines[okIdx].t
	wantDwell := testPacing.DwellMin.Seconds() + testPacing.DwellPerLine.Seconds()*1
	assert.InDelta(t, wantDwell, gap, 1e-9)

	// step two's two output events ("line1\r\n", "line2\r\n") are one run:
	// they must share exactly the same timestamp, proving the dwell was not
	// paid between them.
	line1Idx, line2Idx := -1, -1
	for i, l := range lines {
		if l.code == "o" && l.data == "line1\r\n" {
			line1Idx = i
		}
		if l.code == "o" && l.data == "line2\r\n" {
			line2Idx = i
		}
	}
	require.NotEqual(t, -1, line1Idx)
	require.NotEqual(t, -1, line2Idx)
	assert.Equal(t, lines[line1Idx].t, lines[line2Idx].t, "consecutive output events of one command must not each pay the dwell")
}

func TestPublishCast_TimestampsNonDecreasing(t *testing.T) {
	out, err := PublishCast([]byte(twoStepCast), testPacing)
	require.NoError(t, err)
	_, lines := parseLines(t, out)
	require.NotEmpty(t, lines)
	for i := 1; i < len(lines); i++ {
		assert.GreaterOrEqual(t, lines[i].t, lines[i-1].t, "event %d ordered before %d", i, i-1)
	}
}

func TestPublishCast_TotalDurationInExpectedBand(t *testing.T) {
	out, err := PublishCast([]byte(twoStepCast), testPacing)
	require.NoError(t, err)
	_, lines := parseLines(t, out)
	require.NotEmpty(t, lines)

	last := lines[len(lines)-1].t
	// Lower bound: at minimum both Narrate dwells plus the typed prompts
	// happened, ignoring the trailing (unobservable) output dwell.
	minExpected := 2*testPacing.Narrate.Seconds() + 2*testPacing.BeforeOutput.Seconds()
	// Upper bound: generously over-estimate every dwell as if it were the
	// max, so a regression that pegs everything to DwellMax still fails this.
	maxExpected := 5.0
	assert.GreaterOrEqual(t, last, minExpected)
	assert.LessOrEqual(t, last, maxExpected)
}

func TestPublishCast_HeaderPassesThroughAsV2(t *testing.T) {
	out, err := PublishCast([]byte(twoStepCast), testPacing)
	require.NoError(t, err)
	header, _ := parseLines(t, out)
	assert.Equal(t, float64(2), header["version"])
	assert.Equal(t, float64(80), header["width"])
	assert.Equal(t, float64(24), header["height"])
	assert.Equal(t, "t", header["title"])
}

func TestPublishCast_EmptyCastRoundTrips(t *testing.T) {
	const headerOnly = `{"version":2,"width":80,"height":24,"timestamp":0,"title":"t"}
`
	out, err := PublishCast([]byte(headerOnly), testPacing)
	require.NoError(t, err)
	header, lines := parseLines(t, out)
	assert.Empty(t, lines)
	assert.Equal(t, float64(80), header["width"])
	assert.Equal(t, float64(24), header["height"])
}

func TestPublishCast_MalformedCastReturnsTypedError(t *testing.T) {
	_, err := PublishCast([]byte("not even json\n"), testPacing)
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrMalformedCast))

	_, err = PublishCast([]byte(`{"version":2,"width":80,"height":24}`+"\n"+`["not", "a", "triple", "extra"]`+"\n"), testPacing)
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrMalformedCast))
}

func TestPublishCast_NothingIsStampedAtZero(t *testing.T) {
	p := testPacing
	p.LeadIn = 700 * time.Millisecond
	out, err := PublishCast([]byte(twoStepCast), p)
	require.NoError(t, err)
	_, lines := parseLines(t, out)
	require.NotEmpty(t, lines)
	assert.InDelta(t, p.LeadIn.Seconds(), lines[0].t, 1e-9,
		"the first event must sit at the lead-in, never at t=0")
}
