package cli

import (
	"context"
	"io"
	"testing"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/config"
	pb "github.com/ctxloom/ctxloom/internal/lm/grpc"
	"github.com/ctxloom/ctxloom/internal/shared/agent"
)

// authPingTestConfig is a minimal, isolated config for pingEngineAuth tests:
// AppPaths points at an empty temp dir, so context assembly's default-profile
// fallback finds nothing to resolve (fault-tolerant no-op) rather than
// touching this repo's real bundle content.
func authPingTestConfig(t *testing.T) *config.Config {
	t.Helper()
	return config.NewFixture(config.Fixture{AppPaths: []string{t.TempDir()}})
}

// stubPingClient is a minimal pb.Client for pingEngineAuth/launchDiscovery
// tests: Run returns the configured exit code/error and records the request
// it received, so a test can assert what pingEngineAuth actually sent without
// spawning a real engine subprocess.
type stubPingClient struct {
	exitCode int32
	runErr   error
	gotReq   *pb.RunStart
}

func (s *stubPingClient) Run(_ context.Context, req *pb.RunStart, _ io.Reader, stdout, _ io.Writer, _ <-chan *pb.WindowSize) (int32, error) {
	s.gotReq = req
	if s.runErr != nil {
		return s.exitCode, s.runErr
	}
	_, _ = io.WriteString(stdout, "ok")
	return s.exitCode, nil
}
func (s *stubPingClient) Info(context.Context) (*pb.LLMInfo, error) { return &pb.LLMInfo{}, nil }
func (s *stubPingClient) RunWithModelInfo(context.Context, *pb.RunStart, io.Reader, io.Writer, io.Writer, <-chan *pb.WindowSize) (*pb.RunResult, error) {
	return &pb.RunResult{}, nil
}
func (s *stubPingClient) GetSession(context.Context, string) (*agent.Session, error) { return nil, nil }
func (s *stubPingClient) WatchSession(context.Context, string) (<-chan *pb.WatchEvent, <-chan error, error) {
	return nil, nil, nil
}
func (s *stubPingClient) Chat(context.Context, agent.ChatRequest) (chan<- agent.ChatMessage, <-chan agent.ChatEvent, <-chan error, error) {
	return nil, nil, nil, nil
}
func (s *stubPingClient) ListSessions(context.Context) ([]agent.SessionMeta, error) { return nil, nil }
func (s *stubPingClient) GetPlans(context.Context, string) ([]agent.PlanFile, error) {
	return nil, nil
}
func (s *stubPingClient) Kill() {}

// TestPingEngineAuth_Succeeds: a healthy engine's oneshot round trip (exit 0)
// clears the gate with no error — the ping is a liveness check, not a login
// flow, so success just means "proceed."
func TestPingEngineAuth_Succeeds(t *testing.T) {
	stub := &stubPingClient{exitCode: 0}
	orig := authPingFactory
	authPingFactory = func(string, string, int) (pb.Client, error) { return stub, nil }
	t.Cleanup(func() { authPingFactory = orig })

	err := pingEngineAuth(context.Background(), authPingTestConfig(t), "claude-code", t.TempDir())
	require.NoError(t, err)

	// The smallest possible prompt actually reached the engine.
	require.NotNil(t, stub.gotReq)
	require.NotNil(t, stub.gotReq.Prompt)
	assert.Equal(t, authPingTask, stub.gotReq.Prompt.Content)
	assert.Equal(t, pb.ExecutionMode_ONESHOT, stub.gotReq.Options.Mode)
}

// TestPingEngineAuth_RequestsBypassPermissionExplicitly pins that the ping
// asks for permissions: bypass on its RunOneshot request explicitly, rather
// than riding whatever the chosen engine's llm label declares (or doesn't).
// authPingTestConfig declares no llm permissions at all, so before
// pingEngineAuth carried this override, its request depended entirely on
// operations.effectiveMemberPermission's floor for an unset posture — a
// floor unroasted-spinning replaced with a refusal. This is a PAYLOAD
// assertion on the actual wire request (Options.PermissionMode), not just
// "the ping succeeded": a caller-side fallback that quietly caught a
// refusal and retried some other way could still pass a success-only
// assertion without this request ever carrying bypass.
func TestPingEngineAuth_RequestsBypassPermissionExplicitly(t *testing.T) {
	stub := &stubPingClient{exitCode: 0}
	orig := authPingFactory
	authPingFactory = func(string, string, int) (pb.Client, error) { return stub, nil }
	t.Cleanup(func() { authPingFactory = orig })

	err := pingEngineAuth(context.Background(), authPingTestConfig(t), "claude-code", t.TempDir())
	require.NoError(t, err)

	require.NotNil(t, stub.gotReq)
	require.NotNil(t, stub.gotReq.Options)
	assert.Equal(t, agent.PermissionBypass.String(), stub.gotReq.Options.PermissionMode,
		"the ping must carry an explicit bypass posture on the request, not rely on the label's configured (or unset) permissions")
}

// TestPingEngineAuth_FailsLoud_NamesTheFix: a dead engine (nonzero exit, as a
// real backend reports when auth is missing) fails the ping with an error
// naming BOTH the engine and its specific fix — never a bare "failed."
func TestPingEngineAuth_FailsLoud_NamesTheFix(t *testing.T) {
	tests := []struct {
		engine   string
		wantText string
	}{
		{"claude-code", "claude login"},
		{"codex", "codex login"},
		{"kiro", "kiro-cli login"},
	}
	for _, tt := range tests {
		t.Run(tt.engine, func(t *testing.T) {
			stub := &stubPingClient{exitCode: 1}
			orig := authPingFactory
			authPingFactory = func(string, string, int) (pb.Client, error) { return stub, nil }
			t.Cleanup(func() { authPingFactory = orig })

			err := pingEngineAuth(context.Background(), authPingTestConfig(t), tt.engine, t.TempDir())
			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.engine, "error must name the engine that failed")
			assert.Contains(t, err.Error(), tt.wantText, "error must name THIS engine's specific fix")
		})
	}
}

// TestPingEngineAuth_UnlistedEngine_GetsGenericFix: an engine not (yet) in
// engineAuthFix still fails loud, with a generic-but-actionable fix, rather
// than panicking on a missing map entry.
func TestPingEngineAuth_UnlistedEngine_GetsGenericFix(t *testing.T) {
	stub := &stubPingClient{exitCode: 1}
	orig := authPingFactory
	authPingFactory = func(string, string, int) (pb.Client, error) { return stub, nil }
	t.Cleanup(func() { authPingFactory = orig })

	err := pingEngineAuth(context.Background(), authPingTestConfig(t), "some-future-engine", t.TempDir())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "authenticate the engine")
}

// TestLaunchDiscovery_FailedPing_NeverLaunches: the core new behavior (§12
// Q1) — when the auth ping fails, launchDiscovery returns the error and MUST
// NOT call through to launchEngineWithPromptFn at all. A dead first session
// inside a vendor TUI is invisible failure; this proves init never gets that
// far.
func TestLaunchDiscovery_FailedPing_NeverLaunches(t *testing.T) {
	stub := &stubPingClient{exitCode: 1}
	origFactory := authPingFactory
	authPingFactory = func(string, string, int) (pb.Client, error) { return stub, nil }
	t.Cleanup(func() { authPingFactory = origFactory })

	launchCalled := false
	origLaunch := launchEngineWithPromptFn
	launchEngineWithPromptFn = func(context.Context, string, string) error {
		launchCalled = true
		return nil
	}
	t.Cleanup(func() { launchEngineWithPromptFn = origLaunch })

	cmd := &cobra.Command{}
	cmd.SetContext(context.Background())

	err := launchDiscovery(cmd, "claude-code", t.TempDir()+"/.ctxloom", true)
	require.Error(t, err, "a failed ping must fail init loud, not degrade")
	assert.False(t, launchCalled, "the engine must never be launched after a failed auth ping")
	assert.Contains(t, err.Error(), "claude login")
}

// TestLaunchDiscovery_SuccessfulPing_LaunchesAndPrintsReentryHint: a healthy
// ping proceeds to the launch, and once that session ends, init prints the
// re-entry hint (connect via the configured client / `/ctxloom-init`) with no
// relaunch prompt of its own — init hands off once and is done.
func TestLaunchDiscovery_SuccessfulPing_LaunchesAndPrintsReentryHint(t *testing.T) {
	stub := &stubPingClient{exitCode: 0}
	origFactory := authPingFactory
	authPingFactory = func(string, string, int) (pb.Client, error) { return stub, nil }
	t.Cleanup(func() { authPingFactory = origFactory })

	launchCalled := false
	origLaunch := launchEngineWithPromptFn
	launchEngineWithPromptFn = func(context.Context, string, string) error {
		launchCalled = true
		return nil
	}
	t.Cleanup(func() { launchEngineWithPromptFn = origLaunch })

	cmd := &cobra.Command{}
	cmd.SetContext(context.Background())

	var err error
	out := captureStdout(t, func() {
		err = launchDiscovery(cmd, "claude-code", t.TempDir()+"/.ctxloom", true)
	})
	require.NoError(t, err)
	assert.True(t, launchCalled, "a successful ping must proceed to the launch")

	// Re-entry hint printed; no relaunch prompt (deleted machinery).
	assert.Contains(t, out, "/ctxloom-init")
	assert.NotContains(t, out, "Start your session now")
}

// TestLaunchDiscovery_SessionError_StillExitsCleanly: a session that starts
// (ping succeeded) but ends in error — an interrupted setup, a crashed
// engine — degrades to a warning; launchDiscovery itself still returns nil
// (init exits 0), matching the pre-existing fault-tolerant behavior for a
// launch failure (only the NEW ping is fail-loud).
func TestLaunchDiscovery_SessionError_StillExitsCleanly(t *testing.T) {
	stub := &stubPingClient{exitCode: 0}
	origFactory := authPingFactory
	authPingFactory = func(string, string, int) (pb.Client, error) { return stub, nil }
	t.Cleanup(func() { authPingFactory = origFactory })

	origLaunch := launchEngineWithPromptFn
	launchEngineWithPromptFn = func(context.Context, string, string) error {
		return assert.AnError
	}
	t.Cleanup(func() { launchEngineWithPromptFn = origLaunch })

	cmd := &cobra.Command{}
	cmd.SetContext(context.Background())

	var err error
	out := captureStdout(t, func() {
		err = launchDiscovery(cmd, "claude-code", t.TempDir()+"/.ctxloom", true)
	})
	require.NoError(t, err, "a launch/session error degrades — it is not the ping gate")
	assert.NotContains(t, out, "/ctxloom-init", "no re-entry hint when the session itself errored")
}

// TestLaunchDiscovery_NonInteractive_SkipsPingAndLaunch pins §7/§3's
// non-interactive contract: --non-interactive (interactive=false here) must
// not ping or launch anything — headless init is (a)-(e) only, deterministic,
// no agent.
func TestLaunchDiscovery_NonInteractive_SkipsPingAndLaunch(t *testing.T) {
	pingCalled := false
	origFactory := authPingFactory
	authPingFactory = func(string, string, int) (pb.Client, error) {
		pingCalled = true
		return &stubPingClient{exitCode: 0}, nil
	}
	t.Cleanup(func() { authPingFactory = origFactory })

	launchCalled := false
	origLaunch := launchEngineWithPromptFn
	launchEngineWithPromptFn = func(context.Context, string, string) error {
		launchCalled = true
		return nil
	}
	t.Cleanup(func() { launchEngineWithPromptFn = origLaunch })

	cmd := &cobra.Command{}
	cmd.SetContext(context.Background())

	err := launchDiscovery(cmd, "claude-code", t.TempDir()+"/.ctxloom", false)
	require.NoError(t, err)
	assert.False(t, pingCalled, "non-interactive must not ping the engine's auth")
	assert.False(t, launchCalled, "non-interactive must not launch anything")
}

// TestLaunchDiscovery_SkipLaunch_SkipsPingToo mirrors the non-interactive
// case for --skip-launch: skipping the launch means skipping its gate too.
func TestLaunchDiscovery_SkipLaunch_SkipsPingToo(t *testing.T) {
	origSkip := initSkipLaunch
	initSkipLaunch = true
	t.Cleanup(func() { initSkipLaunch = origSkip })

	pingCalled := false
	origFactory := authPingFactory
	authPingFactory = func(string, string, int) (pb.Client, error) {
		pingCalled = true
		return &stubPingClient{exitCode: 0}, nil
	}
	t.Cleanup(func() { authPingFactory = origFactory })

	cmd := &cobra.Command{}
	cmd.SetContext(context.Background())

	err := launchDiscovery(cmd, "claude-code", t.TempDir()+"/.ctxloom", true)
	require.NoError(t, err)
	assert.False(t, pingCalled)
}
