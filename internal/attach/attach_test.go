package attach

import (
	"context"
	"errors"
	"io"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	agentcoordpb "github.com/ctxloom/ctxloom/internal/agentcoord"
)

// fakeTty is why Tty is an interface. Every assertion below is about something
// only a terminal can show — that raw mode was entered BEFORE any byte moved
// and restored on the way out, that a resize became a frame, that pane bytes
// were written through — and none of it is reachable against *os.File in a
// unit test.
type fakeTty struct {
	mu sync.Mutex

	in      chan []byte // keystrokes to hand to Read
	written []byte      // what the relay wrote to the "screen"

	cols, rows int
	resized    chan struct{}

	rawEntered  int
	rawRestored int
	// rawAtFirstMove records whether raw mode was on the first time any byte
	// moved in either direction. It is the only way to catch a relay that
	// enables raw mode too late.
	rawAtFirstMove *bool
	makeRawErr     error
	sizeErr        error
}

func newFakeTty() *fakeTty {
	return &fakeTty{in: make(chan []byte, 8), cols: 80, rows: 24, resized: make(chan struct{}, 4)}
}

func (f *fakeTty) Read(p []byte) (int, error) {
	b, ok := <-f.in
	if !ok {
		return 0, io.EOF
	}
	f.noteMove()
	return copy(p, b), nil
}

func (f *fakeTty) Write(p []byte) (int, error) {
	f.noteMove()
	f.mu.Lock()
	defer f.mu.Unlock()
	f.written = append(f.written, p...)
	return len(p), nil
}

func (f *fakeTty) noteMove() {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.rawAtFirstMove == nil {
		raw := f.rawEntered > f.rawRestored
		f.rawAtFirstMove = &raw
	}
}

func (f *fakeTty) Size() (int, int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.sizeErr != nil {
		return 0, 0, f.sizeErr
	}
	return f.cols, f.rows, nil
}

func (f *fakeTty) MakeRaw() (func() error, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.makeRawErr != nil {
		return nil, f.makeRawErr
	}
	f.rawEntered++
	return func() error {
		f.mu.Lock()
		defer f.mu.Unlock()
		f.rawRestored++
		return nil
	}, nil
}

func (f *fakeTty) Resized() <-chan struct{} { return f.resized }

func (f *fakeTty) screen() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return string(f.written)
}

func (f *fakeTty) rawCounts() (entered, restored int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.rawEntered, f.rawRestored
}

func (f *fakeTty) setSize(cols, rows int) {
	f.mu.Lock()
	f.cols, f.rows = cols, rows
	f.mu.Unlock()
	f.resized <- struct{}{}
}

// fakeStream records what the relay sent and replays scripted pane frames.
type fakeStream struct {
	mu   sync.Mutex
	sent []*agentcoordpb.AttachClientFrame

	out       chan *agentcoordpb.AttachPaneFrame
	closeSent int
	sendErr   error
}

func newFakeStream() *fakeStream {
	return &fakeStream{out: make(chan *agentcoordpb.AttachPaneFrame, 8)}
}

func (s *fakeStream) Send(f *agentcoordpb.AttachClientFrame) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.sendErr != nil {
		return s.sendErr
	}
	s.sent = append(s.sent, f)
	return nil
}

func (s *fakeStream) Recv() (*agentcoordpb.AttachPaneFrame, error) {
	f, ok := <-s.out
	if !ok {
		return nil, io.EOF
	}
	return f, nil
}

func (s *fakeStream) CloseSend() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.closeSent++
	return nil
}

func (s *fakeStream) frames() []*agentcoordpb.AttachClientFrame {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]*agentcoordpb.AttachClientFrame(nil), s.sent...)
}

// inputs returns every input frame's bytes, concatenated.
func (s *fakeStream) inputs() string {
	var out []byte
	for _, f := range s.frames() {
		if in, ok := f.GetKind().(*agentcoordpb.AttachClientFrame_Input); ok {
			out = append(out, in.Input...)
		}
	}
	return string(out)
}

type fakeConn struct{ s *fakeStream }

func (c fakeConn) AttachPane(context.Context) (Stream, error) { return c.s, nil }

// TestRun_OpensWithTheTerminalSizeAndHarp: the open frame is the whole
// handshake, so a relay that sent input before opening — or opened with a
// zero size, leaving the pane at tmux's default — would produce a terminal
// that looks attached and renders wrong.
func TestRun_OpensWithTheTerminalSizeAndHarp(t *testing.T) {
	tty, stream := newFakeTty(), newFakeStream()
	tty.setSizeQuiet(120, 40)

	go func() {
		time.Sleep(20 * time.Millisecond)
		tty.in <- []byte{DetachKey}
	}()
	err := Run(context.Background(), fakeConn{stream}, "swift-amber-falcon", tty, Options{})
	require.ErrorIs(t, err, ErrDetached)

	frames := stream.frames()
	require.NotEmpty(t, frames)
	open, ok := frames[0].GetKind().(*agentcoordpb.AttachClientFrame_Open)
	require.True(t, ok, "the FIRST frame must be open; got %T", frames[0].GetKind())
	assert.Equal(t, "swift-amber-falcon", open.Open.GetHarp())
	assert.Equal(t, uint32(120), open.Open.GetCols(), "the pane must be opened at the operator's width")
	assert.Equal(t, uint32(40), open.Open.GetRows())
}

// setSizeQuiet sets the size without signalling a resize.
func (f *fakeTty) setSizeQuiet(cols, rows int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.cols, f.rows = cols, rows
}

// TestRun_EntersRawModeBeforeAnyByteMovesAndRestoresIt is the invariant a
// human notices immediately when it breaks: a relay that returns without
// restoring leaves the operator's shell unusable.
//
// It asserts BOTH halves. Restoring alone is satisfied by never entering raw
// mode at all, and entering alone is satisfied by a relay that wrecks the
// terminal on exit.
func TestRun_EntersRawModeBeforeAnyByteMovesAndRestoresIt(t *testing.T) {
	tty, stream := newFakeTty(), newFakeStream()

	go func() {
		stream.out <- outputFrame("hello")
		time.Sleep(20 * time.Millisecond)
		tty.in <- []byte{DetachKey}
	}()
	err := Run(context.Background(), fakeConn{stream}, "beta", tty, Options{})
	require.ErrorIs(t, err, ErrDetached)

	entered, restored := tty.rawCounts()
	assert.Equal(t, 1, entered, "raw mode must be entered exactly once")
	assert.Equal(t, 1, restored, "raw mode must be restored on the way out")

	tty.mu.Lock()
	first := tty.rawAtFirstMove
	tty.mu.Unlock()
	require.NotNil(t, first, "precondition: some byte must have moved for this to mean anything")
	assert.True(t, *first, "raw mode must already be on the first time a byte moves")
}

// TestRun_WritesPaneOutputToTheTerminal: the entire point of attaching.
func TestRun_WritesPaneOutputToTheTerminal(t *testing.T) {
	tty, stream := newFakeTty(), newFakeStream()

	go func() {
		stream.out <- outputFrame("PANE-BYTES-4c2a")
		time.Sleep(30 * time.Millisecond)
		tty.in <- []byte{DetachKey}
	}()
	err := Run(context.Background(), fakeConn{stream}, "gamma", tty, Options{})
	require.ErrorIs(t, err, ErrDetached)

	assert.Contains(t, tty.screen(), "PANE-BYTES-4c2a", "pane output must reach the operator's terminal")
}

// TestRun_ForwardsKeystrokes pins the other direction.
func TestRun_ForwardsKeystrokes(t *testing.T) {
	tty, stream := newFakeTty(), newFakeStream()

	go func() {
		tty.in <- []byte("typed-7b1e")
		time.Sleep(30 * time.Millisecond)
		tty.in <- []byte{DetachKey}
	}()
	err := Run(context.Background(), fakeConn{stream}, "delta", tty, Options{})
	require.ErrorIs(t, err, ErrDetached)

	assert.Equal(t, "typed-7b1e", stream.inputs(), "keystrokes must be forwarded to the pane")
}

// TestRun_ReadOnlySendsNoInput: read-only must be a REFUSAL to transmit, not
// a hope that the far end ignores it. A viewer who believes they are only
// watching must not be able to type into a run at all.
func TestRun_ReadOnlySendsNoInput(t *testing.T) {
	tty, stream := newFakeTty(), newFakeStream()

	go func() {
		tty.in <- []byte("must-not-arrive")
		time.Sleep(30 * time.Millisecond)
		tty.in <- []byte{DetachKey}
	}()
	err := Run(context.Background(), fakeConn{stream}, "epsilon", tty, Options{ReadOnly: true})
	require.ErrorIs(t, err, ErrDetached)

	assert.Empty(t, stream.inputs(), "a read-only attach must put no keystroke on the wire")
	// And the open frame must SAY so, or the far end cannot enforce it either.
	open := stream.frames()[0].GetKind().(*agentcoordpb.AttachClientFrame_Open)
	assert.True(t, open.Open.GetReadOnly(), "read-only must be declared in the open frame")
}

// TestRun_DetachKeyIsConsumedLocally: Ctrl-] ends the relay and must NOT be
// delivered to the pane. Forwarding it would type a control character into
// the agent every time a human leaves.
func TestRun_DetachKeyIsConsumedLocally(t *testing.T) {
	tty, stream := newFakeTty(), newFakeStream()

	go func() { tty.in <- []byte{'a', 'b', DetachKey} }()
	err := Run(context.Background(), fakeConn{stream}, "zeta", tty, Options{})
	require.ErrorIs(t, err, ErrDetached)

	got := stream.inputs()
	assert.Equal(t, "ab", got, "bytes before the detach key are forwarded")
	assert.NotContains(t, got, string([]byte{DetachKey}), "the detach key must never reach the pane")
}

// TestRun_ResizeBecomesAResizeFrame: a full-screen TUI in the pane keeps
// drawing at its original size otherwise, which reads as corruption rather
// than as a missing feature.
func TestRun_ResizeBecomesAResizeFrame(t *testing.T) {
	tty, stream := newFakeTty(), newFakeStream()

	go func() {
		time.Sleep(20 * time.Millisecond)
		tty.setSize(200, 60)
		time.Sleep(50 * time.Millisecond)
		tty.in <- []byte{DetachKey}
	}()
	err := Run(context.Background(), fakeConn{stream}, "eta", tty, Options{})
	require.ErrorIs(t, err, ErrDetached)

	var got *agentcoordpb.AttachResize
	for _, f := range stream.frames() {
		if r, ok := f.GetKind().(*agentcoordpb.AttachClientFrame_Resize); ok {
			got = r.Resize
		}
	}
	require.NotNil(t, got, "a terminal resize must produce a resize frame")
	assert.Equal(t, uint32(200), got.GetCols())
	assert.Equal(t, uint32(60), got.GetRows())
}

// TestRun_PaneClosedCarriesTheExitCode: `ctxloom attach` exits with the run's
// status, so losing the code here would make every ended pane look like a
// success.
func TestRun_PaneClosedCarriesTheExitCode(t *testing.T) {
	tty, stream := newFakeTty(), newFakeStream()

	go func() {
		stream.out <- &agentcoordpb.AttachPaneFrame{
			Kind: &agentcoordpb.AttachPaneFrame_Closed{Closed: &agentcoordpb.AttachClosed{
				ExitCode: 7, Message: "boom",
			}},
		}
	}()
	err := Run(context.Background(), fakeConn{stream}, "theta", tty, Options{})

	var closed *PaneClosedError
	require.ErrorAs(t, err, &closed, "a closed pane must be reported as PaneClosedError, not as a detach")
	assert.Equal(t, int32(7), closed.ExitCode)
	assert.NotErrorIs(t, err, ErrDetached, "a dead pane must not be indistinguishable from a human leaving")

	// Raw mode must still have been restored on this exit path.
	_, restored := tty.rawCounts()
	assert.Equal(t, 1, restored, "raw mode must be restored when the pane closes too")
}

// TestRun_MakeRawFailureAbortsBeforeOpening: if the terminal cannot be put in
// raw mode there is no usable relay, and opening anyway would attach a run to
// a terminal that mangles its input.
func TestRun_MakeRawFailureAbortsBeforeOpening(t *testing.T) {
	tty, stream := newFakeTty(), newFakeStream()
	tty.makeRawErr = errors.New("no tty")
	// Closed so that a relay which wrongly carried on still TERMINATES and
	// gets judged on what it sent, rather than hanging and being "killed" by
	// a test timeout — a timeout would pass this test for the wrong reason.
	close(tty.in)

	err := Run(context.Background(), fakeConn{stream}, "iota", tty, Options{})
	require.Error(t, err)
	assert.ErrorContains(t, err, "raw mode", "the failure must name what could not be done")
	assert.Empty(t, stream.frames(), "nothing may be sent when the terminal could not be prepared")
}

func outputFrame(s string) *agentcoordpb.AttachPaneFrame {
	return &agentcoordpb.AttachPaneFrame{
		Kind: &agentcoordpb.AttachPaneFrame_Output{Output: []byte(s)},
	}
}
