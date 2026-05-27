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

// fakeAIPluginClient implements AIPluginClient for unit-testing
// GRPCClient against a controlled set of responses. No real gRPC,
// no network — pure in-process.
type fakeAIPluginClient struct {
	infoResp *PluginInfo
	infoErr  error

	runStream AIPlugin_RunClient
	runErr    error

	gotInfoCalls int
	gotRunCalls  int
	lastRunReq   *RunRequest
}

func (f *fakeAIPluginClient) Info(ctx context.Context, in *Empty, opts ...googlegrpc.CallOption) (*PluginInfo, error) {
	f.gotInfoCalls++
	return f.infoResp, f.infoErr
}

func (f *fakeAIPluginClient) Run(ctx context.Context, in *RunRequest, opts ...googlegrpc.CallOption) (googlegrpc.ServerStreamingClient[RunResponse], error) {
	f.gotRunCalls++
	f.lastRunReq = in
	if f.runErr != nil {
		return nil, f.runErr
	}
	return f.runStream, nil
}

// fakeStream is a server-streaming client stub that returns canned
// responses, then EOF. The non-Recv methods of grpc.ClientStream are
// no-ops; tests don't exercise them.
type fakeStream struct {
	responses []*RunResponse
	recvErr   error // non-nil aborts the stream after current queue
	idx       int
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
	fake := &fakeAIPluginClient{infoResp: &PluginInfo{Name: "test", Version: "1.2.3"}}
	c := &GRPCClient{client: fake}

	info, err := c.Info(context.Background())
	require.NoError(t, err)
	assert.Equal(t, "test", info.Name)
	assert.Equal(t, "1.2.3", info.Version)
	assert.Equal(t, 1, fake.gotInfoCalls)
}

func TestGRPCClient_Info_PropagatesError(t *testing.T) {
	want := errors.New("boom")
	fake := &fakeAIPluginClient{infoErr: want}
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
	fake := &fakeAIPluginClient{runStream: stream}
	c := &GRPCClient{client: fake}

	var stdout, stderr bytes.Buffer
	exit, err := c.Run(context.Background(), &RunRequest{}, &stdout, &stderr)
	require.NoError(t, err)
	assert.Equal(t, int32(0), exit)
	assert.Equal(t, "hello\nworld\n", stdout.String())
	assert.Equal(t, "warn\n", stderr.String())
}

func TestGRPCClient_RunWithModelInfo_CapturesExitAndModel(t *testing.T) {
	stream := &fakeStream{responses: []*RunResponse{
		{Output: &RunResponse_ExitCode{ExitCode: 42}, ModelInfo: &ModelInfo{ModelName: "haiku"}},
	}}
	fake := &fakeAIPluginClient{runStream: stream}
	c := &GRPCClient{client: fake}

	var stdout, stderr bytes.Buffer
	result, err := c.RunWithModelInfo(context.Background(), &RunRequest{}, &stdout, &stderr)
	require.NoError(t, err)
	assert.Equal(t, int32(42), result.ExitCode)
	require.NotNil(t, result.ModelInfo)
	assert.Equal(t, "haiku", result.ModelInfo.ModelName)
}

func TestGRPCClient_Run_PropagatesStartError(t *testing.T) {
	fake := &fakeAIPluginClient{runErr: errors.New("dial failed")}
	c := &GRPCClient{client: fake}

	exit, err := c.Run(context.Background(), &RunRequest{}, io.Discard, io.Discard)
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
	fake := &fakeAIPluginClient{runStream: stream}
	c := &GRPCClient{client: fake}

	var stdout bytes.Buffer
	_, err := c.RunWithModelInfo(context.Background(), &RunRequest{}, &stdout, io.Discard)
	require.Error(t, err)
	// Partial stdout should still have been written before the error.
	assert.Equal(t, "partial\n", stdout.String())
}

func TestGRPCClient_Run_PassesRequestThrough(t *testing.T) {
	stream := &fakeStream{responses: []*RunResponse{
		{Output: &RunResponse_ExitCode{ExitCode: 0}},
	}}
	fake := &fakeAIPluginClient{runStream: stream}
	c := &GRPCClient{client: fake}

	req := &RunRequest{Prompt: &Fragment{Content: "hi"}}
	_, err := c.Run(context.Background(), req, io.Discard, io.Discard)
	require.NoError(t, err)
	assert.Equal(t, req, fake.lastRunReq, "request must reach the underlying client unchanged")
}

// fakePluginConnection is a stand-in for plugin.Client in tests of
// NewPluginClient. It satisfies the pluginConnection interface with
// configurable error responses at each lifecycle step.
type fakePluginConnection struct {
	clientResult plugin.ClientProtocol
	clientErr    error
	killCalls    int
}

func (f *fakePluginConnection) Client() (plugin.ClientProtocol, error) {
	return f.clientResult, f.clientErr
}
func (f *fakePluginConnection) Kill() { f.killCalls++ }

// fakeClientProtocol is the rpcClient handle that go-plugin's Client()
// returns. Tests use it to control Dispense() behavior.
type fakeClientProtocol struct {
	dispenseResult any
	dispenseErr    error
}

func (f *fakeClientProtocol) Close() error                                { return nil }
func (f *fakeClientProtocol) Dispense(name string) (any, error) {
	return f.dispenseResult, f.dispenseErr
}
func (f *fakeClientProtocol) Ping() error { return nil }

func TestNewPluginClient_ClientErrorTriggersKill(t *testing.T) {
	fake := &fakePluginConnection{clientErr: errors.New("dial failed")}
	orig := dialPluginConnection
	dialPluginConnection = func(cmd string, args []string, logger hclog.Logger) pluginConnection {
		return fake
	}
	t.Cleanup(func() { dialPluginConnection = orig })

	_, err := NewPluginClient("dummy", nil, 0)
	require.Error(t, err)
	assert.Equal(t, 1, fake.killCalls, "Kill must be invoked when Client() fails")
}

func TestNewPluginClient_DispenseErrorTriggersKill(t *testing.T) {
	fake := &fakePluginConnection{
		clientResult: &fakeClientProtocol{dispenseErr: errors.New("dispense failed")},
	}
	orig := dialPluginConnection
	dialPluginConnection = func(cmd string, args []string, logger hclog.Logger) pluginConnection {
		return fake
	}
	t.Cleanup(func() { dialPluginConnection = orig })

	_, err := NewPluginClient("dummy", nil, 0)
	require.Error(t, err)
	assert.Equal(t, 1, fake.killCalls, "Kill must be invoked when Dispense() fails")
}

func TestNewPluginClient_WrongTypeTriggersKill(t *testing.T) {
	fake := &fakePluginConnection{
		clientResult: &fakeClientProtocol{
			dispenseResult: "not a *GRPCClient", // string instead of *GRPCClient
		},
	}
	orig := dialPluginConnection
	dialPluginConnection = func(cmd string, args []string, logger hclog.Logger) pluginConnection {
		return fake
	}
	t.Cleanup(func() { dialPluginConnection = orig })

	_, err := NewPluginClient("dummy", nil, 0)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unexpected plugin type")
	assert.Equal(t, 1, fake.killCalls, "Kill must be invoked on type assertion failure")
}

func TestNewPluginClient_HappyPath(t *testing.T) {
	grpcClient := &GRPCClient{client: &fakeAIPluginClient{}}
	fake := &fakePluginConnection{
		clientResult: &fakeClientProtocol{dispenseResult: grpcClient},
	}
	orig := dialPluginConnection
	dialPluginConnection = func(cmd string, args []string, logger hclog.Logger) pluginConnection {
		return fake
	}
	t.Cleanup(func() { dialPluginConnection = orig })

	pc, err := NewPluginClient("dummy", nil, 0)
	require.NoError(t, err)
	require.NotNil(t, pc)
	assert.Equal(t, 0, fake.killCalls, "Kill must NOT be invoked on success")

	// PluginClient.Kill delegates to the connection.
	pc.Kill()
	assert.Equal(t, 1, fake.killCalls)
}

func TestNewPluginClient_KillIsNilSafe(t *testing.T) {
	// A PluginClient with nil conn (constructed for tests) must not
	// panic when Kill is called.
	pc := &PluginClient{}
	pc.Kill() // no panic
}

// TestNewSelfInvokingClient_UsesOsExecutable confirms the wrapper
// resolves the current binary and passes "plugin serve <backend>"
// args. We capture the dial seam's inputs to verify.
func TestNewSelfInvokingClient_UsesOsExecutable(t *testing.T) {
	var gotCmd string
	var gotArgs []string
	fake := &fakePluginConnection{
		clientResult: &fakeClientProtocol{dispenseResult: &GRPCClient{client: &fakeAIPluginClient{}}},
	}
	orig := dialPluginConnection
	dialPluginConnection = func(cmd string, args []string, logger hclog.Logger) pluginConnection {
		gotCmd = cmd
		gotArgs = args
		return fake
	}
	t.Cleanup(func() { dialPluginConnection = orig })

	_, err := NewSelfInvokingClient("claude-code", 0)
	require.NoError(t, err)
	assert.NotEmpty(t, gotCmd, "self-invoking client must pass the current executable path")
	assert.Equal(t, []string{"plugin", "serve", "claude-code"}, gotArgs)
}

// TestPluginClient_InfoAndRunDelegate confirms PluginClient passes
// through Info/Run/RunWithModelInfo to its embedded GRPCClient.
func TestPluginClient_InfoAndRunDelegate(t *testing.T) {
	fake := &fakeAIPluginClient{infoResp: &PluginInfo{Name: "x"}}
	pc := &PluginClient{grpc: &GRPCClient{client: fake}}

	info, err := pc.Info(t.Context())
	require.NoError(t, err)
	assert.Equal(t, "x", info.Name)
	assert.Equal(t, 1, fake.gotInfoCalls)

	stream := &fakeStream{responses: []*RunResponse{
		{Output: &RunResponse_ExitCode{ExitCode: 7}},
	}}
	fake.runStream = stream
	exit, err := pc.Run(t.Context(), &RunRequest{}, io.Discard, io.Discard)
	require.NoError(t, err)
	assert.Equal(t, int32(7), exit)
}

func TestGRPCClient_Run_StreamWithNoOutputs_ZeroExit(t *testing.T) {
	// A stream that EOFs without any messages — defensive: callers see
	// exit=0 (default) and empty output. Possible if a plugin connects
	// and dies before sending anything.
	stream := &fakeStream{}
	fake := &fakeAIPluginClient{runStream: stream}
	c := &GRPCClient{client: fake}

	var stdout bytes.Buffer
	exit, err := c.Run(context.Background(), &RunRequest{}, &stdout, io.Discard)
	require.NoError(t, err)
	assert.Equal(t, int32(0), exit)
	assert.Empty(t, stdout.String())
}
