package tui

import (
	"bytes"
	"context"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/adapters/termemu"
	"github.com/ctxloom/ctxloom/internal/core/coord"
	"github.com/ctxloom/ctxloom/internal/testsupport"
)

// These tests run the real program: StartSplit over a pipe for the human's
// input and a buffer for the human's terminal. Every wait is on an event —
// a write to the engine or to the terminal — re-checked as it happens.

// outTerm is the human's terminal: it keeps everything the program writes.
type outTerm struct {
	mu   sync.Mutex
	buf  bytes.Buffer
	wake chan struct{}
}

func newOutTerm() *outTerm { return &outTerm{wake: make(chan struct{})} }

func (o *outTerm) Write(p []byte) (int, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	close(o.wake)
	o.wake = make(chan struct{})
	return o.buf.Write(p)
}

func (o *outTerm) bytes() []byte {
	o.mu.Lock()
	defer o.mu.Unlock()
	return append([]byte(nil), o.buf.Bytes()...)
}

// waitUntil blocks until what has been written satisfies cond. A frame the
// program owes arrives within its frame rate, so the wait is bounded well
// inside the test binary's deadline: a wrong frame fails this test, not
// every test after it.
func (o *outTerm) waitUntil(t *testing.T, what string, cond func([]byte) bool) {
	t.Helper()
	bound := time.After(min(testsupport.Budget(t), 30*time.Second))
	for {
		o.mu.Lock()
		got, wake := append([]byte(nil), o.buf.Bytes()...), o.wake
		o.mu.Unlock()
		if cond(got) {
			return
		}
		select {
		case <-wake:
		case <-bound:
			t.Fatalf("the terminal never showed %s", what)
		}
	}
}

// screenOf replays the program's output on a reference terminal. bubbletea
// writes to a non-tty as to a cooked terminal, relying on ONLCR; the
// reference is raw, so the mapping is done here.
func screenOf(out []byte, w, h int) termemu.Screen {
	ref := termemu.NewXVT(w, h, io.Discard, termemu.Callbacks{})
	defer func() { _ = ref.Close() }()
	_, _ = ref.Write(bytes.ReplaceAll(out, []byte("\n"), []byte("\r\n")))
	return ref.Snapshot()
}

type programSplit struct {
	v      *SplitView
	in     *io.PipeWriter
	out    *outTerm
	engine *engineSink
	w, h   int
}

func startProgram(t *testing.T, port *splitPort, w, h int) *programSplit {
	t.Helper()
	return startProgramCtx(context.Background(), t, port, w, h, "")
}

// startProgramCtx is startProgram under ctx, with epilogue as the line the
// split view leaves after its final frame.
func startProgramCtx(ctx context.Context, t *testing.T, port *splitPort, w, h int, epilogue string) *programSplit {
	t.Helper()
	pr, pw := io.Pipe()
	p := &programSplit{in: pw, out: newOutTerm(), engine: newEngineSink(), w: w, h: h}
	prefix, err := ParsePrefixKey("ctrl-]")
	require.NoError(t, err)
	o := SplitOptions{Self: selfHarp, Term: p.engine, Prefix: prefix, In: pr, Out: p.out, Header: Header{Harp: selfHarp}, WindowSize: [2]int{w, h}, Epilogue: epilogue}
	if port != nil {
		o.Port = port.port()
	}
	p.v, err = StartSplit(ctx, o)
	require.NoError(t, err)
	t.Cleanup(func() {
		p.v.Close()
		_ = pw.Close()
	})
	p.engine.waitSized(t, 1)
	return p
}

// paneScreen is the engine's emulated screen.
func (p *programSplit) paneScreen() termemu.Screen { return p.v.pane.Snapshot() }

// waitPaneShown blocks until the human's terminal shows the pane's screen to
// the right of the divider on every row.
func (p *programSplit) waitPaneShown(t *testing.T) {
	t.Helper()
	want := p.paneScreen().Text()
	var last []string
	defer func() {
		if t.Failed() {
			t.Logf("pane:\n%s\nterminal:\n%s", strings.Join(want, "\n"), strings.Join(last, "\n"))
		}
	}()
	p.out.waitUntil(t, "the pane's screen", func(out []byte) bool {
		last = screenOf(out, p.w, p.h).Text()
		for y := range p.h {
			parts := strings.SplitN(last[y], "│", 2)
			if len(parts) != 2 || strings.TrimRight(parts[1], " ") != want[y] {
				return false
			}
		}
		return true
	})
}

// TestSplitView_InputReachesChild: bytes from the human's terminal, decoded
// by bubbletea, routed by the split view and re-encoded by the pane, reach
// the engine in the protocol it asked for. The engine here has pushed kitty
// flags 5, SGR mouse and bracketed paste, as claude does.
func TestSplitView_InputReachesChild(t *testing.T) {
	cases := []struct {
		name, user, child string
	}{
		{"shift+enter (kitty from the user's terminal)", "\x1b[13;2u", "\x1b[13;2u"},
		{"shift+enter (modifyOtherKeys from the user's terminal)", "\x1b[27;2;13~", "\x1b[13;2u"},
		{"ctrl+c", "\x03", "\x1b[99;5u"},
		{"esc (kitty)", "\x1b[27u", "\x1b[27u"},
		{"esc (legacy lone ESC)", "\x1b", "\x1b[27u"},
		{"bracketed paste", "\x1b[200~one\ntwo\x1b[201~", "\x1b[200~one\ntwo\x1b[201~"},
		// The human's column 40 is pane column 40-27=13 (1-based in SGR).
		{"wheel up", "\x1b[<64;40;5M", "\x1b[<64;13;5M"},
		{"wheel down", "\x1b[<65;40;5M", "\x1b[<65;13;5M"},
		{"click", "\x1b[<0;40;5M\x1b[<0;40;5m", "\x1b[<0;13;5M\x1b[<0;13;5m"},
		{"click on the panel goes nowhere", "\x1b[<0;3;5M\x1b[<0;3;5m", ""},
		{"text", "hi", "hi"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := startProgram(t, nil, rosterPaneWidth+1+80, 24)
			_, _ = p.v.Pane().Write([]byte("\x1b[>5u\x1b[?1002h\x1b[?1006h\x1b[?2004h"))
			// A known key after the input, then a reply through the
			// emulator's own pipe: everything the input produced — direct
			// writes and piped ones alike — is in front of both.
			_, _ = p.in.Write([]byte(tc.user))
			if tc.child != "" {
				// Before the barrier key: a lone ESC followed at once by z
				// is alt+z, on any terminal.
				p.engine.waitFor(t, tc.child)
			}
			_, _ = p.in.Write([]byte("z"))
			p.engine.waitFor(t, "z")
			_, _ = p.v.Pane().Write([]byte("\x1b[c"))
			p.engine.waitForMatch(t, daReply)
			got := daReply.ReplaceAllString(p.engine.String(), "")
			require.True(t, strings.HasSuffix(got, "z"), "%q", got)
			assert.Equal(t, tc.child, strings.TrimSuffix(got, "z"))
		})
	}
}

// TestSplitView_ThroughBubbletea_MatchesPane: the engine's screen in the
// pane, composed beside the panel by the real program and replayed on a
// reference terminal — the right-hand side of every row is exactly the
// pane's screen, wide graphemes included, also after bubbletea's diff
// repaint of an update to the right of a VS16 emoji.
func TestSplitView_ThroughBubbletea_MatchesPane(t *testing.T) {
	p := startProgram(t, nil, 110, 16)
	_, _ = p.v.Pane().Write([]byte("\x1b[1;34m╭─ table ─╮\x1b[0m\r\n" +
		"│ 日本語 │ 한글 │\r\n" +
		"│ sun ☀️ │ ok ✅ │\r\n" +
		"\x1b[7mreverse\x1b[0m \x1b[4munder\x1b[0m éx\r\n"))
	p.waitPaneShown(t)
	_, _ = p.v.Pane().Write([]byte("\x1b[3;14HSUN"))
	require.Contains(t, p.paneScreen().Text()[2], "SUN")
	p.waitPaneShown(t)
	assert.Contains(t, string(p.out.bytes()), "\x1b[?2027h", "the renderer is forced to grapheme widths (mode 2027)")
}

// The panel through the real program: the roster arrives over the port, and
// the prefix hands the tree focus.
func TestSplitView_PanelAndFocusThroughTheProgram(t *testing.T) {
	port := newSplitPort()
	port.child("brave-heron", "coder", selfHarp, coord.StateExecuting)
	p := startProgram(t, port, 110, 16)
	p.out.waitUntil(t, "the child in the tree", func(out []byte) bool {
		return strings.Contains(strings.Join(screenOf(out, p.w, p.h).Text(), "\n"), "brave-heron·coder")
	})
	_, _ = p.in.Write([]byte("\x1d"))
	p.out.waitUntil(t, "the tree's key hints", func(out []byte) bool {
		return strings.Contains(strings.Join(screenOf(out, p.w, p.h).Text(), "\n"), "j/k move")
	})
	err := p.v.Summon(context.Background(), "approvals", "approval from brave-heron")
	assert.ErrorIs(t, err, ErrEngaged, "the human is using the tree")
	p.out.waitUntil(t, "the notice", func(out []byte) bool {
		return strings.Contains(strings.Join(screenOf(out, p.w, p.h).Text(), "\n"), "approval from")
	})
}

func TestSplitView_SummonShowsAndHonoursItsContext(t *testing.T) {
	port := newSplitPort()
	port.queue = newFakeQueue(bashReq("a1", "brave-heron", "ls", time.Hour))
	p := startProgram(t, port, 110, 30)
	require.NoError(t, p.v.Summon(context.Background(), "approvals", "approval from brave-heron"), "no input yet: quiet")
	p.out.waitUntil(t, "the approval", func(out []byte) bool {
		return strings.Contains(strings.Join(screenOf(out, p.w, p.h).Text(), "\n"), "keys here never reach the engine")
	})
	assert.ErrorContains(t, p.v.Summon(context.Background(), "palette", "x"), `no view "palette"`, "only the approvals view is summoned")

	q := startProgram(t, newSplitPort(), 110, 30)
	_, _ = q.in.Write([]byte("typing"))
	q.engine.waitFor(t, "typing")
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- q.v.Summon(ctx, "approvals", "n") }()
	cancel()
	assert.ErrorIs(t, <-done, context.Canceled)
}

func TestSplitView_CloseEndsItAndRefusesAfter(t *testing.T) {
	p := startProgram(t, nil, 110, 16)
	assert.True(t, p.v.Ring())
	p.out.waitUntil(t, "the bell", func(out []byte) bool { return bytes.Contains(out, []byte("\a")) })
	p.v.SetApprovals(1, time.Time{}, false)
	p.v.Note("hello", time.Minute)
	p.out.waitUntil(t, "the note and the count", func(out []byte) bool {
		text := strings.Join(screenOf(out, p.w, p.h).Text(), "\n")
		return strings.Contains(text, "hello") && strings.Contains(text, "⚑ 1 approval")
	})
	p.v.Close()
	select {
	case err := <-p.v.Done():
		assert.NoError(t, err, "a closed split view ends cleanly")
	default:
		t.Fatal("Close returned before the program had stopped")
	}
	assert.False(t, p.v.Ring(), "nothing rings once it has stopped")
	assert.ErrorIs(t, p.v.Summon(context.Background(), "approvals", "n"), ErrUnavailable)
	p.v.SetApprovals(2, time.Time{}, true) // returns: the program is gone
	p.v.Close()                            // twice is harmless
}

// mainScreen is what the human's terminal shows once the split view has
// left the alternate screen: everything written after the last 1049l,
// replayed on a fresh reference terminal.
func mainScreen(t *testing.T, out []byte, w, h int) []string {
	t.Helper()
	i := bytes.LastIndex(out, []byte("\x1b[?1049l"))
	require.GreaterOrEqual(t, i, 0, "the split view left the alternate screen")
	return screenOf(out[i:], w, h).Text()
}

// Closing leaves the pane's final frame on the main screen, and the
// epilogue on the line after it: the shell's scrollback keeps the last
// thing the engine showed, not the panel.
func TestSplitView_CloseLeavesTheFinalFrameAndTheEpilogue(t *testing.T) {
	const epilogue = "full session transcript: /x/transcript.jsonl"
	p := startProgramCtx(context.Background(), t, nil, 110, 16, epilogue)
	_, _ = p.v.Pane().Write([]byte("\x1b[1;34mfirst row\x1b[0m\r\nsecond row\r\nlast row"))
	p.waitPaneShown(t)
	p.v.Close()

	got := mainScreen(t, p.out.bytes(), p.w, p.h)
	assert.Equal(t, []string{"first row", "second row", "last row", epilogue}, got[:4],
		"the frame's rows, its trailing blank rows dropped, then the epilogue")
	for _, row := range got[4:] {
		assert.Empty(t, row, "nothing follows the epilogue")
	}
	assert.NotContains(t, strings.Join(got, "\n"), "agents", "the panel is not left behind")
	i := bytes.LastIndex(p.out.bytes(), []byte("\x1b[?1049l"))
	assert.Contains(t, string(p.out.bytes()[i:]), "\x1b[34;1mfirst row\x1b[m\r\n", "the frame keeps its styles, and no padding")

	assert.True(t, strings.HasPrefix(finalFrame(p.v.pane.Frame(), epilogue), "\r"),
		"the frame starts in column 0 wherever the main screen's cursor stood")

	before := len(p.out.bytes())
	p.v.Close()
	assert.Len(t, p.out.bytes(), before, "a second Close prints nothing more")
}

// A run ended by its context (a signal) stops the program on its own; the
// Close that unwinds it still leaves the final frame.
func TestSplitView_CloseAfterTheContextEndedLeavesTheFinalFrame(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	p := startProgramCtx(ctx, t, nil, 110, 16, "")
	_, _ = p.v.Pane().Write([]byte("still here"))
	p.waitPaneShown(t)
	cancel()
	<-p.v.Done()
	p.v.Close()

	got := mainScreen(t, p.out.bytes(), p.w, p.h)
	assert.Equal(t, "still here", got[0])
	for _, row := range got[1:] {
		assert.Empty(t, row, "no epilogue was asked for")
	}
}

// A summon withdrawn by its context never shows later: once the human has
// been quiet long enough, the next summon is the one shown.
func TestSplitView_AWithdrawnSummonIsForgotten(t *testing.T) {
	port := newSplitPort()
	port.queue = newFakeQueue(bashReq("a1", "brave-heron", "ls", time.Hour))
	p := startProgram(t, port, 110, 30)
	_, _ = p.in.Write([]byte("typing"))
	p.engine.waitFor(t, "typing")
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- p.v.Summon(ctx, "approvals", "first") }()
	cancel()
	require.ErrorIs(t, <-done, context.Canceled)
	assert.NoError(t, p.v.Summon(context.Background(), "approvals", "second"), "the withdrawn one did not take the view first")
}

func TestStartSplit_FirstPaneSize(t *testing.T) {
	var got [2]int
	prefix, err := ParsePrefixKey("ctrl-]")
	require.NoError(t, err)
	pr, pw := io.Pipe()
	defer func() { _ = pw.Close() }()
	v, err := StartSplit(context.Background(), SplitOptions{
		Self: selfHarp, Term: newEngineSink(), Prefix: prefix, In: pr, Out: newOutTerm(), WindowSize: [2]int{100, 20},
		NewPane: func(cols, rows int, toChild io.Writer, cb termemu.Callbacks) termemu.Terminal {
			got = [2]int{cols, rows}
			return termemu.NewXVT(cols, rows, toChild, cb)
		},
	})
	require.NoError(t, err)
	defer v.Close()
	assert.Equal(t, [2]int{80, 24}, got, "the emulator starts at 80x24 until the engine is sized")
}

func TestStartSplit_RefusesWithoutATerminal(t *testing.T) {
	_, err := StartSplit(context.Background(), SplitOptions{Out: io.Discard})
	assert.Error(t, err)
	_, err = StartSplit(context.Background(), SplitOptions{Term: newEngineSink()})
	assert.Error(t, err)
}

// A cancelled context ends the program with it.
func TestSplitView_ContextEndsIt(t *testing.T) {
	pr, pw := io.Pipe()
	defer func() { _ = pw.Close() }()
	ctx, cancel := context.WithCancel(context.Background())
	v, err := StartSplit(ctx, SplitOptions{Self: selfHarp, Term: newEngineSink(), In: pr, Out: newOutTerm()})
	require.NoError(t, err)
	cancel()
	assert.Error(t, <-v.Done(), "a killed program says so")
	v.Close()
}

// stalledPane is an emulator whose frame pump never fires: what the engine
// wrote is committed, but the program is never told.
type stalledPane struct{ termemu.Terminal }

func (stalledPane) Ready() <-chan struct{} { return make(chan struct{}) }

// TestSplitView_CloseDrawsTheEnginesLastFrame: the engine's last output is
// on the human's terminal when the split view closes, though no frame was
// adopted for it — the drive closes the split view only once the engine's
// output is all in the pane.
func TestSplitView_CloseDrawsTheEnginesLastFrame(t *testing.T) {
	pr, pw := io.Pipe()
	t.Cleanup(func() { _ = pw.Close() })
	out, engine := newOutTerm(), newEngineSink()
	prefix, err := ParsePrefixKey("ctrl-]")
	require.NoError(t, err)
	v, err := StartSplit(context.Background(), SplitOptions{
		Self: selfHarp, Term: engine, Prefix: prefix, In: pr, Out: out, WindowSize: [2]int{100, 10},
		NewPane: func(cols, rows int, toChild io.Writer, cb termemu.Callbacks) termemu.Terminal {
			return stalledPane{termemu.NewXVT(cols, rows, toChild, cb)}
		},
	})
	require.NoError(t, err)
	engine.waitSized(t, 1)
	_, err = v.Pane().Write([]byte("the engine's last words"))
	require.NoError(t, err)

	v.Close()

	got := out.bytes()
	drawn := got[:max(bytes.LastIndex(got, []byte("\x1b[?1049l")), 0)]
	assert.Contains(t, strings.Join(screenOf(drawn, 100, 10).Text(), "\n"), "the engine's last words")
}
