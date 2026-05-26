package sessions

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func fixedNow(t time.Time) func() time.Time {
	return func() time.Time { return t }
}

func makeEntries(n int, base time.Time) []Entry {
	out := make([]Entry, n)
	for i := range n {
		out[i] = Entry{
			HarpName:   "row-" + string(rune('a'+i)),
			ProjectDir: "/proj",
			StartedAt:  base.Add(-time.Duration(i) * time.Hour),
			Summary:    "summary " + string(rune('a'+i)),
		}
	}
	return out
}

func runPicker(t *testing.T, entries []Entry, input string) (Decision, string) {
	t.Helper()
	var out bytes.Buffer
	p := &Picker{
		Entries: entries,
		In:      strings.NewReader(input),
		Out:     &out,
		Now:     fixedNow(time.Date(2026, 5, 26, 12, 0, 0, 0, time.UTC)),
	}
	dec, err := p.Run()
	require.NoError(t, err)
	return dec, out.String()
}

func TestPicker_EmptyIndex_FallsThroughToNew(t *testing.T) {
	dec, _ := runPicker(t, nil, "")
	assert.Equal(t, NewAction, dec.Action)
}

func TestPicker_ResumeByNumber(t *testing.T) {
	entries := makeEntries(3, time.Date(2026, 5, 26, 10, 0, 0, 0, time.UTC))
	dec, out := runPicker(t, entries, "2\n")
	assert.Equal(t, ResumeAction, dec.Action)
	assert.Equal(t, entries[1].HarpName, dec.FromHarp)
	assert.True(t, dec.RestoreSession, "[s] defaults to checked")
	assert.True(t, dec.RestoreTasks, "[t] defaults to checked")
	assert.Contains(t, out, "row-a")
	assert.Contains(t, out, "row-b")
}

func TestPicker_NKeystrokeForNew(t *testing.T) {
	entries := makeEntries(2, time.Date(2026, 5, 26, 10, 0, 0, 0, time.UTC))
	dec, _ := runPicker(t, entries, "n\n")
	assert.Equal(t, NewAction, dec.Action)
}

func TestPicker_QKeystrokeForQuit(t *testing.T) {
	entries := makeEntries(2, time.Date(2026, 5, 26, 10, 0, 0, 0, time.UTC))
	dec, _ := runPicker(t, entries, "q\n")
	assert.Equal(t, QuitAction, dec.Action)
}

func TestPicker_EOFFallsThroughToNew(t *testing.T) {
	entries := makeEntries(1, time.Date(2026, 5, 26, 10, 0, 0, 0, time.UTC))
	dec, _ := runPicker(t, entries, "")
	assert.Equal(t, NewAction, dec.Action)
}

func TestPicker_ToggleSessionCheckbox(t *testing.T) {
	entries := makeEntries(2, time.Date(2026, 5, 26, 10, 0, 0, 0, time.UTC))
	dec, _ := runPicker(t, entries, "s1\n1\n")
	assert.Equal(t, ResumeAction, dec.Action)
	assert.Equal(t, entries[0].HarpName, dec.FromHarp)
	assert.False(t, dec.RestoreSession, "s1 should have un-checked [s]")
	assert.True(t, dec.RestoreTasks, "[t] untouched should remain checked")
}

func TestPicker_ToggleTasksCheckbox(t *testing.T) {
	entries := makeEntries(2, time.Date(2026, 5, 26, 10, 0, 0, 0, time.UTC))
	dec, _ := runPicker(t, entries, "t2\n2\n")
	assert.Equal(t, ResumeAction, dec.Action)
	assert.Equal(t, entries[1].HarpName, dec.FromHarp)
	assert.True(t, dec.RestoreSession)
	assert.False(t, dec.RestoreTasks, "t2 should have un-checked [t]")
}

func TestPicker_DoubleToggleCancels(t *testing.T) {
	entries := makeEntries(1, time.Date(2026, 5, 26, 10, 0, 0, 0, time.UTC))
	dec, _ := runPicker(t, entries, "s1\ns1\n1\n")
	assert.Equal(t, ResumeAction, dec.Action)
	assert.True(t, dec.RestoreSession, "double-toggle should leave the checkbox back at checked")
}

func TestPicker_RowOutOfRangeLoops(t *testing.T) {
	entries := makeEntries(2, time.Date(2026, 5, 26, 10, 0, 0, 0, time.UTC))
	dec, out := runPicker(t, entries, "99\n1\n")
	assert.Equal(t, ResumeAction, dec.Action)
	assert.Contains(t, out, "out of range")
}

func TestPicker_UnrecognizedInputLoops(t *testing.T) {
	entries := makeEntries(1, time.Date(2026, 5, 26, 10, 0, 0, 0, time.UTC))
	_, out := runPicker(t, entries, "wat\n1\n")
	assert.Contains(t, out, "unrecognized")
}

func TestPicker_HorizonHidesOldEntries(t *testing.T) {
	now := time.Date(2026, 5, 26, 12, 0, 0, 0, time.UTC)
	entries := []Entry{
		{HarpName: "recent", ProjectDir: "/proj", StartedAt: now.Add(-1 * 24 * time.Hour)},
		{HarpName: "old", ProjectDir: "/proj", StartedAt: now.Add(-30 * 24 * time.Hour)},
	}
	// Default 7-day horizon hides the old one.
	dec, out := runPicker(t, entries, "1\n")
	assert.Equal(t, "recent", dec.FromHarp)
	assert.Contains(t, out, "recent")
	assert.NotContains(t, out, "old\n")
	assert.Contains(t, out, "older sessions hidden")
}

func TestPicker_MKeystrokeExpandsHorizon(t *testing.T) {
	now := time.Date(2026, 5, 26, 12, 0, 0, 0, time.UTC)
	entries := []Entry{
		{HarpName: "recent", ProjectDir: "/proj", StartedAt: now.Add(-1 * 24 * time.Hour)},
		{HarpName: "old", ProjectDir: "/proj", StartedAt: now.Add(-10 * 24 * time.Hour)},
	}
	dec, out := runPicker(t, entries, "m\n2\n")
	assert.Equal(t, "old", dec.FromHarp, "m should reveal the older entry; then row 2 picks it")
	assert.Contains(t, out, "old")
}

func TestPicker_DistillKeystrokeNotSupported(t *testing.T) {
	// `d` is not a wired command — sessions without summaries can't be
	// retroactively distilled. The keystroke should fall through to the
	// generic "unrecognized" handler so the user sees a clear error
	// instead of silent acceptance.
	entries := makeEntries(1, time.Date(2026, 5, 26, 10, 0, 0, 0, time.UTC))
	_, out := runPicker(t, entries, "d1\n1\n")
	assert.Contains(t, out, "unrecognized")
}
