package tui

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/ctxloom/ctxloom/internal/adapters/termemu"
)

// SplitView is a running split view: the program, the engine's emulated
// terminal, and the handles the session's presenters drive it by. Every
// method is safe from any goroutine.
type SplitView struct {
	prog *tea.Program
	pane termemu.Terminal
	out  io.Writer
	// epilogue is the line left after the final frame (SplitOptions.Epilogue).
	epilogue string
	cancel   context.CancelFunc
	done     chan error    // the program's result, once
	exited   chan struct{} // closed once the program has returned
	ids      atomic.Uint64
	close    sync.Once
}

// StartSplit starts the split view on o.In/o.Out and returns once it runs.
// The caller copies the engine's output into Pane and watches Done; ctx
// ending stops it, as Close does.
func StartSplit(ctx context.Context, o SplitOptions) (*SplitView, error) {
	if o.Term == nil {
		return nil, errors.New("tui: the split view needs the session's interactive terminal")
	}
	if o.Out == nil {
		return nil, errors.New("tui: the split view needs an output terminal")
	}
	newPane := o.NewPane
	if newPane == nil {
		newPane = func(cols, rows int, toChild io.Writer, cb termemu.Callbacks) termemu.Terminal {
			return termemu.NewXVT(cols, rows, toChild, cb)
		}
	}
	ctx, cancel := context.WithCancel(ctx)
	mb := newPaneMailbox()
	// The emulator's size until the first resize; the engine is not sized
	// before then, so nothing it draws is laid out for this.
	pane := newPane(80, 24, o.Term, mb.callbacks())
	v := &SplitView{pane: pane, out: o.Out, epilogue: o.Epilogue, cancel: cancel, done: make(chan error, 1), exited: make(chan struct{})}
	m := newSplit(ctx, o, pane, func(msg tea.Msg) { go v.prog.Send(msg) }, mb)
	// The run's own context owns SIGINT and SIGTERM, and a cancelled run
	// ends the program with it.
	opts := []tea.ProgramOption{tea.WithContext(ctx), tea.WithoutSignalHandler(), tea.WithInput(o.In), tea.WithOutput(o.Out)}
	if o.WindowSize != [2]int{} {
		opts = append(opts, tea.WithWindowSize(o.WindowSize[0], o.WindowSize[1]))
	}
	v.prog = tea.NewProgram(m, opts...)
	go func() {
		_, err := v.prog.Run()
		cancel()
		v.done <- err
		close(v.exited)
	}()
	return v, nil
}

// Pane is where the engine's output goes: the caller copies the session's
// terminal into it.
func (v *SplitView) Pane() io.Writer { return v.pane }

// Done receives the program's result once it has stopped: nil after Close,
// an error when it failed or its context ended.
func (v *SplitView) Done() <-chan error { return v.done }

// Close stops the split view, restoring the human's terminal, and releases
// the emulator. The pane's last committed frame is drawn first: what the
// engine wrote before Close reaches the human's terminal. Once the main
// screen is back, that frame is written to it, then the epilogue, so the
// shell's scrollback keeps the last thing the engine showed. A split view
// its context already stopped leaves them too.
func (v *SplitView) Close() {
	v.close.Do(func() {
		v.prog.Send(frameMsg{})
		v.prog.Quit()
		<-v.exited
		_, _ = io.WriteString(v.out, finalFrame(v.pane.Frame(), v.epilogue))
		_ = v.pane.Close()
	})
}

// finalFrame is f's rows for the main screen, styled and unpadded, its
// trailing blank rows dropped, then epilogue: each line ended CR LF, since
// the human's terminal may still be raw. It starts with a CR: leaving the
// alternate screen puts the cursor back where it stood on entry, which after
// a line written raw is not column 0.
func finalFrame(f termemu.Frame, epilogue string) string {
	var rows []string
	if f.Buf != nil {
		for y := range f.Buf.Height() {
			rows = append(rows, strings.TrimRight(f.Line(y), " "))
		}
		for len(rows) > 0 && f.Buf.Line(len(rows)-1).String() == "" {
			rows = rows[:len(rows)-1]
		}
	}
	if epilogue != "" {
		rows = append(rows, epilogue)
	}
	var b strings.Builder
	b.WriteString("\r")
	for _, r := range rows {
		b.WriteString(r)
		b.WriteString("\r\n")
	}
	return b.String()
}

// running reports whether the program is still up.
func (v *SplitView) running() bool {
	select {
	case <-v.exited:
		return false
	default:
		return true
	}
}

// Summon asks for the named view (only "approvals" exists) for an approval:
// shown at the human's next pause in typing, inert for its arming window.
// It returns nil once shown; ErrEngaged — after showing notice — when the
// human is using the split view; ctx's error when ctx ends first; and
// ErrUnavailable when the split view has stopped or the session has no
// approval queue.
func (v *SplitView) Summon(ctx context.Context, view, notice string) error {
	if view != "approvals" {
		return fmt.Errorf("tui: no view %q to summon", view)
	}
	if !v.running() {
		return ErrUnavailable
	}
	reply := make(chan error, 1)
	id := v.ids.Add(1)
	v.prog.Send(summonMsg{id: id, notice: notice, reply: reply})
	select {
	case err := <-reply:
		return err
	case <-ctx.Done():
		v.prog.Send(summonCancelMsg{id: id})
		return ctx.Err()
	case <-v.exited:
		return ErrUnavailable
	}
}

// SetApprovals sets the panel's count of approvals waiting and when the
// oldest arrived (zero: unknown). An arrival rings the bell, at most once
// per approvalBellInterval, and not while the approvals view is open: it is
// its own signal.
func (v *SplitView) SetApprovals(n int, oldest time.Time, arrived bool) {
	v.prog.Send(approvalsCountMsg{n: n, oldest: oldest, arrived: arrived})
}

// Note shows text in the panel for d.
func (v *SplitView) Note(text string, d time.Duration) {
	v.prog.Send(noteMsg{text: text, d: d})
}

// Ring rings the bell once and reports whether it could: not once the split
// view has stopped.
func (v *SplitView) Ring() bool {
	if !v.running() {
		return false
	}
	v.prog.Send(ringMsg{})
	return true
}

// paneMailbox carries what the emulator's callbacks report into the
// program. A callback runs inside the emulator's Write, holding its lock,
// and the program reads the emulator to draw: so a callback must never wait
// on the program. It queues instead, without bound and without loss, and one
// pump hands the queue over.
type paneMailbox struct {
	mu    sync.Mutex
	queue []tea.Msg
	wake  chan struct{}
}

func newPaneMailbox() *paneMailbox { return &paneMailbox{wake: make(chan struct{}, 1)} }

func (b *paneMailbox) put(msg tea.Msg) {
	b.mu.Lock()
	b.queue = append(b.queue, msg)
	b.mu.Unlock()
	select {
	case b.wake <- struct{}{}:
	default:
	}
}

// take hands over everything queued.
func (b *paneMailbox) take() []tea.Msg {
	b.mu.Lock()
	defer b.mu.Unlock()
	q := b.queue
	b.queue = nil
	return q
}

// Pane events, as the emulator's callbacks report them.
type (
	paneTitleMsg     string
	paneBellMsg      struct{}
	paneClipboardMsg struct {
		sel  byte
		data string
	}
	paneMouseMsg  termemu.MouseTracking
	paneEventsMsg []tea.Msg
)

func (b *paneMailbox) callbacks() termemu.Callbacks {
	return termemu.Callbacks{
		Title:     func(t string) { b.put(paneTitleMsg(t)) },
		Bell:      func() { b.put(paneBellMsg{}) },
		Clipboard: func(sel byte, data string) { b.put(paneClipboardMsg{sel: sel, data: data}) },
		Mouse:     func(m termemu.MouseTracking) { b.put(paneMouseMsg(m)) },
	}
}

// waitPaneEvents receives the next batch of pane events.
func (s *Split) waitPaneEvents() tea.Cmd {
	if s.driven || s.mailbox == nil {
		return nil
	}
	b, done := s.mailbox, s.ctx.Done()
	return func() tea.Msg {
		select {
		case <-b.wake:
			return paneEventsMsg(b.take())
		case <-done:
			return nil
		}
	}
}

// applyPaneEvents lands what the engine asked of the terminal: its title
// becomes the human's window title, its bell and clipboard writes reach the
// human's terminal, and its mouse tracking becomes the split view's.
func (s *Split) applyPaneEvents(evs paneEventsMsg) tea.Cmd {
	var cmds []tea.Cmd
	for _, ev := range evs {
		switch ev := ev.(type) {
		case paneTitleMsg:
			s.title = string(ev)
		case paneBellMsg:
			cmds = append(cmds, tea.Raw("\a"))
		case paneClipboardMsg:
			if ev.sel == 'p' {
				cmds = append(cmds, tea.SetPrimaryClipboard(ev.data))
			} else {
				cmds = append(cmds, tea.SetClipboard(ev.data))
			}
		case paneMouseMsg:
			s.mouseMode = termemu.MouseTracking(ev)
		}
	}
	return tea.Batch(append(cmds, s.waitPaneEvents())...)
}
