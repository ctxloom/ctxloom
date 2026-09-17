package coord

import (
	"sort"
	"testing"

	"github.com/stretchr/testify/assert"
)

// envKeys is the SET of names an env map stamps, sorted — the thing the parity
// test below compares. Values differ legitimately between an owner and a child
// (different harp, depth, run id); the KEY SET must not.
func envKeys(env map[string]string) []string {
	keys := make([]string, 0, len(env))
	for k := range env {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// TestOwnerRunnerEnv_SameKeySetAsChild is the regression guard for the defect
// that made this constructor exist: the session owner's runner env and a
// spawned child's runner env were built by TWO DIFFERENT PRODUCERS
// (mcp.SessionOwnerEnv hand-built a two-key map; coord.runnerEnv stamps the
// full set), so every stamp the owner's producer did not know about went
// missing on the owner's own runner alone.
//
// A missing stamp is INVISIBLE at the consumer: every one of these vars reads
// unset as its safe default (llm_runner_common.go's consumeCoordinatorReachBack
// — parseRunDepth("") -> 0 for depth, `== "true"` for oneshot), so a producer
// that omits a key looks exactly like a coordinator that meant "off". Nothing
// downstream can tell the difference, which is precisely why the divergence
// has to be caught HERE, at the producers, by comparing key sets.
//
// It compares SETS, not values: the owner's depth (0), oneshot (false) and run
// id ("" — the owner owns no spawned run) are legitimately different from a
// child's. Only presence is the invariant.
func TestOwnerRunnerEnv_SameKeySetAsChild(t *testing.T) {
	c := &Coordinator{}

	const url = "http://127.0.0.1:1/mcp"
	owner := c.OwnerRunnerEnv("owner-harp", "owner-tok", url)
	child := runnerEnv("child-harp", "run-1", "child-tok", url, 1, false)

	assert.Equal(t, envKeys(child), envKeys(owner),
		"the session owner's runner env and a child's must carry the SAME KEY SET — "+
			"a key present on one and absent on the other is the two-producer drift this test exists to catch")

	// Degraded (no reach-back) launches drop the trio WHOLE, on both sides
	// alike: parity must hold there too, or the owner would be the only run
	// whose degraded shape is special.
	ownerDegraded := c.OwnerRunnerEnv("owner-harp", "owner-tok", "")
	childDegraded := runnerEnv("child-harp", "run-1", "child-tok", "", 1, false)
	assert.Equal(t, envKeys(childDegraded), envKeys(ownerDegraded),
		"parity must hold on a degraded launch too, where both sides omit the reach-back trio whole")

	// The owner's own values, which differ from a child's by design and so are
	// pinned here rather than compared: depth 0 (it is the root of the
	// delegation tree), never one-shot, and no spawned-run id.
	assert.Equal(t, "0", owner[EnvRunDepth], "the session owner is depth 0 — the root, not a child")
	assert.Equal(t, "false", owner[EnvRunOneShot], "the session owner's own run is never the oneshot spawn plan")
	assert.Equal(t, "", owner[EnvRunID], "the owner owns no spawned run, so its run id is empty — present, but empty")
	assert.Equal(t, "owner-harp", owner["CTXLOOM_SESSION_HARP"],
		"the harp is stamped by the constructor, which is what retires cli/run.go's hand-patch of this one key")
	assert.Equal(t, "owner-tok", owner[EnvCoordCred])
	assert.Equal(t, url, owner[EnvCoordURL])
}
