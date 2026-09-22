package coord

import (
	"sort"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/ctxloom/ctxloom/internal/core/sessions"
)

// envKeys is the SET of names an env map stamps, sorted.
func envKeys(env map[string]string) []string {
	keys := make([]string, 0, len(env))
	for k := range env {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// TestRunnerEnv_IsTheReachBackTrioAlone: ONE producer (runnerEnv) builds
// every runner env, the session owner's own runner included (StartOwnedRun
// stamps it on a child's terms), and it is the reach-back trio and nothing
// else — a run's identity rides the Launch, and no reader takes a harp, a
// depth or a one-shot fact from the environment.
func TestRunnerEnv_IsTheReachBackTrioAlone(t *testing.T) {
	const url = "http://127.0.0.1:1/mcp"
	env := runnerEnv("run-1", "tok", url)

	assert.ElementsMatch(t, envKeys(sessions.EncodeReach(sessions.Endpoint{URL: url, Credential: "tok"}, "run-1")), envKeys(env),
		"the runner env is the reach-back trio, nothing more")
	assert.NotContains(t, env, "CTXLOOM_SESSION_HARP", "identity rides the Launch, never the environment")
	assert.Equal(t, "run-1", env[EnvRunID])
	assert.Equal(t, "tok", env[EnvCoordCred])
	assert.Equal(t, url, env[EnvCoordURL])

	// A degraded (no reach-back) launch drops the trio WHOLE.
	assert.Empty(t, runnerEnv("run-1", "tok", ""))
}
