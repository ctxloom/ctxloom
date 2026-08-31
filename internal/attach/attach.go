// Package attach is the OPERATOR half of `ctxloom attach <harp>`: it relays a
// human's terminal to a run's live pane over the coordinator's AttachPane
// stream.
//
// WHY A RELAY AND NOT `exec tmux attach`. Agents run containerized and tmux
// lives inside the container, so a host-side `tmux attach` has no socket to
// reach. Relaying is the only route that works for a containerized child, and
// it is therefore the ONLY route — a host fast-path would be a second
// implementation of one concept, which is exactly the host/container split
// this project just finished removing.
package attach

import (
	"context"
	"errors"
	"fmt"
	"io"

	agentcoordpb "github.com/ctxloom/ctxloom/internal/agentcoord"
)

// Conn opens the AttachPane stream. It is an interface rather than a
// *grpc.ClientConn so Run can be driven in tests without a server.
type Conn interface {
	AttachPane(ctx context.Context) (Stream, error)
}

// Stream is one AttachPane bidi stream, narrowed to what the relay uses.
type Stream interface {
	Send(*agentcoordpb.AttachClientFrame) error
	Recv() (*agentcoordpb.AttachPaneFrame, error)
	CloseSend() error
}

// Tty is the operator's terminal.
//
// It is an INTERFACE, not *os.File, so the relay is testable without a real
// device: every rule this package implements (raw mode is entered before any
// byte moves and restored however the relay exits, a resize becomes a resize
// frame, pane output is written through) is otherwise only observable by
// driving a physical terminal, which a unit test cannot do.
type Tty interface {
	io.ReadWriter
	// Size reports the terminal's current dimensions.
	Size() (cols, rows int, err error)
	// MakeRaw puts the terminal in raw mode and returns its restore. Raw mode
	// is what makes the relay transparent: without it the local line
	// discipline eats control keys the remote program needs.
	MakeRaw() (restore func() error, err error)
	// Resized fires whenever the terminal's size changes.
	Resized() <-chan struct{}
}

// ErrDetached is how a clean operator-initiated detach ends. It is a distinct
// error rather than a nil return so a caller can tell "the human left" from
// "the run's pane closed" — the pane survives the first and not the second,
// and reporting them alike would make a dead agent look like a detach.
var ErrDetached = errors.New("detached")

// DetachKey is Ctrl-] — the byte that ends the relay locally without sending
// anything to the pane. A relay with no local escape is a terminal a human
// cannot leave without killing their own client.
const DetachKey = 0x1d

// Options configure one attach.
type Options struct {
	// ReadOnly attaches for observation. Keystrokes are NOT forwarded, which
	// is enforced here as well as at the far end so a read-only viewer's
	// typing never reaches the wire at all.
	ReadOnly bool
}

// Run relays t to harp's pane until the pane closes, the context ends, or the
// operator presses DetachKey.
//
// Raw mode is entered BEFORE the open frame and restored on every exit path:
// a Run that returned leaving the operator's terminal in raw mode is a broken
// shell, which is a worse failure than whatever caused the return.
func Run(ctx context.Context, c Conn, harp string, t Tty, opts Options) error {
	if harp == "" {
		return errors.New("attach: no run named")
	}
	cols, rows, err := t.Size()
	if err != nil {
		return fmt.Errorf("attach: read terminal size: %w", err)
	}
	restore, err := t.MakeRaw()
	if err != nil {
		return fmt.Errorf("attach: put terminal in raw mode: %w", err)
	}
	defer func() { _ = restore() }()

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	stream, err := c.AttachPane(ctx)
	if err != nil {
		return fmt.Errorf("attach: open pane stream: %w", err)
	}
	if err := stream.Send(&agentcoordpb.AttachClientFrame{
		Kind: &agentcoordpb.AttachClientFrame_Open{Open: &agentcoordpb.AttachOpen{
			Harp: harp, Cols: uint32(cols), Rows: uint32(rows), ReadOnly: opts.ReadOnly,
		}},
	}); err != nil {
		return fmt.Errorf("attach: open %q: %w", harp, err)
	}

	// Each direction reports its own terminal reason down one channel; the
	// FIRST to finish ends the relay. Buffered so a loser's send cannot block
	// forever after Run has returned.
	done := make(chan error, 3)
	go func() { done <- pumpInput(ctx, stream, t, opts.ReadOnly) }()
	go func() { done <- pumpResize(ctx, stream, t) }()
	go func() { done <- pumpOutput(stream, t) }()

	select {
	case err := <-done:
		cancel()
		_ = stream.CloseSend()
		return err
	case <-ctx.Done():
		_ = stream.CloseSend()
		return ctx.Err()
	}
}

// pumpInput forwards keystrokes until DetachKey, EOF, or ctx.
//
// In read-only mode it still READS the tty — it must, or DetachKey would
// never be seen and the viewer could not leave — but sends nothing.
func pumpInput(ctx context.Context, stream Stream, t Tty, readOnly bool) error {
	buf := make([]byte, 4096)
	for {
		n, err := t.Read(buf)
		if n > 0 {
			if i := indexByte(buf[:n], DetachKey); i >= 0 {
				// Everything BEFORE the detach key is still the operator's
				// input and is forwarded; the key itself is consumed locally
				// and never reaches the pane.
				if i > 0 && !readOnly {
					if serr := sendInput(stream, buf[:i]); serr != nil {
						return serr
					}
				}
				return ErrDetached
			}
			if !readOnly {
				if serr := sendInput(stream, buf[:n]); serr != nil {
					return serr
				}
			}
		}
		if err != nil {
			if errors.Is(err, io.EOF) {
				return ErrDetached
			}
			return fmt.Errorf("attach: read terminal: %w", err)
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
	}
}

func sendInput(stream Stream, b []byte) error {
	cp := make([]byte, len(b))
	copy(cp, b)
	if err := stream.Send(&agentcoordpb.AttachClientFrame{
		Kind: &agentcoordpb.AttachClientFrame_Input{Input: cp},
	}); err != nil {
		return fmt.Errorf("attach: send input: %w", err)
	}
	return nil
}

// pumpResize keeps the pane the size of the operator's window. Without it a
// full-screen TUI in the pane keeps drawing at the size it started with, and
// the mismatch looks like corruption rather than a missing feature.
func pumpResize(ctx context.Context, stream Stream, t Tty) error {
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case _, ok := <-t.Resized():
			if !ok {
				return nil
			}
			cols, rows, err := t.Size()
			if err != nil {
				continue // a size we cannot read is not worth ending the relay for
			}
			if err := stream.Send(&agentcoordpb.AttachClientFrame{
				Kind: &agentcoordpb.AttachClientFrame_Resize{Resize: &agentcoordpb.AttachResize{
					Cols: uint32(cols), Rows: uint32(rows),
				}},
			}); err != nil {
				return fmt.Errorf("attach: send resize: %w", err)
			}
		}
	}
}

// pumpOutput writes pane bytes to the terminal until the pane closes.
func pumpOutput(stream Stream, t Tty) error {
	for {
		frame, err := stream.Recv()
		if err != nil {
			if errors.Is(err, io.EOF) {
				return nil
			}
			return fmt.Errorf("attach: receive pane output: %w", err)
		}
		switch k := frame.GetKind().(type) {
		case *agentcoordpb.AttachPaneFrame_Output:
			if _, werr := t.Write(k.Output); werr != nil {
				return fmt.Errorf("attach: write to terminal: %w", werr)
			}
		case *agentcoordpb.AttachPaneFrame_Closed:
			return &PaneClosedError{ExitCode: k.Closed.GetExitCode(), Message: k.Closed.GetMessage()}
		}
	}
}

// PaneClosedError reports the run's pane ending, carrying its true exit code
// so `ctxloom attach` can exit with it rather than inventing a status.
type PaneClosedError struct {
	ExitCode int32
	Message  string
}

func (e *PaneClosedError) Error() string {
	if e.Message != "" {
		return fmt.Sprintf("pane closed (exit %d): %s", e.ExitCode, e.Message)
	}
	return fmt.Sprintf("pane closed (exit %d)", e.ExitCode)
}

func indexByte(b []byte, c byte) int {
	for i, x := range b {
		if x == c {
			return i
		}
	}
	return -1
}
