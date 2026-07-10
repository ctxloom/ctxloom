package goplugin

import (
	"bytes"
	"context"
	"errors"
	"io"
	"sync"
	"testing"
	"time"

	pb "github.com/ctxloom/ctxloom/internal/lm/grpc"
	"github.com/ctxloom/ctxloom/internal/vpio"
)

// fakeClient is a minimal pb.Client double that captures the interactive
// Run call's arguments and lets a test control what it returns and when —
// unlike pb.MockClient, it does not drop the resize channel, so it can
// verify the goplugin Session actually relays Resize calls onto the wire.
type fakeClient struct {
	pb.Client // embed (nil) to satisfy the rest of the interface; unused in these tests

	mu       sync.Mutex
	gotStdin io.Reader
	gotOut   io.Writer
	gotErr   io.Writer

	resizeSeen []*pb.WindowSize
	resizeDone chan struct{}

	exitCode int32
	runErr   error
	block    chan struct{} // if non-nil, Run blocks until this closes
}

func (f *fakeClient) Run(_ context.Context, _ *pb.RunStart, stdin io.Reader, stdout, stderr io.Writer, resize <-chan *pb.WindowSize) (int32, error) {
	f.mu.Lock()
	f.gotStdin = stdin
	f.gotOut = stdout
	f.gotErr = stderr
	f.mu.Unlock()

	if resize != nil {
		go func() {
			for ws := range resize {
				f.mu.Lock()
				f.resizeSeen = append(f.resizeSeen, ws)
				f.mu.Unlock()
			}
			if f.resizeDone != nil {
				close(f.resizeDone)
			}
		}()
	}

	if f.block != nil {
		<-f.block
	}
	return f.exitCode, f.runErr
}

func TestLauncher_StartPassesStdioUnchanged(t *testing.T) {
	stdinR, stdinW := io.Pipe()
	defer stdinW.Close()
	var stdout, stderr bytes.Buffer
	fc := &fakeClient{}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sess, err := NewLauncher(fc, &pb.RunStart{}).Start(ctx, vpio.ProcessSpec{
		Stdin: stdinR, Stdout: &stdout, Stderr: &stderr,
	})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	if _, err := sess.Wait(); err != nil {
		t.Fatalf("Wait: %v", err)
	}

	fc.mu.Lock()
	defer fc.mu.Unlock()
	if fc.gotStdin != stdinR {
		t.Errorf("stdin not passed through: got %v, want %v", fc.gotStdin, stdinR)
	}
	if fc.gotOut != &stdout {
		t.Errorf("stdout not passed through")
	}
	if fc.gotErr != &stderr {
		t.Errorf("stderr not passed through")
	}
}

func TestLauncher_StartDoesNotBlockOnRun(t *testing.T) {
	fc := &fakeClient{block: make(chan struct{})}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	started := make(chan struct{})
	go func() {
		sess, err := NewLauncher(fc, &pb.RunStart{}).Start(ctx, vpio.ProcessSpec{})
		if err != nil {
			t.Errorf("Start: %v", err)
		}
		close(started)
		close(fc.block) // let the fake Run return
		if _, err := sess.Wait(); err != nil {
			t.Errorf("Wait: %v", err)
		}
	}()

	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("Start did not return promptly while Run was blocked — it should not block on the underlying transport call")
	}
}

func TestSession_ResizeRelaysOntoTheWire(t *testing.T) {
	fc := &fakeClient{resizeDone: make(chan struct{})}
	ctx, cancel := context.WithCancel(context.Background())

	sess, err := NewLauncher(fc, &pb.RunStart{}).Start(ctx, vpio.ProcessSpec{})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}

	sess.Resize(24, 80)
	sess.Resize(30, 100) // latest-wins coalescing may drop the first

	if _, err := sess.Wait(); err != nil {
		t.Fatalf("Wait: %v", err)
	}

	// Closing ctx tears down the session's resize channel, which ends the
	// fake's drain goroutine — mirrors the real transport's contract that
	// the resize channel closes on ctx.Done() (run_resize_unix.go).
	cancel()

	select {
	case <-fc.resizeDone:
	case <-time.After(2 * time.Second):
		t.Fatal("resize channel never closed after ctx cancellation")
	}

	fc.mu.Lock()
	defer fc.mu.Unlock()
	if len(fc.resizeSeen) == 0 {
		t.Fatal("expected at least one resize event to reach the transport")
	}
	last := fc.resizeSeen[len(fc.resizeSeen)-1]
	if last.Rows != 30 || last.Cols != 100 {
		t.Errorf("last relayed resize = %+v, want {Rows:30 Cols:100}", last)
	}
}

func TestSession_ResizeAfterCloseDoesNotPanicOrBlock(t *testing.T) {
	fc := &fakeClient{}
	ctx, cancel := context.WithCancel(context.Background())

	sess, err := NewLauncher(fc, &pb.RunStart{}).Start(ctx, vpio.ProcessSpec{})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	if _, err := sess.Wait(); err != nil {
		t.Fatalf("Wait: %v", err)
	}
	cancel()
	time.Sleep(50 * time.Millisecond) // let the ctx-done watcher close the resize channel

	done := make(chan struct{})
	go func() {
		sess.Resize(1, 1) // must neither panic (send on closed channel) nor block
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Resize after session close blocked")
	}
}

func TestSession_WaitReturnsExitCodeAndError(t *testing.T) {
	wantErr := errors.New("boom")
	fc := &fakeClient{exitCode: 3, runErr: wantErr}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sess, err := NewLauncher(fc, &pb.RunStart{}).Start(ctx, vpio.ProcessSpec{})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	status, err := sess.Wait()
	if !errors.Is(err, wantErr) {
		t.Errorf("Wait err = %v, want %v", err, wantErr)
	}
	if status.Code != 3 {
		t.Errorf("Wait code = %d, want 3", status.Code)
	}
}

func TestSession_SignalReportsUnsupported(t *testing.T) {
	fc := &fakeClient{}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sess, err := NewLauncher(fc, &pb.RunStart{}).Start(ctx, vpio.ProcessSpec{})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	if err := sess.Signal(nil); err == nil {
		t.Error("Signal: expected a non-nil error (go-plugin transport has no signal channel), got nil")
	}
}
