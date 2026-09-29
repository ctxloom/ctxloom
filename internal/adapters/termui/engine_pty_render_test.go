//go:build !windows

package termui_test

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/adapters/cli/tui"
	"github.com/ctxloom/ctxloom/internal/adapters/operations"
	"github.com/ctxloom/ctxloom/internal/adapters/runner"
	"github.com/ctxloom/ctxloom/internal/adapters/termui"
	"github.com/ctxloom/ctxloom/internal/core/agent"
)

// overlay_render_test.go feeds the controller hand-written engine bytes. This
// file puts a REAL engine process behind it, hosted the way an interactive run
// hosts one (runner.RunLaunchSpec, on a pty of its own): the engine's size,
// its repaints and the overlay's repaint nudge all have to cross that pty as
// real window sizes and real SIGWINCHes before anything shows on screen.

// ptyEngineScript paints every row of the window it believes it has with a
// labelled line, leaving its cursor where the last row's text ends, and
// repaints on every SIGWINCH, counting each one in $WINCHLOG. With $ALT set it
// runs on the alternate screen, as a full-screen engine does. It exits once
// $STOP exists. The trap is installed before the first paint, so a test that
// waits for that paint has ordered every later resize after it.
const ptyEngineScript = `paint() {
  set -- $(stty size); i=1
  while [ "$i" -le "$1" ]; do printf '\033[%d;1H%s row %02d' "$i" "$LABEL" "$i"; i=$((i+1)); done
}
trap 'echo w >> "$WINCHLOG"; paint' WINCH
[ -n "$ALT" ] && printf '\033[?1049h'
paint
while [ ! -e "$STOP" ]; do sleep 0.02; done`

type ptyEngineHarness struct {
	*renderHarness
	winchLog string
	stop     string
	done     chan struct{}
	code     int32
	err      error
}

// newPTYEngineHarness composes the controller and the real overlay over a
// real terminal exactly as newRenderHarness does, and runs the engine behind
// it through runner.RunLaunchSpec: the controller's engine-bound input is the
// engine's stdin, its engine-output seam the engine's stdout, and its
// translated window sizes the engine's resize stream.
func newPTYEngineHarness(t *testing.T, label string, alt bool) *ptyEngineHarness {
	t.Helper()
	ptyDev, slave, tty := newComposedPTY(t)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	var mu sync.Mutex
	var watched []string
	rows := make([]tui.RosterRow, len(renderHarps))
	for i, h := range renderHarps {
		rows[i] = tui.RosterRow{Harp: h, State: "live"}
	}
	src := tui.Sources{
		Roster: func(context.Context) ([]tui.RosterRow, error) { return rows, nil },
		Watch: func(_ context.Context, h string) (*tui.Feed, error) {
			mu.Lock()
			watched = append(watched, h)
			mu.Unlock()
			return &tui.Feed{Source: "live", Events: make(chan operations.SessionFeedEvent), Errs: make(chan error, 1), Cancel: func() {}}, nil
		},
		Now: time.Now,
	}
	resize := make(chan *agent.WindowSize, 4)
	c := termui.New(termui.Options{
		Stdin: slave, TTY: slave, Resize: resize, Prefix: compPrefix, Surround: true,
		Bar:        termui.BarInfo{Harp: "self-session", Engine: "mock", PrefixHint: "^]"},
		NewOverlay: func() termui.Overlay { return tui.NewOverlay(ctx, src, compPrefix) },
	})
	t.Cleanup(c.Close)
	resize <- &agent.WindowSize{Rows: renderRows, Cols: renderCols}
	waitForComposition(t, "surround establish", func() bool { return strings.Contains(tty.String(), "\x1b[1;23r") })

	engineResize := make(chan agent.WindowSize, 8)
	go func() {
		defer close(engineResize)
		for ws := range c.Resize() {
			engineResize <- *ws
		}
	}()

	dir := t.TempDir()
	h := &ptyEngineHarness{
		renderHarness: &renderHarness{t: t, pty: ptyDev, tty: tty, c: c, watched: func() []string {
			mu.Lock()
			defer mu.Unlock()
			return slices.Clone(watched)
		}},
		winchLog: filepath.Join(dir, "winch.log"),
		stop:     filepath.Join(dir, "stop"),
		done:     make(chan struct{}),
	}
	env := append(os.Environ(), "LABEL="+label, "WINCHLOG="+h.winchLog, "STOP="+h.stop)
	if alt {
		env = append(env, "ALT=1")
	}
	spec := agent.LaunchSpec{BinaryPath: "/bin/sh", Args: []string{"-c", ptyEngineScript}, WorkDir: dir, Env: env, Interactive: true}
	go func() {
		defer close(h.done)
		h.code, h.err = runner.RunLaunchSpec(ctx, spec, c.Stdin(), c.Stdout(), io.Discard, engineResize)
	}()
	t.Cleanup(h.end)

	last := fmt.Sprintf("%s row %02d", label, renderRows-1)
	waitForComposition(t, "the engine's first paint", func() bool { return strings.Contains(tty.String(), last) })
	return h
}

// winches is how many SIGWINCHes the engine has handled.
func (h *ptyEngineHarness) winches() int {
	b, err := os.ReadFile(h.winchLog)
	if err != nil {
		return 0
	}
	return strings.Count(string(b), "w")
}

// end stops the engine and requires the launch to report its clean exit.
func (h *ptyEngineHarness) end() {
	select {
	case <-h.done:
	default:
		require.NoError(h.t, os.WriteFile(h.stop, nil, 0o600))
		select {
		case <-h.done:
		case <-time.After(20 * time.Second):
			h.t.Fatal("the engine never exited")
		}
	}
	require.NoError(h.t, h.err)
	assert.Equal(h.t, int32(0), h.code)
}

// A real engine on its own pty: the overlay paints as a panel over it, and
// closing the overlay hands back the engine's screen exactly — and the
// repaint nudge reaches the engine as a real SIGWINCH.
func TestEnginePTYRender_OverlayOverARealEngineReleasesToItsScreen(t *testing.T) {
	h := newPTYEngineHarness(t, "engine", false)
	assertEngineScreenBack(t, h.settle(), "engine")
	require.Zero(t, h.winches(), "nothing has resized the engine yet")

	h.key(string([]byte{compPrefix}))
	waitForComposition(t, "the first roster row's feed opened", func() bool { return slices.Contains(h.watched(), renderHarps[0]) })
	assertPanel(t, h.settle(), 15, 22)

	h.key("q")
	waitForComposition(t, "the repaint nudge to reach the engine", func() bool { return h.winches() > 0 })
	assertEngineScreenBack(t, h.settle(), "engine")
}

// A full-screen engine is drawn over in place, and what the panel covered is
// cleared on release: only the ENGINE can put it back, by repainting when the
// nudge's window sizes reach its pty as a SIGWINCH. So the rows under the
// panel are back if and only if the nudge crossed the pty.
func TestEnginePTYRender_AltScreenEngineRepaintsUnderThePanelOnTheNudge(t *testing.T) {
	h := newPTYEngineHarness(t, "fullscreen", true)

	h.key(string([]byte{compPrefix}))
	waitForComposition(t, "the first roster row's feed opened", func() bool { return slices.Contains(h.watched(), renderHarps[0]) })
	e := h.settle()
	assert.True(t, e.OnAltScreen(), "still the engine's screen")
	assertPanel(t, e, 15, 22)

	h.key("q")
	waitForComposition(t, "the repaint nudge to reach the engine", func() bool { return h.winches() > 0 })
	e = h.settle()
	assert.True(t, e.OnAltScreen(), "release must not take the engine off its own screen")
	for i := 1; i < renderRows; i++ {
		assert.Equal(t, fmt.Sprintf("fullscreen row %02d", i), e.Row(i-1), "row %d is the engine's again:\n%s", i, e)
	}
	assert.Contains(t, e.Row(renderRows-1), "viewer", "the bar is back on the reserved row")
}
