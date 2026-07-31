package goplugin

import (
	"bytes"
	"context"
	"errors"
	"io"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

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
		sess, err := NewLauncher(fc, &pb.RunStart{}).Start(ctx, vpio.ProcessSpec{Stdout: io.Discard, Stderr: io.Discard})
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

// TestSession_ResizeRelaysOntoTheWire is also the pin on the invariant Start's
// doc states: Session.Resize is INDEPENDENT of ProcessSpec.Stdin. The spec here
// carries a nil Stdin and the relay is still required, which is precisely why a
// Launcher may not infer "no resize is ever coming" from a nil Stdin — the
// shortcut that would otherwise close the resize channel early and end
// ptyrunner's initialResizeWait (taskloom `trim-viper`). Verified to bite:
// applying that shortcut (s.stop() when spec.Stdin == nil) fails this test with
// "expected at least one resize event to reach the transport".
func TestSession_ResizeRelaysOntoTheWire(t *testing.T) {
	// Run is held open so the resizes happen against a LIVE session, which is
	// the only state in which a relay is meaningful — a session whose Run has
	// already returned releases itself (U153-F02) and drops resizes by design.
	// Without the hold, whether a resize relays would depend on whether the
	// test goroutine beat the Run goroutine, which is not a property to assert.
	fc := &fakeClient{resizeDone: make(chan struct{}), block: make(chan struct{})}
	ctx, cancel := context.WithCancel(context.Background())

	sess, err := NewLauncher(fc, &pb.RunStart{}).Start(ctx, vpio.ProcessSpec{Stdout: io.Discard, Stderr: io.Discard})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}

	sess.Resize(24, 80)
	sess.Resize(30, 100) // latest-wins coalescing may drop the first

	close(fc.block) // let the turn end
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
func TestSession_WaitAloneReleasesResourcesWithoutCtxCancellation(t *testing.T) {
	fc := &fakeClient{}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel() // safety net only; must not be required for the assertion below

	sess, err := NewLauncher(fc, &pb.RunStart{}).Start(ctx, vpio.ProcessSpec{Stdout: io.Discard, Stderr: io.Discard})
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

	sess, err := NewLauncher(fc, &pb.RunStart{}).Start(ctx, vpio.ProcessSpec{Stdout: io.Discard, Stderr: io.Discard})
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

	sess, err := NewLauncher(fc, &pb.RunStart{}).Start(ctx, vpio.ProcessSpec{Stdout: io.Discard, Stderr: io.Discard})
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

	sess, err := NewLauncher(fc, &pb.RunStart{}).Start(ctx, vpio.ProcessSpec{Stdout: io.Discard, Stderr: io.Discard})
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

// TestSession_ResizeCoalescesLatestWins characterizes every arm of the
// latest-wins relay before it is simplified: an empty buffer takes the size
// directly, a full buffer has its stale size evicted and replaced, the buffer
// never grows past one, and a released session drops the size instead of
// panicking on a closed channel. Green before and after — collapsing three
// sequential selects into evict-then-send is a pure complexity reduction, so
// no test can discriminate the change (template §4 class 2); what these pin is
// that the behaviour it preserves is actually the behaviour.
func TestSession_ResizeCoalescesLatestWins(t *testing.T) {
	s := &Session{resize: make(chan *pb.WindowSize, 1), done: make(chan struct{})}

	s.Resize(1, 1) // empty buffer
	if len(s.resize) != 1 {
		t.Fatalf("buffered %d sizes after one Resize, want 1", len(s.resize))
	}

	s.Resize(2, 2) // full buffer: evict the stale size
	s.Resize(3, 3) // and again, from full
	if len(s.resize) != 1 {
		t.Fatalf("buffered %d sizes, want 1 — the relay must coalesce, never queue", len(s.resize))
	}

	got := <-s.resize
	if got.Rows != 3 || got.Cols != 3 {
		t.Errorf("relayed %+v, want the LATEST {Rows:3 Cols:3}", got)
	}

	// A released session drops rather than sending on a closed channel.
	s.stop()
	s.Resize(4, 4)
}

// writingClient is a pb.Client that Writes to both sinks, exactly as
// internal/lm/grpc's RunWithModelInfo does for a RunResponse_Stdout /
// RunResponse_Stderr frame: an unconditional Write on the caller's writer.
type writingClient struct{ pb.Client }

func (writingClient) Run(_ context.Context, _ *pb.RunStart, _ io.Reader, stdout, stderr io.Writer, _ <-chan *pb.WindowSize) (int32, error) {
	if _, err := stdout.Write([]byte("engine output\n")); err != nil {
		return 1, err
	}
	if _, err := stderr.Write([]byte("engine diagnostic\n")); err != nil {
		return 1, err
	}
	return 0, nil
}

// TestLauncher_StartRefusesNilStdio pins vpio.ProcessSpec's nil contract on
// this transport: Stdin may be nil (a non-interactive turn), Stdout and Stderr
// may not. Both carry the session's own bytes as distinct wire frames here, and
// the pump Writes each one unconditionally — so a nil sink is not "no output
// wanted", it is a nil-interface Write in a goroutine with no recover above it.
// Measured against the unguarded code: `panic: runtime error: invalid memory
// address or nil pointer dereference`, taking the process with it.
//
// The refusal names which field was empty, which is the whole difference
// between this and both alternatives — a panic from a stack that does not
// mention ProcessSpec, or a silent io.Discard that returns exit 0 having
// delivered nothing.
func TestLauncher_StartRefusesNilStdio(t *testing.T) {
	for _, tc := range []struct {
		name string
		spec vpio.ProcessSpec
		want string
	}{
		{"nil stdout", vpio.ProcessSpec{Stderr: io.Discard}, "Stdout is nil"},
		{"nil stderr", vpio.ProcessSpec{Stdout: io.Discard}, "Stderr is nil"},
		{"both nil", vpio.ProcessSpec{}, "Stdout is nil"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sess, err := NewLauncher(writingClient{}, &pb.RunStart{}).Start(t.Context(), tc.spec)
			require.Error(t, err, "a spec this transport cannot honour must fail Start")
			require.Nil(t, sess)
			require.Contains(t, err.Error(), tc.want, "the refusal must name the empty field")
		})
	}
}

// TestLauncher_StartAcceptsANilStdin is the other half: nil Stdin IS
// sanctioned (vpio.ProcessSpec's own doc), so the guard above must not have
// widened into refusing a legitimate non-interactive turn.
func TestLauncher_StartAcceptsANilStdin(t *testing.T) {
	var out, errs bytes.Buffer
	sess, err := NewLauncher(writingClient{}, &pb.RunStart{}).Start(t.Context(),
		vpio.ProcessSpec{Stdout: &out, Stderr: &errs})
	require.NoError(t, err)
	status, err := sess.Wait()
	require.NoError(t, err)
	require.Equal(t, int32(0), status.Code)
	require.Contains(t, out.String(), "engine output")
	require.Contains(t, errs.String(), "engine diagnostic")
}
