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

// TestDiscoveryRunRequest_StatesDefaultPermissionExplicitly pins that the init
// DISCOVERY LAUNCH — the interactive setup session init hands the user off to —
// declares its permission posture on the wire request instead of leaving the
// field at its zero value. An unset field is indistinguishable from a caller
// that forgot, which is precisely the silent fall-through the ping's explicit
// bypass (TestPingEngineAuth_RequestsBypassPermissionExplicitly) closed on the
// other init launch site.
//
// The launch consults exactly ONE rung — the PROJECT DEFAULT — and no other:
// not the engine label, not a binding, not the claude-code host stopgap. That
// asymmetry with `ctxloom run` is deliberate and is the point of the whole
// arrangement. Setup runs inside the vendor's own raw CLI/TUI, whose native
// approval prompts are the consent surface, so the only thing that may widen it
// is a human's explicit, project-scoped, per-directory declaration. Everything
// else the run resolver would consult is a posture inherited from somewhere the
// human did not decide THIS.
//
// These are PAYLOAD assertions on the request that actually rides the wire, not
// on a helper's return value in isolation: launchEngineWithPrompt hands this
// very struct to goplugin.NewLauncher.
//
// MUTATION TARGET (m3): dropping the cfg.GetPermissions() consultation from
// discoveryRunRequest turns the "declared" subtest red.
func TestDiscoveryRunRequest_StatesDefaultPermissionExplicitly(t *testing.T) {
	// Undeclared: the pinned default stands, exactly as before this key existed.
	t.Run("undeclared project default keeps the pinned default", func(t *testing.T) {
		for name, cfg := range map[string]*config.Config{
			"nil config":              nil,
			"config declaring no key": config.NewFixture(config.Fixture{AppPaths: []string{t.TempDir()}}),
		} {
			t.Run(name, func(t *testing.T) {
				req := discoveryRunRequest(cfg, t.TempDir())

				require.NotNil(t, req)
				require.NotNil(t, req.Options)
				assert.Equal(t, agent.PermissionDefault.String(), req.Options.PermissionMode,
					"the discovery launch must state its posture out loud; an empty PermissionMode is a fall-through nobody declared")

				// The posture it states is the vendor TUI's own prompting — NOT a
				// second bypass on top of the engine's native consent surface.
				assert.NotEqual(t, agent.PermissionBypass.String(), req.Options.PermissionMode,
					"an undeclared setup session must never launch at bypass: the vendor TUI's native approval prompts are the consent surface")
				assert.Equal(t, pb.ExecutionMode_INTERACTIVE, req.Options.Mode)
			})
		}
	})

	// Declared: the project's own posture rides the request. A human who wrote
	// `permissions: bypass` into THIS directory's config has said what setup in
	// this directory may do; the launch would be lying to them to pin `default`
	// over it and then behave differently on the very next `ctxloom run`.
	t.Run("a declared project default rides the request", func(t *testing.T) {
		for _, want := range []agent.PermissionMode{
			agent.PermissionBypass, agent.PermissionPlan, agent.PermissionAcceptEdits,
		} {
			t.Run(want.String(), func(t *testing.T) {
				cfg := config.NewFixture(config.Fixture{
					AppPaths:    []string{t.TempDir()},
					Permissions: want.String(),
				})
				req := discoveryRunRequest(cfg, t.TempDir())

				require.NotNil(t, req)
				require.NotNil(t, req.Options)
				assert.Equal(t, want.String(), req.Options.PermissionMode,
					"a project that declared its own posture must launch setup at it, not at the pinned default")
				assert.Equal(t, pb.ExecutionMode_INTERACTIVE, req.Options.Mode)
			})
		}
	})

	// Fault tolerance matches every other config-sourced posture rung: a
	// hand-edited misspelling falls through to the pinned default rather than
	// failing init, and above all never widens.
	t.Run("an unparseable project default falls back to the pinned default", func(t *testing.T) {
		cfg := config.NewFixture(config.Fixture{
			AppPaths:    []string{t.TempDir()},
			Permissions: "byapss",
		})
		req := discoveryRunRequest(cfg, t.TempDir())

		require.NotNil(t, req)
		require.NotNil(t, req.Options)
		assert.Equal(t, agent.PermissionDefault.String(), req.Options.PermissionMode,
			"a misspelled posture must never resolve to anything wider than the pinned default")
	})
}

// TestPrintDiscoveryPostureHint pins the one line the discovery handoff prints
// when it is launching at the PINNED DEFAULT: a project that has not declared a
// posture is told, at the exact moment the posture is about to bite, that the
// key exists and how to set it. A capability nobody is told about is a
// capability nobody has, and init's handoff is the one place in the product
// where a human is already being walked through configuring this directory.
//
// It stays silent once a posture IS declared — repeating the instructions for
// something already done is noise, and the declared posture is visible in the
// session itself.
//
// MUTATION TARGET (m4): deleting the printDiscoveryPostureHint call from
// launchDiscovery, or the fmt.Println inside it, turns this red.
func TestPrintDiscoveryPostureHint(t *testing.T) {
	t.Run("at the pinned default it names the key and how to set it", func(t *testing.T) {
		cfg := config.NewFixture(config.Fixture{AppPaths: []string{t.TempDir()}})

		out := captureStdout(t, func() { printDiscoveryPostureHint(cfg) })

		assert.Contains(t, out, "permissions:",
			"the hint must name the config key itself — a description of the capability without its spelling is not actionable")
		assert.Contains(t, out, ".ctxloom/config.yaml",
			"the hint must name the file it goes in, because WHICH file is the whole restriction: a home config is ignored")
		for _, mode := range agent.PermissionModeNames() {
			assert.Contains(t, out, mode, "the hint must name the accepted postures")
		}
	})

	t.Run("a nil config still prints the hint", func(t *testing.T) {
		// GetConfig returns nil on a load failure, and launchDiscovery is
		// explicitly best-effort about that. A project that could not load has
		// certainly not declared a posture, so the hint is if anything more
		// wanted here — and it must not panic reaching for one.
		out := captureStdout(t, func() { printDiscoveryPostureHint(nil) })
		assert.Contains(t, out, "permissions:")
	})

	t.Run("a declared posture silences the hint", func(t *testing.T) {
		cfg := config.NewFixture(config.Fixture{
			AppPaths:    []string{t.TempDir()},
			Permissions: "bypass",
		})

		out := captureStdout(t, func() { printDiscoveryPostureHint(cfg) })
		assert.Empty(t, out,
			"a project that already declared its posture must not be told how to declare one")
	})
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

	// The project-posture hint rides this same handoff narration. This is the
	// WIRING half of TestPrintDiscoveryPostureHint (which pins the line's
	// content): that test would still pass if the call were deleted from
	// launchDiscovery entirely, and then nobody would ever see it.
	//
	// Conditioned on the ambient config launchDiscovery actually reads, rather
	// than asserted unconditionally: this test does not (and should not) stub
	// GetConfig, so whether the hint is due depends on whether the project this
	// suite runs inside has declared a posture of its own. Silence is the
	// correct output when it has.
	if cfg, cerr := GetConfig(); cerr != nil || cfg.GetPermissions() == "" {
		assert.Contains(t, out, "permissions:",
			"a handoff running at the pinned default must tell the user the project-scoped posture key exists")
	}
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
