package cli

import (
	"context"
	"io"
	"testing"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/adapters/operations"
	"github.com/ctxloom/ctxloom/internal/core/agent"
	"github.com/ctxloom/ctxloom/internal/core/composite"
	"github.com/ctxloom/ctxloom/internal/core/config"
	"github.com/ctxloom/ctxloom/internal/core/engine"
	"github.com/ctxloom/ctxloom/internal/core/launch"
	"github.com/ctxloom/ctxloom/internal/core/sessions"
	"github.com/ctxloom/ctxloom/internal/lm/backends"
	pb "github.com/ctxloom/ctxloom/internal/lm/grpc"
)

// pingTestHarp is any non-empty harp: these tests exercise the ping's own
// branching, not session naming, but the probe now runs inside a named
// session and an EMPTY harp is a refusal (ErrSharedScratchNoHarp), not a
// neutral default.
// authPingTestConfig is a minimal, isolated config for pingEngineAuth tests:
// AppPaths points at an empty temp dir, so context assembly's default-profile
// fallback finds nothing to resolve (fault-tolerant no-op) rather than
// touching this repo's real bundle content.
func authPingTestConfig(t *testing.T) *config.Config {
	t.Helper()
	return gatedFixture(config.Fixture{AppPaths: []string{t.TempDir()}})
}

// testLaunchDeps composes the resolver's ports over cfg with stateless
// doubles: an in-memory session store and the dry-run cell (the project root
// on the host, prepared nowhere), so nothing a setup launch resolves lands on
// disk. It is also installed as initLaunchDeps for the test's duration.
func testLaunchDeps(t *testing.T, cfg *config.Config) launch.Deps {
	t.Helper()
	deps := launch.Deps{
		Snapshot:  &config.Snapshot{Config: cfg},
		Engines:   backends.Engines(),
		Assembler: launchtestAssembler{},
		Cells:     dryCells{},
		Endpoints: sequenceMinter{},
		Sessions:  sessions.NewMemStore(),
		Host:      launch.HostFacts{Home: t.TempDir(), CtxloomHome: t.TempDir(), Binary: "ctxloom"},
	}
	orig := initLaunchDeps
	initLaunchDeps = func(context.Context) (launch.Deps, error) { return deps, nil }
	t.Cleanup(func() { initLaunchDeps = orig })
	return deps
}

// launchtestAssembler composes nothing: the setup launches select no
// profiles, so no assembly runs; the managed surfaces are empty.
type launchtestAssembler struct{}

func (launchtestAssembler) Assemble(context.Context, *config.Snapshot, launch.Selection) (composite.Package, error) {
	return composite.Package{}, nil
}
func (launchtestAssembler) Index(context.Context, *config.Snapshot) (composite.Index, error) {
	return composite.Index{}, nil
}
func (launchtestAssembler) LabelEnv(*config.Snapshot, string) map[string]string { return nil }

// sequenceMinter mints a fresh loopback endpoint per call.
type sequenceMinter struct{}

func (sequenceMinter) MintMCP(_ context.Context, id sessions.Identity, _ launch.Axes) (sessions.Endpoint, error) {
	return sessions.Endpoint{URL: "http://127.0.0.1:1/" + id.Harp, Credential: "c-" + id.Harp}, nil
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
func (s *stubPingClient) RunWithModelInfo(ctx context.Context, req *pb.RunStart, stdin io.Reader, stdout, stderr io.Writer, resize <-chan *pb.WindowSize) (*pb.RunResult, error) {
	code, err := s.Run(ctx, req, stdin, stdout, stderr, resize)
	return &pb.RunResult{ExitCode: code}, err
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

	cfg := authPingTestConfig(t)
	err := pingEngineAuth(context.Background(), testLaunchDeps(t, cfg), cfg, "claude-code", t.TempDir())
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

	cfg := authPingTestConfig(t)
	err := pingEngineAuth(context.Background(), testLaunchDeps(t, cfg), cfg, "claude-code", t.TempDir())
	require.NoError(t, err)

	require.NotNil(t, stub.gotReq)
	require.NotNil(t, stub.gotReq.Options)
	assert.Equal(t, agent.PermissionBypass.String(), stub.gotReq.Options.PermissionMode,
		"the ping must carry an explicit bypass posture on the request, not rely on the label's configured (or unset) permissions")
}

// discoveryLaunch drives launchDiscovery over stateless deps with a
// succeeding probe, capturing the discovery Launch handed to the engine.
func discoveryLaunch(t *testing.T, cfg *config.Config) launch.Launch {
	t.Helper()
	testLaunchDeps(t, cfg)
	stub := &stubPingClient{exitCode: 0}
	origFactory := authPingFactory
	authPingFactory = func(string, string, int) (pb.Client, error) { return stub, nil }
	t.Cleanup(func() { authPingFactory = origFactory })

	var got launch.Launch
	origLaunch := launchEngineWithPromptFn
	launchEngineWithPromptFn = func(_ context.Context, l launch.Launch, _ operations.Opened) error { got = l; return nil }
	t.Cleanup(func() { launchEngineWithPromptFn = origLaunch })

	cmd := &cobra.Command{}
	cmd.SetContext(context.Background())
	_ = captureStdout(t, func() { require.NoError(t, launchDiscovery(cmd, "claude-code", t.TempDir()+"/.ctxloom", true)) })
	require.NotEmpty(t, got.Identity.Harp, "the discovery session resolved")
	// The probe stamped its OWN harp on the request it drove; the session the
	// human works in is another identity.
	require.NotNil(t, stub.gotReq)
	assert.NotEqual(t, stub.gotReq.Options.Env[sessions.EnvHarp], got.Identity.Harp,
		"the auth probe and the discovery session are two launches with two identities")
	return got
}

// TestDiscoveryLaunch_StatesDefaultPermissionExplicitly pins the discovery
// launch's one-rung posture: this project's declared default, else the
// pinned default — never the host stopgap, never a label's.
func TestDiscoveryLaunch_StatesDefaultPermissionExplicitly(t *testing.T) {
	t.Run("undeclared project default keeps the pinned default", func(t *testing.T) {
		l := discoveryLaunch(t, config.NewFixture(config.Fixture{AppPaths: []string{t.TempDir()}}))
		assert.Equal(t, agent.PermissionDefault, l.Permission,
			"an undeclared setup session must never launch at bypass: the vendor TUI's native approval prompts are the consent surface")
		assert.Equal(t, engine.Interactive, l.Mode)
	})

	t.Run("a declared project default rides the launch", func(t *testing.T) {
		for _, want := range []agent.PermissionMode{agent.PermissionBypass, agent.PermissionPlan, agent.PermissionAcceptEdits} {
			t.Run(want.String(), func(t *testing.T) {
				l := discoveryLaunch(t, config.NewFixture(config.Fixture{AppPaths: []string{t.TempDir()}, Permissions: want.String()}))
				assert.Equal(t, want, l.Permission, "a project that declared its own posture must launch setup at it, not at the pinned default")
				assert.Equal(t, engine.Interactive, l.Mode)
			})
		}
	})

	t.Run("an unparseable project default falls back to the pinned default", func(t *testing.T) {
		l := discoveryLaunch(t, config.NewFixture(config.Fixture{AppPaths: []string{t.TempDir()}, Permissions: "byapss"}))
		assert.Equal(t, agent.PermissionDefault, l.Permission, "a misspelled posture must never resolve to anything wider than the pinned default")
	})
}

// TestDiscoveryLaunch_CarriesTheSetupPrompt: the discovery session opens
// on the setup skill.
func TestDiscoveryLaunch_CarriesTheSetupPrompt(t *testing.T) {
	cfg := config.NewFixture(config.Fixture{AppPaths: []string{t.TempDir()}})
	l := discoveryLaunch(t, cfg)
	assert.Equal(t, discoverySessionPrompt(cfg), l.Prompt)
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
//
// EVERY registered backend runs the SAME assertions: this is a conformance
// suite over backends.List(), not a hand-maintained table of engine/expected
// pairs. A table drifts the moment a backend is added or removed, and it
// duplicates the fix strings each engine's own declaration owns — so the expected
// text is read from production via engineAuthFixHint rather than re-typed
// here. A newly registered backend is covered without editing this file.
func TestPingEngineAuth_FailsLoud_NamesTheFix(t *testing.T) {
	engines := backends.List()
	require.NotEmpty(t, engines,
		"the backend registry is empty — every subtest below would be skipped and this suite would pass having checked nothing")

	for _, engine := range engines {
		t.Run(engine, func(t *testing.T) {
			stub := &stubPingClient{exitCode: 1}
			orig := authPingFactory
			authPingFactory = func(string, string, int) (pb.Client, error) { return stub, nil }
			t.Cleanup(func() { authPingFactory = orig })

			cfg := authPingTestConfig(t)
			err := pingEngineAuth(context.Background(), testLaunchDeps(t, cfg), cfg, engine, t.TempDir())
			require.Error(t, err)
			assert.Contains(t, err.Error(), engine, "error must name the engine that failed")
			assert.Contains(t, err.Error(), engineAuthFixHint(engine),
				"error must name THIS engine's specific fix, as production states it")
		})
	}
}

// TestPingEngineAuth_UnlistedEngine_GetsGenericFix: an engine that is not
// registered still fails loud, with a generic-but-actionable fix, rather
// than blanking on a missing declaration.
func TestPingEngineAuth_UnlistedEngine_GetsGenericFix(t *testing.T) {
	stub := &stubPingClient{exitCode: 1}
	orig := authPingFactory
	authPingFactory = func(string, string, int) (pb.Client, error) { return stub, nil }
	t.Cleanup(func() { authPingFactory = orig })

	cfg := authPingTestConfig(t)
	err := pingEngineAuth(context.Background(), testLaunchDeps(t, cfg), cfg, "some-future-engine", t.TempDir())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "authenticate the engine")
}

// TestLaunchDiscovery_FailedPing_NeverLaunches: the core new behavior (§12
// Q1) — when the auth ping fails, launchDiscovery returns the error and MUST
// NOT call through to launchEngineWithPromptFn at all. A dead first session
// inside a vendor TUI is invisible failure; this proves init never gets that
// far.
func TestLaunchDiscovery_FailedPing_NeverLaunches(t *testing.T) {
	testLaunchDeps(t, authPingTestConfig(t))
	stub := &stubPingClient{exitCode: 1}
	origFactory := authPingFactory
	authPingFactory = func(string, string, int) (pb.Client, error) { return stub, nil }
	t.Cleanup(func() { authPingFactory = origFactory })

	launchCalled := false
	origLaunch := launchEngineWithPromptFn
	launchEngineWithPromptFn = func(context.Context, launch.Launch, operations.Opened) error {
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
	testLaunchDeps(t, authPingTestConfig(t))
	stub := &stubPingClient{exitCode: 0}
	origFactory := authPingFactory
	authPingFactory = func(string, string, int) (pb.Client, error) { return stub, nil }
	t.Cleanup(func() { authPingFactory = origFactory })

	launchCalled := false
	origLaunch := launchEngineWithPromptFn
	launchEngineWithPromptFn = func(context.Context, launch.Launch, operations.Opened) error {
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

// TestLaunchDiscovery_SessionError_FailsLoudByDefaultDegradesUnderFlag: a
// session that starts (ping succeeded) but fails to launch or ends in error —
// an unminted harp, an interrupted setup, a crashed engine — must NOT be
// swallowed into a clean exit (CLAUDE.md's refuse-when-something-is-amiss
// posture: default REFUSE, --degraded WARNS and continues). This replaces the
// prior fault-tolerant "degrades to a warning always" behavior, which was the
// exact defect this fix closes: init's setup session could fail to start and
// `ctxloom init` would still report success.
//
// Both arms are pinned per CLAUDE.md's testing bar for a refuse/--degraded
// choke: the same failure must refuse by default and warn-then-continue
// under --degraded, never just one arm.
func TestLaunchDiscovery_SessionError_FailsLoudByDefaultDegradesUnderFlag(t *testing.T) {
	setup := func(t *testing.T) *cobra.Command {
		t.Helper()
		testLaunchDeps(t, authPingTestConfig(t))
		stub := &stubPingClient{exitCode: 0}
		origFactory := authPingFactory
		authPingFactory = func(string, string, int) (pb.Client, error) { return stub, nil }
		t.Cleanup(func() { authPingFactory = origFactory })

		origLaunch := launchEngineWithPromptFn
		launchEngineWithPromptFn = func(context.Context, launch.Launch, operations.Opened) error {
			return assert.AnError
		}
		t.Cleanup(func() { launchEngineWithPromptFn = origLaunch })

		cmd := &cobra.Command{}
		cmd.SetContext(context.Background())
		return cmd
	}

	t.Run("strict mode (default) refuses", func(t *testing.T) {
		resetStrictness(t)
		cmd := setup(t)

		var err error
		out := captureStdout(t, func() {
			err = launchDiscovery(cmd, "claude-code", t.TempDir()+"/.ctxloom", true)
		})
		require.Error(t, err, "a session that failed to launch must refuse, not exit clean")
		assert.NotContains(t, out, "/ctxloom-init", "no re-entry hint when the session itself errored")
	})

	t.Run("--degraded warns and continues", func(t *testing.T) {
		resetStrictness(t)
		degradedForTest(t)
		cmd := setup(t)

		var err error
		out := captureStdout(t, func() {
			err = launchDiscovery(cmd, "claude-code", t.TempDir()+"/.ctxloom", true)
		})
		require.NoError(t, err, "degraded mode is the escape hatch — it must not abort init")
		assert.NotContains(t, out, "/ctxloom-init", "no re-entry hint when the session itself errored")
	})
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
	launchEngineWithPromptFn = func(context.Context, launch.Launch, operations.Opened) error {
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
