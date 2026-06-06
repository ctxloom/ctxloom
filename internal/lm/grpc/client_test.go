package grpc

import (
	"bytes"
	"context"
	"errors"
	"io"
	"testing"

	"github.com/hashicorp/go-hclog"
	"github.com/hashicorp/go-plugin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	googlegrpc "google.golang.org/grpc"
	"google.golang.org/grpc/metadata"
)

// fakeLLMClient implements LLMClient for unit-testing
// GRPCClient against a controlled set of responses. No real gRPC,
// no network — pure in-process.
type fakeLLMClient struct {
	infoResp *LLMInfo
	infoErr  error

	runStream LLM_RunClient
	runErr    error

	sessionResp *SessionData
	sessionErr  error
	listResp    *SessionList
	listErr     error
	plansResp   *PlansData
	plansErr    error

	gotInfoCalls int
	gotRunCalls  int
}

func (f *fakeLLMClient) Info(ctx context.Context, in *Empty, opts ...googlegrpc.CallOption) (*LLMInfo, error) {
	f.gotInfoCalls++
	return f.infoResp, f.infoErr
}

func (f *fakeLLMClient) Run(ctx context.Context, opts ...googlegrpc.CallOption) (googlegrpc.BidiStreamingClient[RunInput, RunResponse], error) {
	f.gotRunCalls++
	if f.runErr != nil {
		return nil, f.runErr
	}
	return f.runStream, nil
}

func (f *fakeLLMClient) GetSession(ctx context.Context, in *GetSessionRequest, opts ...googlegrpc.CallOption) (*SessionData, error) {
	return f.sessionResp, f.sessionErr
}

func (f *fakeLLMClient) ListSessions(ctx context.Context, in *ListSessionsRequest, opts ...googlegrpc.CallOption) (*SessionList, error) {
	return f.listResp, f.listErr
}

func (f *fakeLLMClient) GetPlans(ctx context.Context, in *GetPlansRequest, opts ...googlegrpc.CallOption) (*PlansData, error) {
	return f.plansResp, f.plansErr
}

// fakeStream is a server-streaming client stub that returns canned
// responses, then EOF. The non-Recv methods of grpc.ClientStream are
// no-ops; tests don't exercise them.
type fakeStream struct {
	responses []*RunResponse
	recvErr   error // non-nil aborts the stream after current queue
	idx       int
	sent      []*RunInput // captures what the client Sends (start, stdin, resize)
}

func (s *fakeStream) Send(in *RunInput) error {
	s.sent = append(s.sent, in)
	return nil
}

// startSent returns the RunStart the client sent as the first input, or nil.
func (s *fakeStream) startSent() *RunStart {
	for _, in := range s.sent {
		if st := in.GetStart(); st != nil {
			return st
		}
	}
	return nil
}

func (s *fakeStream) Recv() (*RunResponse, error) {
	if s.idx >= len(s.responses) {
		if s.recvErr != nil {
			return nil, s.recvErr
		}
		return nil, io.EOF
	}
	resp := s.responses[s.idx]
	s.idx++
	return resp, nil
}

// grpc.ClientStream method stubs — only Recv() carries semantics here.
func (s *fakeStream) Header() (metadata.MD, error) { return nil, nil }
func (s *fakeStream) Trailer() metadata.MD         { return nil }
func (s *fakeStream) CloseSend() error             { return nil }
func (s *fakeStream) Context() context.Context     { return context.Background() }
func (s *fakeStream) SendMsg(m any) error          { return nil }
func (s *fakeStream) RecvMsg(m any) error          { return nil }

func TestGRPCClient_Info_Delegates(t *testing.T) {
	fake := &fakeLLMClient{infoResp: &LLMInfo{Name: "test", Version: "1.2.3"}}
	c := &GRPCClient{client: fake}

	info, err := c.Info(context.Background())
	require.NoError(t, err)
	assert.Equal(t, "test", info.Name)
	assert.Equal(t, "1.2.3", info.Version)
	assert.Equal(t, 1, fake.gotInfoCalls)
}

func TestGRPCClient_Info_PropagatesError(t *testing.T) {
	want := errors.New("boom")
	fake := &fakeLLMClient{infoErr: want}
	c := &GRPCClient{client: fake}

	_, err := c.Info(context.Background())
	assert.ErrorIs(t, err, want)
}

func TestGRPCClient_Run_RoutesStdoutStderr(t *testing.T) {
	stream := &fakeStream{responses: []*RunResponse{
		{Output: &RunResponse_Stdout{Stdout: []byte("hello\n")}},
		{Output: &RunResponse_Stderr{Stderr: []byte("warn\n")}},
		{Output: &RunResponse_Stdout{Stdout: []byte("world\n")}},
		{Output: &RunResponse_ExitCode{ExitCode: 0}, ModelInfo: &ModelInfo{ModelName: "claude-sonnet"}},
	}}
	fake := &fakeLLMClient{runStream: stream}
	c := &GRPCClient{client: fake}

	var stdout, stderr bytes.Buffer
	exit, err := c.Run(context.Background(), &RunStart{}, &stdout, &stderr)
	require.NoError(t, err)
	assert.Equal(t, int32(0), exit)
	assert.Equal(t, "hello\nworld\n", stdout.String())
	assert.Equal(t, "warn\n", stderr.String())
}

func TestGRPCClient_RunWithModelInfo_CapturesExitAndModel(t *testing.T) {
	stream := &fakeStream{responses: []*RunResponse{
		{Output: &RunResponse_ExitCode{ExitCode: 42}, ModelInfo: &ModelInfo{ModelName: "haiku"}},
	}}
	fake := &fakeLLMClient{runStream: stream}
	c := &GRPCClient{client: fake}

	var stdout, stderr bytes.Buffer
	result, err := c.RunWithModelInfo(context.Background(), &RunStart{}, &stdout, &stderr)
	require.NoError(t, err)
	assert.Equal(t, int32(42), result.ExitCode)
	require.NotNil(t, result.ModelInfo)
	assert.Equal(t, "haiku", result.ModelInfo.ModelName)
}

func TestGRPCClient_Run_PropagatesStartError(t *testing.T) {
	fake := &fakeLLMClient{runErr: errors.New("dial failed")}
	c := &GRPCClient{client: fake}

	exit, err := c.Run(context.Background(), &RunStart{}, io.Discard, io.Discard)
	require.Error(t, err)
	assert.Equal(t, int32(1), exit, "start error must yield exit=1")
}

func TestGRPCClient_RunWithModelInfo_PropagatesStreamRecvError(t *testing.T) {
	stream := &fakeStream{
		responses: []*RunResponse{
			{Output: &RunResponse_Stdout{Stdout: []byte("partial\n")}},
		},
		recvErr: errors.New("stream broken"),
	}
	fake := &fakeLLMClient{runStream: stream}
	c := &GRPCClient{client: fake}

	var stdout bytes.Buffer
	_, err := c.RunWithModelInfo(context.Background(), &RunStart{}, &stdout, io.Discard)
	require.Error(t, err)
	// Partial stdout should still have been written before the error.
	assert.Equal(t, "partial\n", stdout.String())
}

func TestGRPCClient_Run_PassesRequestThrough(t *testing.T) {
	stream := &fakeStream{responses: []*RunResponse{
		{Output: &RunResponse_ExitCode{ExitCode: 0}},
	}}
	fake := &fakeLLMClient{runStream: stream}
	c := &GRPCClient{client: fake}

	req := &RunStart{Prompt: &Fragment{Content: "hi"}}
	_, err := c.Run(context.Background(), req, io.Discard, io.Discard)
	require.NoError(t, err)
	assert.Equal(t, req, stream.startSent(), "the start must be sent on the bidi stream unchanged")
}

// fakeLLMConnection is a stand-in for plugin.Client in tests of
// NewLLMRunner. It satisfies the llmConnection interface with
// configurable error responses at each lifecycle step.
type fakeLLMConnection struct {
	clientResult plugin.ClientProtocol
	clientErr    error
	killCalls    int
}

func (f *fakeLLMConnection) Client() (plugin.ClientProtocol, error) {
	return f.clientResult, f.clientErr
}
func (f *fakeLLMConnection) Kill() { f.killCalls++ }

// fakeClientProtocol is the rpcClient handle that go-plugin's Client()
// returns. Tests use it to control Dispense() behavior.
type fakeClientProtocol struct {
	dispenseResult any
	dispenseErr    error
}

func (f *fakeClientProtocol) Close() error { return nil }
func (f *fakeClientProtocol) Dispense(name string) (any, error) {
	return f.dispenseResult, f.dispenseErr
}
func (f *fakeClientProtocol) Ping() error { return nil }

func TestNewLLMRunner_ClientErrorTriggersKill(t *testing.T) {
	fake := &fakeLLMConnection{clientErr: errors.New("dial failed")}
	orig := dialLLMConnection
	dialLLMConnection = func(cmd string, args []string, logger hclog.Logger) llmConnection {
		return fake
	}
	t.Cleanup(func() { dialLLMConnection = orig })

	_, err := NewLLMRunner("dummy", nil, 0)
	require.Error(t, err)
	assert.Equal(t, 1, fake.killCalls, "Kill must be invoked when Client() fails")
}

func TestNewLLMRunner_DispenseErrorTriggersKill(t *testing.T) {
	fake := &fakeLLMConnection{
		clientResult: &fakeClientProtocol{dispenseErr: errors.New("dispense failed")},
	}
	orig := dialLLMConnection
	dialLLMConnection = func(cmd string, args []string, logger hclog.Logger) llmConnection {
		return fake
	}
	t.Cleanup(func() { dialLLMConnection = orig })

	_, err := NewLLMRunner("dummy", nil, 0)
	require.Error(t, err)
	assert.Equal(t, 1, fake.killCalls, "Kill must be invoked when Dispense() fails")
}

func TestNewLLMRunner_WrongTypeTriggersKill(t *testing.T) {
	fake := &fakeLLMConnection{
		clientResult: &fakeClientProtocol{
			dispenseResult: "not a *GRPCClient", // string instead of *GRPCClient
		},
	}
	orig := dialLLMConnection
	dialLLMConnection = func(cmd string, args []string, logger hclog.Logger) llmConnection {
		return fake
	}
	t.Cleanup(func() { dialLLMConnection = orig })

	_, err := NewLLMRunner("dummy", nil, 0)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unexpected plugin type")
	assert.Equal(t, 1, fake.killCalls, "Kill must be invoked on type assertion failure")
}

func TestNewLLMRunner_HappyPath(t *testing.T) {
	grpcClient := &GRPCClient{client: &fakeLLMClient{}}
	fake := &fakeLLMConnection{
		clientResult: &fakeClientProtocol{dispenseResult: grpcClient},
	}
	orig := dialLLMConnection
	dialLLMConnection = func(cmd string, args []string, logger hclog.Logger) llmConnection {
		return fake
	}
	t.Cleanup(func() { dialLLMConnection = orig })

	pc, err := NewLLMRunner("dummy", nil, 0)
	require.NoError(t, err)
	require.NotNil(t, pc)
	assert.Equal(t, 0, fake.killCalls, "Kill must NOT be invoked on success")

	// LLMRunner.Kill delegates to the connection.
	pc.Kill()
	assert.Equal(t, 1, fake.killCalls)
}

func TestNewLLMRunner_KillIsNilSafe(t *testing.T) {
	// A LLMRunner with nil conn (constructed for tests) must not
	// panic when Kill is called.
	pc := &LLMRunner{}
	pc.Kill() // no panic
}

// TestNewSelfInvokingClient_UsesOsExecutable confirms the wrapper
// resolves the current binary and passes "llm serve <backend>"
// args. We capture the dial seam's inputs to verify.
func TestNewSelfInvokingClient_UsesOsExecutable(t *testing.T) {
	var gotCmd string
	var gotArgs []string
	fake := &fakeLLMConnection{
		clientResult: &fakeClientProtocol{dispenseResult: &GRPCClient{client: &fakeLLMClient{}}},
	}
	orig := dialLLMConnection
	dialLLMConnection = func(cmd string, args []string, logger hclog.Logger) llmConnection {
		gotCmd = cmd
		gotArgs = args
		return fake
	}
	t.Cleanup(func() { dialLLMConnection = orig })

	_, err := NewSelfInvokingClient("claude-code", 0)
	require.NoError(t, err)
	assert.NotEmpty(t, gotCmd, "self-invoking client must pass the current executable path")
	assert.Equal(t, []string{"llm", "serve", "claude-code"}, gotArgs)
}

// TestLLMRunner_InfoAndRunDelegate confirms LLMRunner passes
// through Info/Run/RunWithModelInfo to its embedded GRPCClient.
func TestLLMRunner_InfoAndRunDelegate(t *testing.T) {
	fake := &fakeLLMClient{infoResp: &LLMInfo{Name: "x"}}
	pc := &LLMRunner{grpc: &GRPCClient{client: fake}}

	info, err := pc.Info(t.Context())
	require.NoError(t, err)
	assert.Equal(t, "x", info.Name)
	assert.Equal(t, 1, fake.gotInfoCalls)

	stream := &fakeStream{responses: []*RunResponse{
		{Output: &RunResponse_ExitCode{ExitCode: 7}},
	}}
	fake.runStream = stream
	exit, err := pc.Run(t.Context(), &RunStart{}, io.Discard, io.Discard)
	require.NoError(t, err)
	assert.Equal(t, int32(7), exit)
}

func TestGRPCClient_Run_StreamWithNoOutputs_ZeroExit(t *testing.T) {
	// A stream that EOFs without any messages — defensive: callers see
	// exit=0 (default) and empty output. Possible if a plugin connects
	// and dies before sending anything.
	stream := &fakeStream{}
	fake := &fakeLLMClient{runStream: stream}
	c := &GRPCClient{client: fake}

	var stdout bytes.Buffer
	exit, err := c.Run(context.Background(), &RunStart{}, &stdout, io.Discard)
	require.NoError(t, err)
	assert.Equal(t, int32(0), exit)
	assert.Empty(t, stdout.String())
}
