package grpc

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os/exec"
	"reflect"
	"testing"

	"github.com/hashicorp/go-hclog"
	"github.com/hashicorp/go-plugin"
	"github.com/hashicorp/go-plugin/runner"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	googlegrpc "google.golang.org/grpc"
	"google.golang.org/grpc/metadata"

	"github.com/ctxloom/ctxloom/internal/version"
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

	watchStream googlegrpc.ServerStreamingClient[WatchEvent]
	watchErr    error

	chatStream googlegrpc.BidiStreamingClient[ChatInput, ChatEvent]
	chatErr    error

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

func (f *fakeLLMClient) WatchSession(ctx context.Context, in *WatchSessionRequest, opts ...googlegrpc.CallOption) (googlegrpc.ServerStreamingClient[WatchEvent], error) {
	if f.watchErr != nil {
		return nil, f.watchErr
	}
	return f.watchStream, nil
}

func (f *fakeLLMClient) Chat(ctx context.Context, opts ...googlegrpc.CallOption) (googlegrpc.BidiStreamingClient[ChatInput, ChatEvent], error) {
	if f.chatErr != nil {
		return nil, f.chatErr
	}
	return f.chatStream, nil
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
	responses      []*RunResponse
	recvErr        error // non-nil aborts the stream after current queue
	idx            int
	sent           []*RunInput // captures what the client Sends (start, stdin, resize)
	closeSendCalls int
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
func (s *fakeStream) CloseSend() error {
	s.closeSendCalls++
	return nil
}
func (s *fakeStream) Context() context.Context { return context.Background() }
func (s *fakeStream) SendMsg(m any) error      { return nil }
func (s *fakeStream) RecvMsg(m any) error      { return nil }

// TestVerbosityToHclogLevel_MappingMatchesItsDoc pins the verbosity->level
// ladder the doc comment on verbosityToHclogLevel states. The doc and the
// code used to disagree (the doc claimed 1=Warn, 2=Info, 3+=Debug/Trace
// while the code returned Info/Debug/Trace); the code is the intended ladder, so
// this table is what stops the two drifting apart again in either direction.
func TestVerbosityToHclogLevel_MappingMatchesItsDoc(t *testing.T) {
	for _, tc := range []struct {
		verbosity int
		want      hclog.Level
	}{
		{-1, hclog.Error},
		{0, hclog.Error},
		{1, hclog.Info},
		{2, hclog.Debug},
		{3, hclog.Trace},
		{9, hclog.Trace},
	} {
		assert.Equal(t, tc.want, verbosityToHclogLevel(tc.verbosity), "verbosity %d", tc.verbosity)
	}
	assert.NotEqual(t, hclog.Warn, verbosityToHclogLevel(1),
		"hclog.Warn is not a rung on this ladder — verbosity 1 is Info")
}

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
	exit, err := c.Run(context.Background(), &RunStart{}, nil, &stdout, &stderr, nil)
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
	result, err := c.RunWithModelInfo(context.Background(), &RunStart{}, nil, &stdout, &stderr, nil)
	require.NoError(t, err)
	assert.Equal(t, int32(42), result.ExitCode)
	require.NotNil(t, result.ModelInfo)
	assert.Equal(t, "haiku", result.ModelInfo.ModelName)
}

// TestGRPCClient_RunWithModelInfo_ClosesSendWhenNoStdinNoResize pins the
// fix: a caller with neither a stdin source nor a resize source (oneshot, or
// an interactive run whose frontend has no real tty) will never send
// anything past the initial RunStart, so RunWithModelInfo must half-close
// the stream immediately. This is what lets the server's Recv loop
// (server.go) close its own resizeCh right away instead of only at the end
// of the run, which is what let ptyrunner's pre-Start wait always burn its
// full initialResizeWait for these callers.
func TestGRPCClient_RunWithModelInfo_ClosesSendWhenNoStdinNoResize(t *testing.T) {
	stream := &fakeStream{responses: []*RunResponse{
		{Output: &RunResponse_ExitCode{ExitCode: 0}},
	}}
	fake := &fakeLLMClient{runStream: stream}
	c := &GRPCClient{client: fake}

	var stdout, stderr bytes.Buffer
	_, err := c.RunWithModelInfo(context.Background(), &RunStart{}, nil, &stdout, &stderr, nil)
	require.NoError(t, err)
	assert.Equal(t, 1, stream.closeSendCalls,
		"nil stdin and nil resize means nothing else will ever be sent — CloseSend must fire once")
}

// TestGRPCClient_RunWithModelInfo_DoesNotCloseSendWithLiveResize guards the
// other half of the fix: a caller that DOES have a resize source (even if
// stdin is nil) still intends to send more input, so CloseSend must not fire
// — the resize pump goroutine below needs the send half to stay open.
func TestGRPCClient_RunWithModelInfo_DoesNotCloseSendWithLiveResize(t *testing.T) {
	stream := &fakeStream{responses: []*RunResponse{
		{Output: &RunResponse_ExitCode{ExitCode: 0}},
	}}
	fake := &fakeLLMClient{runStream: stream}
	c := &GRPCClient{client: fake}

	resize := make(chan *WindowSize)
	close(resize) // drains immediately; the point is only that it's non-nil

	var stdout, stderr bytes.Buffer
	_, err := c.RunWithModelInfo(context.Background(), &RunStart{}, nil, &stdout, &stderr, resize)
	require.NoError(t, err)
	assert.Equal(t, 0, stream.closeSendCalls,
		"a live (even if already-closed) resize channel means the caller intends to send resizes — must not half-close")
}

func TestGRPCClient_Run_PropagatesStartError(t *testing.T) {
	fake := &fakeLLMClient{runErr: errors.New("dial failed")}
	c := &GRPCClient{client: fake}

	exit, err := c.Run(context.Background(), &RunStart{}, nil, io.Discard, io.Discard, nil)
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
	_, err := c.RunWithModelInfo(context.Background(), &RunStart{}, nil, &stdout, io.Discard, nil)
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
	_, err := c.Run(context.Background(), req, nil, io.Discard, io.Discard, nil)
	require.NoError(t, err)
	assert.Equal(t, req, stream.startSent(), "the start must be sent on the bidi stream unchanged")
}

// fakeLLMConnection is a stand-in for plugin.Client in tests of
// runnerFromConn. It satisfies the llmConnection interface with
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

// The four tests below pin runnerFromConn's error/kill contract directly —
// the shared machinery NewSelfInvokingClientForLabelEnv (the only production
// entry point left after the external-plugin-binary path's removal) also
// depends on. They used to drive this through the now-deleted NewLLMRunner
// wrapper (dialLLMConnection mocked, cmd/args ignored); calling
// runnerFromConn directly with a fake llmConnection tests the same contract
// without the wrapper.

func TestRunnerFromConn_ClientErrorTriggersKill(t *testing.T) {
	fake := &fakeLLMConnection{clientErr: errors.New("dial failed")}

	_, err := runnerFromConn(fake)
	require.Error(t, err)
	assert.Equal(t, 1, fake.killCalls, "Kill must be invoked when Client() fails")
}

func TestRunnerFromConn_DispenseErrorTriggersKill(t *testing.T) {
	fake := &fakeLLMConnection{
		clientResult: &fakeClientProtocol{dispenseErr: errors.New("dispense failed")},
	}

	_, err := runnerFromConn(fake)
	require.Error(t, err)
	assert.Equal(t, 1, fake.killCalls, "Kill must be invoked when Dispense() fails")
}

func TestRunnerFromConn_WrongTypeTriggersKill(t *testing.T) {
	fake := &fakeLLMConnection{
		clientResult: &fakeClientProtocol{
			dispenseResult: "not a *GRPCClient", // string instead of *GRPCClient
		},
	}

	_, err := runnerFromConn(fake)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unexpected plugin type")
	assert.Equal(t, 1, fake.killCalls, "Kill must be invoked on type assertion failure")
}

func TestRunnerFromConn_HappyPath(t *testing.T) {
	grpcClient := &GRPCClient{client: &fakeLLMClient{}}
	fake := &fakeLLMConnection{
		clientResult: &fakeClientProtocol{dispenseResult: grpcClient},
	}

	pc, err := runnerFromConn(fake)
	require.NoError(t, err)
	require.NotNil(t, pc)
	assert.Equal(t, 0, fake.killCalls, "Kill must NOT be invoked on success")

	// LLMRunner.Kill delegates to the connection.
	pc.Kill()
	assert.Equal(t, 1, fake.killCalls)
}

// TestRunnerFromConn_VersionMismatchTriggersKill pins exposable-rental unit
// 1's daemon-staleness gate: a daemon reporting a DIFFERENT ctxloom build
// stamp than this process's own must be refused outright, not silently
// used — the whole point of the handshake. Flip the SkipSetup-equivalent
// gate here (checkDaemonVersion's `!=` comparison) and this goes red.
func TestRunnerFromConn_VersionMismatchTriggersKill(t *testing.T) {
	orig := version.Version
	version.Version = "v1.2.3-abcdef-20260101T000000"
	t.Cleanup(func() { version.Version = orig })

	grpcClient := &GRPCClient{client: &fakeLLMClient{
		infoResp: &LLMInfo{CtxloomVersion: "v1.0.0-stale00-20260101T000000"},
	}}
	fake := &fakeLLMConnection{
		clientResult: &fakeClientProtocol{dispenseResult: grpcClient},
	}

	_, err := runnerFromConn(fake)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "stale daemon")
	assert.Equal(t, 1, fake.killCalls, "a version-mismatched daemon must be killed, never handed to a caller")
}

// TestRunnerFromConn_VersionMatchSucceeds is the control: identical stamps
// on both sides must NOT be refused.
func TestRunnerFromConn_VersionMatchSucceeds(t *testing.T) {
	orig := version.Version
	version.Version = "v1.2.3-abcdef-20260101T000000"
	t.Cleanup(func() { version.Version = orig })

	grpcClient := &GRPCClient{client: &fakeLLMClient{
		infoResp: &LLMInfo{CtxloomVersion: version.Version},
	}}
	fake := &fakeLLMConnection{
		clientResult: &fakeClientProtocol{dispenseResult: grpcClient},
	}

	pc, err := runnerFromConn(fake)
	require.NoError(t, err)
	require.NotNil(t, pc)
	assert.Equal(t, 0, fake.killCalls)
}

// TestRunnerFromConn_InfoErrorTriggersKill: the handshake call itself
// failing (not just a mismatched answer) is refused too — a daemon that
// cannot even answer Info is not one to trust with a real turn.
func TestRunnerFromConn_InfoErrorTriggersKill(t *testing.T) {
	grpcClient := &GRPCClient{client: &fakeLLMClient{infoErr: errors.New("boom")}}
	fake := &fakeLLMConnection{
		clientResult: &fakeClientProtocol{dispenseResult: grpcClient},
	}

	_, err := runnerFromConn(fake)
	require.Error(t, err)
	assert.Equal(t, 1, fake.killCalls)
}

// TestLLMRunnerKill_NilReceiverFromFailedSpawn_NoPanic pins the climatic-rebel
// fix: a failed spawn (runnerFromConn's Client()-error arm, exercised here
// via a stand-in fakeLLMConnection so no real process or credentials are
// needed) returns a nil *LLMRunner. Boxing that nil pointer into the Client
// interface — exactly what every SpawnClient/Runtime.Spawn call site does at
// its `(Client, error)`-typed return — produces a NON-nil interface value
// (Go's typed-nil-in-interface pitfall), so a caller's `if client != nil`
// guard (cli/run.go's teardownTransport) still calls Kill on the nil
// receiver. Before the fix this panicked at client.go's `p.conn != nil`
// check, exactly as reported for the --degraded path with no credentials
// (a failed spawn, not a concurrent teardown race — this is deterministic
// and single-goroutine, reproduced here with no concurrency at all).
func TestLLMRunnerKill_NilReceiverFromFailedSpawn_NoPanic(t *testing.T) {
	fake := &fakeLLMConnection{clientErr: errors.New("dial failed")}

	runner, err := runnerFromConn(fake)
	require.Error(t, err)
	require.Nil(t, runner, "a failed spawn must hand back a nil *LLMRunner")

	// The boxing step: assigning the nil *LLMRunner into the Client interface
	// is exactly what happens at every SpawnClient/Runtime.Spawn return. A
	// plain `!= nil` (not testify's require.NotNil/assert.Nil, which reach
	// through the interface via reflection and correctly report the
	// underlying pointer as nil) is what production code actually does —
	// cli/run.go's teardownTransport guards with a bare `if st.client != nil`
	// — so the test must use the same comparison to reproduce what fooled it.
	var client Client = runner
	if client == nil {
		t.Fatal("expected a NON-nil interface (typed-nil-in-interface pitfall) from a plain `!= nil` comparison, matching what production's teardownTransport guard actually sees")
	}

	assert.NotPanics(t, func() { client.Kill() }, "Kill on a never-started runner must be a safe no-op, not a panic that masks the spawn's own error")
}

// TestCheckDaemonVersion pins the unstamped-build safety valve directly:
// "" and "dev" on either side are "cannot verify", never a refusal — a bare
// `go build` (bypassing the task runner's ldflags stamp) must not brick
// every daemon dial for local iteration.
func TestCheckDaemonVersion(t *testing.T) {
	orig := version.Version
	t.Cleanup(func() { version.Version = orig })

	version.Version = "v1.0.0"
	assert.NoError(t, checkDaemonVersion("v1.0.0"), "identical stamps must pass")
	assert.Error(t, checkDaemonVersion("v0.9.0"), "a different stamp must be refused")
	assert.NoError(t, checkDaemonVersion(""), "an unstamped daemon (predates this field) cannot be verified, so it passes")
	assert.NoError(t, checkDaemonVersion("dev"), "a bare-`go build` daemon cannot be verified either")

	version.Version = "dev"
	assert.NoError(t, checkDaemonVersion("v9.9.9"), "an unstamped CLIENT cannot verify a daemon either, regardless of the daemon's own stamp")
}

// TestNewContainerClient_ThreadsRunnerFuncAndSocketDir characterizes the whole
// container dial: the caller's RunnerFunc and the host socket dir are the only
// two things NewContainerClient contributes to the dial (the in-container argv
// is built inside the RunnerFunc, which already knows the backend and label),
// and a dispense failure must still tear the connection down so a started
// container never leaks. A prior fix removed the two parameters this proves
// are not consulted; the assertions below are unchanged by that removal.
func TestNewContainerClient_ThreadsRunnerFuncAndSocketDir(t *testing.T) {
	grpcClient := &GRPCClient{client: &fakeLLMClient{}}
	fake := &fakeLLMConnection{clientResult: &fakeClientProtocol{dispenseResult: grpcClient}}

	var (
		gotRunnerFunc ContainerRunnerFunc
		gotSocketDir  string
	)
	orig := dialContainerConnection
	dialContainerConnection = func(runnerFunc ContainerRunnerFunc, socketTempDir string, _ hclog.Logger) llmConnection {
		gotRunnerFunc, gotSocketDir = runnerFunc, socketTempDir
		return fake
	}
	t.Cleanup(func() { dialContainerConnection = orig })

	called := false
	rf := ContainerRunnerFunc(func(hclog.Logger, *exec.Cmd, string) (runner.Runner, error) {
		called = true
		return nil, errors.New("not launched in this test")
	})

	pc, err := NewContainerClient(0, rf, "/host/sockets")
	require.NoError(t, err)
	require.NotNil(t, pc)
	assert.Equal(t, "/host/sockets", gotSocketDir)
	require.NotNil(t, gotRunnerFunc, "the caller's launcher must reach the dial")
	_, _ = gotRunnerFunc(hclog.NewNullLogger(), nil, "")
	assert.True(t, called, "the RunnerFunc that reached the dial must be the caller's own")
	assert.Equal(t, 0, fake.killCalls)
}

// TestNewContainerClient_DispenseFailureKillsConnection is the leak half of the
// same path: a half-started container must be torn down, not left running.
func TestNewContainerClient_DispenseFailureKillsConnection(t *testing.T) {
	fake := &fakeLLMConnection{clientResult: &fakeClientProtocol{dispenseErr: errors.New("dispense failed")}}
	orig := dialContainerConnection
	dialContainerConnection = func(ContainerRunnerFunc, string, hclog.Logger) llmConnection { return fake }
	t.Cleanup(func() { dialContainerConnection = orig })

	_, err := NewContainerClient(0, nil, "/host/sockets")
	require.Error(t, err)
	assert.Equal(t, 1, fake.killCalls, "a container whose plugin never dispensed must be killed")
}

// TestDefaultClientFactory_PassesLabelToServe pins the oneshot fix: the
// factory must carry the resolved config label into the self-invoked
// `llm serve` argv, or serve's map-ordered type scan picks an arbitrary
// same-type label's binary/args/env.
func TestDefaultClientFactory_PassesLabelToServe(t *testing.T) {
	grpcClient := &GRPCClient{client: &fakeLLMClient{}}
	fake := &fakeLLMConnection{
		clientResult: &fakeClientProtocol{dispenseResult: grpcClient},
	}
	var gotArgs []string
	orig := dialLLMConnection
	dialLLMConnection = func(cmd string, args []string, env []string, logger hclog.Logger) llmConnection {
		gotArgs = args
		return fake
	}
	t.Cleanup(func() { dialLLMConnection = orig })

	t.Run("label rides as --label", func(t *testing.T) {
		_, err := DefaultClientFactory()("claude-code", "claude-fast", 0)
		require.NoError(t, err)
		assert.Equal(t, []string{"llm", "serve", "claude-code", "--label", "claude-fast"}, gotArgs)
	})

	t.Run("empty label is omitted", func(t *testing.T) {
		_, err := DefaultClientFactory()("claude-code", "", 0)
		require.NoError(t, err)
		assert.Equal(t, []string{"llm", "serve", "claude-code"}, gotArgs)
	})
}

func TestLLMRunner_KillIsNilSafe(t *testing.T) {
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
	dialLLMConnection = func(cmd string, args []string, env []string, logger hclog.Logger) llmConnection {
		gotCmd = cmd
		gotArgs = args
		return fake
	}
	t.Cleanup(func() { dialLLMConnection = orig })

	_, err := NewSelfInvokingClientForLabel("claude-code", "", 0)
	require.NoError(t, err)
	assert.NotEmpty(t, gotCmd, "self-invoking client must pass the current executable path")
	assert.Equal(t, []string{"llm", "serve", "claude-code"}, gotArgs)
}

// TestLLMRunner_InfoAndRunDelegate confirms LLMRunner passes
// through Info/Run/RunWithModelInfo to its embedded GRPCClient.
func TestLLMRunner_InfoAndRunDelegate(t *testing.T) {
	fake := &fakeLLMClient{infoResp: &LLMInfo{Name: "x"}}
	pc := &LLMRunner{GRPCClient: &GRPCClient{client: fake}}

	info, err := pc.Info(t.Context())
	require.NoError(t, err)
	assert.Equal(t, "x", info.Name)
	assert.Equal(t, 1, fake.gotInfoCalls)

	stream := &fakeStream{responses: []*RunResponse{
		{Output: &RunResponse_ExitCode{ExitCode: 7}},
	}}
	fake.runStream = stream
	exit, err := pc.Run(t.Context(), &RunStart{}, nil, io.Discard, io.Discard, nil)
	require.NoError(t, err)
	assert.Equal(t, int32(7), exit)
}

// TestLLMRunner_DelegatesSessionAndPlanOperations pins the same
// pass-through behavior for GetSession/ListSessions/GetPlans:
// LLMRunner has no logic of its own here, it must reach the exact data its
// embedded GRPCClient decoded from the wire.
func TestLLMRunner_DelegatesSessionAndPlanOperations(t *testing.T) {
	fake := &fakeLLMClient{
		sessionResp: &SessionData{Id: "sess-1"},
		listResp:    &SessionList{Sessions: []*SessionMeta{{Id: "meta-1"}}},
		plansResp:   &PlansData{Plans: []*PlanFile{{Name: "plan-a", Content: "do the thing"}}},
	}
	pc := &LLMRunner{GRPCClient: &GRPCClient{client: fake}}

	sess, err := pc.GetSession(t.Context(), "sess-1")
	require.NoError(t, err)
	assert.Equal(t, "sess-1", sess.ID)

	metas, err := pc.ListSessions(t.Context())
	require.NoError(t, err)
	require.Len(t, metas, 1)
	assert.Equal(t, "meta-1", metas[0].ID)

	plans, err := pc.GetPlans(t.Context(), "some-harp")
	require.NoError(t, err)
	require.Len(t, plans, 1)
	assert.Equal(t, "plan-a", plans[0].Name)
	assert.Equal(t, "do the thing", plans[0].Content)
}

// failingWriter fails every Write — a closed pipe (`ctxloom run | head`) or a
// full disk on a redirected run, as seen by the client's output plumbing.
type failingWriter struct{ err error }

func (w failingWriter) Write([]byte) (int, error) { return 0, w.err }

// TestGRPCClient_RunWithModelInfo_StdoutWriteErrorSurfaces pins that the
// client discarded the error from every write to the caller's stdout/stderr, so
// a redirected run that could not persist its output still exited 0 with a
// silently truncated transcript. The write failure is the run's failure.
func TestGRPCClient_RunWithModelInfo_StdoutWriteErrorSurfaces(t *testing.T) {
	want := errors.New("no space left on device")
	stream := &fakeStream{responses: []*RunResponse{
		{Output: &RunResponse_Stdout{Stdout: []byte("output nobody can keep\n")}},
		{Output: &RunResponse_ExitCode{ExitCode: 0}},
	}}
	c := &GRPCClient{client: &fakeLLMClient{runStream: stream}}

	_, err := c.RunWithModelInfo(context.Background(), &RunStart{}, nil, failingWriter{err: want}, io.Discard, nil)
	require.Error(t, err)
	assert.ErrorIs(t, err, want)
}

// TestGRPCClient_RunWithModelInfo_StderrWriteErrorSurfaces is the same defect on
// the stderr leg — diagnostics that never reached the caller must not be
// reported as delivered either.
func TestGRPCClient_RunWithModelInfo_StderrWriteErrorSurfaces(t *testing.T) {
	want := errors.New("broken pipe")
	stream := &fakeStream{responses: []*RunResponse{
		{Output: &RunResponse_Stderr{Stderr: []byte("a warning nobody can keep\n")}},
		{Output: &RunResponse_ExitCode{ExitCode: 0}},
	}}
	c := &GRPCClient{client: &fakeLLMClient{runStream: stream}}

	_, err := c.RunWithModelInfo(context.Background(), &RunStart{}, nil, io.Discard, failingWriter{err: want}, nil)
	require.Error(t, err)
	assert.ErrorIs(t, err, want)
}

// TestGRPCClient_Run_StreamEndsWithoutExitCode_IsAnError pins that the
// server sends the exit code as the FINAL message of every completed run
// (server.go's Run ends with exactly that Send), so a stream that reaches EOF
// without one means the plugin connected and died mid-run. Zero-valuing
// RunResult.ExitCode made that indistinguishable from a genuine "exited 0":
// `ctxloom run` reported success for a run that never happened.
func TestGRPCClient_Run_StreamEndsWithoutExitCode_IsAnError(t *testing.T) {
	stream := &fakeStream{}
	fake := &fakeLLMClient{runStream: stream}
	c := &GRPCClient{client: fake}

	var stdout bytes.Buffer
	exit, err := c.Run(context.Background(), &RunStart{}, nil, &stdout, io.Discard, nil)
	require.Error(t, err, "an exit-code-less stream must not be reported as a successful run")
	assert.NotEqual(t, int32(0), exit, "a failed run must not hand back a success exit code")
	assert.Empty(t, stdout.String())
}

// TestGRPCClient_Run_PartialOutputThenNoExitCode_IsAnError is the same defect
// with output already delivered: bytes on stdout are not evidence the run
// completed, so the missing terminal exit code still has to fail.
func TestGRPCClient_Run_PartialOutputThenNoExitCode_IsAnError(t *testing.T) {
	stream := &fakeStream{responses: []*RunResponse{
		{Output: &RunResponse_Stdout{Stdout: []byte("half a run\n")}},
	}}
	c := &GRPCClient{client: &fakeLLMClient{runStream: stream}}

	var stdout bytes.Buffer
	_, err := c.RunWithModelInfo(context.Background(), &RunStart{}, nil, &stdout, io.Discard, nil)
	require.Error(t, err)
	assert.Equal(t, "half a run\n", stdout.String(), "output seen before the truncation is still delivered")
}

// TestContainerRunnerFunc_IsADefinedType pins that ContainerRunnerFunc was
// a type ALIAS, so it named go-plugin's runner signature without introducing a
// type at all — every unrelated three-argument function of the same shape was
// silently the same type, and the name existed only in the source text. As a
// defined type it is a real type identity, and it stays assignable to
// go-plugin's own ClientConfig.RunnerFunc field (an unnamed func type).
func TestContainerRunnerFunc_IsADefinedType(t *testing.T) {
	var f ContainerRunnerFunc
	typ := reflect.TypeOf(&f).Elem()
	assert.Equal(t, "ContainerRunnerFunc", typ.Name(),
		"an alias reports an empty type name — the contract must be a defined type")

	// Assignability to go-plugin's field is the constraint the defined type must
	// not break: ClientConfig.RunnerFunc is an unnamed func type, so this
	// compiles only while the underlying signatures still match exactly.
	cfg := plugin.ClientConfig{}
	cfg.RunnerFunc = f
	assert.Nil(t, cfg.RunnerFunc)
}

// TestContainerClientConfig_SkipsHostEnv pins the fix at the seam where
// the container transport's environment is decided. containerHandshakeEnv (the
// isolation runner's curation) promises "ONLY the go-plugin handshake vars …
// never the host's full environment", but its third arm is a PLUGIN_ PREFIX
// match, which is a promise about the ENV IT IS HANDED, not about go-plugin.
// go-plugin appends os.Environ() ahead of its own handshake vars unless
// SkipHostEnv is set, so without this an ambient host PLUGIN_* variable crosses
// into the container and onto the `docker run` argv, which is world-readable
// via /proc/<pid>/cmdline. SkipHostEnv makes the curation's description true by
// construction rather than by hope.
func TestContainerClientConfig_SkipsHostEnv(t *testing.T) {
	rf := ContainerRunnerFunc(func(hclog.Logger, *exec.Cmd, string) (runner.Runner, error) {
		return nil, nil
	})
	cfg := ContainerClientConfig(rf, "/tmp/sock", hclog.NewNullLogger())

	assert.True(t, cfg.SkipHostEnv,
		"the container transport must not seed the plugin Cmd's env from this process's environment: the runner's PLUGIN_ prefix curation would forward an ambient host PLUGIN_* var into the container")
	assert.NotNil(t, cfg.RunnerFunc, "the container transport still launches through the caller's runner")
	require.NotNil(t, cfg.UnixSocketConfig)
	assert.Equal(t, "/tmp/sock", cfg.UnixSocketConfig.TempDir)
	assert.Equal(t, HandshakeConfig.MagicCookieKey, cfg.MagicCookieKey,
		"the magic cookie is set by go-plugin itself and survives SkipHostEnv — it is not a host var")
}
