package grpc

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"sync"
	"testing"
	"time"

	"github.com/ctxloom/ctxloom/internal/shared/agent"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	googlegrpc "google.golang.org/grpc"
	"google.golang.org/grpc/metadata"
)

// fakeRunServer captures everything the GRPCServer streams during Run.
// Implements LLM_RunServer well enough for these tests; the
// methods our code path doesn't touch are no-ops.
type fakeRunServer struct {
	sent    []*RunResponse
	recv    []*RunInput // queued inputs Recv returns in order, then io.EOF
	recvIdx int
	ctx     context.Context
}

func (s *fakeRunServer) Send(r *RunResponse) error {
	s.sent = append(s.sent, r)
	return nil
}
func (s *fakeRunServer) Recv() (*RunInput, error) {
	if s.recvIdx >= len(s.recv) {
		return nil, io.EOF
	}
	in := s.recv[s.recvIdx]
	s.recvIdx++
	return in, nil
}
func (s *fakeRunServer) Context() context.Context     { return s.ctx }
func (s *fakeRunServer) SetHeader(metadata.MD) error  { return nil }
func (s *fakeRunServer) SendHeader(metadata.MD) error { return nil }
func (s *fakeRunServer) SetTrailer(metadata.MD)       {}
func (s *fakeRunServer) SendMsg(m any) error          { return s.Send(m.(*RunResponse)) }
func (s *fakeRunServer) RecvMsg(m any) error          { return io.EOF }

func newFakeRunServer() *fakeRunServer {
	return &fakeRunServer{ctx: context.Background()}
}

// runStartInput wraps a RunStart as the first bidi Run input.
func runStartInput(start *RunStart) *RunInput {
	return &RunInput{Input: &RunInput_Start{Start: start}}
}

// Compile-time check that fakeRunServer satisfies the generated stream interface.
var _ googlegrpc.BidiStreamingServer[RunInput, RunResponse] = (*fakeRunServer)(nil)

// TestStreamWriter_StdoutAndStderr exercises the per-byte stream
// writer that GRPCServer.Run uses to fan stdout/stderr from the
// backend into discrete RunResponse messages.
func TestStreamWriter_StdoutAndStderr(t *testing.T) {
	stream := newFakeRunServer()

	var sendMu sync.Mutex
	stdoutW := &streamWriter{stream: stream, sendMu: &sendMu, isStderr: false}
	stderrW := &streamWriter{stream: stream, sendMu: &sendMu, isStderr: true}

	n, err := stdoutW.Write([]byte("hi"))
	require.NoError(t, err)
	assert.Equal(t, 2, n)

	n, err = stderrW.Write([]byte("warn"))
	require.NoError(t, err)
	assert.Equal(t, 4, n)

	require.Len(t, stream.sent, 2)
	out1 := stream.sent[0].GetStdout()
	out2 := stream.sent[1].GetStderr()
	assert.Equal(t, "hi", string(out1))
	assert.Equal(t, "warn", string(out2))
}

func TestStreamWriter_PropagatesSendError(t *testing.T) {
	want := errors.New("send broken")
	stream := &erroringRunServer{err: want}
	w := &streamWriter{stream: stream, sendMu: &sync.Mutex{}}
	_, err := w.Write([]byte("x"))
	assert.ErrorIs(t, err, want)
}

// erroringRunServer is the fakeRunServer variant whose Send returns an
// error; used to confirm streamWriter surfaces send failures rather
// than swallowing them.
type erroringRunServer struct {
	fakeRunServer
	err error
}

func (e *erroringRunServer) Send(r *RunResponse) error { return e.err }

func TestConvertModelInfoToProto(t *testing.T) {
	t.Run("nil_in_nil_out", func(t *testing.T) {
		assert.Nil(t, convertModelInfoToProto(nil))
	})
	t.Run("happy_path", func(t *testing.T) {
		out := convertModelInfoToProto(&agent.ModelInfo{
			ModelName:    "claude-sonnet-4-6",
			ModelVersion: "4.6",
			Provider:     "anthropic",
		})
		require.NotNil(t, out)
		assert.Equal(t, "claude-sonnet-4-6", out.ModelName)
		assert.Equal(t, "4.6", out.ModelVersion)
		assert.Equal(t, "anthropic", out.Provider)
	})
}

// fakeBackend implements just enough of agent.Backend to drive
// GRPCServer.Run / GRPCServer.Info in tests. No real LLM, no real
// session storage.
type fakeBackend struct {
	name           string
	version        string
	modes          []agent.ExecutionMode
	setupCalled    bool
	setupErr       error
	executeResult  *agent.ExecuteResult
	executeErr     error
	cleanupCalled  bool
	cleanupErr     error
	captureStdout  string
	captureStderr  string
	history        agent.SessionHistory
	capturedPerm   agent.PermissionMode // posture the server decoded into the request
	capturedPrompt string               // req.Prompt.Content the server actually handed to Execute
	// Cells the server decoded into the Setup / Execute requests, so a test can
	// prove cell_kind flows onto BOTH.
	capturedSetupCell   agent.CellKind
	capturedExecuteCell agent.CellKind
}

func (f *fakeBackend) Name() string                          { return f.name }
func (f *fakeBackend) Version() string                       { return f.version }
func (f *fakeBackend) SupportedModes() []agent.ExecutionMode { return f.modes }
func (f *fakeBackend) History() agent.SessionHistory         { return f.history }

func (f *fakeBackend) Setup(ctx context.Context, req *agent.SetupRequest) error {
	f.setupCalled = true
	f.capturedSetupCell = req.CellKind
	return f.setupErr
}

func (f *fakeBackend) Execute(ctx context.Context, req *agent.ExecuteRequest, stdout, stderr io.Writer) (*agent.ExecuteResult, error) {
	f.capturedPerm = req.Permissions
	f.capturedExecuteCell = req.CellKind
	f.capturedPrompt = agent.GetPromptContent(req.Prompt)
	if f.captureStdout != "" {
		_, _ = stdout.Write([]byte(f.captureStdout))
	}
	if f.captureStderr != "" {
		_, _ = stderr.Write([]byte(f.captureStderr))
	}
	if f.executeErr != nil {
		return nil, f.executeErr
	}
	if f.executeResult != nil {
		return f.executeResult, nil
	}
	return &agent.ExecuteResult{ExitCode: 0}, nil
}

func (f *fakeBackend) Cleanup(ctx context.Context) error {
	f.cleanupCalled = true
	return f.cleanupErr
}

func TestGRPCServer_Info_ReportsBackendMetadata(t *testing.T) {
	srv := &GRPCServer{Impl: &fakeBackend{
		name:    "claude-code",
		version: "1.2.3",
		modes:   []agent.ExecutionMode{agent.ExecutionMode(0), agent.ExecutionMode(1)},
	}}
	info, err := srv.Info(context.Background(), &Empty{})
	require.NoError(t, err)
	assert.Equal(t, "claude-code", info.Name)
	assert.Equal(t, "1.2.3", info.Version)
	assert.Len(t, info.SupportedModes, 2)
}

func TestGRPCServer_Run_FullLifecycle(t *testing.T) {
	backend := &fakeBackend{
		name:          "claude-code",
		captureStdout: "model output\n",
		captureStderr: "model warning\n",
		executeResult: &agent.ExecuteResult{
			ExitCode: 0,
			ModelInfo: &agent.ModelInfo{
				ModelName: "claude-haiku",
				Provider:  "anthropic",
			},
		},
	}
	srv := &GRPCServer{Impl: backend}
	stream := newFakeRunServer()

	stream.recv = []*RunInput{runStartInput(&RunStart{
		Prompt:    &Fragment{Content: "hi"},
		Fragments: []*Fragment{{Content: "ctx"}},
		Options:   &RunOptions{WorkDir: "/tmp", PermissionMode: agent.PermissionBypass.String()},
	})}
	err := srv.Run(stream)
	require.NoError(t, err)

	// Setup + Cleanup called once each; stdout/stderr/exit all sent.
	assert.True(t, backend.setupCalled, "Setup must run by default")
	assert.True(t, backend.cleanupCalled, "Cleanup must always run")

	// Inspect the message types streamed back.
	var hasStdout, hasStderr, hasExit bool
	for _, msg := range stream.sent {
		switch msg.Output.(type) {
		case *RunResponse_Stdout:
			hasStdout = true
		case *RunResponse_Stderr:
			hasStderr = true
		case *RunResponse_ExitCode:
			hasExit = true
			assert.Equal(t, int32(0), msg.GetExitCode())
			require.NotNil(t, msg.ModelInfo)
			assert.Equal(t, "claude-haiku", msg.ModelInfo.ModelName)
		}
	}
	assert.True(t, hasStdout && hasStderr && hasExit,
		"expected stdout + stderr + exit_code in stream; got stdout=%v stderr=%v exit=%v",
		hasStdout, hasStderr, hasExit)
}

// TestGRPCServer_Run_HeadlessFloorsWouldBlockPosture pins fix O: the server's
// decode boundary floors a would-block posture up to bypass for a ONESHOT (a
// headless run has no human to answer the engine and would hang), as defense in
// depth for a gRPC caller that didn't apply the CLI resolver's floor. A
// safe-headless posture (plan/bypass) and interactive runs are left untouched.
func TestGRPCServer_Run_HeadlessFloorsWouldBlockPosture(t *testing.T) {
	cases := []struct {
		name string
		mode ExecutionMode
		perm agent.PermissionMode
		want agent.PermissionMode
	}{
		{"oneshot floors default to bypass", ExecutionMode_ONESHOT, agent.PermissionDefault, agent.PermissionBypass},
		{"oneshot floors acceptEdits to bypass", ExecutionMode_ONESHOT, agent.PermissionAcceptEdits, agent.PermissionBypass},
		{"oneshot keeps safe-headless plan", ExecutionMode_ONESHOT, agent.PermissionPlan, agent.PermissionPlan},
		{"oneshot keeps bypass", ExecutionMode_ONESHOT, agent.PermissionBypass, agent.PermissionBypass},
		{"interactive keeps default", ExecutionMode_INTERACTIVE, agent.PermissionDefault, agent.PermissionDefault},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			backend := &fakeBackend{name: "capture", executeResult: &agent.ExecuteResult{ExitCode: 0}}
			srv := &GRPCServer{Impl: backend}
			stream := newFakeRunServer()
			stream.recv = []*RunInput{runStartInput(&RunStart{
				Prompt:  &Fragment{Content: "hi"},
				Options: &RunOptions{WorkDir: "/tmp", Mode: tc.mode, PermissionMode: tc.perm.String()},
			})}
			require.NoError(t, srv.Run(stream))
			assert.Equal(t, tc.want, backend.capturedPerm)
		})
	}
}

func TestGRPCServer_Run_SkipSetup(t *testing.T) {
	backend := &fakeBackend{executeResult: &agent.ExecuteResult{ExitCode: 0}}
	srv := &GRPCServer{Impl: backend}
	stream := newFakeRunServer()

	stream.recv = []*RunInput{runStartInput(&RunStart{
		Options: &RunOptions{SkipSetup: true},
	})}
	err := srv.Run(stream)
	require.NoError(t, err)
	assert.False(t, backend.setupCalled, "SkipSetup must skip Setup")
	assert.True(t, backend.cleanupCalled, "Cleanup runs regardless")
}

// TestGRPCServer_Run_SkipSetupDeliversFragmentsViaPrompt is the regression for
// dire-petal (SILENT NO-OP): the oneshot "none"-isolation member path
// (operations/oneshot.go's runResolvedAgent) sets BOTH SkipSetup:true and
// Fragments:[{Content: composedContext}] — SkipSetup skips Setup, and Setup was
// the ONLY path that ever converted+delivered req.Fragments to the backend, so
// the composed context used to be silently discarded: the member ran
// context-free, reported exit 0, and produced plausible-looking output with
// zero context delivered. A sentinel string planted in Fragments (something the
// backend could not otherwise produce) must reach Execute's Prompt — the one
// channel a SkipSetup run still has — proving delivery, not just non-crash.
func TestGRPCServer_Run_SkipSetupDeliversFragmentsViaPrompt(t *testing.T) {
	const sentinel = "CTXLOOM-DIRE-PETAL-SENTINEL-7f3ac1"
	backend := &fakeBackend{executeResult: &agent.ExecuteResult{ExitCode: 0}}
	srv := &GRPCServer{Impl: backend}
	stream := newFakeRunServer()

	stream.recv = []*RunInput{runStartInput(&RunStart{
		Prompt:    &Fragment{Content: "do the task"},
		Fragments: []*Fragment{{Content: sentinel}},
		Options:   &RunOptions{SkipSetup: true},
	})}
	err := srv.Run(stream)
	require.NoError(t, err)
	assert.False(t, backend.setupCalled, "SkipSetup must still skip Setup — this is not a route back to the full setup path")
	assert.Contains(t, backend.capturedPrompt, sentinel,
		"a SkipSetup run's Fragments must reach the backend somehow (smuggled into the prompt) instead of being silently dropped")
	assert.Contains(t, backend.capturedPrompt, "do the task",
		"smuggling the fragment content must not clobber the original task prompt")
}

func TestGRPCServer_Run_ExecuteErrorPropagates(t *testing.T) {
	want := errors.New("execute failed")
	backend := &fakeBackend{executeErr: want}
	srv := &GRPCServer{Impl: backend}
	stream := newFakeRunServer()

	stream.recv = []*RunInput{runStartInput(&RunStart{Options: &RunOptions{}})}
	err := srv.Run(stream)
	assert.ErrorIs(t, err, want)
	// A failed launch must still release the backend's resources — and the
	// warn-only cleanup must not mask the Execute error asserted above.
	assert.True(t, backend.cleanupCalled, "Cleanup must run even when Execute fails")
}

// stdinClosingBackend simulates the pty stdin copier exiting mid-run: Execute
// consumes one stdin byte, releases the wire stdin (the io.PipeReader the
// server's stream pump writes into), then waits for a resize to prove the pump
// kept draining Recv after the stdin write started failing.
//
// It releases by calling req.StdinCleanup, which is exactly what ptyrunner
// does, and deliberately NOT by type-asserting req.Stdin to an io.Closer. That
// distinction is the point: a backend that closed the reader itself would keep
// this test green even if GRPCServer.Run stopped supplying a cleanup entirely,
// so the assertion would cover the pump alone and never the wiring that feeds
// it. Reaching through the real field is what makes a broken thread visible
// here.
type stdinClosingBackend struct {
	fakeBackend
	resize   agent.WindowSize
	resizeOK bool
}

func (f *stdinClosingBackend) Execute(ctx context.Context, req *agent.ExecuteRequest, stdout, stderr io.Writer) (*agent.ExecuteResult, error) {
	buf := make([]byte, 1)
	if _, err := req.Stdin.Read(buf); err != nil {
		return nil, fmt.Errorf("read stdin: %w", err)
	}
	if req.StdinCleanup == nil {
		return nil, errors.New("no StdinCleanup reached the backend: GRPCServer.Run owns the stdin pipe and must supply its release, or nothing can retire the wire stdin and the stream pump parks forever on the next write")
	}
	req.StdinCleanup()
	select {
	case ws, ok := <-req.Resize:
		f.resize, f.resizeOK = ws, ok
	case <-time.After(5 * time.Second):
		return nil, errors.New("resize never arrived: the stream pump stopped draining after the stdin pipe closed")
	}
	return &agent.ExecuteResult{ExitCode: 0}, nil
}

// TestGRPCServer_Run_ResizeStillFlowsAfterStdinPipeCloses is the regression
// for the wedged-pump bug: once the pty's stdin copier released the pipe
// reader, the pump's next stdinW.Write must fail fast (not park forever) and
// the pump must keep consuming Recv so resize messages still reach the pty.
//
// Because the backend releases through req.StdinCleanup, this also pins the
// wiring: GRPCServer.Run must hand its own pipe's closer down the turn. Pass
// nil there and this goes red rather than silently reverting to the wedge.
func TestGRPCServer_Run_ResizeStillFlowsAfterStdinPipeCloses(t *testing.T) {
	backend := &stdinClosingBackend{}
	srv := &GRPCServer{Impl: backend}
	stream := newFakeRunServer()

	stream.recv = []*RunInput{
		runStartInput(&RunStart{Options: &RunOptions{SkipSetup: true}}),
		{Input: &RunInput_Stdin{Stdin: []byte("a")}}, // consumed by Execute, which then closes the pipe
		{Input: &RunInput_Stdin{Stdin: []byte("b")}}, // hits the closed pipe → ErrClosedPipe, not a parked Write
		{Input: &RunInput_Resize{Resize: &WindowSize{Rows: 50, Cols: 120}}},
	}
	err := srv.Run(stream)
	require.NoError(t, err)
	require.True(t, backend.resizeOK, "resize must be delivered after the stdin pipe closes")
	assert.Equal(t, uint16(50), backend.resize.Rows)
	assert.Equal(t, uint16(120), backend.resize.Cols)
}

func TestGRPCServer_Run_NilOptionsTreatedAsEmpty(t *testing.T) {
	// Defensive — protobuf nil options shouldn't panic.
	srv := &GRPCServer{Impl: &fakeBackend{executeResult: &agent.ExecuteResult{ExitCode: 0}}}
	stream := newFakeRunServer()
	stream.recv = []*RunInput{runStartInput(&RunStart{})}
	err := srv.Run(stream)
	require.NoError(t, err)
}

// launchCapturingBackend embeds BaseBackend and routes Execute through the
// injected Launcher, capturing the resulting LaunchSpec. It is a real backend
// (not a hand-rolled fake) so the whole WorkDir → SetWorkDir → LaunchSpec.WorkDir
// chain runs, letting a test assert the cwd the runtime would exec in.
type launchCapturingBackend struct {
	agent.BaseBackend
	captured agent.LaunchSpec
}

func newLaunchCapturingBackend() *launchCapturingBackend {
	b := &launchCapturingBackend{BaseBackend: agent.NewBaseBackend("capture", "1.0.0")}
	b.BinaryPath = "/bin/true"
	b.SetLauncher(func(_ context.Context, spec agent.LaunchSpec, _ io.Reader, _, _ io.Writer, _ <-chan agent.WindowSize) (int32, error) {
		b.captured = spec
		return 0, nil
	})
	return b
}

func (b *launchCapturingBackend) History() agent.SessionHistory { return nil }

func (b *launchCapturingBackend) Setup(_ context.Context, req *agent.SetupRequest) error {
	b.SetWorkDir(req.WorkDir)
	return nil
}

func (b *launchCapturingBackend) Cleanup(_ context.Context) error { return nil }

func (b *launchCapturingBackend) Execute(ctx context.Context, req *agent.ExecuteRequest, stdout, stderr io.Writer) (*agent.ExecuteResult, error) {
	code, err := b.RunNonInteractive(ctx, nil, req.Env, nil, stdout, stderr)
	return &agent.ExecuteResult{ExitCode: code}, err
}

// TestGRPCServer_Run_SkipSetupHonorsWorkDir is the regression for the SkipSetup
// cwd gap: on the oneshot SkipSetup path Setup is skipped, so its
// SetWorkDir never runs; the passed WorkDir must still reach the child (the
// engine's cwd) instead of defaulting to the plugin's inherited "." — otherwise
// per-agent isolation can never set a workspace. Before the fix the captured
// spec.WorkDir was ".".
func TestGRPCServer_Run_SkipSetupHonorsWorkDir(t *testing.T) {
	backend := newLaunchCapturingBackend()
	srv := &GRPCServer{Impl: backend}
	stream := newFakeRunServer()

	stream.recv = []*RunInput{runStartInput(&RunStart{
		Prompt:  &Fragment{Content: "hi"},
		Options: &RunOptions{WorkDir: "/tmp/agent-workspace", Mode: ExecutionMode_ONESHOT, SkipSetup: true},
	})}
	err := srv.Run(stream)
	require.NoError(t, err)
	assert.Equal(t, "/tmp/agent-workspace", backend.captured.WorkDir,
		"SkipSetup run must exec in the passed WorkDir, not the plugin's inherited cwd")
}

// Silence unused import in case bytes goes unused after a future edit.
var _ = bytes.NewBuffer
