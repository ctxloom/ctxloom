package termemu

import (
	"bytes"
	"encoding/base64"
	"errors"
	"image/color"
	"io"
	"strconv"
	"sync"
	"time"

	uv "github.com/charmbracelet/ultraviolet"
	"github.com/charmbracelet/x/ansi"
	"github.com/charmbracelet/x/ansi/parser"
	"github.com/charmbracelet/x/vt"
)

// modeSyncOutput is DEC private mode 2026, synchronized output: the child
// brackets each frame in CSI ? 2026 h ... CSI ? 2026 l and a terminal shows
// nothing of the frame until the closing sequence. x/vt does not implement
// it, but reports every DEC mode change through its EnableMode/DisableMode
// callbacks, which is all the gate needs.
var modeSyncOutput = ansi.DECMode(2026)

// SyncTimeout bounds how long a child may hold a synchronized frame open
// before the screen is shown anyway, as real terminals do (ghostty and kitty
// use about a second), so a child that dies mid-frame cannot freeze the pane.
const SyncTimeout = time.Second

// init patches x/ansi's PROCESS-GLOBAL parser transition table (x#848, open
// upstream): in OSC and DCS strings the table treats byte 0x9C as the 8-bit
// String Terminator, but in a UTF-8 stream 0x9C is a continuation byte. Every
// Dingbat U+2700-U+273F carries it, and claude puts one in its OSC 0 window
// title on every spinner tick (✳ is E2 9C B3), so unpatched, the rest of the
// title is printed into the screen at the cursor. A UTF-8 terminal never
// honours 8-bit C1 controls inside a string; make 0x9C payload. The same
// table drives lipgloss/x/ansi width measurement, which gains the same fix.
//
// This is the one patch to the dependency set; upstream x#848 is the real
// fix, after which this block is deleted.
func init() {
	for _, st := range []parser.State{parser.OscStringState, parser.DcsStringState} {
		parser.Table.AddOne(0x9C, st, parser.PutAction, st)
	}
}

// XVT is the charmbracelet/x/vt implementation of Terminal.
type XVT struct {
	mu   sync.Mutex
	emu  *vt.Emulator
	keys keyState
	cb   Callbacks
	// mouseModes are the child's enabled mouse tracking modes; the highest
	// one is its tracking.
	mouseModes map[ansi.Mode]bool
	mouse      MouseTracking

	// toChild is the child's input. Only the reply drain writes it: every
	// byte bound for the child, the emulator's replies and encoded input
	// alike, crosses the emulator's pipe first, in the order it was sent.
	toChild io.Writer

	syncing     bool
	syncSince   time.Time
	syncTimeout time.Duration
	now         func() time.Time // the sync timeout's clock
	// frame is the last materialised frame; seq counts commits. A commit
	// only marks the screen dirty: copying its cells is deferred to the next
	// Frame call, or to the moment a synchronized frame opens (the last
	// instant the pre-frame screen exists).
	frame     Frame
	seq       uint64
	dirty     bool
	ready     chan struct{}
	altScreen bool
	cursorOn  bool
	drained   chan struct{}
}

var _ Terminal = (*XVT)(nil)

// NewXVT returns a cols x rows pane whose replies and encoded input are
// written to toChild (the child pty's master), and which reports what the
// child asks of the terminal beyond its screen through cb.
func NewXVT(cols, rows int, toChild io.Writer, cb Callbacks) *XVT {
	t := &XVT{
		emu:         vt.NewEmulator(cols, rows),
		cb:          cb,
		mouseModes:  map[ansi.Mode]bool{},
		toChild:     toChild,
		ready:       make(chan struct{}, 1),
		drained:     make(chan struct{}),
		cursorOn:    true,
		syncTimeout: SyncTimeout,
		now:         time.Now,
	}
	t.emu.SetCallbacks(vt.Callbacks{
		EnableMode:       func(m ansi.Mode) { t.modeChanged(m, true) },
		DisableMode:      func(m ansi.Mode) { t.modeChanged(m, false) },
		AltScreen:        func(on bool) { t.altScreen = on },
		CursorVisibility: func(on bool) { t.cursorOn = on },
		Title:            cb.Title,
		Bell:             cb.Bell,
	})
	t.registerHandlers()
	// x#939: the emulator answers queries (DA, DECRQM, CPR, OSC 10/11) by
	// writing to an unbuffered io.Pipe, so Write blocks forever unless
	// something reads the other end. Drain it for the pane's whole life.
	go t.drain()
	t.commit()
	return t
}

// drainSource is what the reply drain reads the emulator's pipe through:
// the pipe itself. Tests wrap it to make the drain lag.
var drainSource = func(r io.Reader) io.Reader { return r }

func (t *XVT) drain() {
	defer close(t.drained)
	_, _ = io.Copy(childWriter{t.toChild}, drainSource(t.emu))
}

// childWriter is the drain's side of the child's input. A write the child
// refuses is dropped rather than ending the drain: undrained, the emulator's
// next reply blocks its Write forever.
type childWriter struct{ w io.Writer }

func (c childWriter) Write(p []byte) (int, error) {
	_, _ = c.w.Write(p)
	return len(p), nil
}

// handled wraps a handler that takes the sequence whole: x/vt's default
// handler for it, if any, never runs.
func handled(f func(params ansi.Params)) vt.CsiHandler {
	return func(params ansi.Params) bool {
		f(params)
		return true
	}
}

// registerHandlers adds the adapter's handlers in front of x/vt's defaults
// (x/vt calls the last-registered handler first; returning false falls
// through to the next).
func (t *XVT) registerHandlers() {
	// x#974: DECSTBM and DECSLRM margins past the screen are accepted, and the
	// next scroll inside the region panics in ultraviolet's InsertLineArea.
	// A child still drawing for its pre-resize height sends exactly that.
	// Clamp the parameters in place (the slice aliases the parser's own
	// parameter buffer, which the default handler re-reads), as xterm does.
	t.emu.RegisterCsiHandler('r', func(params ansi.Params) bool {
		clampParam(params, 1, t.emu.Height())
		return false
	})
	t.emu.RegisterCsiHandler('s', func(params ansi.Params) bool {
		clampParam(params, 1, t.emu.Width())
		return false
	})

	// Kitty keyboard protocol state, which x/vt ignores: query, push, pop,
	// set. Answering the query is what makes claude push its flags.
	t.emu.RegisterCsiHandler(ansi.Command('?', 0, 'u'), handled(func(ansi.Params) {
		_, _ = io.WriteString(t.emu.InputPipe(), "\x1b[?"+strconv.Itoa(t.keys.kittyFlags())+"u")
	}))
	t.emu.RegisterCsiHandler(ansi.Command('>', 0, 'u'), handled(func(params ansi.Params) {
		f, _, _ := params.Param(0, 0)
		t.keys.kittyStack = append(t.keys.kittyStack, f)
	}))
	t.emu.RegisterCsiHandler(ansi.Command('<', 0, 'u'), handled(func(params ansi.Params) {
		n, _, _ := params.Param(0, 1)
		t.keys.kittyStack = t.keys.kittyStack[:max(0, len(t.keys.kittyStack)-n)]
	}))
	t.emu.RegisterCsiHandler(ansi.Command('=', 0, 'u'), handled(func(params ansi.Params) {
		f, _, _ := params.Param(0, 0)
		mode, _, _ := params.Param(1, 1)
		cur := t.keys.kittyFlags()
		switch mode {
		case 2:
			f |= cur
		case 3:
			f = cur &^ f
		}
		if len(t.keys.kittyStack) == 0 {
			t.keys.kittyStack = append(t.keys.kittyStack, f)
		} else {
			t.keys.kittyStack[len(t.keys.kittyStack)-1] = f
		}
	}))
	// OSC 52, which x/vt drops: the child writing the clipboard. A read
	// ("?") is never answered — the human's clipboard is not the child's to
	// see — and nothing reaches the callback but a decoded write.
	t.emu.RegisterOscHandler(52, func(data []byte) bool {
		t.clipboard(data)
		return true
	})

	// xterm modifyOtherKeys: CSI > 4 ; n m (CSI > 4 m resets it).
	t.emu.RegisterCsiHandler(ansi.Command('>', 0, 'm'), handled(func(params ansi.Params) {
		if r, _, _ := params.Param(0, 0); r == 4 {
			t.keys.modifyKeys, _, _ = params.Param(1, 0)
		}
	}))
}

// clampParam caps parameter i at limit, in place, keeping its sub-parameter
// flag. x/vt's own handler then validates the region and ignores an empty
// one, as xterm does.
func clampParam(params ansi.Params, i, limit int) {
	if i < len(params) && params[i].Param(0) > limit {
		params[i] = ansi.Param(limit) | params[i]&^ansi.Param(parser.ParamMask)
	}
}

// clipboard decodes one OSC 52 payload ("52;<selections>;<base64>") and
// hands a write to the callback. The selection defaults to 'c', as xterm's
// does when none is named. A read asks with "?" as the payload, which is no
// base64, so it is dropped with every other payload that does not decode.
func (t *XVT) clipboard(data []byte) {
	if t.cb.Clipboard == nil {
		return
	}
	_, rest, _ := bytes.Cut(data, []byte{';'})
	sels, payload, ok := bytes.Cut(rest, []byte{';'})
	text, err := base64.StdEncoding.DecodeString(string(payload))
	if !ok || err != nil {
		return
	}
	sel := byte('c')
	if len(sels) > 0 {
		sel = sels[0]
	}
	t.cb.Clipboard(sel, string(text))
}

// mouseTrackingOf orders the tracking modes from least to most reporting.
var mouseTrackingOf = []struct {
	mode     ansi.Mode
	tracking MouseTracking
}{
	{ansi.ModeMouseX10, MouseClicks},
	{ansi.ModeMouseNormal, MouseClicks},
	{ansi.ModeMouseButtonEvent, MouseDrags},
	{ansi.ModeMouseAnyEvent, MouseMotion},
}

// mouseChanged records one tracking mode and reports the resulting tracking
// when it changed.
func (t *XVT) mouseChanged(m ansi.Mode, on bool) {
	t.mouseModes[m] = on
	next := MouseOff
	for _, mt := range mouseTrackingOf {
		if t.mouseModes[mt.mode] {
			next = mt.tracking
		}
	}
	if next == t.mouse {
		return
	}
	t.mouse = next
	if t.cb.Mouse != nil {
		t.cb.Mouse(next)
	}
}

func (t *XVT) modeChanged(m ansi.Mode, on bool) {
	switch m {
	case ansi.ModeMouseX10, ansi.ModeMouseNormal, ansi.ModeMouseButtonEvent, ansi.ModeMouseAnyEvent:
		t.mouseChanged(m, on)
	case modeSyncOutput:
		if on && !t.syncing {
			// Materialise any committed-but-unrendered screen NOW: from here
			// until the frame closes, the live screen is half-drawn.
			t.materialize()
			t.syncing = true
			t.syncSince = t.now()
		} else if !on && t.syncing {
			t.syncing = false
			// Commit at the boundary, not at the end of the Write. If the rest
			// of this chunk opens the next frame, that open materialises this
			// one first.
			t.commit()
		}
	case ansi.ModeCursorKeys:
		t.keys.appCursor = on
	}
}

// commit marks the live screen as a frame to draw and signals Ready.
// Called with mu held (directly or from inside the parser).
func (t *XVT) commit() {
	t.seq++
	t.dirty = true
	select {
	case t.ready <- struct{}{}:
	default:
	}
}

// materialize copies the committed screen into a frame if it has not been
// yet. Cells are copied as the emulator holds them (a wide grapheme's
// trailing columns stay its zero-width placeholders), with no colour baked
// in: the default colours are the human's terminal's own.
func (t *XVT) materialize() {
	if !t.dirty {
		return
	}
	w, h := t.emu.Width(), t.emu.Height()
	buf := uv.NewBuffer(w, h)
	for y := range h {
		line := buf.Lines[y]
		for x := range w {
			if c := t.emu.CellAt(x, y); c != nil {
				line[x] = *c
			}
		}
	}
	t.frame = Frame{
		Seq: t.seq, Buf: buf, Cursor: t.emu.CursorPosition(), CursorVisible: t.cursorOn,
		History: t.emu.ScrollbackLen(),
	}
	t.dirty = false
}

// Write feeds child output to the emulator.
func (t *XVT) Write(p []byte) (int, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	n, err := t.emu.Write(p)
	switch {
	case !t.syncing:
		t.commit()
	case t.now().Sub(t.syncSince) > t.syncTimeout:
		t.syncing = false
		t.commit()
	}
	return n, err
}

// SetSyncTimeout replaces SyncTimeout for this pane. Replays that run slower
// than real time (byte-at-a-time tests) raise it so a frame they are still
// feeding is not released as stuck.
func (t *XVT) SetSyncTimeout(d time.Duration) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.syncTimeout = d
}

// Resize resizes the emulated screen and commits the result: a resize is a
// frame boundary whatever the child is doing.
func (t *XVT) Resize(cols, rows int) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.emu.Resize(cols, rows)
	t.commit()
}

// Size returns the screen size.
func (t *XVT) Size() (int, int) {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.emu.Width(), t.emu.Height()
}

// Frame returns the last committed frame.
func (t *XVT) Frame() Frame {
	t.mu.Lock()
	defer t.mu.Unlock()
	if !t.syncing {
		t.materialize()
	}
	return t.frame
}

// Ready signals committed frames.
func (t *XVT) Ready() <-chan struct{} { return t.ready }

// Syncing reports whether the child holds a synchronized frame open.
func (t *XVT) Syncing() bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.syncing
}

// Snapshot copies the live screen.
func (t *XVT) Snapshot() Screen {
	t.mu.Lock()
	defer t.mu.Unlock()
	w, h := t.emu.Width(), t.emu.Height()
	s := Screen{Cols: w, Rows: h, Cells: make([][]Cell, h), AltScreen: t.altScreen, CursorVisible: t.cursorOn}
	for y := range h {
		// A wide grapheme's trailing columns are left as the zero Cell:
		// Width 0, as Cell documents.
		row := make([]Cell, w)
		for x := 0; x < w; {
			c := t.emu.CellAt(x, y) // never nil inside the screen
			cw := max(c.Width, 1)
			row[x] = Cell{Content: c.Content, Width: cw, Style: c.Style}
			x += cw
		}
		s.Cells[y] = row
	}
	pos := t.emu.CursorPosition()
	s.CursorX, s.CursorY = pos.X, pos.Y
	return s
}

// History reads the main screen's scrollback.
func (t *XVT) History(from, n int) []string {
	t.mu.Lock()
	defer t.mu.Unlock()
	sb := t.emu.Scrollback()
	end := min(from+n, sb.Len())
	from = max(from, 0)
	if end <= from {
		return nil
	}
	out := make([]string, 0, end-from)
	for i := from; i < end; i++ {
		out = append(out, sb.Line(i).Render())
	}
	return out
}

// SendKey encodes key for the child. It crosses the emulator's reply pipe,
// the one path every input takes (mouse reports, pastes, focus reports, the
// emulator's replies), so it reaches the child behind whatever was sent
// before it. Like them it returns once the drain has taken it, before the
// child has.
func (t *XVT) SendKey(key KeyPress) {
	_, _ = t.emu.InputPipe().Write(t.encode(key))
}

// encode is key's bytes in the protocol the child asked for (nil: none).
func (t *XVT) encode(key KeyPress) []byte {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.keys.encodeKey(key)
}

// SendMouse encodes a pane-relative mouse event in the mouse mode and
// encoding the child enabled (nothing when it enabled none).
func (t *XVT) SendMouse(m MouseEvent) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.emu.SendMouse(m)
}

// Paste sends text, bracketed when the child enabled bracketed paste.
func (t *XVT) Paste(text string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.emu.Paste(text)
}

// SetDefaultColors sets the colours the child's defaults stand for.
func (t *XVT) SetDefaultColors(fg, bg color.Color) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if fg != nil {
		t.emu.SetDefaultForegroundColor(fg)
	}
	if bg != nil {
		t.emu.SetDefaultBackgroundColor(bg)
	}
}

// Focus reports a focus change to a child that asked for focus reports. The
// report crosses the reply pipe, so it is ordered with the emulator's own
// replies.
func (t *XVT) Focus(in bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if in {
		t.emu.Focus()
	} else {
		t.emu.Blur()
	}
}

// Close stops the pane and waits for its reply drain to finish.
//
// It closes the emulator's reply pipe rather than calling Emulator.Close:
// that writes Emulator.closed, which the drain goroutine's Read reads with no
// lock (x#879, a data race under -race). Closing the pipe ends the drain the
// same way; later writes from the emulator fail on the closed pipe.
func (t *XVT) Close() error {
	pw, ok := t.emu.InputPipe().(*io.PipeWriter)
	if !ok {
		return errors.New("termemu: x/vt input pipe is not an *io.PipeWriter")
	}
	_ = pw.CloseWithError(io.EOF)
	<-t.drained
	return nil
}
