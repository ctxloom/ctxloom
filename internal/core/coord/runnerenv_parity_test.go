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

// TestOwnerRunnerEnv_IsTheChildsTrioPlusThePluginArmsHarp: ONE producer
// (runnerEnv) builds every runner env. A hosted run's is the reach-back
// trio alone — its identity rides the Launch. The session owner's own
// runner is the plugin-hosted arm: no StartRun ever reaches it, so its harp
// rides the process env as the one stopgap carrier, deleted with that arm
// (Part 4.1, slice 13). Nothing else differs, and no reader takes a depth or
// a one-shot fact from the environment on either side.
func TestOwnerRunnerEnv_IsTheChildsTrioPlusThePluginArmsHarp(t *testing.T) {
	c := &Coordinator{}

	const url = "http://127.0.0.1:1/mcp"
	owner := c.OwnerRunnerEnv("owner-harp", "owner-tok", url)
	child := runnerEnv("child-harp", "run-1", "child-tok", url)

	assert.ElementsMatch(t, append(envKeys(child), "CTXLOOM_SESSION_HARP"), envKeys(owner),
		"the owner's runner env is the child's key set plus the plugin arm's harp carrier, nothing more")
	assert.Equal(t, "owner-harp", owner["CTXLOOM_SESSION_HARP"],
		"the harp is stamped by the constructor, which is what retires cli/run.go's hand-patch of this one key")
	assert.Equal(t, "", owner[EnvRunID], "the owner owns no spawned run, so its run id is empty — present, but empty")
	assert.Equal(t, "owner-tok", owner[EnvCoordCred])
	assert.Equal(t, url, owner[EnvCoordURL])

	// Degraded (no reach-back) launches drop the trio WHOLE on both sides.
	assert.Empty(t, runnerEnv("child-harp", "run-1", "child-tok", ""))
	assert.Equal(t, []string{"CTXLOOM_SESSION_HARP"}, envKeys(c.OwnerRunnerEnv("owner-harp", "owner-tok", "")))
}
