// Package goplugin is the go-plugin implementation of the
// VIRTUALIZED-PROCESS-IO seam (internal/vpio): it wraps the existing
// hashicorp/go-plugin-backed bidirectional Run RPC (internal/lm/grpc,
// llm.proto's `Run`) behind vpio.Launcher/vpio.Session.
//
// SWAP POINT: this is the seam's go-plugin transport. internal/vpio/dockerexec
// is a second, SHIPPED implementation of vpio.Launcher/vpio.Session (the
// `docker exec -it` transport for the container-isolation runtime) — above-
// the-seam callers (internal/cli/run.go, internal/cli/init.go,
// internal/termui) reference only vpio types and never this package's
// concrete types, so they needed no change when it landed. Only a host-pty
// implementation remains registered future work.
//
// This package does not modify internal/lm/grpc's Run RPC, its client
// implementation (GRPCClient/LLMRunner), or the wire protocol (llm.proto) —
// it drives the unchanged pb.Client.Run exactly as the pre-extraction call
// site did, just from behind Start/Session instead of one blocking call. Nor
// does it rename the upstream github.com/hashicorp/go-plugin dependency; it wraps it.
package goplugin

import (
	"context"
	"sync"

	pb "github.com/ctxloom/ctxloom/internal/lm/grpc"
	"github.com/ctxloom/ctxloom/internal/vpio"
)

// Launcher binds an already-spawned plugin client (spawned upstream via
// policy.SpawnClient, before the interactive turn begins)
// and its resolved RunStart to a vpio.Launcher. Start begins the
// interactive Run turn on that already-live connection; it does not spawn a
// new OS process or container — that already happened.
type Launcher struct {
	client pb.Client
	req    *pb.RunStart
}

var _ vpio.Launcher = (*Launcher)(nil)

// NewLauncher builds a Launcher for one interactive turn against client,
// carrying the resolved RunStart req.
func NewLauncher(client pb.Client, req *pb.RunStart) *Launcher {
	return &Launcher{client: client, req: req}
}

// runResult is the outcome client.Run eventually delivers.
type runResult struct {
	code int32
	err  error
}

// Start begins the interactive Run turn: it hands spec's stdio to
// client.Run exactly as the pre-extraction call site did (same signature,
// same goroutine-pump semantics inside GRPCClient.RunWithModelInfo — MOVED,
// not rewritten) and returns a Session that relays Resize calls onto the
// same resize channel client.Run already understands. Always succeeds
// synchronously here (the underlying transport is already connected); the
// error return is load-bearing for the sibling dockerexec transport, whose
// Start CAN fail synchronously (a missing runtime binary or `docker exec`
// refusing to attach — see dockerexec.Session's TestStart_FailsWhenBinaryMissing).
func (l *Launcher) Start(ctx context.Context, spec vpio.ProcessSpec) (vpio.Session, error) {
	resizeCh := make(chan *pb.WindowSize, 1)
	s := &Session{resize: resizeCh, result: make(chan runResult, 1)}

	// The resize channel must eventually close so client.Run's internal
	// resize-pump goroutine (internal/lm/grpc's RunWithModelInfo, unchanged)
	// doesn't park forever — mirrors the pre-extraction contract, where the
	// SIGWINCH-sourced channel itself closed on ctx.Done() (see
	// internal/cli/run_resize_unix.go).
	//
	// NOTE (DEFECT T2, deliberately NOT fixed here): a run.go caller whose
	// interactive turn has no real tty passes a nil ProcessSpec.Stdin, and
	// above-the-seam's pumpResize is then a no-op — so in that case nothing
	// will ever call Session.Resize below, and this resizeCh sits live,
	// unfed, and open until ctx.Done() (i.e. the whole run), forcing
	// ptyrunner's pre-Start wait to always run its full initialResizeWait.
	// Deciding "no resize is coming" from spec.Stdin here was tried and
	// reverted: it broke TestSession_ResizeRelaysOntoTheWire, which
	// deliberately calls Resize with a nil-Stdin ProcessSpec and expects it
	// to relay — Session.Resize is documented as independent of Stdin, so a
	// Launcher cannot infer "no resize ever" from Stdin's nilness alone.
	// Fixing this properly needs an explicit "no resize" signal from the
	// caller (e.g. a new vpio.ProcessSpec field, threaded from run.go's own
	// nil-resize local), which is outside the files this fix is scoped to.
	go func() {
		<-ctx.Done()
		s.stop()
	}()

	go func() {
		code, err := l.client.Run(ctx, l.req, spec.Stdin, spec.Stdout, spec.Stderr, resizeCh)
		s.result <- runResult{code: code, err: err}
	}()

	return s, nil
}

// Session is the goplugin vpio.Session.
type Session struct {
	mu     sync.Mutex
	resize chan *pb.WindowSize
	closed bool
	result chan runResult

	waitOnce sync.Once
	status   vpio.ExitStatus
	waitErr  error
}

var _ vpio.Session = (*Session)(nil)

// Resize relays onto the bidi stream's resize channel — the same
// latest-wins coalescing send watchResize (internal/cli/run_resize_unix.go)
// performed before extraction, now done here since Resize is a method call
// rather than a channel the transport ranges over directly. Never blocks: a
// full buffer evicts the pending (stale) size, and a session that has
// already ended (or is ending concurrently) silently drops the resize
// rather than racing the close.
func (s *Session) Resize(rows, cols uint32) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return
	}
	ws := &pb.WindowSize{Rows: rows, Cols: cols}
	select {
	case s.resize <- ws:
		return
	default:
	}
	select {
	case <-s.resize:
	default:
	}
	select {
	case s.resize <- ws:
	default:
	}
}

// Wait blocks for client.Run's terminal result. Idempotent: the result is
// delivered once and cached, so every later call returns it again instead of
// parking on a channel nothing will write to a second time. The sibling
// transport (dockerexec.Session) makes the same promise, so a caller writing
// the natural `defer session.Wait()` behaves the same behind either one.
func (s *Session) Wait() (vpio.ExitStatus, error) {
	s.waitOnce.Do(func() {
		r := <-s.result
		s.status = vpio.ExitStatus{Code: r.code}
		s.waitErr = r.err
	})
	return s.status, s.waitErr
}

// stop marks the session closed and closes the resize channel, exactly
// once, race-free against a concurrent Resize call.
func (s *Session) stop() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return
	}
	s.closed = true
	close(s.resize)
}
