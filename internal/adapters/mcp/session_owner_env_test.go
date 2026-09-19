package mcp

import (
	"sort"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/adapters/agents"
	"github.com/ctxloom/ctxloom/internal/core/agent"
	"github.com/ctxloom/ctxloom/internal/core/coord"
)

func envKeySet(env map[string]string) []string {
	keys := make([]string, 0, len(env))
	for k := range env {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// TestSessionOwnerEnv_IsTheCoordinatorsOwnProducer closes the half of the
// two-producer defect that the coord-side parity test
// (TestOwnerRunnerEnv_SameKeySetAsChild) cannot see. That test proves the
// coordinator stamps an owner and a child alike; this one proves the OWNER'S
// ENV ACTUALLY COMES FROM IT. The original defect lived here, not there:
// SessionOwnerEnv hand-built a two-key map, so a coordinator that stamped
// everything correctly still handed the session owner's runner a map missing
// the depth, oneshot and spool-posture stamps — and cli/run.go patched the one
// noticed key (CTXLOOM_SESSION_HARP) back in by hand. Anyone re-introducing a
// hand-built map here would leave the coord test green.
//
// It compares against OwnerRunnerEnv rather than a hardcoded key list on
// purpose: a list written down here would be a THIRD statement of the same
// truth, and would have to be remembered every time a new stamp is added —
// exactly the maintenance the defect came from. Deriving the expectation from
// the one producer means a new stamp needs no edit here and cannot be
// forgotten.
func TestSessionOwnerEnv_IsTheCoordinatorsOwnProducer(t *testing.T) {
	_, c, _ := buildHostCoordinator(t, map[string]agents.Agent{
		"worker": headlessAgent("p1"),
	})

	env, err := SessionOwnerEnv(c, "owner-harp", agent.RuntimeHost)
	require.NoError(t, err)

	// The credential is freshly minted per call, so the expectation is rebuilt
	// from THIS call's own token/url: the comparison under test is the key set
	// and the stamped postures, not the secret.
	want := c.OwnerRunnerEnv("owner-harp", env[coord.EnvCoordCred], env[coord.EnvCoordURL])
	assert.Equal(t, envKeySet(want), envKeySet(env),
		"SessionOwnerEnv must delegate to coord.OwnerRunnerEnv, not hand-build a map — "+
			"a key it omits goes missing on the human's own top-level runner alone, and reads downstream as a deliberate default")
	assert.Equal(t, want, env, "including the values: this function mints the credential and resolves the endpoint, then hands both to the one producer")

	// The keys the hand-built map used to lose, named explicitly so a failure
	// says what went missing rather than only that two maps differ.
	assert.Contains(t, env, "CTXLOOM_SESSION_HARP", "the harp is stamped by the producer — cli/run.go no longer patches it back in")
	assert.Contains(t, env, coord.EnvRunDepth)
	assert.Contains(t, env, coord.EnvRunOneShot)
	assert.Equal(t, "owner-harp", env["CTXLOOM_SESSION_HARP"])
	assert.Equal(t, "0", env[coord.EnvRunDepth], "the session owner is the root of the delegation tree")
	assert.NotEmpty(t, env[coord.EnvCoordCred], "the minted owner credential still rides")
}
