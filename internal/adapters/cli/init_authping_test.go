package cli

import (
	"context"
	"errors"
	"testing"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/adapters/fsstore"
	"github.com/ctxloom/ctxloom/internal/adapters/operations"
	"github.com/ctxloom/ctxloom/internal/core/agent"
	"github.com/ctxloom/ctxloom/internal/core/agents"
	"github.com/ctxloom/ctxloom/internal/core/composite"
	"github.com/ctxloom/ctxloom/internal/core/config"
	"github.com/ctxloom/ctxloom/internal/core/coord"
	"github.com/ctxloom/ctxloom/internal/core/engine"
	"github.com/ctxloom/ctxloom/internal/core/launch"
	"github.com/ctxloom/ctxloom/internal/core/sessions"
	"github.com/ctxloom/ctxloom/internal/engines"
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
		SessionClaims: fsstore.SessionClaims,
		Snapshot:      &config.Snapshot{Config: cfg},
		Engines:       engines.Registry(),
		Assembler:     launchtestAssembler{},
		Cells:         dryCells{},
		Endpoints:     sequenceMinter{},
		Sessions:      sessions.NewMemStore(),
		Host:          launch.HostFacts{Home: t.TempDir(), CtxloomHome: t.TempDir(), Binary: "ctxloom"},
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

// stubRunHost is a minimal operations.RunHost for pingEngineAuth /
// launchDiscovery tests: StartOwnedRun records the launch the probe started
// (never starting a runner), Turn records the prompt and answers "ok" or the
// configured failure, so a test asserts what pingEngineAuth actually asked
// for without a coordinator or an engine subprocess. A test installs it
// through stubPingHosts.
type stubRunHost struct {
	turnErr   error
	gotLaunch *launch.Launch
	gotPrompt string
}

func (s *stubRunHost) Owner() coord.Identity { return coord.Identity{Harp: "test-owner"} }
func (s *stubRunHost) StartOwnedRun(_ context.Context, _ coord.Identity, spec coord.OwnerRun, _ coord.OwnedRunStarter, _ string) (*coord.RunOutcome, error) {
	l := spec.Launch
	s.gotLaunch = &l
	return &coord.RunOutcome{RunID: "run-probe", Harp: l.Identity.Harp}, nil
}
func (s *stubRunHost) Turn(_ context.Context, _ string, t engine.Turn) (engine.TurnResult, error) {
	s.gotPrompt = t.Prompt
	if s.turnErr != nil {
		return engine.TurnResult{}, s.turnErr
	}
	return engine.TurnResult{Answer: "ok"}, nil
}

// stubPingHosts installs stub as the probe's RunHost for the test's duration.
func stubPingHosts(t *testing.T, stub *stubRunHost) {
	t.Helper()
	orig := authPingHosts
	authPingHosts = operations.RunHostFunc(func(context.Context, string, string) (operations.RunHost, error) { return stub, nil })
	t.Cleanup(func() { authPingHosts = orig })
}

// errEngineDead is the failure a dead engine's turn reports (as a real
// backend does when auth is missing).
var errEngineDead = errors.New("engine exited with code 1")

// TestPingEngineAuth_Succeeds: a healthy engine's one-shot turn (an answer)
// clears the gate with no error — the ping is a liveness check, not a login
// flow, so success just means "proceed."
func TestPingEngineAuth_Succeeds(t *testing.T) {
	stub := &stubRunHost{}
	stubPingHosts(t, stub)

	cfg := authPingTestConfig(t)
	err := pingEngineAuth(context.Background(), testLaunchDeps(t, cfg), cfg, "claude-code", t.TempDir())
	require.NoError(t, err)

	// The smallest possible prompt actually reached the engine, as a turn on
	// a structured run.
	require.NotNil(t, stub.gotLaunch)
	assert.Equal(t, authPingTask, stub.gotPrompt)
	assert.Equal(t, engine.Structured, stub.gotLaunch.Mode)
}

// TestPingEngineAuth_RequestsBypassPermissionExplicitly pins that the ping
// asks for permissions: bypass on its launch explicitly, rather than riding
// whatever the chosen engine's llm label declares (or doesn't).
// authPingTestConfig declares no llm permissions at all, so before
// pingEngineAuth carried this override, its launch depended entirely on
// operations.effectiveMemberPermission's floor for an unset posture — a
// floor unroasted-spinning replaced with a refusal. This is a PAYLOAD
// assertion on the launch the run started with (Launch.Permission), not
// just "the ping succeeded": a caller-side fallback that quietly caught a
// refusal and retried some other way could still pass a success-only
// assertion without this launch ever carrying bypass.
func TestPingEngineAuth_RequestsBypassPermissionExplicitly(t *testing.T) {
	stub := &stubRunHost{}
	stubPingHosts(t, stub)

	cfg := authPingTestConfig(t)
	err := pingEngineAuth(context.Background(), testLaunchDeps(t, cfg), cfg, "claude-code", t.TempDir())
	require.NoError(t, err)

	require.NotNil(t, stub.gotLaunch)
	assert.Equal(t, agent.PermissionBypass, stub.gotLaunch.Permission,
		"the ping must carry an explicit bypass posture on the launch, not rely on the label's configured (or unset) permissions")
}

// discoveryLaunch drives launchDiscovery over stateless deps with a
// succeeding probe, capturing the discovery Launch handed to the engine.
func discoveryLaunch(t *testing.T, cfg *config.Config) launch.Launch {
	t.Helper()
	testLaunchDeps(t, cfg)
	stub := &stubRunHost{}
	stubPingHosts(t, stub)

	var got launch.Launch
	origLaunch := launchEngineWithPromptFn
	launchEngineWithPromptFn = func(_ context.Context, _ launch.Deps, _ string, l launch.Launch) error { got = l; return nil }
	t.Cleanup(func() { launchEngineWithPromptFn = origLaunch })

	cmd := &cobra.Command{}
	cmd.SetContext(context.Background())
	_ = captureStdout(t, func() { require.NoError(t, launchDiscovery(cmd, "claude-code", t.TempDir()+"/.ctxloom", true)) })
	require.NotEmpty(t, got.Identity.Harp, "the discovery session resolved")
	// The probe ran under its OWN harp; the session the human works in is
	// another identity.
	require.NotNil(t, stub.gotLaunch)
	assert.NotEqual(t, stub.gotLaunch.Identity.Harp, got.Identity.Harp,
		"the auth probe and the discovery session are two launches with two identities")
	return got
}

// TestDiscoveryLaunch_StatesDefaultPermissionExplicitly pins the discovery
// launch's one-rung posture: this project's declared default, else the
// pinned default — never the engine's host default, never a label's.
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

// TestPingEngineAuth_FailsLoud_NamesTheFix: a dead engine (a failed turn, as
// a real backend reports when auth is missing) fails the ping with an error
// naming BOTH the engine and its specific fix — never a bare "failed."
//
// EVERY registered backend runs the SAME assertions: this is a conformance
// suite over operations.EngineNames(), not a hand-maintained table of engine/expected
// pairs. A table drifts the moment a backend is added or removed, and it
// duplicates the fix strings each engine's own declaration owns — so the expected
// text is read from production via engineAuthFixHint rather than re-typed
// here. A newly registered backend is covered without editing this file.
func TestPingEngineAuth_FailsLoud_NamesTheFix(t *testing.T) {
	engines := operations.EngineNames(engines.Registry())
	require.NotEmpty(t, engines,
		"the backend registry is empty — every subtest below would be skipped and this suite would pass having checked nothing")

	for _, engine := range engines {
		t.Run(engine, func(t *testing.T) {
			stub := &stubRunHost{turnErr: errEngineDead}
			stubPingHosts(t, stub)

			cfg := authPingTestConfig(t)
			err := pingEngineAuth(context.Background(), testLaunchDeps(t, cfg), cfg, engine, t.TempDir())
			require.Error(t, err)
			assert.Contains(t, err.Error(), engine, "error must name the engine that failed")
			assert.Contains(t, err.Error(), engineAuthFixHint(engine),
				"error must name THIS engine's specific fix, as production states it")
		})
	}
}

// TestPingEngineAuth_ReportsTheEngineError_NotAnAuthVerdict: the probe's
// message carries the ENGINE'S OWN error and offers authentication as a
// CANDIDATE cause, never as the verdict.
//
// This is the regression that cost the most. The probe used to report every
// failure as "auth check failed" and hand back only the login hint, so a
// launch the engine refused for its own reasons — here, a config file it
// would not start against — sent the user to re-run a login that was already
// good, while the actual refusal went unmentioned. The engine's error is the
// load-bearing part of the message; the hint is a guess and must read as one.
func TestPingEngineAuth_ReportsTheEngineError_NotAnAuthVerdict(t *testing.T) {
	refusal := errors.New("Invalid MCP configuration: MCP config file not found")
	stub := &stubRunHost{turnErr: refusal}
	stubPingHosts(t, stub)

	cfg := authPingTestConfig(t)
	err := pingEngineAuth(context.Background(), testLaunchDeps(t, cfg), cfg, "claude-code", t.TempDir())
	require.Error(t, err)

	assert.Contains(t, err.Error(), refusal.Error(),
		"the engine's own refusal is the load-bearing part of the message and must survive into it")
	assert.Contains(t, err.Error(), probeDidNotAnswer,
		"the message must name what actually failed — the liveness probe — as production states it")
	assert.Contains(t, err.Error(), probeAuthGuess,
		"the auth hint must stay CONDITIONAL; an unconditional auth verdict is what sent users to fix working credentials")
}

// TestPingEngineAuth_UnlistedEngine_GetsGenericFix: an engine that is not
// registered still fails loud, with a generic-but-actionable fix, rather
// than blanking on a missing declaration.
func TestPingEngineAuth_UnlistedEngine_GetsGenericFix(t *testing.T) {
	stub := &stubRunHost{turnErr: errEngineDead}
	stubPingHosts(t, stub)

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
	stub := &stubRunHost{turnErr: errEngineDead}
	stubPingHosts(t, stub)

	launchCalled := false
	origLaunch := launchEngineWithPromptFn
	launchEngineWithPromptFn = func(context.Context, launch.Deps, string, launch.Launch) error {
		launchCalled = true
		return nil
	}
	t.Cleanup(func() { launchEngineWithPromptFn = origLaunch })

	cmd := &cobra.Command{}
	cmd.SetContext(context.Background())

	err := launchDiscovery(cmd, "claude-code", t.TempDir()+"/.ctxloom", true)
	require.Error(t, err, "a failed ping must fail init loud, not degrade")
	assert.False(t, launchCalled, "the engine must never be launched after a failed auth ping")
	assert.Contains(t, err.Error(), "ctxloom auth mint --engine claude-code --mode token")
}

// TestLaunchDiscovery_SuccessfulPing_LaunchesAndPrintsReentryHint: a healthy
// ping proceeds to the launch, and once that session ends, init prints the
// re-entry hint (connect via the configured client / `/ctxloom-init`) with no
// relaunch prompt of its own — init hands off once and is done.
func TestLaunchDiscovery_SuccessfulPing_LaunchesAndPrintsReentryHint(t *testing.T) {
	testLaunchDeps(t, authPingTestConfig(t))
	stub := &stubRunHost{}
	stubPingHosts(t, stub)

	launchCalled := false
	origLaunch := launchEngineWithPromptFn
	launchEngineWithPromptFn = func(context.Context, launch.Deps, string, launch.Launch) error {
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
		stub := &stubRunHost{}
		stubPingHosts(t, stub)

		origLaunch := launchEngineWithPromptFn
		launchEngineWithPromptFn = func(context.Context, launch.Deps, string, launch.Launch) error {
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
	origHosts := authPingHosts
	authPingHosts = operations.RunHostFunc(func(context.Context, string, string) (operations.RunHost, error) {
		pingCalled = true
		return &stubRunHost{}, nil
	})
	t.Cleanup(func() { authPingHosts = origHosts })

	launchCalled := false
	origLaunch := launchEngineWithPromptFn
	launchEngineWithPromptFn = func(context.Context, launch.Deps, string, launch.Launch) error {
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
	origHosts := authPingHosts
	authPingHosts = operations.RunHostFunc(func(context.Context, string, string) (operations.RunHost, error) {
		pingCalled = true
		return &stubRunHost{}, nil
	})
	t.Cleanup(func() { authPingHosts = origHosts })

	cmd := &cobra.Command{}
	cmd.SetContext(context.Background())

	err := launchDiscovery(cmd, "claude-code", t.TempDir()+"/.ctxloom", true)
	require.NoError(t, err)
	assert.False(t, pingCalled)
}

// recordingCells records the CellRequest a launch asked for, over an inner
// Cells.
type recordingCells struct {
	inner launch.Cells
	got   *launch.CellRequest
}

func (c recordingCells) Prepare(ctx context.Context, req launch.CellRequest) (launch.Cell, error) {
	*c.got = req
	return c.inner.Prepare(ctx, req)
}

// The setup probe runs in the DEFAULT AGENT's declared auth mode — the
// credential of the session init is about to launch — so an init whose
// default agent shares the human's login never mints a token nobody will
// use. With no default agent it declares nothing.
func TestPingEngineAuth_RunsInTheDefaultAgentsAuthMode(t *testing.T) {
	for _, tc := range []struct {
		name string
		cfg  config.Fixture
		want string
	}{
		{"the default agent's login", config.Fixture{
			Agents:       map[string]agents.Agent{"dev": {Name: "dev", LLM: "claude-code", Auth: "login"}},
			DefaultAgent: "dev",
		}, string(engine.AuthLogin)},
		{"no default agent", config.Fixture{}, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			stubPingHosts(t, &stubRunHost{})
			tc.cfg.AppPaths = []string{t.TempDir()}
			cfg := gatedFixture(tc.cfg)
			deps := testLaunchDeps(t, cfg)
			var got launch.CellRequest
			deps.Cells = recordingCells{inner: deps.Cells, got: &got}
			require.NoError(t, pingEngineAuth(context.Background(), deps, cfg, "claude-code", t.TempDir()))
			assert.Equal(t, tc.want, got.Auth)
		})
	}
}
