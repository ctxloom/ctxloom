//go:build integration

package integration

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/tests/integration/testenv"
)

// ctxloom writes on three channels and they must not cross:
//
//	stdout   the ENGINE's protocol payload. A hook whose stdout carries
//	         anything else breaks the parser on the other end.
//	stderr   HUMAN-readable diagnostics (clidiag), hooks included. Claude Code
//	         renders a SessionStart hook's stderr as an error and a statusline's
//	         stderr lands outside the alt-screen, so this surface is expensive
//	         and is reserved for messages a person can act on.
//	log file the STRUCTURED zap stream, ~/.ctxloom/logs/ctxloom.log.
//
// The routing landed with the move of the zap sink off stderr, but nothing
// asserted where each channel's bytes actually go — logsink's own tests cover
// the file mechanics (creates the dir, appends, rolls an oversized log aside)
// and stop there. The separation has already been broken once in this codebase
// in the other direction: redirecting clidiag broke the fail-loud contract that
// TestHookApproach_MissingCacheFileInjectsNothingAndSaysSo pins. It is a
// two-sided invariant, so both sides are asserted here.
//
// The provocation is a home config carrying `agents` keys. Those are
// ScopeShared, so dropLayerScopeViolations discards them at load and emits a
// warn-level `config_layer_scope_warning` — one warn-level log per load, on a
// path every command takes, which is exactly the traffic that made structured
// output on stderr intolerable in the first place.
func TestLogChannels_StructuredGoesToTheFileAndNeverToAHooksStderr(t *testing.T) {
	env, err := testenv.NewTestEnvironment()
	require.NoError(t, err)
	t.Cleanup(func() { _ = env.Cleanup() })

	home := t.TempDir()
	projectDir := t.TempDir()

	// A home config whose keys are not allowed at the home layer: dropped on
	// load, with a warn-level structured log each time.
	require.NoError(t, os.MkdirAll(filepath.Join(home, ".ctxloom"), 0o755))
	require.NoError(t, os.WriteFile(
		filepath.Join(home, ".ctxloom", "config.yaml"),
		[]byte("version: \"1.0.0\"\nagents:\n  scoped-out:\n    runtime: host\n"),
		0o644,
	))

	// A hook is the sharpest case: its stdout is a protocol channel, and it is
	// the process whose stderr the engine renders.
	// extraEnv is appended after the isolated environment, so HOME here wins
	// and both the config load and the log sink resolve under this test's home.
	cmd := env.Command(
		[]string{"HOME=" + home, "CTXLOOM_HOME=" + home},
		"hook", "inject-context", "--project", projectDir, "deadbeefdeadbeef",
	)
	cmd.Stdin = strings.NewReader(`{"session_id":"log-channels","source":"startup"}`)
	var stderr strings.Builder
	cmd.Stderr = &stderr
	stdout, runErr := cmd.Output()
	require.NoError(t, runErr, "the hook must not fail the engine's session start; stderr: %s", stderr.String())

	logPath := filepath.Join(home, ".ctxloom", "logs", "ctxloom.log")
	logBytes, readErr := os.ReadFile(logPath) //nolint:gosec // a path this test built
	require.NoError(t, readErr, "the structured log must exist at %s", logPath)
	logged := string(logBytes)

	// PRECONDITION. Without a warn-level record actually produced, every
	// "no zap JSON on stderr" assertion below would pass for the trivial reason
	// that nothing was logged anywhere.
	require.Contains(t, logged, structuredWarnKey,
		"fixture failure: the home config was supposed to provoke a warn-level structured log, and did not")

	// 1. STDOUT is the protocol payload and nothing else.
	var payload map[string]any
	require.NoError(t, json.Unmarshal(stdout, &payload),
		"the hook's stdout must be the engine's parseable payload, got %q", string(stdout))
	assert.NotContains(t, string(stdout), structuredWarnKey,
		"structured log records must never reach a hook's stdout, which the engine parses")

	// 2. STDERR carries no structured output.
	assert.NotContains(t, stderr.String(), structuredWarnKey,
		"the structured record reached a hook's stderr, which the engine renders as an error")
	assert.NotContains(t, stderr.String(), `"level":"warn"`,
		"zap JSON reached a hook's stderr")

	// 3. The LOG FILE carries the structured stream and not the human channel.
	// clidiag's messages are prefixed for a reader; a prefixed line in the JSON
	// stream means the two writers were pointed at one destination.
	for _, line := range strings.Split(logged, "\n") {
		require.False(t, strings.HasPrefix(strings.TrimSpace(line), "ctxloom:"),
			"a human-readable clidiag line reached the structured log: %q", line)
	}
}

// structuredWarnKey is the zap message the fixture provokes. Naming the record
// rather than grepping for "warn" keeps the assertions specific to the record
// this test actually caused.
const structuredWarnKey = "config_layer_scope_warning"
