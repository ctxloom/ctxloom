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

// TestSession_WaitAloneReleasesResourcesWithoutCtxCancellation inverts
// U153-F02: the ctx-done watcher goroutine and the resize channel are tied
// to the CALLER's context, not to the session's own lifetime (Start's
// `go func() { <-ctx.Done(); s.stop() }()`), so both outlive a session that
// has already completed — Wait returning releases nothing. For a one-shot
// `ctxloom run` this is harmless, but any caller holding one long-lived ctx
// across multiple turns (internal/cli/run.go does exactly this) accumulates
// a goroutine and an open channel per turn.
//
// The correct contract: once Wait returns, the session is DONE and its
// resources are released on their own, with ctx cancellation only as the
// early-abort path — not the only path. This test starts a session, lets it
// finish, calls Wait, and — WITHOUT ever cancelling ctx — requires the
// session to already be stopped.
//
func TestSession_WaitAloneReleasesResourcesWithoutCtxCancellation(t *testing.T) {
	fc := &fakeClient{}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel() // safety net only; must not be required for the assertion below

	sess, err := NewLauncher(fc, &pb.RunStart{}).Start(ctx, vpio.ProcessSpec{})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	if _, err := sess.Wait(); err != nil {
		t.Fatalf("Wait: %v", err)
	}

	s := sess.(*Session)
	s.mu.Lock()
	closed := s.closed
	s.mu.Unlock()
	if !closed {
		t.Fatal("session must be stopped once Wait returns, without needing ctx cancellation")
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

// TestSession_WaitIsIdempotent pins U153-F01: Wait read the single-slot result
// channel directly, so the FIRST call drained it and every later call blocked
// forever. The sibling implementation of the same vpio.Session method
// (dockerexec.Session) guards its Wait with a sync.Once and returns the cached
// result, so the two transports behind one interface disagreed about whether
// calling Wait twice is legal. A caller writing the natural
// `defer session.Wait()` next to an explicit Wait deadlocks on one transport
// and not the other, and no signature says which.
//
// Bounded (template §11j): the failure mode here is a PARK, not an assertion,
// so the second Wait runs on its own goroutine under a short deadline —
// otherwise the red burns the whole test timeout instead of failing.
func TestSession_WaitIsIdempotent(t *testing.T) {
	fc := &fakeClient{exitCode: 5}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sess, err := NewLauncher(fc, &pb.RunStart{}).Start(ctx, vpio.ProcessSpec{})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	first, err := sess.Wait()
	if err != nil {
		t.Fatalf("Wait: %v", err)
	}

	type outcome struct {
		status vpio.ExitStatus
		err    error
	}
	again := make(chan outcome, 1)
	go func() {
		s, e := sess.Wait()
		again <- outcome{s, e}
	}()

	select {
	case got := <-again:
		if got.status != first || got.err != nil {
			t.Errorf("second Wait = %+v, %v; want the cached %+v, nil", got.status, got.err, first)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("a second Wait blocked forever — the result channel was drained by the first")
	}
}
