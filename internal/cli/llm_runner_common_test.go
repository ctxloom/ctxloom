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

// TestLlmServe_MalformedConfigAbortsInsteadOfLaunching pins U037-F03: `llm
// serve`/`llm host`/`llm turn` are process-owning entry points that used to
// call config.Load() directly and never surface its warnings (via
// printConfigWarnings) or gate on them (via failOnFindings) — so a
// corrupted/malformed config.yaml silently downgraded to a warning and the
// engine launched with an empty/partial context regardless. standUpRunner now
// calls printConfigWarnings on every successful load, and all three RunEs
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
	// is exactly the class printConfigWarnings/failOnFindings must catch.
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

	// printConfigWarnings/failOnFindings write straight to os.Stderr (both
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

// TestRunnerMustRefuseNoConfigReachBack pins U037-F05: standUpRunner must not
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
		"a loaded config (however degraded) takes the normal serveRunnerMCP path instead")
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

// TestStandUpRunner_ConfiguresFromTheLabelItWasGiven is the U037-F23 pin. `llm
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
// of U037-F19: the reach-back is read into the standup's own state and then
// removed from the environment, so nothing the runner spawns can inherit it.
func TestConsumeCoordinatorReachBack_ReadsThenScrubs(t *testing.T) {
	env := map[string]string{
		coord.EnvCoordURL:         "tcp://127.0.0.1:1",
		coord.EnvCoordCred:        "the-token",
		coord.EnvRunID:            "run-7",
		coord.EnvAgentCoordinator: "1",
		coord.EnvCellWorkDir:      "/work/cell",
		"CTXLOOM_SESSION_HARP":    "regal-rash-dash",
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
	assert.False(t, reach.leaf, "a Coordinator-capable delegated child is not a leaf")

	for _, k := range coordinatorEnvKeys {
		assert.NotContainsf(t, env, k, "%s must not survive into the engine child's environment", k)
	}
}

// TestConsumeCoordinatorReachBack_LeafGate pins the leaf classification, which
// decides whether the coordinator-only MCP tools are served: a delegated child
// (RunID set) that was not resolved Coordinator-capable is a leaf; the top-level
// human session, which sets no RunID, never is.
func TestConsumeCoordinatorReachBack_LeafGate(t *testing.T) {
	leafFor := func(env map[string]string) bool {
		reach, err := consumeCoordinatorReachBack("mock",
			func(k string) string { return env[k] },
			func(string) error { return nil })
		require.NoError(t, err)
		return reach.leaf
	}
	assert.True(t, leafFor(map[string]string{coord.EnvRunID: "run-7"}), "delegated child, not coordinator-capable")
	assert.False(t, leafFor(map[string]string{coord.EnvRunID: "run-7", coord.EnvAgentCoordinator: "1"}))
	assert.False(t, leafFor(map[string]string{}), "the human session is never gated")
}

// TestConsumeCoordinatorReachBack_FailedScrubRefusesToLaunch is the U037-F19
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

// TestExportRunnerMCPSocket pins U037-F19's other swallowed syscall. The export
// used to be `_ = os.Setenv(...)`, so a failure left the endpoint listening and
// unaddressable — the child's shim would find no socket and stand up a rogue
// local coordinator nobody reads.
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
