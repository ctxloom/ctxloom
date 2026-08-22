package grpc

import (
	"bytes"
	"context"
	"io"
	"net"
	"testing"
	"time"

	"github.com/ctxloom/ctxloom/internal/shared/agent"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	googlegrpc "google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/test/bufconn"
)

// stdinDrainBackend is an engine that consumes its whole stdin before it can
// produce anything: Execute blocks in io.ReadAll until the plugin-side stdin
// pipe CLOSES, then echoes what it read behind a marker. That makes its output
// a direct observation of the half-close reaching the server — the marker can
// only ever appear after GRPCServer.Run's Recv loop ended and closed stdinW,
// which happens only when the client half-closes the send direction. A
// backend that merely returned would prove nothing: the turn would end for
// reasons of its own.
type stdinDrainBackend struct {
	// entered closes just before the engine blocks on its stdin read. It is
	// the proof-of-execution seam: a turn that never got off the ground
	// (stream never opened, start never dispatched, backend never reached)
	// would otherwise fail this test's hang guard for a reason that has
	// nothing to do with the half-close.
	entered chan struct{}
}

func (b *stdinDrainBackend) Name() string                                     { return "stdin-drain" }
func (b *stdinDrainBackend) Version() string                                  { return "test" }
func (b *stdinDrainBackend) SupportedModes() []agent.ExecutionMode            { return nil }
func (b *stdinDrainBackend) History() agent.SessionHistory                    { return nil }
func (b *stdinDrainBackend) Setup(context.Context, *agent.SetupRequest) error { return nil }
func (b *stdinDrainBackend) Cleanup(context.Context) error                    { return nil }

func (b *stdinDrainBackend) Execute(_ context.Context, req *agent.ExecuteRequest, stdout, _ io.Writer) (*agent.ExecuteResult, error) {
	if req.Stdin == nil {
		return nil, io.ErrUnexpectedEOF
	}
	close(b.entered)
	data, err := io.ReadAll(req.Stdin)
	if err != nil {
		return nil, err
	}
	if _, werr := stdout.Write(append([]byte("stdin-eof:"), data...)); werr != nil {
		return nil, werr
	}
	return &agent.ExecuteResult{ExitCode: 9}, nil
}

// runStreamHangGuard bounds how long the test waits for a turn that, when the
// defect is present, waits FOREVER. It is a hang detector, not a performance
// assertion: the fixed path half-closes the moment stdin reports EOF, so a
// generous bound cannot turn a broken implementation green — only an infinite
// wait becomes a legible failure.
const runStreamHangGuard = 15 * time.Second

// TestGRPCClient_RunWithModelInfo_FiniteStdinHalfClosesTheSendDirection pins
// dreary-stegosaur: a finite (non-tty) stdin handed to RunWithModelInfo — a
// vpio ProcessSpec.Stdin from a test, the container-run driver, or any
// embedder — must half-close the Run stream's send direction when it reports
// io.EOF. Without that the plugin-side stdin pipe stays open and an engine
// that reads to EOF blocks until the turn's deadline.
//
// Driven over a REAL gRPC transport (bufconn) rather than an in-memory fake
// stream, because CloseSend's effect is a TRANSPORT event: the server's Recv
// returning io.EOF. A fake that never modelled half-close could not tell the
// two behaviours apart.
func TestGRPCClient_RunWithModelInfo_FiniteStdinHalfClosesTheSendDirection(t *testing.T) {
	lis := bufconn.Listen(1 << 20)
	grpcServer := googlegrpc.NewServer()
	backend := &stdinDrainBackend{entered: make(chan struct{})}
	RegisterLLMServer(grpcServer, &GRPCServer{Impl: backend})

	serveErr := make(chan error, 1)
	go func() { serveErr <- grpcServer.Serve(lis) }()

	conn, err := googlegrpc.NewClient(
		"passthrough:///bufnet",
		googlegrpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) {
			return lis.DialContext(ctx)
		}),
		googlegrpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	require.NoError(t, err)

	// NO deadline on this context, deliberately: a turn that completes only
	// because its context expired is the very failure this test exists to
	// catch, so expiry must not be available as an alternative explanation
	// for completion. Cancel is cleanup only, after the outcome is decided.
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(func() {
		cancel()
		_ = conn.Close()
		grpcServer.Stop()
		<-serveErr
	})

	const payload = "keystrokes-then-eof"

	// A finite source: an io.Pipe the writer closes. Its Read returns io.EOF
	// once, exactly as a file, a bytes.Reader, or a closed OS pipe would.
	stdinR, stdinW := io.Pipe()
	go func() {
		_, _ = io.WriteString(stdinW, payload)
		_ = stdinW.Close()
	}()

	client := &GRPCClient{client: NewLLMClient(conn)}
	req := &RunStart{
		Prompt:  &Fragment{Content: "hi"},
		Options: &RunOptions{SkipSetup: true},
	}

	type outcome struct {
		res *RunResult
		err error
	}
	done := make(chan outcome, 1)
	var stdout, stderr bytes.Buffer
	go func() {
		res, rerr := client.RunWithModelInfo(ctx, req, stdinR, &stdout, &stderr, nil)
		done <- outcome{res: res, err: rerr}
	}()

	select {
	case got := <-done:
		require.NoError(t, got.err)
		require.NotNil(t, got.res)
		// THE mechanism assertion. This exact string exists only if the
		// engine's io.ReadAll returned, i.e. the server closed the
		// plugin-side stdin pipe, i.e. CloseSend crossed the wire — and it
		// carries the payload, so the keystrokes were delivered BEFORE the
		// close rather than truncated by it.
		assert.Equal(t, "stdin-eof:"+payload, stdout.String(),
			"the engine must observe stdin EOF and see every byte written before it")
		assert.Equal(t, int32(9), got.res.ExitCode,
			"the turn's terminal exit code must arrive, which it can only do after the engine's stdin read completed")
	case <-time.After(runStreamHangGuard):
		select {
		case <-backend.entered:
		default:
			t.Fatal("the engine never reached its stdin read within the hang guard — this turn never got off the ground, so the failure below would say nothing about the half-close. Fix the harness, not the pump.")
		}
		t.Fatalf("RunWithModelInfo did not complete within %s: the stdin pump saw io.EOF and returned without half-closing the send direction, so the plugin-side stdin pipe never closed and the engine is still parked in its read", runStreamHangGuard)
	}
}
