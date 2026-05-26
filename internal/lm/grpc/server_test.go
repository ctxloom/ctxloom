package grpc

import (
	"bytes"
	"context"
	"errors"
	"io"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	googlegrpc "google.golang.org/grpc"
	"google.golang.org/grpc/metadata"

	"github.com/ctxloom/ctxloom/internal/lm/backends"
)

// fakeRunServer captures everything the GRPCServer streams during Run.
// Implements AIPlugin_RunServer well enough for these tests; the
// methods our code path doesn't touch are no-ops.
type fakeRunServer struct {
	sent []*RunResponse
	ctx  context.Context
}

func (s *fakeRunServer) Send(r *RunResponse) error {
	s.sent = append(s.sent, r)
	return nil
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

// Compile-time check that fakeRunServer satisfies the generated stream interface.
var _ googlegrpc.ServerStreamingServer[RunResponse] = (*fakeRunServer)(nil)

// TestStreamWriter_StdoutAndStderr exercises the per-byte stream
// writer that GRPCServer.Run uses to fan stdout/stderr from the
// backend into discrete RunResponse messages.
func TestStreamWriter_StdoutAndStderr(t *testing.T) {
	stream := newFakeRunServer()

	stdoutW := &streamWriter{stream: stream, isStderr: false}
	stderrW := &streamWriter{stream: stream, isStderr: true}

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
	w := &streamWriter{stream: stream}
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
		out := convertModelInfoToProto(&backends.ModelInfo{
			ModelName: "claude-sonnet-4-6",
			ModelVersion: "4.6",
			Provider: "anthropic",
		})
		require.NotNil(t, out)
		assert.Equal(t, "claude-sonnet-4-6", out.ModelName)
		assert.Equal(t, "4.6", out.ModelVersion)
		assert.Equal(t, "anthropic", out.Provider)
	})
}

// fakeBackend implements just enough of backends.Backend to drive
// GRPCServer.Run / GRPCServer.Info in tests. No real LLM, no real
// session storage.
type fakeBackend struct {
	name           string
	version        string
	modes          []backends.ExecutionMode
	setupCalled    bool
	executeResult  *backends.ExecuteResult
	executeErr     error
	cleanupCalled  bool
	cleanupErr     error
	captureStdout  string
	captureStderr  string
}

func (f *fakeBackend) Name() string                             { return f.name }
func (f *fakeBackend) Version() string                          { return f.version }
func (f *fakeBackend) SupportedModes() []backends.ExecutionMode { return f.modes }
func (f *fakeBackend) Skills() backends.SkillRegistry           { return nil }
func (f *fakeBackend) MCP() backends.MCPManager                 { return nil }
func (f *fakeBackend) Context() backends.ContextProvider        { return nil }
func (f *fakeBackend) Lifecycle() backends.LifecycleHandler     { return nil }
func (f *fakeBackend) History() backends.SessionHistory         { return nil }

func (f *fakeBackend) Setup(ctx context.Context, req *backends.SetupRequest) error {
	f.setupCalled = true
	return nil
}

func (f *fakeBackend) Execute(ctx context.Context, req *backends.ExecuteRequest, stdout, stderr io.Writer) (*backends.ExecuteResult, error) {
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
	return &backends.ExecuteResult{ExitCode: 0}, nil
}

func (f *fakeBackend) Cleanup(ctx context.Context) error {
	f.cleanupCalled = true
	return f.cleanupErr
}

func TestGRPCServer_Info_ReportsBackendMetadata(t *testing.T) {
	srv := &GRPCServer{Impl: &fakeBackend{
		name:    "claude-code",
		version: "1.2.3",
		modes:   []backends.ExecutionMode{backends.ExecutionMode(0), backends.ExecutionMode(1)},
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
		executeResult: &backends.ExecuteResult{
			ExitCode: 0,
			ModelInfo: &backends.ModelInfo{
				ModelName: "claude-haiku",
				Provider:  "anthropic",
			},
		},
	}
	srv := &GRPCServer{Impl: backend}
	stream := newFakeRunServer()

	err := srv.Run(&RunRequest{
		Prompt:    &Fragment{Content: "hi"},
		Fragments: []*Fragment{{Content: "ctx"}},
		Options:   &RunOptions{WorkDir: "/tmp", AutoApprove: true},
	}, stream)
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

func TestGRPCServer_Run_SkipSetup(t *testing.T) {
	backend := &fakeBackend{executeResult: &backends.ExecuteResult{ExitCode: 0}}
	srv := &GRPCServer{Impl: backend}
	stream := newFakeRunServer()

	err := srv.Run(&RunRequest{
		Options: &RunOptions{SkipSetup: true},
	}, stream)
	require.NoError(t, err)
	assert.False(t, backend.setupCalled, "SkipSetup must skip Setup")
	assert.True(t, backend.cleanupCalled, "Cleanup runs regardless")
}

func TestGRPCServer_Run_ExecuteErrorPropagates(t *testing.T) {
	want := errors.New("execute failed")
	srv := &GRPCServer{Impl: &fakeBackend{executeErr: want}}
	stream := newFakeRunServer()

	err := srv.Run(&RunRequest{Options: &RunOptions{}}, stream)
	assert.ErrorIs(t, err, want)
}

func TestGRPCServer_Run_NilOptionsTreatedAsEmpty(t *testing.T) {
	// Defensive — protobuf nil options shouldn't panic.
	srv := &GRPCServer{Impl: &fakeBackend{executeResult: &backends.ExecuteResult{ExitCode: 0}}}
	stream := newFakeRunServer()
	err := srv.Run(&RunRequest{}, stream)
	require.NoError(t, err)
}

// Silence unused import in case bytes goes unused after a future edit.
var _ = bytes.NewBuffer
