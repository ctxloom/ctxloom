package cli

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/agentcoord/coord"
	"github.com/ctxloom/ctxloom/internal/config"
	"github.com/ctxloom/ctxloom/internal/lm/backends"
	"github.com/ctxloom/ctxloom/internal/shared/agent"
	"github.com/ctxloom/ctxloom/internal/testsupport"
)

// testConfig is an empty-but-valid config: enough for the leafness arithmetic
// below, which reads only the resolved delegation-depth cap. A local copy of
// internal/mcp's fixture of the same name — the two packages' test binaries
// share no code, and the fixture is four lines of literal.
func testConfig() *config.Config {
	return config.NewFixture(config.Fixture{
		LM:       config.LMConfig{Configs: map[string]config.LLMConfig{}},
		Profiles: config.ProfilesConfig{Definitions: map[string]config.Profile{}},
	})
}

// TestLlmServe_MalformedConfigAbortsInsteadOfLaunching pins that `llm
// serve`/`llm host`/`llm turn` are process-owning entry points that used to
// call config.Load() directly and never surface its warnings (via
// printAndRecordConfigWarnings) or gate on them (via failOnFindings) — so a
// corrupted/malformed config.yaml silently downgraded to a warning and the
// engine launched with an empty/partial context regardless. standUpRunner now
// calls printAndRecordConfigWarnings on every successful load, and all three RunEs
// checkpoint + gate on failOnFindings before doing anything process-owning.
//
// This reaches the fix without a live coordinator: with no CTXLOOM_COORD_*
// env set, standUpRunner's own "no reach-back" early return fires right after
// the config-load block this fix touches, well before anything that would
// need a real coordinator or spawn a real engine.
func TestLlmServe_MalformedConfigAbortsInsteadOfLaunching(t *testing.T) {
	dir := testsupport.ProjectDir(t)
	require.NoError(t, os.MkdirAll(filepath.Join(dir, ".ctxloom"), 0o755))
	// Invalid YAML: config.Load degrades this to a WarnKindParse warning
	// (CLAUDE.md fault tolerance — it does not become a Load() error), which
	// is exactly the class printAndRecordConfigWarnings/failOnFindings must catch.
	require.NoError(t, os.WriteFile(filepath.Join(dir, ".ctxloom", "config.yaml"), []byte("invalid: ["), 0o644))
	config.Invalidate()
	t.Cleanup(config.Invalidate)

	var out bytes.Buffer
	rootCmd.SetOut(&out)
	rootCmd.SetErr(&out)
	rootCmd.SetArgs([]string{"llm", "serve", "claude-code"})
	t.Cleanup(func() {
		rootCmd.SetOut(nil)
		rootCmd.SetErr(nil)
		rootCmd.SetArgs(nil)
	})

	// printAndRecordConfigWarnings/failOnFindings write straight to os.Stderr (both
	// standUpRunner and the RunE call them that way, matching run.go/
	// mcp_server.go), not through cmd.OutOrStdout()/cmd.ErrOrStderr() — so the
	// real fd, not rootCmd's buffer, is where the abort text lands.
	realStderr := os.Stderr
	r, w, perr := os.Pipe()
	require.NoError(t, perr)
	os.Stderr = w
	t.Cleanup(func() { os.Stderr = realStderr })

	err := rootCmd.Execute()
	require.NoError(t, w.Close())
	os.Stderr = realStderr
	captured, rerr := io.ReadAll(r)
	require.NoError(t, rerr)

	require.Error(t, err, "a malformed config.yaml must abort a process-owning runner entry point (llm serve), not silently launch the engine unconfigured")
	var exitErr *ExitError
	require.ErrorAs(t, err, &exitErr, "the abort must be failOnFindings' distinct fatal-findings exit, not an ordinary error")
	require.Equal(t, exitCodeFatalFindings, exitErr.Code)
	require.Contains(t, string(captured), "failed to parse config", "the finding must echo the underlying parse warning")
}

// TestLlmServe_UnknownBackendConfigIsNotFatal is the negative control: a
// clean config (no warnings) must NOT trip the new failOnFindings gate, so
// `llm serve` still reaches its "unknown backend" error for a bad backend
// name exactly as before this fix — proving the gate only fires on a real
// finding, not on every invocation.
func TestLlmServe_CleanConfigReachesUnknownBackendError(t *testing.T) {
	testsupport.ProjectDir(t)
	config.Invalidate()
	t.Cleanup(config.Invalidate)

	var out bytes.Buffer
	rootCmd.SetOut(&out)
	rootCmd.SetErr(&out)
	rootCmd.SetArgs([]string{"llm", "serve", "not-a-real-backend"})
	t.Cleanup(func() {
		rootCmd.SetOut(nil)
		rootCmd.SetErr(nil)
		rootCmd.SetArgs(nil)
	})

	err := rootCmd.Execute()
	require.Error(t, err)
	require.Contains(t, err.Error(), "unknown backend")
}

// TestRunnerMustRefuseNoConfigReachBack pins that standUpRunner must not
// silently BindHome (and let its caller launch the engine) when this runner
// hosts a delegated run but config.Load() failed — there would be no
// runner-local MCP endpoint, so CTXLOOM_MCP_SOCKET is never exported and the
// child's shim would stand up a rogue local coordinator nobody reads, exactly
// the condition the adjacent merr-driven fail-loud already refuses for.
//
// A live end-to-end repro of config.Load() returning a hard error (cfg ==
// nil) is impractical to construct here: config.Load's own fault tolerance
// (CLAUDE.md) downgrades essentially every real-world load fault — unreadable
// file, malformed YAML, schema-invalid document — to a warning on a non-nil
// Config (see TestLlmServe_MalformedConfigAbortsInsteadOfLaunching for that
// class), not to a Load() error; the two remaining internal error returns
// (confload.Merge's own doc calls them "believed unable to fail" for
// in-memory input) are not reachable from a real config.yaml. So this test
// pins the extracted decision directly rather than driving it through a real
// config load.
func TestRunnerMustRefuseNoConfigReachBack(t *testing.T) {
	hostedRun := &coord.EngineHost{}
	assert.True(t, runnerMustRefuseNoConfigReachBack(nil, hostedRun),
		"no config + a hosted delegated run must refuse to launch (no reach-back)")
	assert.False(t, runnerMustRefuseNoConfigReachBack(nil, nil),
		"no config but no hosted run (e.g. `llm serve` with no RunID) has nothing to refuse for")
	assert.False(t, runnerMustRefuseNoConfigReachBack(&config.Config{}, hostedRun),
		"a loaded config (however degraded) takes the normal mcp.ServeRunnerMCP path instead")
}

// labelCapturingBackend is a Configurable agent.Backend double that records the
// typed config standUpRunner resolved for it. It embeds the mock backend purely
// to satisfy agent.Backend; only Configure matters here.
type labelCapturingBackend struct {
	*backends.Mock
	got agent.BackendConfig
}

func (b *labelCapturingBackend) Configure(bc agent.BackendConfig) { b.got = bc }

// twoMockLabelProject writes a project whose config declares two mock-typed LLM
// labels with distinguishable models, so a label mix-up is visible in the
// resolved config rather than silently harmless.
func twoMockLabelProject(t *testing.T) {
	t.Helper()
	dir := testsupport.ProjectDir(t)
	require.NoError(t, os.MkdirAll(filepath.Join(dir, ".ctxloom"), 0o755))
	body := "llm:\n  configs:\n    alpha:\n      type: mock\n      model: model-alpha\n    beta:\n      type: mock\n      model: model-beta\n"
	require.NoError(t, os.WriteFile(filepath.Join(dir, ".ctxloom", "config.yaml"), []byte(body), 0o644))
	config.Invalidate()
	t.Cleanup(config.Invalidate)
}

// TestStandUpRunner_ConfiguresFromTheLabelItWasGiven pins that `llm
// host` and `llm turn` each used to copy their own --label into `llm serve`'s
// package-global llmServeLabel, because standUpRunner read that global instead
// of taking the label as an argument: three commands writing one mutable global
// to pass an argument down one call. The label is now a parameter, and this pins
// that the parameter is what selects the entry — nothing else can.
//
// No reach-back trio is set, so standUpRunner returns at its own "nothing to
// dial" early exit immediately after the config/configure block under test: no
// coordinator, no engine, no socket.
func TestStandUpRunner_ConfiguresFromTheLabelItWasGiven(t *testing.T) {
	twoMockLabelProject(t)
	testsupport.Isolate(t) // no CTXLOOM_COORD_* trio in the environment

	for _, label := range []string{"alpha", "beta"} {
		backend := &labelCapturingBackend{Mock: backends.NewMock()}
		standup, err := func() (*runnerStandup, error) {
			cmd, _ := testCmd()
			return standUpRunner(cmd, backend, "mock", label)
		}()
		require.NoError(t, err)
		require.NotNil(t, standup)

		cfg, ok := backend.got.(*backends.MockConfig)
		require.Truef(t, ok, "label %q must resolve a mock config, got %T", label, backend.got)
		assert.Equal(t, "model-"+label, cfg.Model,
			"the label passed to standUpRunner is what selects the entry")
	}
}

// TestConsumeCoordinatorReachBack_ReadsThenScrubs is the characterization half
// of the reach-back scrub: it is read into the standup's own state and then
// removed from the environment, so nothing the runner spawns can inherit it.
func TestConsumeCoordinatorReachBack_ReadsThenScrubs(t *testing.T) {
	env := map[string]string{
		coord.EnvCoordURL:      "tcp://127.0.0.1:1",
		coord.EnvCoordCred:     "the-token",
		coord.EnvRunID:         "run-7",
		coord.EnvRunDepth:      "1",
		coord.EnvCellWorkDir:   "/work/cell",
		"CTXLOOM_SESSION_HARP": "regal-rash-dash",
	}
	reach, err := consumeCoordinatorReachBack("mock",
		func(k string) string { return env[k] },
		func(k string) error { delete(env, k); return nil })

	require.NoError(t, err)
	assert.Equal(t, "tcp://127.0.0.1:1", reach.home.URL)
	assert.Equal(t, "the-token", reach.home.Token)
	assert.Equal(t, "run-7", reach.home.RunID)
	assert.Equal(t, "mock", reach.home.Harness)
	assert.Equal(t, "regal-rash-dash", reach.harp)
	assert.Equal(t, "/work/cell", reach.cellWorkDir)
	assert.Equal(t, 1, reach.depth, "the stamped depth is read as-is, leafness is decided later against the resolved cap")

	for _, k := range coordinatorEnvKeys {
		assert.NotContainsf(t, env, k, "%s must not survive into the engine child's environment", k)
	}
}

// TestConsumeCoordinatorReachBack_DepthParsing pins parseRunDepth's contract:
// unset, empty, and unparseable ALL read as depth 0 — never an error, never
// "unknown" — and a real numeric value round-trips exactly. This is read-only
// env parsing; the leaf DECISION (depth compared against the resolved cap)
// is a separate concern — see TestRunnerIsLeaf_* below.
func TestConsumeCoordinatorReachBack_DepthParsing(t *testing.T) {
	depthFor := func(env map[string]string) int {
		reach, err := consumeCoordinatorReachBack("mock",
			func(k string) string { return env[k] },
			func(string) error { return nil })
		require.NoError(t, err)
		return reach.depth
	}
	assert.Equal(t, 0, depthFor(map[string]string{}), "unset reads as depth 0 (the session owner)")
	assert.Equal(t, 0, depthFor(map[string]string{coord.EnvRunDepth: ""}), "empty reads as depth 0")
	assert.Equal(t, 0, depthFor(map[string]string{coord.EnvRunDepth: "not-a-number"}), "unparseable reads as depth 0, never an error")
	assert.Equal(t, 3, depthFor(map[string]string{coord.EnvRunDepth: "3"}), "a real numeric value round-trips exactly")
}

// TestRunnerIsLeaf_OwnerNeverLeafAtBuiltInCap pins the regression this whole
// design exists to prevent: depth 0, conversational (the session owner, on
// EITHER the plugin-hosted or the container ViaStartRun owned-run path) is
// never a leaf at the built-in default cap.
func TestRunnerIsLeaf_OwnerNeverLeafAtBuiltInCap(t *testing.T) {
	assert.False(t, runnerIsLeaf(0, false, testConfig()))
}

// TestRunnerIsLeaf_DelegatedChildIsLeafAtBuiltInCap pins the other direction:
// a depth-1 delegated child IS a leaf at the built-in default cap (1) — it
// must not receive the coordinator-only MCP tools.
func TestRunnerIsLeaf_DelegatedChildIsLeafAtBuiltInCap(t *testing.T) {
	assert.True(t, runnerIsLeaf(1, false, testConfig()))
}

// TestRunnerIsLeaf_CapBoundary pins the exact boundary the comparison must
// use: a run AT the cap is a leaf, a run one shallower is not. Using a
// RAISED cap (3, not the built-in 1) also proves the cap is read from
// config, not hardcoded — see the next test for the direct version of that
// claim.
func TestRunnerIsLeaf_CapBoundary(t *testing.T) {
	cfg := config.NewFixture(config.Fixture{Delegation: config.DelegationConfig{Depth: 3}})
	assert.False(t, runnerIsLeaf(2, false, cfg), "one shallower than the cap must NOT be a leaf")
	assert.True(t, runnerIsLeaf(3, false, cfg), "AT the cap must be a leaf")
}

// TestRunnerIsLeaf_CapIsReadFromConfig pins that the cap is genuinely
// resolved from the loaded config, not the built-in default: with
// delegation.depth raised to 2, a depth-1 caller (a leaf at the built-in
// default of 1) is no longer a leaf. If GetDelegationDepth ever started
// ignoring the config value and returning only the built-in default, this
// assertion would flip to true and the test would fail — the config key
// would be decorative.
func TestRunnerIsLeaf_CapIsReadFromConfig(t *testing.T) {
	raised := config.NewFixture(config.Fixture{Delegation: config.DelegationConfig{Depth: 2}})
	assert.False(t, runnerIsLeaf(1, false, raised), "delegation.depth=2 must let a depth-1 caller through")
	assert.True(t, runnerIsLeaf(1, false, testConfig()), "the built-in default (1) still gates the same caller")
}

// TestRunnerIsLeaf_OneShotIsLeafEvenAtDepthZero pins the newest rule: a
// `driving: oneshot` run is a leaf REGARDLESS of depth — its engine tears
// down at every turn boundary, so it cannot hold a coordination
// relationship with a child across turns. Depth 0 is the interesting case:
// the depth term ALONE would say "not a leaf" (see
// TestRunnerIsLeaf_OwnerNeverLeafAtBuiltInCap), so this is the one case
// that actually exercises the OR.
func TestRunnerIsLeaf_OneShotIsLeafEvenAtDepthZero(t *testing.T) {
	assert.True(t, runnerIsLeaf(0, true, testConfig()))
}

// TestRunnerIsLeaf_ConversationalAtDepthZeroIsNotLeaf is the control for the
// test above: a conversational (non-oneshot) run at depth 0 is NOT a leaf,
// so a rule that degenerated to "always leaf" (e.g. dropping the `oneshot ||`
// and returning true unconditionally) cannot pass both tests at once.
func TestRunnerIsLeaf_ConversationalAtDepthZeroIsNotLeaf(t *testing.T) {
	assert.False(t, runnerIsLeaf(0, false, testConfig()))
}

// TestConsumeCoordinatorReachBack_FailedScrubRefusesToLaunch pins the
// fix: the scrub used to be `_ = os.Unsetenv(k)`, so a key that survived left
// the coordinator credential in the environment the engine child inherits —
// third-party code handed the token, with the runner reporting a clean standup.
// It is now a refusal, and the refusal names every key that stuck.
func TestConsumeCoordinatorReachBack_FailedScrubRefusesToLaunch(t *testing.T) {
	_, err := consumeCoordinatorReachBack("mock",
		func(string) string { return "sensitive" },
		func(k string) error {
			if k == coord.EnvCoordCred {
				return assert.AnError
			}
			return nil
		})

	require.Error(t, err, "an unscrubbed credential must abort the standup")
	assert.Contains(t, err.Error(), coord.EnvCoordCred, "the refusal must name the key that survived")
	assert.NotContains(t, err.Error(), "sensitive", "the refusal must not echo the credential value")
}

// TestExportRunnerMCPSocket pins the other swallowed syscall in the same
// reach-back path. The export used to be `_ = os.Setenv(...)`, so a failure
// left the endpoint listening and unaddressable — the child's shim would
// find no socket and stand up a rogue local coordinator nobody reads.
func TestExportRunnerMCPSocket(t *testing.T) {
	var gotKey, gotVal string
	require.NoError(t, exportRunnerMCPSocket(func(k, v string) error {
		gotKey, gotVal = k, v
		return nil
	}, "/run/ctxloom/local/mcp-1.sock"))
	assert.Equal(t, coord.EnvMCPSocket, gotKey)
	assert.Equal(t, "/run/ctxloom/local/mcp-1.sock", gotVal)

	err := exportRunnerMCPSocket(func(string, string) error { return assert.AnError }, "/sock")
	require.Error(t, err, "a failed export must be reported, not dropped")
	assert.Contains(t, err.Error(), coord.EnvMCPSocket)
}

// The three tests below are the characterization set for standUpRunner's
// split into named concerns. Behaviour is unchanged by definition, so no
// test can discriminate the refactor (template §4, case 2: pure complexity
// reduction) — their job is to be green before AND after, covering every arm the
// split moves that is reachable without a live coordinator.
//
// The arms deliberately NOT covered here, and why: dial-home failure,
// EngineHost creation, and mcp.ServeRunnerMCP failure all need a real coordinator
// endpoint (coord.NewHome retries with backoff), which is integration territory,
// not a unit gate. The two fail-loud decisions those arms guard are pinned
// directly instead — runnerMustRefuseNoConfigReachBack and
// exportRunnerMCPSocket, above.

// TestStandUpRunner_NoReachBackIsAQuietNoOp: with no coordinator trio in the
// environment there is nothing to dial or host, and that is a success — a
// top-level `llm serve`, or a `llm host` launched by hand.
func TestStandUpRunner_NoReachBackIsAQuietNoOp(t *testing.T) {
	twoMockLabelProject(t)
	testsupport.Isolate(t)

	cmd, _ := testCmd()
	standup, err := standUpRunner(cmd, backends.NewMock(), "mock", "")

	require.NoError(t, err)
	require.NotNil(t, standup)
	assert.Nil(t, standup.home, "nothing was dialed")
	assert.Nil(t, standup.engineHost, "no delegated run is hosted")
	assert.Nil(t, standup.endpointClose, "no runner-local MCP endpoint was stood up")
	standup.teardown() // must be safe on an all-nil standup
}

// TestStandUpRunner_ConfiguresEvenWithoutAReachBack: the config-load and
// backend-configure concern runs before the reach-back decision, so an
// unconnected runner is still configured from its label.
func TestStandUpRunner_ConfiguresEvenWithoutAReachBack(t *testing.T) {
	twoMockLabelProject(t)
	testsupport.Isolate(t)

	backend := &labelCapturingBackend{Mock: backends.NewMock()}
	cmd, _ := testCmd()
	_, err := standUpRunner(cmd, backend, "mock", "beta")

	require.NoError(t, err)
	require.NotNil(t, backend.got, "the backend is configured regardless of reach-back")
}

// TestStandUpRunner_UnconfigurableBackendIsNotAnError: a backend that does not
// implement backends.Configurable takes the same path with no config applied.
func TestStandUpRunner_UnconfigurableBackendIsNotAnError(t *testing.T) {
	twoMockLabelProject(t)
	testsupport.Isolate(t)

	cmd, _ := testCmd()
	standup, err := standUpRunner(cmd, backends.NewMock(), "mock", "beta")

	require.NoError(t, err)
	require.NotNil(t, standup)
}
