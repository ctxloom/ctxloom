//go:build !windows

package termui_test

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/adapters/cli/tui"
	"github.com/ctxloom/ctxloom/internal/adapters/runner"
	"github.com/ctxloom/ctxloom/internal/adapters/termui"
	"github.com/ctxloom/ctxloom/internal/core/agent"
	"github.com/ctxloom/ctxloom/internal/testsupport/vtemu"
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
// $STOP exists, never with a repaint still owed. The trap is installed before
// the first paint, so a test that waits for that paint has ordered every later
// resize after it.
//
// The trap only marks a repaint owed; the main loop paints. A trap that
// painted itself could run nested inside a paint that had already read the
// size (dash runs a trap at the next command boundary, even inside another
// trap's), and that outer paint then finished at the stale size after the
// nested one: the nudge's shrink repaint outliving its restore, leaving the
// engine's last paint a row short
// (TestPTYEngineScript_ASIGWINCHMidRepaintNeverLeavesAStaleSizeLast).
// $PAINTHOOK runs between a paint's size read and its rows, with the size in
// $1; it is empty except where a test holds a paint there.
const ptyEngineScript = `paint() {
  set -- $(stty size); eval "$PAINTHOOK"; i=1
  while [ "$i" -le "$1" ]; do printf '\033[%d;1H%s row %02d' "$i" "$LABEL" "$i"; i=$((i+1)); done
}
trap 'echo w >> "$WINCHLOG"; owed=1' WINCH
[ -n "$ALT" ] && printf '\033[?1049h'
owed=1
while :; do
  if [ -n "$owed" ]; then owed=; paint
  elif [ -e "$STOP" ]; then break
  else sleep 0.02; fi
done`

type ptyEngineHarness struct {
	*renderHarness
	label    string
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
func newPTYEngineHarness(t *testing.T, label string, alt bool, opts ...func(*termui.Options)) *ptyEngineHarness {
	t.Helper()
	ptyDev, slave, tty := newComposedPTY(t)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	src, watches := renderSources()
	resize := make(chan *agent.WindowSize, 4)
	o := termui.Options{
		Stdin: slave, TTY: slave, Resize: resize, Prefix: compPrefix, Surround: true,
		Bar:        termui.BarInfo{Harp: "self-session", Engine: "mock", PrefixHint: "^]"},
		NewOverlay: func(start termui.OverlayStart) termui.Overlay { return tui.NewOverlay(ctx, src, compPrefix, start) },
	}
	for _, f := range opts {
		f(&o)
	}
	c := termui.New(o)
	t.Cleanup(c.Close)
	resize <- &agent.WindowSize{Rows: renderRows, Cols: renderCols}
	tty.waitUntil(t, "the surround's region", contains("\x1b[1;23r"))

	engineResize := make(chan agent.WindowSize, 8)
	go func() {
		defer close(engineResize)
		for ws := range c.Resize() {
			engineResize <- *ws
		}
	}()

	dir := t.TempDir()
	h := &ptyEngineHarness{
		renderHarness: &renderHarness{t: t, pty: ptyDev, tty: tty, c: c, watches: watches, slave: slave},
		label:         label,
		winchLog:      filepath.Join(dir, "winch.log"),
		stop:          filepath.Join(dir, "stop"),
		done:          make(chan struct{}),
	}
	env := append(os.Environ(), "PAINTHOOK=", "LABEL="+label, "WINCHLOG="+h.winchLog, "STOP="+h.stop)
	if alt {
		env = append(env, "ALT=1")
	}
	spec := agent.LaunchSpec{BinaryPath: "/bin/sh", Args: []string{"-c", ptyEngineScript}, WorkDir: dir, Env: env, Interactive: true}
	go func() {
		defer close(h.done)
		h.code, h.err = runner.RunLaunchSpec(ctx, spec, c.Stdin(), c.Stdout(), io.Discard, engineResize)
	}()
	t.Cleanup(h.end)

	tty.waitUntil(t, "the engine's first paint", contains(fmt.Sprintf("%s row %02d", label, renderRows-1)))
	return h
}

// repainted waits for the engine's repaint at its full size in what arrived
// after since, and requires the SIGWINCH behind it. The script logs a
// SIGWINCH before it repaints, and nothing else paints the engine's last row
// after a release, so the repaint arriving orders the log entry before it.
func (h *ptyEngineHarness) repainted(since int) {
	h.t.Helper()
	last := fmt.Sprintf("%s row %02d", h.label, renderRows-1)
	h.tty.waitUntil(h.t, "the engine's repaint on the nudge", func(s string) bool {
		return len(s) > since && strings.Contains(s[since:], last)
	})
	require.Positive(h.t, h.winches(), "the nudge reached the engine as a SIGWINCH")
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
		await(h.t, "the engine's exit", h.done)
	}
	require.NoError(h.t, h.err)
	assert.Equal(h.t, int32(0), h.code)
}

// A real engine on its own pty: the overlay paints as a panel over it, and
// closing the overlay hands back the engine's screen exactly — and the
// repaint nudge reaches the engine as a real SIGWINCH.
func TestEnginePTYRender_OverlayOverARealEngineReleasesToItsScreen(t *testing.T) {
	h := newPTYEngineHarness(t, "engine", false)
	h.screenWhen("the engine's screen", engineBack("engine"))
	require.Zero(t, h.winches(), "nothing has resized the engine yet")

	h.key(string([]byte{compPrefix}))
	awaitWatch(t, h.watches, renderHarps[0])
	h.screenWhen("the panel", panel(15, 22))

	since := len(h.tty.String())
	h.key("q")
	h.repainted(since)
	h.screenWhen("the engine's screen back", engineBack("engine"))
}

// A full-screen engine is drawn over in place, and what the panel covered is
// cleared on release: only the ENGINE can put it back, by repainting when the
// nudge's window sizes reach its pty as a SIGWINCH. So the rows under the
// panel are back if and only if the nudge crossed the pty.
func TestEnginePTYRender_AltScreenEngineRepaintsUnderThePanelOnTheNudge(t *testing.T) {
	h := newPTYEngineHarness(t, "fullscreen", true)

	h.key(string([]byte{compPrefix}))
	awaitWatch(t, h.watches, renderHarps[0])
	h.screenWhen("the panel over the engine", func(t tb, e *vtemu.Screen) {
		assert.True(t, e.OnAltScreen(), "still the engine's screen")
		assertPanel(t, e, 15, 22)
	})

	since := len(h.tty.String())
	h.key("q")
	h.repainted(since)
	h.screenWhen("the engine's repainted screen", fullscreenBack)
}

// fullscreenBack is a screenWhen check: a full-screen engine has its own
// screen again, every row repainted, with the bar back.
func fullscreenBack(t tb, e *vtemu.Screen) {
	t.Helper()
	assert.True(t, e.OnAltScreen(), "the engine stays on its own screen")
	for i := 1; i < renderRows; i++ {
		assert.Equal(t, fmt.Sprintf("fullscreen row %02d", i), e.Row(i-1), "row %d is the engine's again, and nothing of the overlay survives:\n%s", i, e)
	}
	assert.Contains(t, e.Row(renderRows-1), "viewer", "the bar is back on the reserved row")
}

// A SIGWINCH that lands while the engine is mid-repaint — its size already
// read, its rows not yet painted — must still leave the engine's last paint at
// the size the window has now. The nudge's wiggle makes exactly this window:
// the shrink's repaint reads the shrunk size, and the restore's SIGWINCH can
// arrive before that repaint has drawn a row. PAINTHOOK holds the shrink's
// repaint right after its size read until the restore has been applied (a
// TIOCSWINSZ queues its SIGWINCH before it returns), so the interleaving is
// forced, not hoped for.
func TestPTYEngineScript_ASIGWINCHMidRepaintNeverLeavesAStaleSizeLast(t *testing.T) {
	p, slave, tty := newComposedPTY(t)
	full := renderRows - 1
	require.NoError(t, p.Resize(renderCols, full))
	dir := t.TempDir()
	reached, gate := filepath.Join(dir, "reached"), filepath.Join(dir, "gate")
	require.NoError(t, syscall.Mkfifo(reached, 0o600))
	require.NoError(t, syscall.Mkfifo(gate, 0o600))
	stop := filepath.Join(dir, "stop")
	hook := fmt.Sprintf(`[ "$1" -lt %d ] && { echo > "$REACHED"; cat "$GATE" > /dev/null; }`, full)

	cmd := exec.Command("/bin/sh", "-c", ptyEngineScript)
	cmd.Env = append(os.Environ(), "LABEL=engine", "WINCHLOG="+filepath.Join(dir, "winch.log"), "STOP="+stop,
		"PAINTHOOK="+hook, "REACHED="+reached, "GATE="+gate)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = slave, slave, slave
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true, Setctty: true}
	require.NoError(t, cmd.Start())
	exited := make(chan error, 1)
	go func() { exited <- cmd.Wait() }()
	t.Cleanup(func() { _ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) })
	tty.waitUntil(t, "the first paint", contains(fmt.Sprintf("engine row %02d", full)))

	// The shrink: its repaint reads the shrunk size and stops in the hook.
	require.NoError(t, p.Resize(renderCols, full-1))
	await(t, "the shrink's repaint holding after its size read", fifoDrained(reached))
	// The restore, applied while that repaint holds: its SIGWINCH is queued
	// before Resize returns.
	require.NoError(t, p.Resize(renderCols, full))
	await(t, "the held repaint released", fifoFed(gate))
	require.NoError(t, os.WriteFile(stop, nil, 0o600))
	require.NoError(t, await(t, "the engine's exit", exited))

	// A fence written behind the engine's last byte orders all of it first.
	fence := "\x1b[?7799$p"
	_, err := io.WriteString(slave, fence)
	require.NoError(t, err)
	tty.waitUntil(t, "the fence behind the engine", contains(fence))

	e := emulate(tty.String())
	r, c := e.Cursor()
	assert.Equal(t, [2]int{full - 1, len("engine row 23")}, [2]int{r, c},
		"the engine's last paint is at the window's current size, its cursor after row %d:\n%s", full, e)
}

// fifoDrained opens the fifo at path for reading and reports once its writer
// has opened, written and closed it.
func fifoDrained(path string) <-chan struct{} {
	done := make(chan struct{})
	go func() {
		if f, err := os.Open(path); err == nil {
			_, _ = io.Copy(io.Discard, f)
			_ = f.Close()
		}
		close(done)
	}()
	return done
}

// fifoFed opens the fifo at path for writing and closes it at once, reporting
// once its reader has opened it (and so will see EOF).
func fifoFed(path string) <-chan struct{} {
	done := make(chan struct{})
	go func() {
		if f, err := os.OpenFile(path, os.O_WRONLY, 0); err == nil {
			_ = f.Close()
		}
		close(done)
	}()
	return done
}
