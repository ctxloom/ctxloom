package tmuxhost

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/core/spool"
)

// These drive a REAL tmux: the claim a typed wake makes is about what the
// program in the pane RECEIVES — the wake text as one submitted line, or
// nothing at all while a human's draft is on the input line.

// lineReader is a pane program with a cooked-mode line buffer and no prompt
// glyph — the mock's shape — that reports each submitted line.
var lineReader = PaneSpec{Command: "sh", Args: []string{"-c", `while IFS= read -r x; do echo "GOT-[$x]"; done`}}

// emptyLine is the mock's composer probe: nothing on the line is empty.
func emptyLine(line string) bool { return line == "" }

func startLineReader(t *testing.T, h *PaneHost, harp string) *recorder {
	t.Helper()
	var r recorder
	require.NoError(t, h.Start(context.Background(), harp, lineReader, &r))
	t.Cleanup(func() { _ = h.Stop(context.Background(), harp) })
	return &r
}

func TestTypedWake_OnAnEmptyInputLineSubmitsTheWakeText(t *testing.T) {
	h := newPaneHostForTest(t)
	r := startLineReader(t, h, "wa")

	w := NewTypedWake(h, "wa", emptyLine)
	require.NoError(t, w.Fire(context.Background(), "0123456789abcdef"))

	want := "GOT-[" + spool.WakeText("0123456789abcdef") + "]"
	waitFor(t, "the wake must arrive as one submitted line, verbatim", func() bool { return strings.Contains(r.text(), want) })
}

// The invariant: a wake NEVER submits a human's draft. The draft stays on the
// line, nothing is typed after it, and the human's own Enter submits exactly
// what they typed.
func TestTypedWake_ADraftOnTheInputLineHoldsTheWakeAndTypesNothing(t *testing.T) {
	h := newPaneHostForTest(t)
	r := startLineReader(t, h, "wb")
	ctx := context.Background()

	require.NoError(t, h.Input(ctx, "wb", []byte("half-typed")))
	waitFor(t, "the draft must be on the line before the wake looks", func() bool { return strings.Contains(r.text(), "half-typed") })

	err := NewTypedWake(h, "wb", emptyLine).Fire(ctx, "0123456789abcdef")
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrWakeHeld), "a held wake is typed, got %v", err)

	require.NoError(t, h.Input(ctx, "wb", []byte("\r")))
	waitFor(t, "the human's Enter submits their draft", func() bool { return strings.Contains(r.text(), "GOT-[half-typed]") })
	assert.NotContains(t, r.text(), "wake", "nothing of the wake reached the program")
}

type holdDetector string

func (d holdDetector) Quiet(context.Context) (bool, string) { return d == "", string(d) }

func TestTypedWake_ADetectorThatIsNotQuietHoldsTheWake(t *testing.T) {
	h := newPaneHostForTest(t)
	r := startLineReader(t, h, "wc")
	ctx := context.Background()

	err := NewTypedWake(h, "wc", emptyLine, holdDetector(""), holdDetector("a human is attached and typing")).Fire(ctx, "0123456789abcdef")
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrWakeHeld))
	assert.Contains(t, err.Error(), "a human is attached and typing", "the hold names the detector's reason")

	require.NoError(t, h.Input(ctx, "wc", []byte("probe\r")))
	waitFor(t, "the pane is alive", func() bool { return strings.Contains(r.text(), "GOT-[probe]") })
	assert.NotContains(t, r.text(), "wake")
}

func TestTypedWake_AnUnknownHarpIsNoPane(t *testing.T) {
	h := newPaneHostForTest(t)
	err := NewTypedWake(h, "nobody", emptyLine).Fire(context.Background(), "0123456789abcdef")
	assert.True(t, errors.Is(err, ErrNoPane), "got %v", err)
}
